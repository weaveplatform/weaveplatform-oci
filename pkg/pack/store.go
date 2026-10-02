package pack

import (
	"context"
	"errors"
	"fmt"
	"io"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/errdef"

	"github.com/deploymenttheory/weaveplatform-oci/pkg/chunk"
)

// OpenLayout opens or creates an OCI image layout directory.
func OpenLayout(ctx context.Context, dir string) (*oci.Store, error) {
	s, err := oci.NewWithContext(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("open layout %s: %w", dir, err)
	}
	return s, nil
}

// sink adapts a content.Storage to chunk.Sink.
type sink struct{ s content.Storage }

func (k sink) Exists(ctx context.Context, d ocispec.Descriptor) (bool, error) {
	ok, err := k.s.Exists(ctx, d)
	if err != nil {
		return false, fmt.Errorf("exists %s: %w", d.Digest, err)
	}
	return ok, nil
}

func (k sink) Push(ctx context.Context, d ocispec.Descriptor, r io.Reader) error {
	if err := k.s.Push(ctx, d, r); err != nil {
		if errors.Is(err, errdef.ErrAlreadyExists) {
			return chunk.ErrAlreadyExists
		}
		return fmt.Errorf("push %s: %w", d.Digest, err)
	}
	return nil
}

// source adapts a content.Fetcher to chunk.Source.
type source struct{ f content.Fetcher }

func (s source) Fetch(ctx context.Context, d ocispec.Descriptor) (io.ReadCloser, error) {
	rc, err := s.f.Fetch(ctx, d)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", d.Digest, err)
	}
	return rc, nil
}

// Source returns a chunk.Source over any content fetcher.
func Source(f content.Fetcher) chunk.Source { return source{f: f} }

func pushBytes(ctx context.Context, s content.Storage, d ocispec.Descriptor, b []byte) error {
	return sink{s: s}.pushBytes(ctx, d, b)
}

func (k sink) pushBytes(ctx context.Context, d ocispec.Descriptor, b []byte) error {
	ok, err := k.Exists(ctx, d)
	if err != nil || ok {
		return err
	}
	if err := k.Push(
		ctx,
		d,
		bytesReader(b),
	); err != nil &&
		!errors.Is(err, chunk.ErrAlreadyExists) {
		return err
	}
	return nil
}
