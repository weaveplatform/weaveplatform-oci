package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/weaveplatform/weaveplatform-oci/pkg/channel"
	"github.com/weaveplatform/weaveplatform-oci/pkg/conformance"
	"github.com/weaveplatform/weaveplatform-oci/pkg/imagecheck"
	"github.com/weaveplatform/weaveplatform-oci/pkg/verify"
)

// Admission configuration is intentionally separate from the untrusted
// publication dispatch. Paths resolve relative to this reviewed policy file.
type imageAdmissionPolicy struct {
	Registry    string            `json:"registry"`
	TrustedRoot string            `json:"trustedRoot"`
	Build       identityPolicy    `json:"build"`
	Acceptance  identityPolicy    `json:"acceptance"`
	Channel     string            `json:"channel"`
	Anchors     []admissionAnchor `json:"anchors"`
	ParentTags  map[string]string `json:"parentTags"`
}

type identityPolicy struct {
	Issuer        string `json:"issuer"`
	SubjectRegexp string `json:"subjectRegexp"`
}

type admissionAnchor struct {
	Name      string `json:"name"`
	PublicKey string `json:"publicKey"`
}

func newImageAdmission(emit func(any) error) *cobra.Command {
	var policyFile, ref, tag, provenance, acceptance string
	cmd := &cobra.Command{
		Use:   "verify-acceptance LAYOUT",
		Short: "Authenticate build and acceptance evidence against reviewed promotion policy",
		Args:  usageArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if policyFile == "" || tag == "" || provenance == "" || acceptance == "" {
				return fmt.Errorf(
					"%w: --policy, --tag, --provenance and --acceptance required",
					errUsage,
				)
			}
			config, err := loadAdmissionPolicy(policyFile)
			if err != nil {
				return err
			}
			store, root, err := resolveRoot(cmd.Context(), args[0], ref)
			if err != nil {
				return err
			}
			inspection, err := conformance.Check(
				cmd.Context(),
				store,
				root,
				conformance.Options{Deep: true},
			)
			if err != nil {
				return fmt.Errorf("candidate inspection: %w", err)
			}
			trusted, err := verify.LoadTrustedRoot(config.TrustedRoot)
			if err != nil {
				return fmt.Errorf("promotion trust: %w", err)
			}
			p := imagecheck.AdmissionPolicy{
				Registry:   config.Registry,
				ParentTags: config.ParentTags,
				Build: &verify.Identity{
					Trusted:       trusted,
					Issuer:        config.Build.Issuer,
					SubjectRegexp: config.Build.SubjectRegexp,
					RequireSCT:    true,
				},
				Acceptance: &verify.Identity{
					Trusted:       trusted,
					Issuer:        config.Acceptance.Issuer,
					SubjectRegexp: config.Acceptance.SubjectRegexp,
					RequireSCT:    true,
				},
			}
			for _, item := range config.Anchors {
				data, e := os.ReadFile(item.PublicKey)
				if e != nil {
					return fmt.Errorf("promotion anchor: %w", e)
				}
				anchor, e := channel.ParseAnchor(item.Name, data)
				if e != nil {
					return fmt.Errorf("promotion anchor: %w", e)
				}
				p.Anchors = append(p.Anchors, anchor)
			}
			p.Channel, err = channel.Load(cmd.Context(), config.Channel, nil)
			if err != nil {
				return fmt.Errorf("promotion channel: %w", err)
			}
			build, err := os.ReadFile(provenance)
			if err != nil {
				return fmt.Errorf("build attestation: %w", err)
			}
			report, err := os.ReadFile(acceptance)
			if err != nil {
				return fmt.Errorf("acceptance attestation: %w", err)
			}
			result, err := imagecheck.Admit(p, inspection, tag, build, report)
			if err != nil {
				return fmt.Errorf("promotion admission: %w", err)
			}
			return emit(result)
		},
	}
	cmd.Flags().StringVar(&policyFile, "policy", "", "Reviewed admission policy JSON")
	cmd.Flags().StringVar(&ref, "ref", "", "Tag or digest in the local layout")
	cmd.Flags().StringVar(&tag, "tag", "", "Immutable candidate tag bound by the acceptance report")
	cmd.Flags().StringVar(&provenance, "provenance", "", "Build provenance Sigstore bundle")
	cmd.Flags().
		StringVar(&acceptance, "acceptance", "", "Acceptance Sigstore bundle (not a bare report)")
	return cmd
}

func loadAdmissionPolicy(file string) (imageAdmissionPolicy, error) {
	var p imageAdmissionPolicy
	f, err := os.Open(file)
	if err != nil {
		return p, fmt.Errorf("admission policy: %w", err)
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, fmt.Errorf("admission policy: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return p, fmt.Errorf("%w: admission policy must contain one JSON object", errUsage)
	}
	if p.Registry == "" || p.TrustedRoot == "" || p.Channel == "" || len(p.Anchors) == 0 {
		return p, fmt.Errorf("%w: registry, trustedRoot, channel and anchors required", errUsage)
	}
	for _, id := range []identityPolicy{p.Build, p.Acceptance} {
		if id.Issuer == "" || !strings.HasPrefix(id.SubjectRegexp, "^") ||
			!strings.HasSuffix(id.SubjectRegexp, "$") {
			return p, fmt.Errorf(
				"%w: explicit issuer and anchored workflow identity required",
				errUsage,
			)
		}
	}
	resolve := func(path string) string {
		if filepath.IsAbs(path) || strings.HasPrefix(path, "https://") ||
			strings.HasPrefix(path, "http://") {
			return path
		}
		return filepath.Join(filepath.Dir(file), path)
	}
	p.TrustedRoot, p.Channel = resolve(p.TrustedRoot), resolve(p.Channel)
	for n := range p.Anchors {
		if p.Anchors[n].Name == "" || p.Anchors[n].PublicKey == "" {
			return p, fmt.Errorf("%w: named anchor key required", errUsage)
		}
		p.Anchors[n].PublicKey = resolve(p.Anchors[n].PublicKey)
	}
	return p, nil
}
