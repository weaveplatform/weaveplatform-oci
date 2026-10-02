package verify_test

import (
	"errors"
	"testing"

	"github.com/opencontainers/go-digest"

	"github.com/weaveplatform/weaveplatform-oci/pkg/verify"
)

// Regression: a nil trusted root used to panic inside sigstore-go.
func TestIdentityWithoutTrustedRootIsAConfigError(t *testing.T) {
	id := &verify.Identity{Issuer: "i", SubjectRegexp: ".*"}
	if _, err := id.VerifyEntity(nil, digest.FromString("x")); !errors.Is(err, verify.ErrConfig) {
		t.Fatalf("want ErrConfig, got %v", err)
	}
}
