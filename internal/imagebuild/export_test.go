package imagebuild

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/weaveplatform/weaveplatform-oci/internal/testbundle"
	"github.com/weaveplatform/weaveplatform-oci/pkg/chunk"
	"github.com/weaveplatform/weaveplatform-oci/pkg/disk/vhd"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
)

func exportFixture(t *testing.T, osName string) ExportOptions {
	t.Helper()
	bundle := filepath.Join(t.TempDir(), "bundle")
	must(t, testbundle.Write(bundle, testbundle.Options{OS: osName, Arch: "arm64"}))
	b, err := pack.LoadBundle(bundle)
	must(t, err)
	layout := filepath.Join(t.TempDir(), "layout")
	store, err := pack.OpenLayout(t.Context(), layout)
	must(t, err)
	m, err := pack.Manifest(t.Context(), b, store, chunk.Options{})
	must(t, err)
	root, err := pack.Index(t.Context(), store, []ocispec.Descriptor{m}, nil)
	must(t, err)
	must(t, store.Tag(t.Context(), root, "r1"))
	return ExportOptions{
		Layout:         layout,
		ExpectedDigest: root.Digest.String(),
		Platform:       osName + "/arm64",
		Target:         "guestweave-macos",
		Out:            filepath.Join(t.TempDir(), "export"),
	}
}

func TestExportTargetsAndSourceBinding(t *testing.T) {
	for _, target := range []string{"guestweave-windows", "guestweave-macos", "azure", "aws", "gcp", "openstack", "vsphere"} {
		t.Run(target, func(t *testing.T) {
			o := exportFixture(t, "windows")
			o.Target = target
			before, err := os.ReadFile(filepath.Join(o.Layout, "index.json"))
			must(t, err)
			var logs bytes.Buffer
			tools := Tools{
				Log: &logs,
				Run: func(_ context.Context, _, _ io.Writer, name string, args ...string) error {
					if name != "qemu-img" || strings.Join(args[:4], " ") != "convert -p -f raw" {
						t.Fatal(name, args)
					}
					if target == "vsphere" &&
						!strings.Contains(strings.Join(args, " "), "subformat=streamOptimized") {
						t.Fatal(args)
					}
					return os.WriteFile(args[len(args)-1], []byte("native disk container"), 0o600)
				},
			}
			result, err := tools.ExportImage(t.Context(), o)
			must(t, err)
			if result.IndexDigest != o.ExpectedDigest || result.ManifestDigest == "" ||
				result.Acceptance != "pending" ||
				len(result.Files) != 1 {
				t.Fatal(result)
			}
			file := result.Files[0]
			if file.Name != "disk0" || file.Role != "system" || file.Size == 0 ||
				!strings.HasPrefix(file.SHA256, "sha256:") {
				t.Fatal(file)
			}
			if !strings.Contains(logs.String(), "acceptance remains pending") {
				t.Fatal(logs.String())
			}
			if _, err := os.Stat(filepath.Join(o.Out, "source")); !os.IsNotExist(err) {
				t.Fatal("source staging retained", err)
			}
			after, err := os.ReadFile(filepath.Join(o.Layout, "index.json"))
			must(t, err)
			if !bytes.Equal(before, after) {
				t.Fatal("source changed")
			}
			data, err := os.ReadFile(filepath.Join(o.Out, "export.json"))
			must(t, err)
			var saved ExportResult
			must(t, json.Unmarshal(data, &saved))
			if saved.Files[0] != file {
				t.Fatal("receipt differs")
			}
			if target == "azure" {
				_, f, size, err := vhd.Raw(filepath.Join(o.Out, file.Path))
				must(t, err)
				must(t, f.Close())
				if size != 8<<20 {
					t.Fatal("changed logical size", size)
				}
			}
			if target == "gcp" {
				f, err := os.Open(filepath.Join(o.Out, file.Path))
				must(t, err)
				defer f.Close()
				gz, err := gzip.NewReader(f)
				must(t, err)
				defer gz.Close()
				tr := tar.NewReader(gz)
				h, err := tr.Next()
				must(t, err)
				if h.Name != "disk.raw" || h.Size != 8<<20 {
					t.Fatal(h)
				}
				if _, err := io.Copy(io.Discard, tr); err != nil {
					t.Fatal(err)
				}
				if _, err := tr.Next(); err != io.EOF {
					t.Fatal(err)
				}
			}
			if _, err := tools.ExportImage(t.Context(), o); err == nil {
				t.Fatal("replaced output")
			}
		})
	}
	o := exportFixture(t, "darwin")
	r, err := (Tools{}).ExportImage(t.Context(), o)
	must(t, err)
	if len(r.Files) != 2 || r.Files[1].Format != "state" || r.Files[1].Name != "auxstorage" ||
		r.Firmware.HardwareModel == "" {
		t.Fatal("lost Apple requirements", r)
	}
	if !strings.Contains(ExportTargets(), "azure") {
		t.Fatal("targets absent")
	}
}

