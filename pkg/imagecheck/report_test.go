package imagecheck

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/weaveplatform/weaveplatform-oci/pkg/channel"
	"github.com/weaveplatform/weaveplatform-oci/pkg/conformance"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

func fixture(os string, agent bool) (Report, conformance.Report) {
	cfg := spec.Config{
		Guest: spec.Guest{OS: os, Arch: "arm64", OSVersion: "26", OSBuild: "26.1", Variant: "base"},
	}
	if agent {
		cfg.Guest.Variant = "agent"
		cfg.Provisioning.Agent = &spec.Agent{Name: "weave-agent", Version: "1"}
	}
	child := ocispec.Descriptor{Digest: digest.FromString(os)}
	image := conformance.Report{
		Root: ocispec.Descriptor{
			MediaType: spec.MediaTypeIndex,
			Digest:    digest.FromString("index"),
		},
		Children: []conformance.Child{
			{Descriptor: child, Description: spec.Description{Config: cfg}},
		},
	}
	p := os + "/arm64"
	r := Report{
		SchemaVersion:   2,
		IndexDigest:     image.Root.Digest.String(),
		Tag:             "r1",
		Passed:          true,
		PlatformDigests: map[string]string{p: child.Digest.String()},
		Platforms:       map[string][]Boot{},
	}
	for _, id := range []string{"a", "b"} {
		c := Boot{
			Platform:       p,
			OSVersion:      "26",
			OSBuild:        "26.1",
			Passed:         true,
			MachineID:      strings.Repeat(id, 32),
			Marker:         "WEAVE-BOOT-OK-" + strings.Repeat(id, 24),
			ElapsedSeconds: 1,
			Identities:     map[string]string{},
			Checks:         map[string]bool{},
		}
		for _, key := range requiredIdentities(os) {
			c.Identities[key] = id + "-" + key
		}
		if agent {
			c.Identities["agentStoreKeyDigest"] = "sha256:" + strings.Repeat(id, 64)
		}
		for _, key := range requiredChecks(cfg) {
			c.Checks[key] = true
		}
		r.Platforms[p] = append(r.Platforms[p], c)
	}
	return r, image
}

