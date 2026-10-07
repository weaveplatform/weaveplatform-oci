package imagecheck

import (
	"fmt"
	"strings"

	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

// Outcome distinguishes a performed operation from an expected limitation.
// An expected limitation is never equivalent to a working desktop operation.
type Outcome struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

const (
	Passed              = "passed"
	ExpectedUnavailable = "expected-unavailable"
	Failed              = "failed"
	Skipped             = "skipped"
)

// ValidationProfile derives the required profile from signed image metadata;
// a report cannot opt a desktop image into weaker headless checks.
func ValidationProfile(cfg spec.Config) string {
	if cfg.Provisioning.Agent == nil {
		return "base"
	}
	if cfg.Guest.OS == "linux" && cfg.Guest.Variant != "desktop" {
		return "agent-headless"
	}
	return "agent-desktop"
}

func checkOutcomes(b Boot, cfg spec.Config) error {
	if (cfg.Provisioning.Agent == nil && cfg.Guest.Variant != "base") ||
		(cfg.Provisioning.Agent != nil && cfg.Guest.Variant != "agent" && cfg.Guest.Variant != "desktop") {
		return fmt.Errorf("%w: image tier and agent declaration disagree", ErrEvidence)
	}
	profile := ValidationProfile(cfg)
	if b.Profile != profile {
		return fmt.Errorf("%w: validation profile must be %s", ErrEvidence, profile)
	}
	if profile == "base" {
		if len(b.Operations) != 0 || len(b.RebootOperations) != 0 {
			return fmt.Errorf("%w: base image cannot claim agent operations", ErrEvidence)
		}
		return nil
	}
	for _, operations := range []map[string]Outcome{b.Operations, b.RebootOperations} {
		for _, capability := range []string{"presence", "exec", "time", "metrics", "clipboard", "session", "display"} {
			outcome := operations[capability]
			if outcome.Status == Passed {
				continue
			}
			if profile == "agent-headless" && outcome.Status == ExpectedUnavailable &&
				strings.TrimSpace(outcome.Reason) != "" &&
				(capability == "clipboard" || capability == "session" || capability == "display") {
				continue
			}
			return fmt.Errorf(
				"%w: %s operation %s is not accepted",
				ErrEvidence,
				profile,
				capability,
			)
		}
		if profile == "agent-desktop" {
			for _, operation := range []string{"clipboard-roundtrip", "display-roundtrip", "desktop-session"} {
				if operations[operation].Status != Passed {
					return fmt.Errorf("%w: desktop requires %s", ErrEvidence, operation)
				}
			}
		}
		// Unknown outcomes must not hide a failed extra operation.
		for name, outcome := range operations {
			if outcome.Status != Passed && !(profile == "agent-headless" &&
				outcome.Status == ExpectedUnavailable && strings.TrimSpace(outcome.Reason) != "" &&
				(name == "clipboard" || name == "session" || name == "display")) {
				return fmt.Errorf(
					"%w: rejected operation %s: %s",
					ErrEvidence,
					name,
					outcome.Status,
				)
			}
		}
	}
	// Reboot/shutdown are observed by the lifecycle adapter, not an RPC reply.
	return nil
}
