package imagecheck

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/weaveplatform/weaveplatform-oci/pkg/channel"
	"github.com/weaveplatform/weaveplatform-oci/pkg/conformance"
	"github.com/weaveplatform/weaveplatform-oci/pkg/verify"
)

// AcceptancePredicate is the predicate attached to the exact tested index.
const AcceptancePredicate = "https://weaveplatform.io/attestations/image-acceptance/v1"

// AdmissionPolicy must come from reviewed operator configuration, never from
// an image-published dispatch. ParentTags selects each current parent from the
// verified channel; historical channel membership alone is insufficient.
type AdmissionPolicy struct {
	Build, Acceptance *verify.Identity
	Anchors           []channel.Anchor
	Channel           channel.Bundle
	ChannelOptions    channel.Options
	Registry          string
	ParentTags        map[string]string
}

// Admission records the authenticated evidence and channel revision used for
// the decision. Promotion must rerun admission against its current base branch.
type Admission struct {
	IndexDigest      string `json:"indexDigest"`
	BuildSigner      string `json:"buildSigner"`
	AcceptanceSigner string `json:"acceptanceSigner"`
	ChannelSequence  uint64 `json:"channelSequence"`
	Report           Report `json:"report"`
}

// Admit requires authenticated build and schema-3 acceptance statements. The
// caller deeply inspects the candidate before passing its conformance report.
func Admit(
	p AdmissionPolicy,
	image conformance.Report,
	tag string,
	buildBundle, acceptanceBundle []byte,
) (Admission, error) {
	evidence, err := VerifyCandidate(CandidatePolicy{
		Build: p.Build, Acceptance: p.Acceptance, Registry: p.Registry,
	}, image, tag, buildBundle, acceptanceBundle)
	if err != nil {
		return Admission{}, err
	}
	return admitCandidate(p, image, evidence)
}

func admitCandidate(
	p AdmissionPolicy,
	image conformance.Report,
	evidence CandidateEvidence,
) (Admission, error) {
	var result Admission
	promoted, _, err := channel.Verify(p.Anchors, p.Channel, p.ChannelOptions)
	if err != nil {
		return result, fmt.Errorf("promotion channel: %w", err)
	}
	if err := CheckCurrentParents(image, *promoted, p.Registry, p.ParentTags); err != nil {
		return result, err
	}
	result.Report = evidence.Report
	result.IndexDigest = evidence.IndexDigest
	result.BuildSigner, result.AcceptanceSigner = evidence.BuildSigner, evidence.AcceptanceSigner
	result.ChannelSequence = promoted.Sequence
	return result, nil
}

// CheckPromotion applies the current evidence format. Check remains compatible
// with historical reports, but those cannot authorize a new promotion.
func CheckPromotion(data []byte, image conformance.Report, tag string) error {
	if err := Check(data, image, tag); err != nil {
		return err
	}
	var report Report
	_ = json.Unmarshal(data, &report)
	if report.SchemaVersion != 3 {
		return fmt.Errorf("%w: new promotions require schema 3", ErrEvidence)
	}
	return nil
}

// CheckCurrentParents verifies only the policy-selected parent tags. It rejects
// missing/ambiguous selectors instead of guessing from mutable tag ordering.
// The manifest's signature must be authenticated before calling this function.
func CheckCurrentParents(
	image conformance.Report,
	promoted channel.Manifest,
	registry string,
	tags map[string]string,
) error {
	selected := promoted
	selected.Images = nil
	seen := map[string]bool{}
	for _, child := range image.Children {
		base := child.Description.Config.Build.Base
		if base == nil {
			continue
		}
		name := strings.TrimPrefix(base.Name, registry+"/")
		if seen[name] {
			continue
		}
		seen[name] = true
		if tags[name] == "" {
			return fmt.Errorf("%w: current parent selector required for %s", ErrEvidence, name)
		}
		matches := 0
		for _, entry := range promoted.Images {
			if entry.Repository == name && entry.Tag == tags[name] {
				selected.Images = append(selected.Images, entry)
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf(
				"%w: current parent %s:%s is missing or ambiguous",
				ErrEvidence,
				name,
				tags[name],
			)
		}
	}
	return CheckParents(image, selected, registry)
}
