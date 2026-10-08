package imagebuild

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/weaveplatform/weaveplatform-oci/pkg/disk/vhd"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
)

func TestDownloadFilesystemFailures(t *testing.T) {
	for _, mode := range []string{"cache-loop", "parent-file", "partial-loop", "metadata-directory", "partial-directory", "promotion", "metadata-removal"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			out := filepath.Join(dir, "image")
			data := []byte("verified media")
			m := mediaFor("https://example.test/image", data)
			switch mode {
			case "cache-loop":
				must(t, os.Symlink(out, out))
			case "parent-file":
				must(t, os.WriteFile(out, nil, 0o600))
				out = filepath.Join(out, "image")
			case "partial-loop":
				must(t, os.Symlink(out+".part", out+".part"))
			case "metadata-directory":
				must(t, os.Mkdir(out+".part.json", 0o700))
			case "partial-directory":
				must(t, os.Mkdir(out+".part", 0o700))
				must(t, writeJSON(out+".part.json", m))
			}
			client := &http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					switch mode {
					case "promotion":
						must(t, os.Mkdir(out, 0o700))
					case "metadata-removal":
						must(t, os.Remove(out+".part.json"))
						must(t, os.Mkdir(out+".part.json", 0o700))
						must(t, os.WriteFile(filepath.Join(out+".part.json", "keep"), nil, 0o600))
					}
					return &http.Response{
						StatusCode:    200,
						Body:          io.NopCloser(bytes.NewReader(data)),
						ContentLength: int64(len(data)),
						Header:        http.Header{},
						Request:       r,
					}, nil
				}),
			}
			if _, err := (Downloader{Client: client}).Download(t.Context(), m, out); err == nil {
				t.Fatal("ignored filesystem failure")
			}
			if mode != "metadata-removal" && mode != "parent-file" {
				info, err := os.Stat(out)
				if err == nil && info.Mode().IsRegular() {
					t.Fatal("promoted failed download")
				}
			}
		})
	}
}

func TestDownloadClosedHandles(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "partial"))
	must(t, err)
	must(t, f.Close())
	m := mediaFor("https://example.test/image", []byte("x"))
	if err := (Downloader{}).transfer(t.Context(), f, m); err == nil {
		t.Fatal("closed file accepted")
	}
	for _, status := range []int{200, 206} {
		client := &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode:    status,
					Header:        http.Header{"Content-Range": []string{"bytes 0-0/1"}},
					ContentLength: 1,
					Body:          io.NopCloser(strings.NewReader("x")),
					Request:       r,
				}, nil
			}),
		}
		if err := (Downloader{}).downloadRange(t.Context(), client, f, m, 0); err == nil {
			t.Fatal("closed destination accepted")
		}
	}
	dir := t.TempDir()
	stat, err := os.Stat(dir)
	must(t, err)
	if err := verifiedFile(dir, Media{Size: stat.Size()}); err == nil {
		t.Fatal("directory hashed as media")
	}
	if _, err := hashSizedFile(dir, stat.Size()); err == nil {
		t.Fatal("directory hashed as Windows media")
	}
}

