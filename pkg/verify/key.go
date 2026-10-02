package verify

import (
	"crypto"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	sgverify "github.com/sigstore/sigstore-go/pkg/verify"
	"github.com/sigstore/sigstore/pkg/cryptoutils"
	"github.com/sigstore/sigstore/pkg/signature"

	"github.com/deploymenttheory/weaveplatform-oci/pkg/profile"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/sign"
)

// KeySet is the public keys trusted for cosign-key signatures, matched by
// cosign's key hint so that rotated keys can coexist.
type KeySet struct {
	keys map[string]*root.ExpiringKey
}

// NewKeySet builds a key set from public keys.
func NewKeySet(keys ...crypto.PublicKey) (*KeySet, error) {
	ks := &KeySet{keys: map[string]*root.ExpiringKey{}}
	for _, k := range keys {
		der, err := x509.MarshalPKIXPublicKey(k)
		if err != nil {
			return nil, fmt.Errorf("%w: public key: %w", ErrConfig, err)
		}
		v, err := signature.LoadVerifier(k, crypto.SHA256)
		if err != nil {
			return nil, fmt.Errorf("%w: public key: %w", ErrConfig, err)
		}
		ks.keys[sign.KeyHint(der)] = root.NewExpiringKey(v, time.Time{}, time.Time{})
	}
	if len(ks.keys) == 0 {
		return nil, fmt.Errorf("%w: no public keys", ErrConfig)
	}
	return ks, nil
}

// LoadKeySet reads PEM public key files (cosign.pub).
func LoadKeySet(paths ...string) (*KeySet, error) {
	keys := make([]crypto.PublicKey, 0, len(paths))
	for _, p := range paths {
		raw, err := os.ReadFile(p) //nolint:gosec // operator-configured key path
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrConfig, err)
		}
		k, err := cryptoutils.UnmarshalPEMToPublicKey(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrConfig, p, err)
		}
		keys = append(keys, k)
	}
	return NewKeySet(keys...)
}

// Verify checks one cosign-key bundle against subject: the DSSE signature
// with a trusted key (no transparency log, no timestamps), the in-toto
// subject digest, and the cosign signing predicate type.
func (ks *KeySet) Verify(bundleJSON []byte, subject digest.Digest) (*SignatureResult, error) {
	var b bundle.Bundle
	if err := b.UnmarshalJSON(bundleJSON); err != nil {
		return nil, fmt.Errorf("%w: bundle: %w", ErrUnverified, err)
	}
	vc, err := b.VerificationContent()
	if err != nil {
		return nil, fmt.Errorf("%w: bundle: %w", ErrUnverified, err)
	}
	pk, ok := vc.(interface{ Hint() string })
	if !ok {
		return nil, fmt.Errorf("%w: bundle is not signed with a public key", ErrUnverified)
	}
	hint := pk.Hint()
	tm := root.NewTrustedPublicKeyMaterialFromMapping(ks.keys)
	v, err := sgverify.NewVerifier(tm, sgverify.WithNoObserverTimestamps())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	raw, err := hex.DecodeString(subject.Encoded())
	if err != nil || subject.Algorithm() != digest.SHA256 {
		return nil, fmt.Errorf("%w: subject %q is not a sha256 digest", ErrConfig, subject)
	}
	res, err := v.Verify(
		&b,
		sgverify.NewPolicy(sgverify.WithArtifactDigest("sha256", raw), sgverify.WithKey()),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnverified, err)
	}
	if res.Statement == nil || res.Statement.GetPredicateType() != sign.PredicateType {
		return nil, fmt.Errorf("%w: predicate type is not %s", ErrUnverified, sign.PredicateType)
	}
	return &SignatureResult{Provider: string(profile.SigningCosignKey), KeyID: hint}, nil
}
