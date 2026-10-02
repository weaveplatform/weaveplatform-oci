package sign_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"io"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/memory"

	"github.com/weaveplatform/weaveplatform-oci/pkg/sign"
)

// manifestRefusing accepts blobs but refuses manifests; it can also claim
// every blob already exists.
type manifestRefusing struct {
	*memory.Store
	claimExists bool
}

func (m manifestRefusing) Exists(ctx context.Context, d ocispec.Descriptor) (bool, error) {
	if m.claimExists && d.MediaType == sign.BundleMediaType {
		return true, nil
	}
	return m.Store.Exists(ctx, d) //nolint:wrapcheck // test double
}

func (m manifestRefusing) Push(ctx context.Context, d ocispec.Descriptor, r io.Reader) error {
	if d.MediaType == ocispec.MediaTypeImageManifest {
		return errors.New("manifest refused")
	}
	return m.Store.Push(ctx, d, r) //nolint:wrapcheck // test double
}

func TestSignManifestPushFailureAndExistingBundle(t *testing.T) {
	ctx := context.Background()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	s, _ := sign.New(k)
	subj := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageIndex,
		Digest:    digest.FromString("s"),
		Size:      1,
	}
	for _, claim := range []bool{false, true} {
		if _, err := s.Sign(
			ctx,
			manifestRefusing{Store: memory.New(), claimExists: claim},
			subj,
		); err == nil {
			t.Fatalf("claimExists=%v: manifest refusal swallowed", claim)
		}
	}
}
