package imagebuild

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/opencontainers/go-digest"
)

// BuildInputs pins the inputs that can change a candidate. PackageDigests
// includes installer evidence and the desktop dependency closure when present.
// Times and event IDs are deliberately excluded from the fingerprint.
type BuildInputs struct {
	Image          string   `json:"image"`
	Platform       string   `json:"platform"`
	Tier           string   `json:"tier"`
	ParentDigest   string   `json:"parentDigest,omitempty"`
	SourceDigest   string   `json:"sourceDigest,omitempty"`
	PackageDigests []string `json:"packageDigests,omitempty"`
	RecipeDigest   string   `json:"recipeDigest"`
	BuilderDigest  string   `json:"builderDigest"`
}

// AcceptedBuild is restored only from authenticated admission records by the
// workflow. The planner is scheduling logic, not an acceptance authority.
type AcceptedBuild struct {
	Image       string `json:"image"`
	Platform    string `json:"platform"`
	Fingerprint string `json:"fingerprint"`
	IndexDigest string `json:"indexDigest"`
}

type RebuildDecision struct {
	BuildInputs
	Fingerprint string `json:"fingerprint"`
	Action      string `json:"action"`
	Reason      string `json:"reason"`
}

// BuildFingerprint normalizes unordered package inputs without mutating them.
func BuildFingerprint(in BuildInputs) (string, error) {
	if !imageNamePattern.MatchString(in.Image) ||
		!slices.Contains(
			[]string{
				"linux/amd64",
				"linux/arm64",
				"darwin/arm64",
				"windows/amd64",
				"windows/arm64",
			},
			in.Platform,
		) ||
		!slices.Contains([]string{"base", "agent", "desktop"}, in.Tier) {
		return "", fmt.Errorf("%w: valid image, platform and tier required", ErrInput)
	}
	if in.Tier == "base" && (in.ParentDigest != "" || in.SourceDigest == "") ||
		in.Tier != "base" && (in.ParentDigest == "" || len(in.PackageDigests) == 0) {
		return "", fmt.Errorf(
			"%w: base requires source media; derived images require parent and packages",
			ErrInput,
		)
	}
	digests := append([]string{in.RecipeDigest, in.BuilderDigest}, in.PackageDigests...)
	for _, optional := range []string{in.ParentDigest, in.SourceDigest} {
		if optional != "" {
			digests = append(digests, optional)
		}
	}
	for _, d := range digests {
		if !shaPattern.MatchString(d) {
			return "", fmt.Errorf("%w: all build inputs must use SHA-256 digests", ErrInput)
		}
	}
	in.PackageDigests = slices.Clone(in.PackageDigests)
	slices.Sort(in.PackageDigests)
	in.PackageDigests = slices.Compact(in.PackageDigests)
	data, err := json.Marshal(in)
	if err != nil {
		return "", fmt.Errorf("encode build inputs: %w", err)
	}
	return digest.FromBytes(data).String(), nil
}

// PlanRebuilds deduplicates repeated events and skips only identical accepted
// inputs. A prior unvalidated candidate must not be supplied as AcceptedBuild.
func PlanRebuilds(inputs []BuildInputs, accepted []AcceptedBuild) ([]RebuildDecision, error) {
	known := map[string]map[string]bool{}
	for _, record := range accepted {
		if !imageNamePattern.MatchString(record.Image) ||
			!shaPattern.MatchString(record.Fingerprint) ||
			!shaPattern.MatchString(record.IndexDigest) {
			return nil, fmt.Errorf("%w: malformed accepted build record", ErrInput)
		}
		key := record.Image + "/" + record.Platform
		if known[key] == nil {
			known[key] = map[string]bool{}
		}
		known[key][record.Fingerprint] = true
	}
	seen := map[string]string{}
	out := make([]RebuildDecision, 0, len(inputs))
	for _, in := range inputs {
		fingerprint, err := BuildFingerprint(in)
		if err != nil {
			return nil, err
		}
		key := in.Image + "/" + in.Platform
		if previous := seen[key]; previous != "" {
			if previous != fingerprint {
				return nil, fmt.Errorf("%w: conflicting inputs for %s", ErrInput, key)
			}
			continue
		}
		seen[key] = fingerprint
		decision := RebuildDecision{
			BuildInputs: in,
			Fingerprint: fingerprint,
			Action:      "build",
			Reason:      "no accepted image for these inputs",
		}
		if known[key][fingerprint] {
			decision.Action, decision.Reason = "skip", "identical inputs already accepted"
		}
		out = append(out, decision)
	}
	slices.SortFunc(out, func(a, b RebuildDecision) int {
		return compareBuildKey(a.Image+"/"+a.Platform, b.Image+"/"+b.Platform)
	})
	return out, nil
}

func compareBuildKey(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