func TestBootFilesystemFailures(t *testing.T) {
	fakeFirmware(t)
	for _, mode := range []string{"disk-escape", "workspace", "firmware-copy", "result-write", "log-write", "serial-write"} {
		t.Run(mode, func(t *testing.T) {
			bundle := bootBundle(t, "arm64")
			out := filepath.Join(t.TempDir(), "report")
			if mode == "disk-escape" {
				disk := filepath.Join(bundle, "disk0.img")
				must(t, os.Remove(disk))
				external := filepath.Join(t.TempDir(), "original")
				must(t, os.WriteFile(external, []byte("keep"), 0o600))
				must(t, os.Symlink(external, disk))
			}
			if mode == "workspace" {
				missing := filepath.Join(t.TempDir(), "missing")
				for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
					t.Setenv(name, missing)
				}
			}
			fake := &fakeQEMU{t: t}
			tools := Tools{
				Run: func(ctx context.Context, w, e io.Writer, name string, args ...string) error {
					if name == "qemu-img" && args[0] == "resize" && mode == "firmware-copy" {
						must(t, os.Mkdir(filepath.Join(filepath.Dir(args[2]), "vars.fd"), 0o700))
					}
					if strings.HasPrefix(name, "qemu-system") && mode == "result-write" {
						must(t, os.Mkdir(filepath.Join(out, "result.json"), 0o700))
					}
					if name == "qemu-img" && args[0] == "resize" && mode == "log-write" {
						must(t, os.Mkdir(filepath.Join(out, "qemu.log"), 0o700))
					}
					if name == "qemu-img" && args[0] == "resize" && mode == "serial-write" {
						must(t, os.Mkdir(filepath.Join(out, "serial.log"), 0o700))
					}
					return fake.run(ctx, w, e, name, args...)
				},
			}
			if _, err := tools.BootLinux(
				t.Context(),
				BootOptions{Bundle: bundle, Report: out, Timeout: time.Second},
			); err == nil {
				t.Fatal("ignored " + mode)
			}
		})
	}
	work := t.TempDir()
	must(t, os.Mkdir(filepath.Join(work, "seed"), 0o700))
	if err := (Tools{}).seed(t.Context(), work, "", "", ""); err == nil {
		t.Fatal("reused seed")
	}
	payload := t.TempDir()
	must(t, os.Symlink("missing", filepath.Join(payload, "link")))
	if err := copyTree(payload, filepath.Join(t.TempDir(), "copy")); err == nil {
		t.Fatal("copied payload symlink")
	}
}

func TestLinuxValidationFailureReports(t *testing.T) {
	fakeFirmware(t)
	for _, mode := range []string{"input", "existing", "cancelled", "boot", "acceptance-write"} {
		t.Run(mode, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "candidate")
			o := ValidateOptions{
				Bundles: []string{bootBundle(t, "arm64")},
				Arches:  []string{"arm64"},
				Out:     out,
				Timeout: time.Second,
			}
			ctx := t.Context()
			switch mode {
			case "input":
				o.Bundles = nil
			case "existing":
				must(t, os.Mkdir(out, 0o700))
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			fake := &fakeQEMU{t: t, missingMarker: mode == "boot"}
			tools := Tools{
				Run: func(ctx context.Context, w, e io.Writer, name string, args ...string) error {
					if mode == "acceptance-write" && strings.HasPrefix(name, "qemu-system") &&
						fake.boots == 0 {
						must(t, os.Mkdir(filepath.Join(out, "acceptance.json"), 0o700))
					}
					return fake.run(ctx, w, e, name, args...)
				},
			}
			result, err := tools.ValidateLinux(ctx, o)
			if err == nil {
				t.Fatal("accepted " + mode)
			}
			if mode == "boot" {
				if result.Passed {
					t.Fatal("failed boot passed")
				}
				var saved Acceptance
				must(t, readJSON(filepath.Join(out, "acceptance.json"), &saved))
				if saved.Passed || len(saved.Platforms["linux/arm64"]) != 1 {
					t.Fatal(saved)
				}
			}
		})
	}
}

func TestCandidateRejectsInvalidLayouts(t *testing.T) {
	for _, mode := range []string{"invalid-index", "no-tag", "missing-blob"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			store, err := pack.OpenLayout(t.Context(), dir)
			must(t, err)
			if mode == "invalid-index" {
				must(t, os.WriteFile(filepath.Join(dir, "index.json"), []byte("{"), 0o600))
			}
			if mode == "missing-blob" {
				raw := []byte(`{"schemaVersion":2,"manifests":[]}`)
				desc := ocispec.Descriptor{
					MediaType: ocispec.MediaTypeImageIndex,
					Digest:    digest.FromBytes(raw),
					Size:      int64(len(raw)),
				}
				must(t, store.Push(t.Context(), desc, bytes.NewReader(raw)))
				must(t, store.Tag(t.Context(), desc, "test"))
				must(t, os.Remove(filepath.Join(dir, "blobs", "sha256", desc.Digest.Encoded())))
			}
			if _, _, err := openCandidate(t.Context(), dir); err == nil {
				t.Fatal("accepted " + mode)
			}
		})
	}
	store, err := pack.OpenLayout(t.Context(), t.TempDir())
	must(t, err)
	if _, err := (Tools{}).validateClones(
		t.Context(),
		store,
		ocispec.Descriptor{},
		"arm64",
		ValidateOptions{Out: "missing"},
	); err == nil {
		t.Fatal("missing unpack output")
	}
	if _, err := (Tools{}).validateClones(
		t.Context(),
		store,
		ocispec.Descriptor{},
		"arm64",
		ValidateOptions{Out: t.TempDir()},
	); err == nil {
		t.Fatal("missing manifest")
	}
}

