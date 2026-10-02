package verify

import (
	"encoding/hex"
	"fmt"

	"github.com/opencontainers/go-digest"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	sgverify "github.com/sigstore/sigstore-go/pkg/verify"

	"github.com/deploymenttheory/weaveplatform-oci/pkg/profile"
)

// SLSAProvenanceV1 is the predicate type of GitHub build provenance attestations.
const SLSAProvenanceV1 = "https://slsa.dev/provenance/v1"

// Identity pins a keyless (Fulcio certificate) signing identity, verified
// against a trusted root that includes the transparency log.
type Identity struct {
	Trusted       root.TrustedMaterial
	Issuer        string // https://token.actions.githubusercontent.com
	SubjectRegexp string // the publishing workflow path and ref
	// PredicateType defaults to SLSA provenance v1.
	PredicateType string
	// RequireSCT requires a certificate transparency timestamp in the
	// certificate (true for the public-good Sigstore instance).
	RequireSCT bool
}

// LoadTrustedRoot reads a trusted_root.json (for example from
// `gh attestation trusted-root`), for offline verification.
func LoadTrustedRoot(path string) (root.TrustedMaterial, error) {
	tr, err := root.NewTrustedRootFromPath(path)
	if err != nil {
		return nil, fmt.Errorf("%w: trusted root: %w", ErrConfig, err)
	}
	return tr, nil
}

// Verify checks one attestation bundle against subject: certificate chain,
// transparency-log inclusion and integrated time, identity, and predicate.
func (id *Identity) Verify(bundleJSON []byte, subject digest.Digest) (*SignatureResult, error) {
	var b bundle.Bundle
	if err := b.UnmarshalJSON(bundleJSON); err != nil {
		return nil, fmt.Errorf("%w: bundle: %w", ErrUnverified, err)
	}
	return id.verifyEntity(&b, subject)
}

func (id *Identity) verifyEntity(
	e sgverify.SignedEntity,
	subject digest.Digest,
) (*SignatureResult, error) {
	if id.Trusted == nil {
		// sigstore-go accepts nil trusted material and then panics in Verify
		return nil, fmt.Errorf("%w: attestation verification needs a trusted root", ErrConfig)
	}
	opts := []sgverify.VerifierOption{
		sgverify.WithTransparencyLog(1),
		sgverify.WithObserverTimestamps(1),
	}
	if id.RequireSCT {
		opts = append(opts, sgverify.WithSignedCertificateTimestamps(1))
	}
	v, err := sgverify.NewVerifier(id.Trusted, opts...)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	ci, err := sgverify.NewShortCertificateIdentity(id.Issuer, "", "", id.SubjectRegexp)
	if err != nil {
		return nil, fmt.Errorf("%w: identity: %w", ErrConfig, err)
	}
	raw, err := hex.DecodeString(subject.Encoded())
	if err != nil || subject.Algorithm() != digest.SHA256 {
		return nil, fmt.Errorf("%w: subject %q is not a sha256 digest", ErrConfig, subject)
	}
	res, err := v.Verify(
		e,
		sgverify.NewPolicy(
			sgverify.WithArtifactDigest("sha256", raw),
			sgverify.WithCertificateIdentity(ci),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnverified, err)
	}
	want := id.PredicateType
	if want == "" {
		want = SLSAProvenanceV1
	}
	if res.Statement == nil || res.Statement.GetPredicateType() != want {
		return nil, fmt.Errorf("%w: predicate type is not %s", ErrUnverified, want)
	}
	r := &SignatureResult{Provider: string(profile.SigningGitHubAttestation), Issuer: id.Issuer}
	if res.Signature != nil && res.Signature.Certificate != nil {
		r.Identity = res.Signature.Certificate.SubjectAlternativeName
	}
	return r, nil
}
