package profile_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weaveplatform/weaveplatform-oci/pkg/profile"
)

const valid = `schemaVersion: 1
default: private
profiles:
  - name: github
    kind: github
    registry: {host: ghcr.io, namespace: weaveplatform/weave-images}
    signing: {provider: github-attestation}
    verify:
      mode: both
      identity:
        issuer: https://token.actions.githubusercontent.com
        subjectRegexp: '^https://github.com/weaveplatform/weaveplatform-oci/\.github/workflows/publish\.yml@refs/.*$'
    channel:
      manifest: https://raw.githubusercontent.com/weaveplatform/weaveplatform-channels/main/channels/stable.json
      anchors: [{name: weaveplatform, publicKey: keys/root.pub}]
  - name: private
    kind: private
    registry: {host: "zot.example:5000", namespace: weave-images, plainHTTP: true}
    signing: {provider: cosign-key, key: cosign.key}
    verify: {mode: signature, publicKeys: [cosign.pub, ABSOLUTE]}
  - name: office
    kind: hybrid
    registry: {host: ghcr.io, namespace: weaveplatform/weave-images}
    mirrors: [{host: "zot.office:5000", insecureSkipTLSVerify: true}]
    signing: {provider: none}
    verify: {mode: none}
`

func TestLoadResolvesRelativePaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "oci-profiles.yaml")
	abs := filepath.Join(t.TempDir(), "other.pub") // absolute on every OS
	if err := os.WriteFile(
		path,
		[]byte(strings.Replace(valid, "ABSOLUTE", "'"+abs+"'", 1)),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	f, err := profile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.Select("")
	if err != nil || p.Name != "private" {
		t.Fatalf("default: %v %v", p.Name, err)
	}
	if p.Signing.Key != filepath.Join(dir, "cosign.key") || p.Verify.PublicKeys[0] != filepath.Join(dir, "cosign.pub") ||
		p.Verify.PublicKeys[1] != abs ||
		!p.Registry.PlainHTTP {
		t.Fatalf("paths %+v", p)
	}
	g, _ := f.Select("github")
	if g.Channel.Anchors[0].PublicKey != filepath.Join(dir, "keys", "root.pub") ||
		!strings.HasPrefix(g.Channel.Manifest, "https://") {
		t.Fatalf("github %+v", g.Channel)
	}
	if _, err := f.Select("nope"); !errors.Is(err, profile.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := profile.Load(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("missing file loaded")
	}
	_ = os.WriteFile(path, []byte("schemaVersion: 2\nprofiles: []\n"), 0o600)
	if _, err := profile.Load(path); !errors.Is(err, profile.ErrInvalid) {
		t.Fatal(err)
	}
}

func TestSelectOnlyProfile(t *testing.T) {
	f, err := profile.Parse([]byte(`schemaVersion: 1
profiles:
  - {name: only, kind: github, registry: {host: ghcr.io}, signing: {provider: none}, verify: {mode: none}}
`))
	if err != nil {
		t.Fatal(err)
	}
	if p, err := f.Select(""); err != nil || p.Name != "only" {
		t.Fatal(err)
	}
}

func TestPath(t *testing.T) {
	if p, _ := profile.Path("/x.yaml"); p != "/x.yaml" {
		t.Fatal(p)
	}
	t.Setenv(profile.EnvProfiles, "/env.yaml")
	if p, _ := profile.Path(""); p != "/env.yaml" {
		t.Fatal(p)
	}
	t.Setenv(profile.EnvProfiles, "")
	if p, err := profile.Path(
		"",
	); err != nil ||
		!strings.HasSuffix(p, filepath.Join("weave", "oci-profiles.yaml")) {
		t.Fatalf("%q %v", p, err)
	}
}

func TestValidationRules(t *testing.T) {
	base := "  - {name: p, kind: %s, registry: {host: %s}, %s signing: {provider: %s%s}, verify: {%s}%s}\n"
	cases := map[string][]string{
		"unknown key": {"github", "ghcr.io", "bogus: 1,", "none", "", "mode: none", ""},
		"bad kind":    {"cloud", "ghcr.io", "", "none", "", "mode: none", ""},
		"bad host":    {"github", "https://ghcr.io", "", "none", "", "mode: none", ""},
		"bad mirror host": {
			"hybrid",
			"ghcr.io",
			"mirrors: [{host: 'a b'}],",
			"none",
			"",
			"mode: none",
			"",
		},
		"github with mirrors": {
			"github",
			"ghcr.io",
			"mirrors: [{host: m}],",
			"none",
			"",
			"mode: none",
			"",
		},
		"github cosign": {
			"github",
			"ghcr.io",
			"",
			"cosign-key",
			", key: k",
			"mode: none",
			"",
		},
		"private attestation": {
			"private",
			"zot",
			"",
			"github-attestation",
			"",
			"mode: none",
			"",
		},
		"hybrid without mirrors": {"hybrid", "ghcr.io", "", "none", "", "mode: none", ""},
		"hybrid cosign": {
			"hybrid",
			"ghcr.io",
			"mirrors: [{host: m}],",
			"cosign-key",
			"",
			"mode: none",
			"",
		},
		"bad provider":       {"github", "ghcr.io", "", "magic", "", "mode: none", ""},
		"key without cosign": {"github", "ghcr.io", "", "none", ", key: k", "mode: none", ""},
		"bad mode":           {"github", "ghcr.io", "", "none", "", "mode: trust-me", ""},
		"channel without anchors": {
			"github",
			"ghcr.io",
			"",
			"none",
			"",
			"mode: channel",
			", channel: {manifest: m}",
		},
		"anchor without key": {
			"github",
			"ghcr.io",
			"",
			"none",
			"",
			"mode: channel",
			", channel: {manifest: m, anchors: [{name: a}]}",
		},
		"cosign without keys": {"private", "zot", "", "cosign-key", "", "mode: signature", ""},
		"attest without identity": {
			"github",
			"ghcr.io",
			"",
			"github-attestation",
			"",
			"mode: signature",
			"",
		},
		"bad subject regexp": {
			"github", "ghcr.io", "", "github-attestation", "",
			"mode: signature, identity: {issuer: i, subjectRegexp: '('}", "",
		},
		"signature with none": {
			"github",
			"ghcr.io",
			"",
			"none",
			"",
			"mode: both",
			", channel: {manifest: m, anchors: [{name: a, publicKey: k}]}",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			doc := "schemaVersion: 1\nprofiles:\n" + sprintf(base, c)
			if _, err := profile.Parse([]byte(doc)); !errors.Is(err, profile.ErrInvalid) {
				t.Fatalf("accepted:\n%s\n%v", doc, err)
			}
		})
	}
	dup := "schemaVersion: 1\ndefault: x\nprofiles:\n" +
		"  - {name: '', kind: github, registry: {host: ghcr.io}, signing: {provider: none}, verify: {mode: none}}\n" +
		"  - {name: '', kind: github, registry: {host: ghcr.io}, signing: {provider: none}, verify: {mode: none}}\n"
	_, err := profile.Parse([]byte(dup))
	if err == nil || !strings.Contains(err.Error(), "duplicate") ||
		!strings.Contains(err.Error(), "default profile") ||
		!strings.Contains(err.Error(), "name is required") {
		t.Fatalf("%v", err)
	}
	if _, err := profile.Parse(
		[]byte("schemaVersion: 1\nprofiles: []\n"),
	); !errors.Is(
		err,
		profile.ErrInvalid,
	) {
		t.Fatal("empty profiles accepted")
	}
}

func sprintf(format string, args []string) string {
	a := make([]any, len(args))
	for i, v := range args {
		a[i] = v
	}
	return strings.TrimRight(fmtSprintf(format, a...), "") //nolint:gocritic // helper
}