func TestWindowsOutputFailures(t *testing.T) {
	_, source := windowsFixture()
	marker := "WEAVE-IMAGE-READY-0123456789abcdef01234567"
	for _, mode := range []string{"existing-seed", "invalid-recipe", "iso-output"} {
		out := t.TempDir()
		s := source
		switch mode {
		case "existing-seed":
			must(t, os.Mkdir(filepath.Join(out, "seed"), 0o700))
		case "invalid-recipe":
			s.Edition = "unknown"
		case "iso-output":
			must(t, os.Mkdir(filepath.Join(out, "seed.iso"), 0o700))
		}
		if _, err := windowsSeed(out, s, marker); err == nil {
			t.Fatal("accepted " + mode)
		}
	}
	for _, mode := range []string{"git", "bundle-write"} {
		out := t.TempDir()
		disk := filepath.Join(out, "disk.vhd")
		must(t, os.WriteFile(disk, make([]byte, 1024), 0o600))
		must(t, vhd.Append(disk, time.Now()))
		tools := Tools{Run: func(_ context.Context, w, _ io.Writer, _ string, _ ...string) error {
			if mode == "git" {
				return ErrInput
			}
			must(t, os.Mkdir(filepath.Join(out, "bundle", "bundle.json"), 0o700))
			_, err := fmt.Fprint(w, "abcdef")
			return err
		}}
		if _, err := tools.windowsBundle(
			t.Context(),
			WindowsOptions{Out: out},
			source,
			WindowsInstallResult{},
		); err == nil {
			t.Fatal(mode)
		}
	}
}

func TestSourceValidationFailures(t *testing.T) {
	selection, s := windowsFixture()
	if err := s.validate(WindowsSelection{}, false); err == nil {
		t.Fatal("invalid selection")
	}
	for _, mode := range []string{"kind", "url"} {
		bad := s
		if mode == "kind" {
			bad.Kind = "unknown"
		} else {
			bad.URI = "://invalid"
		}
		if err := bad.validate(selection, false); err == nil {
			t.Fatal(mode)
		}
	}
	s.SHA256 = ""
	cache := filepath.Join(t.TempDir(), "cache")
	must(t, os.WriteFile(cache, nil, 0o600))
	if _, _, err := (Packages{}).AcquireWindows(t.Context(), s, selection, cache); err == nil {
		t.Fatal("file cache")
	}
	if windowsProgress(io.Discard) != io.Discard {
		t.Fatal("lost progress writer")
	}
}

func TestSystemDiskRequiresExistingBundleRoot(t *testing.T) {
	root := t.TempDir()
	must(t, os.WriteFile(filepath.Join(root, "disk.img"), []byte("original"), 0o600))
	b := pack.Bundle{
		Dir:  filepath.Join(root, "missing"),
		File: pack.BundleFile{Disks: []pack.BundleDisk{{Role: "system", Path: "../disk.img"}}},
	}
	if _, err := systemDisk(b); err == nil {
		t.Fatal("nonexistent bundle root accepted")
	}
}

func TestDeepCheckRejectsIncompleteIndex(t *testing.T) {
	store, err := pack.OpenLayout(t.Context(), t.TempDir())
	must(t, err)
	raw := []byte(
		`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`,
	)
	desc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageIndex,
		Digest:    digest.FromBytes(raw),
		Size:      int64(len(raw)),
	}
	must(t, store.Push(t.Context(), desc, bytes.NewReader(raw)))
	if report, err := deepCheck(t.Context(), store, desc); err == nil || report.OK() {
		t.Fatal("empty candidate accepted", err)
	}
}
