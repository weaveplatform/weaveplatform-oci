package verify

import (
	"github.com/opencontainers/go-digest"
	sgverify "github.com/sigstore/sigstore-go/pkg/verify"
)

// VerifyEntity exposes keyless verification of an in-memory signed entity
// (the virtual Sigstore's test entities are not serialisable bundles).
func (id *Identity) VerifyEntity(e sgverify.SignedEntity, subject digest.Digest) (*SignatureResult, error) {
	return id.verifyEntity(e, subject)
}
