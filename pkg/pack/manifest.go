package pack

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"

	"github.com/weaveplatform/weaveplatform-oci/pkg/chunk"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

// MaxStateSize caps a state file; state blobs are small and never chunked.
const MaxStateSize = 256 << 20

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// Manifest chunks every disk of b into store, writes the state and config
// blobs, pushes the manifest and returns its descriptor. The result is
// validated with spec.Inspect before it is returned.
func Manifest(
	ctx context.Context,
	b Bundle,
	store content.Storage,
	o chunk.Options,
) (ocispec.Descriptor, error) {
	o.ChunkSize = spec.ChunkSize // the contract fixes the chunk size
	f := b.File
	cfg := spec.Config{
		SchemaVersion: 1,
		Guest:         f.Guest,
		Firmware:      f.Firmware,
		Resources:     f.Resources,
		Provisioning:  f.Provisioning,
		Build:         f.Build,
		State:         []spec.StateEntry{},
	}
	var layers []ocispec.Descriptor
	for _, d := range f.Disks {
		disk, chunks, err := packDisk(ctx, b, d, store, o)
		if err != nil {
			return ocispec.Descriptor{}, err
		}
		cfg.Disks = append(cfg.Disks, disk)
		for _, c := range chunks {
			layers = append(layers, c.Descriptor)
		}
	}
	for _, s := range f.State {
		entry, layer, err := packState(ctx, b, s, store)
		if err != nil {
			return ocispec.Descriptor{}, err
		}
		cfg.State = append(cfg.State, entry)
		layers = append(layers, layer)
	}
	raw, err := cfg.Marshal()
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	if _, err := spec.ParseConfig(raw); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("bundle %s: %w", b.Dir, err)
	}
	cfgDesc := content.NewDescriptorFromBytes(spec.MediaTypeConfig, raw)
	if err := pushBytes(ctx, store, cfgDesc, raw); err != nil {
		return ocispec.Descriptor{}, err
	}
	ann := map[string]string{}
	for k, v := range f.Annotations {
		ann[k] = v
	}
	for k, v := range spec.GuestAnnotations(cfg) {
		ann[k] = v
	}
	ann[spec.AnnotationCreated] = cfg.Build.Created
	desc, err := oras.PackManifest(
		ctx,
		store,
		oras.PackManifestVersion1_1,
		spec.ArtifactType,
		oras.PackManifestOptions{
			Layers:              layers,
			ConfigDescriptor:    &cfgDesc,
			ManifestAnnotations: ann,
		},
	)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("pack manifest: %w", err)
	}
	mraw, err := content.FetchAll(ctx, store, desc)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("read back manifest: %w", err)
	}
	if _, err := spec.Inspect(mraw, raw); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("bundle %s: %w", b.Dir, err)
	}
	return desc, nil
}

func packDisk(
	ctx context.Context,
	b Bundle,
	d BundleDisk,
	store content.Storage,
	o chunk.Options,
) (spec.Disk, []spec.ChunkLayer, error) {
	p, err := b.path(d.Path)
	if err != nil {
		return spec.Disk{}, nil, err
	}
	fh, err := os.Open(p) //nolint:gosec // path checked to be inside the bundle
	if err != nil {
		return spec.Disk{}, nil, fmt.Errorf("disk %s: %w", d.Name, err)
	}
	defer func() { _ = fh.Close() }()
	st, err := fh.Stat()
	if err != nil {
		return spec.Disk{}, nil, fmt.Errorf("disk %s: %w", d.Name, err)
	}
	chunks, err := chunk.Split(ctx, fh, st.Size(), d.Name, sink{s: store}, o)
	if err != nil {
		return spec.Disk{}, nil, fmt.Errorf("disk %s: %w", d.Name, err)
	}
	disk := spec.Disk{
		Name:        d.Name,
		Role:        d.Role,
		LogicalSize: st.Size(),
		ChunkSize:   spec.ChunkSize,
		ChunkCount:  int64(len(chunks)),
		Compression: "zstd",
	}
	for _, c := range chunks {
		if c.Zero {
			disk.ZeroChunks++
		}
	}
	return disk, chunks, nil
}

func packState(
	ctx context.Context,
	b Bundle,
	s BundleState,
	store content.Storage,
) (spec.StateEntry, ocispec.Descriptor, error) {
	mt, ok := spec.StateMediaType(s.Name)
	if !ok {
		return spec.StateEntry{}, ocispec.Descriptor{}, fmt.Errorf(
			"%w: unknown state %q",
			ErrBundle,
			s.Name,
		)
	}
	p, err := b.path(s.Path)
	if err != nil {
		return spec.StateEntry{}, ocispec.Descriptor{}, err
	}
	st, err := os.Stat(p)
	if err != nil {
		return spec.StateEntry{}, ocispec.Descriptor{}, fmt.Errorf("state %s: %w", s.Name, err)
	}
	if st.Size() > MaxStateSize {
		return spec.StateEntry{}, ocispec.Descriptor{}, fmt.Errorf(
			"%w: state %s is %d bytes, limit %d",
			ErrBundle,
			s.Name,
			st.Size(),
			MaxStateSize,
		)
	}
	raw, err := os.ReadFile(p) //nolint:gosec // path checked to be inside the bundle
	if err != nil {
		return spec.StateEntry{}, ocispec.Descriptor{}, fmt.Errorf("state %s: %w", s.Name, err)
	}
	desc := ocispec.Descriptor{
		MediaType: mt,
		Digest:    digest.FromBytes(raw),
		Size:      int64(len(raw)),
		Annotations: map[string]string{
			spec.AnnotationTitle:          filepath.Base(p),
			spec.AnnotationStateName:      s.Name,
			spec.AnnotationStateSemantics: s.Semantics,
		},
	}
	if err := pushBytes(ctx, store, desc, raw); err != nil {
		return spec.StateEntry{}, ocispec.Descriptor{}, err
	}
	return spec.StateEntry{
		Name:      s.Name,
		MediaType: mt,
		Semantics: s.Semantics,
		Required:  s.Required,
	}, desc, nil
}
