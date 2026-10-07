// Package imagecheck checks image acceptance evidence against the exact OCI
// subject. It does not authenticate reports: publication workflows must attest
// them, and promotion must verify that attestation before calling Check.
package imagecheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/weaveplatform/weaveplatform-oci/pkg/channel"
	"github.com/weaveplatform/weaveplatform-oci/pkg/conformance"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

var ErrEvidence = errors.New("image acceptance evidence rejected")

// Report preserves the Linux schema-1 report; schema 2 adds native and agent
// checks and requires a manifest binding for every platform.
type Report struct {
	SchemaVersion   int               `json:"schemaVersion"`
	IndexDigest     string            `json:"indexDigest"`
	Tag             string            `json:"tag"`
	Passed          bool              `json:"passed"`
	PlatformDigests map[string]string `json:"platformDigests,omitempty"`
	Platforms       map[string][]Boot `json:"platforms"`
}

// Boot is one fresh clone. Checks are named assertions emitted by the native
// validator. Missing checks, including skipped checks, never count as passed.
type Boot struct {
	Platform       string            `json:"platform"`
	OSVersion      string            `json:"osVersion"`
	OSBuild        string            `json:"osBuild,omitempty"`
	Edition        string            `json:"edition,omitempty"`
	Accelerator    string            `json:"accelerator"`
	Passed         bool              `json:"passed"`
	Marker         string            `json:"marker"`
	MachineID      string            `json:"machineID,omitempty"` //nolint:tagliatelle // Existing report wire format.
	Error          string            `json:"error,omitempty"`
	ElapsedSeconds float64           `json:"elapsedSeconds"`
	Identities     map[string]string `json:"identities,omitempty"`
	Checks         map[string]bool   `json:"checks,omitempty"`
}

var (
	markerPattern    = regexp.MustCompile(`^WEAVE-BOOT-OK-[a-f0-9]{24}$`)
	linuxIdentity    = regexp.MustCompile(`^[a-f0-9]{32}$`)
	keyDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// Check refuses partial, stale or failed evidence. A valid report alone is not
// a signature or channel authorization.
func Check(data []byte, image conformance.Report, tag string) error {
	var r Report
	if err := json.Unmarshal(data, &r); err != nil {
		return fmt.Errorf("%w: decode report: %w", ErrEvidence, err)
	}
	if !image.OK() || len(image.Children) == 0 || image.Root.MediaType != spec.MediaTypeIndex ||
		(r.SchemaVersion != 1 && r.SchemaVersion != 2) || !r.Passed || r.IndexDigest != image.Root.Digest.String() ||
		r.Tag != tag || tag == "" || len(r.Platforms) != len(image.Children) {
		return fmt.Errorf("%w: report must pass and match the complete index and tag", ErrEvidence)
	}
	if r.SchemaVersion == 2 && len(r.PlatformDigests) != len(image.Children) {
		return fmt.Errorf("%w: platform manifest bindings required", ErrEvidence)
	}
	for _, child := range image.Children {
		cfg := child.Description.Config
		platform := cfg.Guest.OS + "/" + cfg.Guest.Arch
		if r.SchemaVersion == 1 &&
			(cfg.Guest.OS != "linux" || cfg.Guest.Variant != "base" || cfg.Provisioning.Agent != nil) {
			return fmt.Errorf("%w: schema 1 only validates Linux base images", ErrEvidence)
		}
		if r.SchemaVersion == 2 && r.PlatformDigests[platform] != child.Descriptor.Digest.String() {
			return fmt.Errorf("%w: wrong manifest for %s", ErrEvidence, platform)
		}
		if err := checkClones(r.Platforms[platform], cfg, r.SchemaVersion); err != nil {
			return err
		}
	}
	return nil
}

func checkClones(clones []Boot, cfg spec.Config, schema int) error {
	platform := cfg.Guest.OS + "/" + cfg.Guest.Arch
	if len(clones) != 2 {
		return fmt.Errorf("%w: %s requires two fresh clones", ErrEvidence, platform)
	}
	for _, c := range clones {
		if !c.Passed || c.Error != "" || c.Platform != platform ||
			c.OSVersion != cfg.Guest.OSVersion ||
			c.MachineID == "" ||
			!markerPattern.MatchString(c.Marker) ||
			c.ElapsedSeconds <= 0 {
			return fmt.Errorf(
				"%w: %s clone did not pass its boot/version/identity checks",
				ErrEvidence,
				platform,
			)
		}
		if cfg.Guest.OS == "linux" &&
			(!linuxIdentity.MatchString(c.MachineID) || c.MachineID == strings.Repeat("0", 32)) {
			return fmt.Errorf("%w: invalid Linux machine identity", ErrEvidence)
		}
		if schema == 2 {
			if c.OSBuild != cfg.Guest.OSBuild || c.Edition != cfg.Guest.Edition {
				return fmt.Errorf("%w: guest build/edition mismatch", ErrEvidence)
			}
			for _, name := range requiredChecks(cfg) {
				if !c.Checks[name] {
					return fmt.Errorf(
						"%w: %s missing successful check %s",
						ErrEvidence,
						platform,
						name,
					)
				}
			}
		}
	}
	if clones[0].MachineID == clones[1].MachineID || clones[0].Marker == clones[1].Marker {
		return fmt.Errorf("%w: clones reused an identity or boot challenge", ErrEvidence)
	}
	if schema == 2 {
		identities := requiredIdentities(cfg.Guest.OS)
		if cfg.Provisioning.Agent != nil {
			identities = append(identities, "agentStoreKeyDigest")
		}
		for _, name := range identities {
			a, b := clones[0].Identities[name], clones[1].Identities[name]
			if name == "agentStoreKeyDigest" &&
				(!keyDigestPattern.MatchString(a) || !keyDigestPattern.MatchString(b)) {
				return fmt.Errorf(
					"%w: agent clones require store-key SHA-256 evidence",
					ErrEvidence,
				)
			}
			if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" || strings.EqualFold(a, b) {
				return fmt.Errorf("%w: clones need distinct %s", ErrEvidence, name)
			}
		}
	}
	return nil
}

