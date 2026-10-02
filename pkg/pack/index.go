package pack

import (
	"context"
	"encoding/json"
	"fmt"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"

	"github.com/deploymenttheory/weaveplatform-oci/pkg/spec"
)

// indexAnnotationKeys are copied from the first child manifest to the index
// (contract §8.2).
var indexAnnotationKeys = []string{
	spec.AnnotationCreated,
	spec.AnnotationVersion,
	spec.AnnotationSource,
	spec.AnnotationDescription,
}

// Index builds a contract index over child manifests already in store,
// pushes it and returns its descriptor. ann overrides the annotations copied
// from the first child.
func Index(
	ctx context.Context,
	store content.Storage,
	children []ocispec.Descriptor,
	ann map[string]string,
) (ocispec.Descriptor, error) {
	idx := ocispec.Index{
		Versioned:    specsVersioned(),
		MediaType:    spec.MediaTypeIndex,
		ArtifactType: spec.ArtifactType,
		Annotations:  map[string]string{},
	}
	for i, child := range children {
		d, err := Describe(ctx, store, child)
		if err != nil {
			return ocispec.Descriptor{}, err
		}
		if i == 0 {
			for _, k := range indexAnnotationKeys {
				if v := d.Annotations[k]; v != "" {
					idx.Annotations[k] = v
				}
			}
		}
		p := d.Platform
		ga := spec.GuestAnnotations(d.Config)
		delete(ga, spec.AnnotationDiskTotalSize)
		idx.Manifests = append(idx.Manifests, ocispec.Descriptor{
			MediaType:    spec.MediaTypeManifest,
			ArtifactType: spec.ArtifactType,
			Digest:       child.Digest,
			Size:         child.Size,
			Platform:     &p,
			Annotations:  ga,
		})
	}
	for k, v := range ann {
		idx.Annotations[k] = v
	}
	raw, err := json.Marshal(idx)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("encode index: %w", err)
	}
	if _, err := spec.InspectIndex(raw); err != nil {
		return ocispec.Descriptor{}, err //nolint:wrapcheck // spec errors carry the rule list
	}
	desc := content.NewDescriptorFromBytes(spec.MediaTypeIndex, raw)
	desc.ArtifactType = spec.ArtifactType
	desc.Annotations = idx.Annotations
	if err := pushBytes(ctx, store, desc, raw); err != nil {
		return ocispec.Descriptor{}, err
	}
	return desc, nil
}

// Describe fetches a manifest and its config from f and inspects them.
func Describe(
	ctx context.Context,
	f content.Fetcher,
	m ocispec.Descriptor,
) (spec.Description, error) {
	mraw, err := content.FetchAll(ctx, f, m)
	if err != nil {
		return spec.Description{}, fmt.Errorf("fetch manifest %s: %w", m.Digest, err)
	}
	var man ocispec.Manifest
	if err := json.Unmarshal(mraw, &man); err != nil {
		return spec.Description{}, fmt.Errorf("decode manifest %s: %w", m.Digest, err)
	}
	if man.Config.Size > spec.MaxConfigSize {
		return spec.Description{}, fmt.Errorf(
			"%w: config of %s exceeds %d bytes",
			spec.ErrInvalid,
			m.Digest,
			spec.MaxConfigSize,
		)
	}
	craw, err := content.FetchAll(ctx, f, man.Config)
	if err != nil {
		return spec.Description{}, fmt.Errorf("fetch config of %s: %w", m.Digest, err)
	}
	d, err := spec.Inspect(mraw, craw)
	if err != nil {
		return d, fmt.Errorf("manifest %s: %w", m.Digest, err)
	}
	return d, nil
}
