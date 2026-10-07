package imagebuild

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/weaveplatform/weaveplatform-oci/pkg/chunk"
	"github.com/weaveplatform/weaveplatform-oci/pkg/disk/vhd"
	"github.com/weaveplatform/weaveplatform-oci/pkg/disk/virtualdisk"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

// ExportOptions selects a target representation of a pinned platform image.
type ExportOptions struct {
	Layout, ExpectedDigest, Platform, Target, Out string
}

// ExportResult binds target files to the source OCI index and platform. Export
// is a storage conversion only; target boot acceptance remains pending.
type ExportResult struct {
	SchemaVersion  int           `json:"schemaVersion"`
	Target         string        `json:"target"`
	Platform       string        `json:"platform"`
	IndexDigest    string        `json:"indexDigest"`
	ManifestDigest string        `json:"manifestDigest"`
	Guest          spec.Guest    `json:"guest"`
	Firmware       spec.Firmware `json:"firmware"`
	Files          []ExportFile  `json:"files"`
	Acceptance     string        `json:"acceptance"`
}

// ExportFile records the hash of the target container, not its guest sectors.
type ExportFile struct {
	Name   string `json:"name"`
	Role   string `json:"role,omitempty"`
	Path   string `json:"path"`
	Format string `json:"format"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func exportFormat(target, osName string) (string, error) {
	if osName == "darwin" && target != "guestweave-macos" {
		return "", fmt.Errorf("%w: macOS export requires an Apple host target", ErrInput)
	}
	switch target {
	case "guestweave-macos", "aws":
		return "raw", nil
	case "guestweave-windows":
		return "vhdx", nil
	case "azure":
		return "vhd", nil
	case "gcp":
		return "tar.gz", nil
	case "openstack":
		return "qcow2", nil
	case "vsphere":
		return "vmdk", nil
	default:
		return "", fmt.Errorf("%w: unknown export target %q", ErrInput, target)
	}
}

// ExportImage prepares a target's disk containers, without changing the OCI
// contract or claiming provider-specific guest adaptation or boot validation.
func (t Tools) ExportImage(ctx context.Context, o ExportOptions) (result ExportResult, err error) {
	if o.ExpectedDigest == "" || o.Out == "" || o.Platform == "" {
		return result, fmt.Errorf("%w: pinned index, platform and output required", ErrInput)
	}
	store, report, err := openCandidate(ctx, o.Layout)
	if err != nil {
		return result, err
	}
	if report.Root.Digest.String() != o.ExpectedDigest ||
		report.Root.MediaType != spec.MediaTypeIndex {
		return result, fmt.Errorf("%w: source index changed", ErrInput)
	}
	selected := -1
	for i, c := range report.Children {
		g := c.Description.Config.Guest
		if g.OS+"/"+g.Arch == o.Platform {
			selected = i
		}
	}
	if selected < 0 {
		return result, fmt.Errorf("%w: source lacks %s", ErrInput, o.Platform)
	}
	child := report.Children[selected]
	format, err := exportFormat(o.Target, child.Description.Config.Guest.OS)
	if err != nil {
		return result, err
	}
	if err := newDirectory(o.Out); err != nil {
		return result, err
	}
	progress := newNativeProgress(ctx, t.Log, "export "+o.Platform+" to "+o.Target)
	finish := progress.start()
	defer func() { finish(err) }()
	progress.step("unpacking verified source sectors")
	bundleDir := filepath.Join(o.Out, "source")
	if _, err := pack.Unpack(
		ctx,
		store,
		child.Descriptor,
		bundleDir,
		chunk.AssembleOptions{},
	); err != nil {
		return result, fmt.Errorf("unpack export source: %w", err)
	}
	defer os.RemoveAll(bundleDir)
	b, err := pack.LoadBundle(bundleDir)
	if err != nil {
		return result, fmt.Errorf("load export bundle: %w", err)
	}
	result = ExportResult{
		SchemaVersion:  1,
		Target:         o.Target,
		Platform:       o.Platform,
		IndexDigest:    o.ExpectedDigest,
		ManifestDigest: child.Descriptor.Digest.String(),
		Guest:          b.File.Guest,
		Firmware:       b.File.Firmware,
		Acceptance:     "pending",
	}
	for _, disk := range b.File.Disks {
		progress.step("converting " + disk.Name + " to " + format)
		source := filepath.Join(bundleDir, disk.Path)
		dest := filepath.Join(o.Out, disk.Name+"."+format)
		if err := t.exportDisk(ctx, source, dest, format); err != nil {
			return result, err
		}
		file, err := describeExport(ctx, dest, format)
		if err != nil {
			return result, err
		}
		file.Name, file.Role = disk.Name, disk.Role
		result.Files = append(result.Files, file)
	}
	// Carry platform requirements, never regenerated identity (TPM/VMGS/ECID).
	for _, state := range b.File.State {
		if state.Semantics != spec.SemanticsCarry {
			continue
		}
		dest := filepath.Join(o.Out, "state-"+filepath.Base(state.Path))
		if err := os.Rename(filepath.Join(bundleDir, state.Path), dest); err != nil {
			return result, fmt.Errorf("export state: %w", err)
		}
		file, err := describeExport(ctx, dest, "state")
		if err != nil {
			return result, err
		}
		file.Name = state.Name
		result.Files = append(result.Files, file)
	}
	if err := writeJSON(filepath.Join(o.Out, "export.json"), result); err != nil {
		return result, err
	}
	progress.step("target files ready; target acceptance remains pending")
	return result, nil
}

func (t Tools) exportDisk(ctx context.Context, source, dest, format string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("export disk: %w", err)
	}
	switch format {
	case "raw":
		if err := os.Rename(source, dest); err != nil {
			return fmt.Errorf("export raw: %w", err)
		}
		return nil
	case "vhd":
		info, err := os.Stat(source)
		if err != nil {
			return fmt.Errorf("stat raw export: %w", err)
		}
		if info.Size()%(1<<20) != 0 {
			return fmt.Errorf("%w: Azure disk must be aligned to 1 MiB", ErrInput)
		}
		if err := vhd.Append(source, time.Now()); err != nil {
			return fmt.Errorf("fixed VHD export: %w", err)
		}
		if err := os.Rename(source, dest); err != nil {
			return fmt.Errorf("move VHD: %w", err)
		}
		return nil
	case "tar.gz":
		return exportTar(ctx, source, dest)
	case "vhdx":
		if runtime.GOOS == "windows" && t.Run == nil {
			if err := vhd.Append(source, time.Now()); err != nil {
				return fmt.Errorf("VHDX bridge: %w", err)
			}
			if err := virtualdisk.Convert(ctx, source, dest, virtualdisk.VHDX); err != nil {
				return fmt.Errorf("convert VHDX: %w", err)
			}
			return nil
		}
	}
	args := []string{"convert", "-p", "-f", "raw", "-O", format}
	if format == "vmdk" {
		args = append(args, "-o", "subformat=streamOptimized")
	}
	args = append(args, source, dest)
	return t.run(ctx, nil, "qemu-img", args...)
}

func exportTar(ctx context.Context, source, dest string) error {
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open tar source: %w", err)
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("stat tar source: %w", err)
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create export archive: %w", err)
	}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	err = tw.WriteHeader(
		&tar.Header{Name: "disk.raw", Mode: 0o600, Size: info.Size(), Typeflag: tar.TypeReg},
	)
	if err == nil {
		_, err = io.Copy(tw, contextReader{ctx, in})
	}
	if err := errors.Join(err, tw.Close(), gz.Close(), out.Close()); err != nil {
		return fmt.Errorf("write export archive: %w", err)
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, fmt.Errorf("read export: %w", err)
	}
	return r.r.Read(p) //nolint:wrapcheck // Preserve io.Reader EOF semantics.
}

func describeExport(ctx context.Context, path, format string) (ExportFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return ExportFile{}, fmt.Errorf("read export: %w", err)
	}
	defer f.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, contextReader{ctx, f})
	if err != nil {
		return ExportFile{}, fmt.Errorf("hash export: %w", err)
	}
	return ExportFile{
		Path:   filepath.Base(path),
		Format: format,
		SHA256: "sha256:" + hex.EncodeToString(hash.Sum(nil)),
		Size:   size,
	}, nil
}

// ExportTargets is the stable CLI list; actual OS/provider compatibility must
// additionally be proven by each target's acceptance suite.
func ExportTargets() string {
	return strings.Join(
		[]string{
			"guestweave-windows",
			"guestweave-macos",
			"azure",
			"aws",
			"gcp",
			"openstack",
			"vsphere",
		},
		", ",
	)
}