func encoded(t *testing.T, r Report) []byte {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestReportChecksEveryTargetAndTier(t *testing.T) {
	for _, os := range []string{"linux", "windows", "darwin"} {
		for _, agent := range []bool{false, true} {
			r, image := fixture(os, agent)
			if err := Check(encoded(t, r), image, "r1"); err != nil {
				t.Fatal(os, agent, err)
			}
			for _, key := range requiredChecks(image.Children[0].Description.Config) {
				r.Platforms[os+"/arm64"][1].Checks[key] = false
				if err := Check(encoded(t, r), image, "r1"); !errors.Is(err, ErrEvidence) {
					t.Fatal(key, err)
				}
				r.Platforms[os+"/arm64"][1].Checks[key] = true
			}
			r.SchemaVersion = 1
			err := Check(encoded(t, r), image, "r1")
			if (err == nil) != (os == "linux" && !agent) {
				t.Fatal("legacy evidence policy", os, agent, err)
			}
		}
	}
}

func TestReportRejectsPartialStaleAndFailedEvidence(t *testing.T) {
	for name, mutate := range map[string]func(*Report, *conformance.Report){
		"schema":          func(r *Report, _ *conformance.Report) { r.SchemaVersion = 3 },
		"failed":          func(r *Report, _ *conformance.Report) { r.Passed = false },
		"digest":          func(r *Report, _ *conformance.Report) { r.IndexDigest = digest.FromString("other").String() },
		"tag":             func(r *Report, _ *conformance.Report) { r.Tag = "other" },
		"platform absent": func(r *Report, _ *conformance.Report) { delete(r.Platforms, "linux/arm64") },
		"wrong platform": func(r *Report, _ *conformance.Report) {
			r.Platforms["linux/amd64"] = r.Platforms["linux/arm64"]
			delete(r.Platforms, "linux/arm64")
		},
		"manifest absent": func(r *Report, _ *conformance.Report) { r.PlatformDigests = nil },
		"manifest changed": func(r *Report, _ *conformance.Report) {
			r.PlatformDigests["linux/arm64"] = digest.FromString("other").String()
		},
		"one clone":      func(r *Report, _ *conformance.Report) { r.Platforms["linux/arm64"] = r.Platforms["linux/arm64"][:1] },
		"clone failed":   func(r *Report, _ *conformance.Report) { r.Platforms["linux/arm64"][0].Passed = false },
		"clone error":    func(r *Report, _ *conformance.Report) { r.Platforms["linux/arm64"][0].Error = "failed" },
		"clone version":  func(r *Report, _ *conformance.Report) { r.Platforms["linux/arm64"][0].OSVersion = "wrong" },
		"clone build":    func(r *Report, _ *conformance.Report) { r.Platforms["linux/arm64"][0].OSBuild = "wrong" },
		"clone edition":  func(r *Report, _ *conformance.Report) { r.Platforms["linux/arm64"][0].Edition = "wrong" },
		"clone platform": func(r *Report, _ *conformance.Report) { r.Platforms["linux/arm64"][0].Platform = "linux/amd64" },
		"clone marker":   func(r *Report, _ *conformance.Report) { r.Platforms["linux/arm64"][0].Marker = "old" },
		"clone time":     func(r *Report, _ *conformance.Report) { r.Platforms["linux/arm64"][0].ElapsedSeconds = 0 },
		"clone identity": func(r *Report, _ *conformance.Report) { r.Platforms["linux/arm64"][0].MachineID = "bad" },
		"empty identity": func(r *Report, _ *conformance.Report) { r.Platforms["linux/arm64"][0].MachineID = "" },
		"zero identity": func(r *Report, _ *conformance.Report) {
			r.Platforms["linux/arm64"][0].MachineID = strings.Repeat("0", 32)
		},
		"reused identity": func(r *Report, _ *conformance.Report) {
			r.Platforms["linux/arm64"][1].MachineID = r.Platforms["linux/arm64"][0].MachineID
		},
		"reused marker": func(r *Report, _ *conformance.Report) {
			r.Platforms["linux/arm64"][1].Marker = r.Platforms["linux/arm64"][0].Marker
		},
		"identity absent": func(r *Report, _ *conformance.Report) { r.Platforms["linux/arm64"][1].Identities = nil },
		"identity reused": func(r *Report, _ *conformance.Report) {
			r.Platforms["linux/arm64"][1].Identities = r.Platforms["linux/arm64"][0].Identities
		},
		"invalid image": func(_ *Report, i *conformance.Report) { i.IndexProblems = []spec.Problem{{Message: "bad"}} },
		"empty image":   func(_ *Report, i *conformance.Report) { i.Children = nil },
		"not index":     func(_ *Report, i *conformance.Report) { i.Root.MediaType = spec.MediaTypeManifest },
	} {
		t.Run(name, func(t *testing.T) {
			r, i := fixture("linux", false)
			mutate(&r, &i)
			if err := Check(encoded(t, r), i, "r1"); !errors.Is(err, ErrEvidence) {
				t.Fatal("invalid evidence accepted", err)
			}
		})
	}
	_, i := fixture("linux", false)
	if err := Check([]byte("invalid"), i, "r1"); !errors.Is(err, ErrEvidence) {
		t.Fatal(err)
	}
}

func TestAgentReportRequiresIndependentStoreKeys(t *testing.T) {
	for _, key := range []string{"", "secret", "sha256:" + strings.Repeat("a", 64)} {
		r, i := fixture("windows", true)
		r.Platforms["windows/arm64"][1].Identities["agentStoreKeyDigest"] = key
		if err := Check(encoded(t, r), i, "r1"); !errors.Is(err, ErrEvidence) {
			t.Fatal("accepted missing, invalid or reused agent store key", err)
		}
	}
}

func TestParentPromotionUsesRepositoryPlatformAndChildDigest(t *testing.T) {
	_, i := fixture("windows", true)
	d := digest.FromString("base").String()
	i.Children[0].Description.Config.Build.Base = &spec.BaseImage{
		Name:   "ghcr.io/org/base",
		Digest: d,
	}
	m := channel.Manifest{
		Images: []channel.Image{
			{
				Repository: "org/base",
				Digest:     digest.FromString("parent-index").String(),
				Platforms:  []channel.Platform{{OS: "windows", Arch: "arm64", Digest: d}},
			},
		},
	}
	if err := CheckParents(i, m, "ghcr.io"); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"empty", "repo", "arch", "os", "index", "registry", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			data, _ := json.Marshal(m)
			var copyM channel.Manifest
			_ = json.Unmarshal(data, &copyM)
			copyI := i
			registry := "ghcr.io"
			switch mode {
			case "empty":
				copyM.Images = nil
			case "repo":
				copyM.Images[0].Repository = "other/base"
			case "arch":
				copyM.Images[0].Platforms[0].Arch = "amd64"
			case "os":
				copyM.Images[0].Platforms[0].OS = "linux"
			case "index":
				copyM.Images[0].Platforms[0].Digest = copyM.Images[0].Digest
			case "registry":
				registry = "other.registry"
			case "invalid":
				copyI.Children = nil
			}
			if err := CheckParents(copyI, copyM, registry); !errors.Is(err, ErrEvidence) {
				t.Fatal(err)
			}
		})
	}
	i.Children[0].Description.Config.Build.Base = nil
	if err := CheckParents(i, channel.Manifest{}, "ghcr.io"); err != nil {
		t.Fatal(err)
	}
}
