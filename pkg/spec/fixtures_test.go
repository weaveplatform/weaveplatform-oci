package spec_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

var update = flag.Bool("update", false, "rewrite spec/testdata fixtures")

// fixture is one normative example under spec/testdata. Valid fixtures must
// inspect cleanly; invalid ones must report every listed rule.
type fixture struct {
	name     string
	manifest []byte
	config   []byte
	index    []byte
	rules    []int
}

func pretty(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

func fixtures(t *testing.T) []fixture {
	t.Helper()
	var out []fixture
	for _, osName := range []string{spec.OSDarwin, spec.OSWindows, spec.OSLinux} {
		m, c := manifestFor(t, validConfig(osName))
		out = append(out, fixture{name: "valid/" + osName, manifest: pretty(t, m), config: c})
	}
	arm := validConfig(spec.OSLinux)
	amd := validConfig(spec.OSLinux)
	amd.Guest.Arch = spec.ArchAMD64
	idx, _ := indexFor(t, arm, amd)
	out = append(out, fixture{name: "valid/linux-index", index: pretty(t, idx)})

	withConfig := func(name string, rules []int, edit func(map[string]any)) fixture {
		cfg := configJSON(t, validConfig(spec.OSDarwin), edit)
		m, _ := manifestFor(t, validConfig(spec.OSDarwin))
		m.Config.Digest, m.Config.Size = digest.FromBytes(cfg), int64(len(cfg))
		return fixture{name: "invalid/" + name, manifest: pretty(t, m), config: cfg, rules: rules}
	}
	out = append(
		out,
		withConfig(
			"ecid-in-config",
			[]int{8},
			func(m map[string]any) { m["firmware"].(map[string]any)["ecid"] = "AAAA" },
		),
		withConfig(
			"mac-address-in-config",
			[]int{8},
			func(m map[string]any) { m["macAddress"] = "00:11:22:33:44:55" },
		),
	)
	m, c := manifestFor(t, validConfig(spec.OSDarwin))
	m.Layers = append(m.Layers[:5], m.Layers[6:]...)
	out = append(
		out,
		fixture{name: "invalid/chunk-gap", manifest: pretty(t, m), config: c, rules: []int{12}},
	)
	m, c = manifestFor(t, validConfig(spec.OSDarwin))
	m.Layers = m.Layers[:len(m.Layers)-1]
	out = append(
		out,
		fixture{
			name:     "invalid/state-without-layer",
			manifest: pretty(t, m),
			config:   c,
			rules:    []int{13},
		},
	)
	bad := idx
	bad.Manifests = append([]ocispec.Descriptor{}, idx.Manifests...)
	p := *bad.Manifests[1].Platform
	p.Architecture = "x86_64"
	bad.Manifests[1].Platform = &p
	out = append(
		out,
		fixture{name: "invalid/non-goarch-platform", index: pretty(t, bad), rules: []int{2, 4}},
	)
	return out
}

func TestFixtures(t *testing.T) {
	for _, f := range fixtures(t) {
		t.Run(f.name, func(t *testing.T) {
			files := map[string][]byte{
				"manifest.json": f.manifest,
				"config.json":   f.config,
				"index.json":    f.index,
			}
			for suffix, want := range files {
				if want == nil {
					continue
				}
				path := filepath.Join("testdata", filepath.FromSlash(f.name)+"."+suffix)
				if *update {
					if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, want, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				got, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("%v (run go test ./spec -run TestFixtures -update)", err)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf(
						"%s differs from the generator; fixtures and contract must change together",
						path,
					)
				}
			}
			var err error
			if f.index != nil {
				_, err = spec.InspectIndex(f.index)
			} else {
				_, err = spec.Inspect(f.manifest, f.config)
			}
			if len(f.rules) == 0 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var ve *spec.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("invalid fixture accepted: %v", err)
			}
			for _, r := range f.rules {
				if !ve.HasRule(r) {
					t.Errorf("rule %d not reported: %v", r, err)
				}
			}
		})
	}
}
