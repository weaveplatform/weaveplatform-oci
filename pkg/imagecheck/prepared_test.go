package imagecheck

import (
	"strings"
	"testing"

	"github.com/weaveplatform/weaveplatform-oci/pkg/conformance"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

func preparedFixture() (Report, conformance.Report) {
	r, image := currentFixture("darwin", false, false)
	cfg := &image.Children[0].Description.Config
	cfg.Guest.Variant = spec.TierPrepared
	cfg.Provisioning = spec.Provisioning{DefaultUser: "weave", CredentialHint: "baked"}
	cfg.Build.Base = &spec.BaseImage{
		Name:   "ghcr.io/weaveplatform/weave-images/macos-26-base",
		Digest: "sha256:" + strings.Repeat("c", 64),
	}
	for n := range r.Platforms["darwin/arm64"] {
		b := &r.Platforms["darwin/arm64"][n]
		b.Profile = ValidationProfile(*cfg)
		b.Checks["ssh-host-key-after-reboot"] = true
		b.Identities["sshHostKeyDigest"] = "sha256:" + strings.Repeat([]string{"a", "b"}[n], 64)
		b.Operations, b.RebootOperations = map[string]Outcome{}, map[string]Outcome{}
		for _, operations := range []map[string]Outcome{b.Operations, b.RebootOperations} {
			for _, name := range []string{"account-login", "administrator", "ssh", "automatic-login", "desktop-session", "setup-complete", "agent-absent"} {
				operations[name] = Outcome{Status: Passed}
			}
		}
	}
	return r, image
}

func TestPreparedProfileRequiresBothBoots(t *testing.T) {
	r, image := preparedFixture()
	if err := CheckPromotion(encoded(t, r), image, "r1"); err != nil {
		t.Fatal(err)
	}
	for _, reboot := range []bool{false, true} {
		for _, name := range []string{"account-login", "administrator", "ssh", "automatic-login", "desktop-session", "setup-complete", "agent-absent", "extra"} {
			for _, status := range []string{Skipped, Failed, ExpectedUnavailable, ""} {
				r, image := preparedFixture()
				ops := r.Platforms["darwin/arm64"][1].Operations
				if reboot {
					ops = r.Platforms["darwin/arm64"][1].RebootOperations
				}
				ops[name] = Outcome{Status: status, Reason: "not performed"}
				if err := CheckPromotion(encoded(t, r), image, "r1"); err == nil {
					t.Fatal(reboot, name, status)
				}
			}
		}
	}
}

func TestPreparedProfileRejectsWeakerEvidence(t *testing.T) {
	for name, mutate := range map[string]func(*Report, *spec.Config){
		"reboot host key": func(r *Report, _ *spec.Config) {
			r.Platforms["darwin/arm64"][0].Checks["ssh-host-key-after-reboot"] = false
		},
		"legacy":       func(r *Report, _ *spec.Config) { r.SchemaVersion = 2 },
		"base profile": func(r *Report, _ *spec.Config) { r.Platforms["darwin/arm64"][0].Profile = "base" },
		"wrong OS":     func(_ *Report, c *spec.Config) { c.Guest.OS = "linux" },
		"wrong arch":   func(_ *Report, c *spec.Config) { c.Guest.Arch = "amd64" },
		"agent":        func(_ *Report, c *spec.Config) { c.Provisioning.Agent = &spec.Agent{Name: "weave-agent", Version: "1"} },
		"user":         func(_ *Report, c *spec.Config) { c.Provisioning.DefaultUser = "admin" },
		"credential":   func(_ *Report, c *spec.Config) { c.Provisioning.CredentialHint = "none" },
		"parent":       func(_ *Report, c *spec.Config) { c.Build.Base = nil },
		"shared host key": func(r *Report, _ *spec.Config) {
			r.Platforms["darwin/arm64"][1].Identities["sshHostKeyDigest"] = r.Platforms["darwin/arm64"][0].Identities["sshHostKeyDigest"]
		},
		"missing host key": func(r *Report, _ *spec.Config) { delete(r.Platforms["darwin/arm64"][0].Identities, "sshHostKeyDigest") },
	} {
		t.Run(name, func(t *testing.T) {
			r, image := preparedFixture()
			mutate(&r, &image.Children[0].Description.Config)
			if err := Check(encoded(t, r), image, "r1"); err == nil {
				t.Fatal("accepted incomplete prepared evidence")
			}
		})
	}
}