func TestExportRejectsInvalidInputAndToolFailures(t *testing.T) {
	base := exportFixture(t, "windows")
	for _, mode := range []string{"digest absent", "wrong digest", "missing layout", "out absent", "platform absent", "wrong platform", "unknown target", "tool fails", "tool no output", "cancelled", "report blocked"} {
		t.Run(mode, func(t *testing.T) {
			o := base
			o.Out = filepath.Join(t.TempDir(), "export")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			tools := Tools{}
			switch mode {
			case "digest absent":
				o.ExpectedDigest = ""
			case "wrong digest":
				o.ExpectedDigest = "sha256:" + strings.Repeat("a", 64)
			case "missing layout":
				o.Layout += "absent"
			case "out absent":
				o.Out = ""
			case "platform absent":
				o.Platform = ""
			case "wrong platform":
				o.Platform = "windows/amd64"
			case "unknown target":
				o.Target = "unknown"
			case "cancelled":
				cancel()
			case "tool fails", "tool no output", "report blocked":
				o.Target = "openstack"
				tools.Run = func(_ context.Context, _, _ io.Writer, _ string, args ...string) error {
					if mode == "tool fails" {
						return os.ErrPermission
					}
					if mode == "report blocked" {
						must(t, os.Mkdir(filepath.Join(o.Out, "export.json"), 0o750))
						return os.WriteFile(args[len(args)-1], []byte("disk"), 0o600)
					}
					return nil
				}
			}
			if _, err := tools.ExportImage(ctx, o); err == nil {
				t.Fatal("accepted invalid export")
			}
		})
	}
	if _, err := exportFormat("azure", "darwin"); !errors.Is(err, ErrInput) {
		t.Fatal(err)
	}
}

func TestDiskExportIOAndCancellation(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "raw")
	must(t, os.WriteFile(raw, []byte("small"), 0o600))
	tools := Tools{}
	for _, format := range []string{"raw", "vhd", "tar.gz"} {
		if err := tools.exportDisk(
			t.Context(),
			filepath.Join(dir, "absent"),
			filepath.Join(dir, "out"),
			format,
		); err == nil {
			t.Fatal(format)
		}
	}
	if err := tools.exportDisk(
		t.Context(),
		raw,
		filepath.Join(dir, "out"),
		"vhd",
	); !errors.Is(
		err,
		ErrInput,
	) {
		t.Fatal(err)
	}
	if err := tools.exportDisk(
		t.Context(),
		raw,
		filepath.Join(dir, "missing", "out"),
		"raw",
	); err == nil {
		t.Fatal("rename failure ignored")
	}
	if err := exportTar(t.Context(), raw, filepath.Join(dir, "missing", "out")); err == nil {
		t.Fatal("archive creation failure ignored")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := tools.exportDisk(
		ctx,
		raw,
		filepath.Join(dir, "out"),
		"raw",
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatal(err)
	}
	if err := exportTar(
		ctx,
		raw,
		filepath.Join(dir, "cancel.tar.gz"),
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatal(err)
	}
	if _, err := describeExport(ctx, raw, "raw"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := describeExport(t.Context(), dir, "raw"); err == nil {
		t.Fatal("hashed directory")
	}
	must(t, os.WriteFile(raw, make([]byte, 1<<20), 0o600))
	if err := tools.exportDisk(
		t.Context(),
		raw,
		filepath.Join(dir, "missing", "out"),
		"vhd",
	); err == nil {
		t.Fatal("VHD rename failure ignored")
	}
}