func requiredIdentities(os string) []string {
	switch os {
	case "windows":
		return []string{"machineGUID", "machineSID", "vmID", "macAddress"}
	case "darwin":
		return []string{"hardwareUUID", "machineIdentifier", "macAddress"}
	default:
		return []string{"machineID"}
	}
}

func requiredChecks(cfg spec.Config) []string {
	names := []string{
		"boot",
		"shutdown",
		"os-version",
		"reboot-identity",
		"fresh-firmware",
		"no-build-credentials",
	}
	if cfg.Provisioning.Agent == nil {
		return append(names, "agent-absent")
	}
	names = append(
		names,
		"agent-startup",
		"agent-version",
		"host-trust",
		"reject-cross-clone-trust",
		"trust-after-reboot",
		"fresh-agent-store",
		"manifest-sequence",
	)
	for _, module := range []string{"presence", "exec", "power", "time", "metrics", "clipboard", "session", "display"} {
		names = append(names, "module-"+module+"-installed", "module-"+module+"-operation")
	}
	return names
}

// CheckParents enforces platform-manifest lineage against a channel whose
// signature has already been verified by the caller. Index digests cannot
// substitute for a parent platform digest.
func CheckParents(image conformance.Report, promoted channel.Manifest, registry string) error {
	if !image.OK() || len(image.Children) == 0 {
		return fmt.Errorf("%w: invalid candidate", ErrEvidence)
	}
	for _, child := range image.Children {
		cfg := child.Description.Config
		if cfg.Build.Base == nil {
			continue
		}
		name := strings.TrimPrefix(cfg.Build.Base.Name, registry+"/")
		found := false
		for _, parent := range promoted.Images {
			if parent.Repository != name {
				continue
			}
			for _, p := range parent.Platforms {
				if p.OS == cfg.Guest.OS && p.Arch == cfg.Guest.Arch &&
					p.Digest == cfg.Build.Base.Digest {
					found = true
				}
			}
		}
		if !found {
			return fmt.Errorf(
				"%w: parent %s@%s is not promoted for %s/%s",
				ErrEvidence,
				name,
				cfg.Build.Base.Digest,
				cfg.Guest.OS,
				cfg.Guest.Arch,
			)
		}
	}
	return nil
}
