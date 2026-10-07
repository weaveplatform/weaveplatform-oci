package publish

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/oci"

	"github.com/weaveplatform/weaveplatform-oci/pkg/conformance"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

func prepare(
	ctx context.Context,
	r Request,
	work string,
) (*oci.Store, ocispec.Descriptor, []spec.Description, error) {
	if r.Layout == "" {
		store, err := oci.NewWithContext(ctx, filepath.Join(work, "layout"))
		if err != nil {
			return nil, ocispec.Descriptor{}, nil, fmt.Errorf("publish: %w", err)
		}
		root, children, err := packAll(ctx, store, r.Bundles, r.Chunk)
		return store, root, children, err
	}
	if err := digest.Digest(r.ExpectedDigest).Validate(); err != nil {
		return nil, ocispec.Descriptor{}, nil, fmt.Errorf(
			"%w: expected digest: %w",
			ErrRequest,
			err,
		)
	}
	// Do not turn a missing input into an empty OCI store.
	if _, err := os.Stat(filepath.Join(r.Layout, "oci-layout")); err != nil {
		return nil, ocispec.Descriptor{}, nil, fmt.Errorf("read publication layout: %w", err)
	}
	store, err := oci.NewWithContext(ctx, r.Layout)
	if err != nil {
		return nil, ocispec.Descriptor{}, nil, fmt.Errorf(
			"%w: open publication layout: %w",
			ErrSelfCheck,
			err,
		)
	}
	root, err := store.Resolve(ctx, r.LayoutRef)
	if err != nil {
		return nil, root, nil, fmt.Errorf("resolve publication layout: %w", err)
	}
	if root.Digest.String() != r.ExpectedDigest || root.MediaType != spec.MediaTypeIndex {
		return nil, root, nil, fmt.Errorf(
			"%w: layout must resolve to the accepted index %s",
			ErrRequest,
			r.ExpectedDigest,
		)
	}
	report, err := conformance.Check(ctx, store, root, conformance.Options{})
	if err != nil || !report.OK() {
		return nil, root, nil, fmt.Errorf(
			"%w: layout conformance: %v %v",
			ErrSelfCheck,
			err,
			report.Problems(),
		)
	}
	children := make([]spec.Description, 0, len(report.Children))
	for _, child := range report.Children {
		children = append(children, child.Description)
	}
	return store, root, children, nil
}
