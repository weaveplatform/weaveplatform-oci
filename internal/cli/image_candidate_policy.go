package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/weaveplatform/weaveplatform-oci/pkg/imagecheck"
	"github.com/weaveplatform/weaveplatform-oci/pkg/verify"
)

// Candidate verification deliberately has no channel or parent selector fields.
// Supplying promotion policy here is an error, never an instruction to ignore it.
type imageCandidatePolicy struct {
	Registry    string         `json:"registry"`
	TrustedRoot string         `json:"trustedRoot"`
	Build       identityPolicy `json:"build"`
	Acceptance  identityPolicy `json:"acceptance"`
}

func loadCandidatePolicy(file string) (imageCandidatePolicy, error) {
	var p imageCandidatePolicy
	f, err := os.Open(file)
	if err != nil {
		return p, fmt.Errorf("candidate policy: %w", err)
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, fmt.Errorf("candidate policy: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return p, fmt.Errorf("%w: candidate policy must contain one JSON object", errUsage)
	}
	if p.Registry == "" || p.TrustedRoot == "" {
		return p, fmt.Errorf("%w: registry and trustedRoot required", errUsage)
	}
	for _, id := range []identityPolicy{p.Build, p.Acceptance} {
		if id.Issuer == "" || !strings.HasPrefix(id.SubjectRegexp, "^") ||
			!strings.HasSuffix(id.SubjectRegexp, "$") {
			return p, fmt.Errorf(
				"%w: explicit issuer and anchored workflow identity required",
				errUsage,
			)
		}
		if _, err := regexp.Compile(id.SubjectRegexp); err != nil {
			return p, fmt.Errorf("%w: candidate workflow identity: %w", errUsage, err)
		}
	}
	if !filepath.IsAbs(p.TrustedRoot) {
		p.TrustedRoot = filepath.Join(filepath.Dir(file), p.TrustedRoot)
	}
	return p, nil
}

func (config imageCandidatePolicy) candidate() (imagecheck.CandidatePolicy, error) {
	trusted, err := verify.LoadTrustedRoot(config.TrustedRoot)
	if err != nil {
		return imagecheck.CandidatePolicy{}, fmt.Errorf("candidate trust: %w", err)
	}
	return imagecheck.CandidatePolicy{
		Registry: config.Registry,
		Build: &verify.Identity{
			Trusted: trusted, Issuer: config.Build.Issuer,
			SubjectRegexp: config.Build.SubjectRegexp, RequireSCT: true,
		},
		Acceptance: &verify.Identity{
			Trusted: trusted, Issuer: config.Acceptance.Issuer,
			SubjectRegexp: config.Acceptance.SubjectRegexp, RequireSCT: true,
		},
	}, nil
}
