package verify

import (
	"context"
	"fmt"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
)

// StoreSource is a Source over a local graph store (the device cache or an
// imported OCI layout), where referrers are the subject's predecessors.
type StoreSource struct {
	Store content.ReadOnlyGraphStorage
}

// Referrers returns predecessors whose manifest has the given artifactType.
func (s StoreSource) Referrers(ctx context.Context, subject ocispec.Descriptor, artifactType string) ([]ocispec.Descriptor, error) {
	preds, err := s.Store.Predecessors(ctx, subject)
	if err != nil {
		return nil, fmt.Errorf("predecessors of %s: %w", subject.Digest, err)
	}
	var out []ocispec.Descriptor
	for _, p := range preds {
		raw, err := content.FetchAll(ctx, s.Store, p)
		if err != nil {
			return nil, fmt.Errorf("referrer %s: %w", p.Digest, err)
		}
		m, err := decodeManifest(raw)
		if err != nil || m.Subject == nil {
			continue
		}
		if artifactType == "" || m.ArtifactType == artifactType {
			p.ArtifactType = m.ArtifactType
			out = append(out, p)
		}
	}
	return out, nil
}

// FetchAll reads a manifest or blob, verified against its descriptor.
func (s StoreSource) FetchAll(ctx context.Context, d ocispec.Descriptor) ([]byte, error) {
	b, err := content.FetchAll(ctx, s.Store, d)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", d.Digest, err)
	}
	return b, nil
}
