package imagecheck

import (
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"

	"github.com/weaveplatform/weaveplatform-oci/pkg/verify"
)

func TestCandidateEvidenceDoesNotAuthorizePromotion(t *testing.T) {
	authority, err := ca.NewVirtualSigstore()
	require(t, err)
	promoted := admissionPolicy(t, authority)
	policy := CandidatePolicy{
		Build:      promoted.Build,
		Acceptance: promoted.Acceptance,
		Registry:   promoted.Registry,
	}
	report, image := currentFixture("linux", false, false)
	build := signedPredicate(
		t,
		authority,
		image.Root.Digest,
		verify.SLSAProvenanceV1,
		map[string]any{},
	)
	accepted := signedPredicate(t, authority, image.Root.Digest, AcceptancePredicate, report)
	evidence, err := VerifyCandidate(policy, image, "r1", build, accepted)
	require(t, err)
	if evidence.IndexDigest != image.Root.Digest.String() || evidence.BuildSigner == "" ||
		evidence.AcceptanceSigner == "" ||
		evidence.Report.SchemaVersion != 3 {
		t.Fatal(evidence)
	}
	// No channel keys or bundle are necessary for candidate evidence. They remain
	// mandatory for promotion even for parentless bases.
	withoutChannel := AdmissionPolicy{
		Build:      policy.Build,
		Acceptance: policy.Acceptance,
		Registry:   policy.Registry,
	}
	if _, err := Admit(withoutChannel, image, "r1", build, accepted); err == nil {
		t.Fatal("promoted with no release authority")
	}
	for _, mode := range []string{"wrong issuer", "wrong subject", "wrong root", "wrong digest", "wrong tag", "wrong build predicate", "legacy", "failed", "duplicate clone"} {
		t.Run(mode, func(t *testing.T) {
			p := policy
			id := *policy.Acceptance
			p.Acceptance = &id
			r, im := currentFixture("linux", false, false)
			b, a, tag := build, accepted, "r1"
			switch mode {
			case "wrong issuer":
				id.Issuer = "https://untrusted.invalid"
			case "wrong subject":
				id.SubjectRegexp = "^https://untrusted.invalid$"
			case "wrong root":
				id.Trusted, err = ca.NewVirtualSigstore()
				require(t, err)
			case "wrong digest":
				im.Root.Digest = digest.FromString("different")
			case "wrong tag":
				tag = "other"
			case "wrong build predicate":
				b = accepted
			case "legacy":
				r.SchemaVersion = 2
				a = signedPredicate(t, authority, im.Root.Digest, AcceptancePredicate, r)
			case "failed":
				r.Passed = false
				a = signedPredicate(t, authority, im.Root.Digest, AcceptancePredicate, r)
			case "duplicate clone":
				for platform, clones := range r.Platforms {
					clones[1] = clones[0]
					r.Platforms[platform] = clones
				}
				a = signedPredicate(t, authority, im.Root.Digest, AcceptancePredicate, r)
			}
			if _, err := VerifyCandidate(p, im, tag, b, a); err == nil {
				t.Fatal("accepted invalid candidate evidence")
			}
		})
	}
}
