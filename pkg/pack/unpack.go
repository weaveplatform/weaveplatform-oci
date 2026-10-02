package pack

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"

	"github.com/weaveplatform/weaveplatform-oci/pkg/chunk"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

// derivedAnnotations are recomputed by Manifest and so are not written back
// into bundle.json by Unpack.
var derivedAnnotations = map[string]bool{
	spec.AnnotationCreated:       true,
	spec.AnnotationOS:            true,
	spec.AnnotationArch:          true,
	spec.AnnotationOSVersion:     true,
	spec.AnnotationOSBuild:       true,
	spec.AnnotationDistro:        true,
	spec.AnnotationDiskTotalSize: true,
}

// UnpackResult reports what Unpack produced.
type UnpackResult struct {
	Description spec.Description
	Stats       chunk.Stats
}

// Unpack materialises the manifest m from f into dir as a bundle: one raw
// disk per config disk (dir/<name>.img), the state files, and bundle.json.
// Packing the result again yields the same manifest digest.
func Unpack(
	ctx context.Context,
	f content.Fetcher,
	m ocispec.Descriptor,
	dir string,
	o chunk.AssembleOptions,
) (UnpackResult, error) {
	d, err := Describe(ctx, f, m)
	if err != nil {
		return UnpackResult{}, err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return UnpackResult{}, fmt.Errorf("create %s: %w", dir, err)
	}
	cfg := d.Config
	bf := BundleFile{
		SchemaVersion: 1,
		Guest:         cfg.Guest,
		Firmware:      cfg.Firmware,
		Resources:     cfg.Resources,
		Provisioning:  cfg.Provisioning,
		Build:         cfg.Build,
		Annotations:   map[string]string{},
	}
	for k, v := range d.Annotations {
		if !derivedAnnotations[k] {
			bf.Annotations[k] = v
		}
	}
	var total chunk.Stats
	for _, disk := range d.Disks {
		rel := disk.Name + ".img"
		s, err := assembleDisk(ctx, f, disk, filepath.Join(dir, rel), o)
		if err != nil {
			return UnpackResult{}, err
		}
		total.Fetched += s.Fetched
		total.Zero += s.Zero
		total.Resumed += s.Resumed
		total.Retried += s.Retried
		bf.Disks = append(bf.Disks, BundleDisk{Name: disk.Name, Role: disk.Role, Path: rel})
	}
	for _, st := range d.State {
		rel := stateFileName(st)
		raw, err := content.FetchAll(ctx, f, st.Descriptor)
		if err != nil {
			return UnpackResult{}, fmt.Errorf("state %s: %w", st.Name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), raw, 0o600); err != nil {
			return UnpackResult{}, fmt.Errorf("state %s: %w", st.Name, err)
		}
		bf.State = append(
			bf.State,
			BundleState{Name: st.Name, Path: rel, Semantics: st.Semantics, Required: st.Required},
		)
	}
	if err := WriteBundleFile(dir, bf); err != nil {
		return UnpackResult{}, err
	}
	return UnpackResult{Description: d, Stats: total}, nil
}

func assembleDisk(
	ctx context.Context,
	f content.Fetcher,
	disk spec.DiskLayout,
	path string,
	o chunk.AssembleOptions,
) (chunk.Stats, error) {
	w, err := chunk.OpenFile(path)
	if err != nil {
		return chunk.Stats{}, err //nolint:wrapcheck // chunk errors name the file
	}
	if !o.Resume {
		if err := w.Truncate(0); err != nil {
			_ = w.Close()
			return chunk.Stats{}, fmt.Errorf("disk %s: reset: %w", disk.Name, err)
		}
	}
	s, err := chunk.Assemble(ctx, disk.LogicalSize, disk.Chunks, Source(f), w, o)
	cerr := w.Close()
	if err != nil {
		return s, fmt.Errorf("disk %s: %w", disk.Name, err)
	}
	if cerr != nil {
		return s, fmt.Errorf("disk %s: close: %w", disk.Name, cerr)
	}
	return s, nil
}

// stateFileName keeps the title the producer used when it is a plain file
// name, otherwise falls back to <name>.bin.
func stateFileName(s spec.StateLayer) string {
	t := s.Descriptor.Annotations[spec.AnnotationTitle]
	if t == "" || t == BundleFileName || strings.HasPrefix(t, ".") ||
		strings.HasSuffix(t, ".img") ||
		strings.ContainsAny(t, `/\:`) {
		return s.Name + ".bin"
	}
	return t
}
