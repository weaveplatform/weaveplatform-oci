package imagecheck

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/weaveplatform/weaveplatform-oci/pkg/conformance"
	"github.com/weaveplatform/weaveplatform-oci/pkg/verify"
)

// CandidatePolicy authenticates a producer and its acceptance evidence. It must
// come from reviewed configuration, never from an artifact or dispatch. It does
// not authorize release promotion or establish a current release-channel parent.
type CandidatePolicy struct {
	Build, Acceptance *verify.Identity
	Registry          string
}

// CandidateEvidence records verified evidence for immutable candidate bytes.
// It is not a promotion admission and contains no release-channel decision.
type CandidateEvidence struct {
	IndexDigest      string `json:"indexDigest"`
	BuildSigner      string `json:"buildSigner"`
	AcceptanceSigner string `json:"acceptanceSigner"`
	Report           Report `json:"report"`
}

// VerifyCandidate authenticates build provenance and schema-3 acceptance for the
// exact image. The caller must deeply inspect the image before supplying its
// conformance report. Use VerifyCandidatePublished to inspect registry bytes.
func VerifyCandidate(p CandidatePolicy, image conformance.Report, tag string,
	buildBundle, acceptanceBundle []byte,
) (CandidateEvidence, error) {
	var result CandidateEvidence
	if p.Build == nil || p.Acceptance == nil || p.Registry == "" ||
		!image.OK() || len(image.Children) == 0 {
		return result, fmt.Errorf(
			"%w: trusted identities, registry and valid image required",
			ErrEvidence,
		)
	}
	build := *p.Build
	build.PredicateType = verify.SLSAProvenanceV1
	signer, _, err := build.VerifyStatement(buildBundle, image.Root.Digest)
	if err != nil {
		return result, fmt.Errorf("build attestation: %w", err)
	}
	acceptance := *p.Acceptance
	acceptance.PredicateType = AcceptancePredicate
	accepted, statement, err := acceptance.VerifyStatement(acceptanceBundle, image.Root.Digest)
	if err != nil {
		return result, fmt.Errorf("acceptance attestation: %w", err)
	}
	data, err := protojson.Marshal(statement.GetPredicate())
	if err != nil {
		return result, fmt.Errorf("acceptance predicate: %w", err)
	}
	if err := CheckPromotion(data, image, tag); err != nil {
		return result, err
	}
	// CheckPromotion decoded this exact authenticated predicate.
	_ = json.Unmarshal(data, &result.Report)
	result.IndexDigest = image.Root.Digest.String()
	result.BuildSigner, result.AcceptanceSigner = signer.Identity, accepted.Identity
	return result, nil
}
