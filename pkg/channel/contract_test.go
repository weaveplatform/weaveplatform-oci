package channel_test

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/weaveplatform/weaveplatform-oci/pkg/channel"
)

// The channel manifest format belongs to weaveplatform-agent-core: its
// schema/channel-manifest.schema.json is the contract, and core and the
// channels promote workflow both read and write documents against it. This
// package writes the same files, so everything it produces is validated here
// against a byte-for-byte copy of that schema (`make channel-schema`
// refreshes it). A field added here and not there would otherwise surface
// only as a device refusing a signed manifest.
func contractSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile("testdata/channel-manifest.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	const id = "channel-manifest.schema.json"
	if err := c.AddResource(id, doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func conforms(t *testing.T, s *jsonschema.Schema, name string, raw []byte) {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if err := s.Validate(doc); err != nil {
		t.Fatalf("%s does not match agent-core's schema: %v\n%s", name, err, raw)
	}
}

func contractDigest(c byte) string { return "sha256:" + strings.Repeat(string(c), 64) }

func TestContractNewAndPromote(t *testing.T) {
	s := contractSchema(t)
	at := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)

	raw, err := channel.New("stable", at)
	if err != nil {
		t.Fatal(err)
	}
	conforms(t, s, "New", raw)

	images := []channel.Image{
		{ // the minimum: no platforms, no signer, no build date
			Repository: "weaveplatform/weave-images/ubuntu-24.04",
			Tag:        "24.04-20261002-r1",
			Digest:     contractDigest('a'),
		},
		{ // what publish writes with a cosign key
			Repository: "weaveplatform/weave-images/macos-26-base",
			Tag:        "26.0-25A354-r1",
			Digest:     contractDigest('b'),
			Platforms: []channel.Platform{
				{OS: "darwin", Arch: "arm64", OSVersion: "26.0", Digest: contractDigest('c')},
			},
			Signature: &channel.Signer{Provider: "cosign-key", KeyID: "c2lnbmluZy1rZXk="},
			BuildDate: at.Format(time.RFC3339),
		},
		{ // a GitHub attestation signer and several platforms
			Repository: "weaveplatform/weave-images/windows-11-base",
			Tag:        "11-25H2-26200.6584-r1",
			Digest:     contractDigest('d'),
			Platforms: []channel.Platform{
				{OS: "windows", Arch: "amd64", OSVersion: "10.0.26200.6584", Digest: contractDigest('e')},
				{OS: "windows", Arch: "arm64", OSVersion: "10.0.26200.6584", Digest: contractDigest('f')},
			},
			Signature: &channel.Signer{
				Provider:      "github-attestation",
				Issuer:        "https://token.actions.githubusercontent.com",
				SubjectRegexp: `^https://github\.com/weaveplatform/weaveplatform-oci/`,
			},
			BuildDate: at.Format(time.RFC3339),
		},
	}
	for _, img := range images {
		if raw, err = channel.Promote(raw, img, at); err != nil {
			t.Fatal(err)
		}
		conforms(t, s, "Promote "+img.Repository, raw)
	}
}

// The rolling channel also carries core and modules, written by the channels
// promote workflow. Promote must leave those exactly as they were and still
// produce a valid document.
func TestContractPromoteKeepsModules(t *testing.T) {
	s := contractSchema(t)
	seed := []byte(`{
  "schema": 1,
  "channel": "stable",
  "generated_at": "2026-10-01T00:00:00Z",
  "sequence": 7,
  "expires": "2027-01-01T00:00:00Z",
  "protocol": {"min": 1, "max": 1},
  "core": {"version": "1.4.0", "artifacts": [
    {"os": "linux", "arch": "arm64", "url": "https://example.invalid/core", "digest": "` + contractDigest('1') + `", "size": 10}
  ]},
  "modules": [{
    "id": "sysinfo", "version": "0.3.0", "protocol": 1, "privilege": "user", "session": "system",
    "capabilities": [], "subscribes": [],
    "artifacts": [{"os": "linux", "arch": "arm64", "url": "ghcr.io/x#linux-arm64", "digest": "` + contractDigest('2') + `", "size": 20}]
  }]
}`)
	conforms(t, s, "seed", seed)
	out, err := channel.Promote(seed, channel.Image{
		Repository: "weaveplatform/weave-images/ubuntu-24.04",
		Tag:        "24.04-20261002-r1",
		Digest:     contractDigest('a'),
	}, time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	conforms(t, s, "promoted seed", out)
	var before, after map[string]json.RawMessage
	_ = json.Unmarshal(seed, &before)
	_ = json.Unmarshal(out, &after)
	for _, k := range []string{"core", "modules", "expires", "protocol", "schema", "channel"} {
		var a, b any
		_ = json.Unmarshal(before[k], &a)
		_ = json.Unmarshal(after[k], &b)
		if ja, jb := mustJSON(t, a), mustJSON(t, b); ja != jb {
			t.Fatalf("%s changed: %s -> %s", k, ja, jb)
		}
	}
}

// The schema is strict (additionalProperties false throughout). A field
// that only this package knows about must fail here, not on a device.
func TestContractSchemaIsStrict(t *testing.T) {
	s := contractSchema(t)
	raw, err := channel.New("stable", time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"unknown top-level field": func(d map[string]any) { d["mirrors"] = []any{} },
		"image without a digest": func(d map[string]any) {
			d["images"] = []any{map[string]any{"repository": "r", "tag": "t"}}
		},
		"unknown signer provider": func(d map[string]any) {
			d["images"] = []any{map[string]any{
				"repository": "r", "tag": "t", "digest": contractDigest('a'),
				"signature": map[string]any{"provider": "notation"},
			}}
		},
	} {
		var d map[string]any
		_ = json.Unmarshal(raw, &d)
		mutate(d)
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(mustJSON(t, d)))
		if err != nil {
			t.Fatal(err)
		}
		if s.Validate(doc) == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
