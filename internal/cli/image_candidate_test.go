package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/testing/data"

	"github.com/weaveplatform/weaveplatform-oci/pkg/channel"
	"github.com/weaveplatform/weaveplatform-oci/pkg/client"
	"github.com/weaveplatform/weaveplatform-oci/pkg/imagecheck"
)

func candidatePolicyFixture(t *testing.T) (string, imageCandidatePolicy) {
	t.Helper()
	dir := t.TempDir()
	root, err := data.TrustedRoot(t, "public-good.json").MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "root.json"), root, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, imageCandidatePolicy{
		Registry:    "ghcr.io",
		TrustedRoot: "root.json",
		Build: identityPolicy{
			Issuer:        "https://token.actions.githubusercontent.com",
			SubjectRegexp: "^build$",
		},
		Acceptance: identityPolicy{
			Issuer:        "https://token.actions.githubusercontent.com",
			SubjectRegexp: "^acceptance$",
		},
	}
}

func writeCandidatePolicy(t *testing.T, dir string, p imageCandidatePolicy) string {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestCandidatePolicyRejectsPromotionConfiguration(t *testing.T) {
	dir, p := candidatePolicyFixture(t)
	file := writeCandidatePolicy(t, dir, p)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"channel", "anchors", "parentTags", "unknown"} {
		t.Run(field, func(t *testing.T) {
			invalid := strings.TrimSuffix(string(raw), "}") + `,"` + field + `":null}`
			if err := os.WriteFile(file, []byte(invalid), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadCandidatePolicy(file); err == nil {
				t.Fatalf("ignored forbidden policy field %s", field)
			}
		})
	}
	for _, invalid := range []string{"{", "{}", "null", string(raw) + " {}", string(raw) + " trailing", strings.Replace(string(raw), `"issuer":`, `"unknown":`, 1)} {
		if err := os.WriteFile(file, []byte(invalid), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadCandidatePolicy(file); err == nil {
			t.Fatalf("accepted invalid policy %s", invalid)
		}
	}
	if _, err := loadCandidatePolicy(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("accepted missing policy")
	}
}

func TestCandidatePolicyRequiresAndResolvesReviewedTrust(t *testing.T) {
	for _, mode := range []string{"relative", "absolute", "registry", "root", "issuer", "acceptance issuer", "prefix", "suffix", "regexp", "missing root", "invalid root"} {
		t.Run(mode, func(t *testing.T) {
			dir, p := candidatePolicyFixture(t)
			switch mode {
			case "absolute":
				p.TrustedRoot = filepath.Join(dir, p.TrustedRoot)
			case "registry":
				p.Registry = ""
			case "root":
				p.TrustedRoot = ""
			case "issuer":
				p.Build.Issuer = ""
			case "acceptance issuer":
				p.Acceptance.Issuer = ""
			case "prefix":
				p.Build.SubjectRegexp = "build$"
			case "suffix":
				p.Acceptance.SubjectRegexp = "^acceptance"
			case "regexp":
				p.Acceptance.SubjectRegexp = "^[$"
			case "missing root":
				p.TrustedRoot = "missing.json"
			case "invalid root":
				if err := os.WriteFile(
					filepath.Join(dir, p.TrustedRoot),
					[]byte(`{}`),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			}
			config, err := loadCandidatePolicy(writeCandidatePolicy(t, dir, p))
			if mode != "relative" && mode != "absolute" && mode != "missing root" &&
				mode != "invalid root" {
				if err == nil {
					t.Fatal("invalid policy accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			policy, err := config.candidate()
			if mode == "missing root" || mode == "invalid root" {
				if err == nil {
					t.Fatal("invalid trusted root accepted")
				}
				return
			}
			if err != nil || config.TrustedRoot != filepath.Join(dir, "root.json") {
				t.Fatal(config, err)
			}
			if policy.Registry != p.Registry || policy.Build.Trusted == nil || policy.Acceptance.Trusted == nil ||
				!policy.Build.RequireSCT || !policy.Acceptance.RequireSCT ||
				policy.Build.Issuer != p.Build.Issuer || policy.Build.SubjectRegexp != p.Build.SubjectRegexp ||
				policy.Acceptance.Issuer != p.Acceptance.Issuer ||
				policy.Acceptance.SubjectRegexp != p.Acceptance.SubjectRegexp {
				t.Fatal("reviewed trust changed", policy)
			}
		})
	}
}

func TestCandidateCommandRoutesOnlyReviewedPolicyAndEmitsVerifiedResult(t *testing.T) {
	for _, mode := range []string{"success", "arguments", "flags", "policy", "trust", "entry", "verification", "output"} {
		t.Run(mode, func(t *testing.T) {
			dir, config := candidatePolicyFixture(t)
			policy := writeCandidatePolicy(t, dir, config)
			entry := filepath.Join(dir, "entry.json")
			if err := os.WriteFile(
				entry,
				[]byte(`{"repository":"images/base","tag":"r1","digest":"sha256:abc"}`),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(dir, "audit")
			args := []string{entry, "--policy", policy, "--out", out}
			switch mode {
			case "arguments":
				args = nil
			case "flags":
				args = []string{entry}
			case "policy":
				args[2] = filepath.Join(dir, "missing")
			case "trust":
				if err := os.Remove(filepath.Join(dir, "root.json")); err != nil {
					t.Fatal(err)
				}
			case "entry":
				args[0] = filepath.Join(dir, "missing-entry")
			}
			verified, emitted := false, false
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cmd := newImageCandidateWithVerifier(func(v any) error {
				emitted = true
				result, ok := v.(imagecheck.CandidateEvidence)
				if !verified || !ok || result.IndexDigest != "authenticated-index" {
					t.Fatal("emitted unauthenticated or different result", v)
				}
				if mode == "output" {
					return io.ErrClosedPipe
				}
				return nil
			}, func(actual context.Context, p imagecheck.CandidatePolicy, c *client.Client, e channel.Image, layout string) (imagecheck.CandidateEvidence, error) {
				verified = true
				if actual != ctx || c.Profile().Registry.Host != "ghcr.io" || c.Profile().Registry.PlainHTTP ||
					p.Registry != "ghcr.io" || p.Build.SubjectRegexp != "^build$" || p.Acceptance.SubjectRegexp != "^acceptance$" ||
					!p.Build.RequireSCT || !p.Acceptance.RequireSCT || e.Repository != "images/base" || e.Tag != "r1" ||
					e.Digest != "sha256:abc" ||
					layout != out {
					t.Fatal("command changed its reviewed inputs", p, e, layout)
				}
				if mode == "verification" {
					return imagecheck.CandidateEvidence{}, imagecheck.ErrEvidence
				}
				return imagecheck.CandidateEvidence{IndexDigest: "authenticated-index"}, nil
			})
			cmd.SetContext(ctx)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(args)
			err := cmd.Execute()
			if (err == nil) != (mode == "success") {
				t.Fatal(mode, err)
			}
			if mode == "verification" && (!errors.Is(err, imagecheck.ErrEvidence) || emitted) {
				t.Fatal("verification failure swallowed", err)
			}
			if mode == "output" && !errors.Is(err, io.ErrClosedPipe) {
				t.Fatal("output failure swallowed", err)
			}
			if (mode == "success" || mode == "output") != emitted {
				t.Fatal("unexpected result emission", mode, emitted)
			}
		})
	}
}
