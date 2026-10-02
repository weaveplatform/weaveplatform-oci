package channel_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"

	"github.com/deploymenttheory/weaveplatform-oci/pkg/channel"
)

var now = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

// chain builds a root, an endorsed signing key and a signed manifest.
type chain struct {
	rootKey, rootPub, signKey, signPub []byte
	bundle                             channel.Bundle
	anchor                             channel.Anchor
}

func newChain(t *testing.T, manifest []byte) chain {
	t.Helper()
	var c chain
	var err error
	if c.rootKey, c.rootPub, err = channel.GenerateKey(channel.RootKeyID); err != nil {
		t.Fatal(err)
	}
	if c.signKey, c.signPub, err = channel.GenerateKey("signing-2026"); err != nil {
		t.Fatal(err)
	}
	endorse, err := channel.Endorse(c.rootKey, c.signPub)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := channel.Sign(c.signKey, channel.ManifestContext, manifest)
	if err != nil {
		t.Fatal(err)
	}
	c.bundle = channel.Bundle{
		Manifest:      manifest,
		ManifestSig:   sig,
		SigningKey:    c.signPub,
		SigningKeySig: endorse,
	}
	if c.anchor, err = channel.ParseAnchor("org", c.rootPub); err != nil {
		t.Fatal(err)
	}
	return c
}

var testDigest = digest.FromString("index")

// existing is a module channel as weavemanifest's promote workflow writes it.
const existing = `{
  "schema": 1,
  "channel": "stable",
  "generated_at": "2026-10-01T00:00:00Z",
  "sequence": 7,
  "protocol": {"min": 1, "max": 2},
  "core": {"version": "0.5.0", "artifacts": [{"os": "linux", "arch": "arm64", "url": "u", "digest": "sha256:` + "0000000000000000000000000000000000000000000000000000000000000000" + `", "size": 1}]},
  "modules": [{"id": "guestweave", "version": "0.2.0"}]
}
`

func promoted(t *testing.T) []byte {
	t.Helper()
	out, err := channel.Promote([]byte(existing), channel.Image{
		Repository: "deploymenttheory/weave-images/ubuntu-24.04",
		Tag:        "24.04-r1",
		Digest:     testDigest.String(),
		Platforms: []channel.Platform{
			{OS: "linux", Arch: "arm64", Digest: digest.FromString("m").String()},
		},
		Signature: &channel.Signer{Provider: "cosign-key", KeyID: "hint"},
		BuildDate: "2026-10-02",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPromotePreservesEverythingElse(t *testing.T) {
	out := promoted(t)
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["sequence"].(float64) != 8 || doc["generated_at"] != "2026-10-02T12:00:00Z" ||
		doc["core"].(map[string]any)["version"] != "0.5.0" || len(doc["modules"].([]any)) != 1 {
		t.Fatalf("%s", out)
	}
	// replacing the same repository and tag keeps one entry; a second tag adds one
	again, _ := channel.Promote(
		out,
		channel.Image{
			Repository: "deploymenttheory/weave-images/ubuntu-24.04",
			Tag:        "24.04-r1",
			Digest:     digest.FromString("new").String(),
		},
		now,
	)
	more, _ := channel.Promote(
		again,
		channel.Image{
			Repository: "deploymenttheory/weave-images/a",
			Tag:        "1",
			Digest:     testDigest.String(),
		},
		now,
	)
	m, err := channel.Parse(more)
	if err != nil || len(m.Images) != 2 ||
		m.Images[0].Repository != "deploymenttheory/weave-images/a" ||
		m.Sequence != 10 {
		t.Fatalf("%v %+v", err, m)
	}
	for _, bad := range []channel.Image{{Repository: "r", Tag: "t", Digest: "nope"}, {Tag: "t", Digest: testDigest.String()}, {Repository: "r", Digest: testDigest.String()}} {
		if _, err := channel.Promote(out, bad, now); !errors.Is(err, channel.ErrFormat) {
			t.Fatalf("%+v accepted", bad)
		}
	}
	if _, err := channel.Promote(
		[]byte("{"),
		channel.Image{},
		now,
	); !errors.Is(
		err,
		channel.ErrFormat,
	) {
		t.Fatal("bad JSON")
	}
	if _, err := channel.Promote(
		[]byte(`{"schema":2}`),
		channel.Image{},
		now,
	); !errors.Is(
		err,
		channel.ErrFormat,
	) {
		t.Fatal("bad envelope")
	}
	fresh, err := channel.New("org-images", now)
	if err != nil {
		t.Fatal(err)
	}
	if m, err := channel.Parse(fresh); err != nil || m.Channel != "org-images" {
		t.Fatal(err)
	}
}

func TestVerifyChain(t *testing.T) {
	c := newChain(t, promoted(t))
	m, a, err := channel.Verify(
		[]channel.Anchor{c.anchor},
		c.bundle,
		channel.Options{Now: func() time.Time { return now }, MinSequence: 8},
	)
	if err != nil || a.Name != "org" || m.Sequence != 8 {
		t.Fatalf("%v %+v", err, m)
	}
	img, err := m.Image("deploymenttheory/weave-images/ubuntu-24.04", testDigest)
	if err != nil || img.Signature.KeyID != "hint" {
		t.Fatal(err)
	}
	if _, err := m.Image("", testDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Image("other", testDigest); !errors.Is(err, channel.ErrNotListed) {
		t.Fatal(err)
	}
	// anchors are tried in order; an unrelated anchor first is fine
	other := newChain(t, promoted(t))
	if _, a, err := channel.Verify(
		[]channel.Anchor{other.anchor, c.anchor},
		c.bundle,
		channel.Options{},
	); err != nil ||
		a.Name != "org" {
		t.Fatal(err)
	}
	mut := func(f func(b *channel.Bundle)) channel.Bundle {
		b := c.bundle
		f(&b)
		return b
	}
	tampered := bytes.Replace(c.bundle.Manifest, []byte(`"stable"`), []byte(`"stablf"`), 1)
	signedByOther, _ := channel.Sign(other.signKey, channel.ManifestContext, c.bundle.Manifest)
	endorsedBySigning, _ := channel.Sign(c.signKey, channel.EndorseContext, c.signPub)
	cases := map[string]struct {
		b    channel.Bundle
		want error
	}{
		"tampered manifest": {
			mut(func(b *channel.Bundle) { b.Manifest = tampered }),
			channel.ErrSignature,
		},
		"wrong anchor": {other.bundle, channel.ErrSignature},
		"key id mismatch": {
			mut(func(b *channel.Bundle) { b.ManifestSig = signedByOther }),
			channel.ErrSignature,
		},
		"endorsed by non-root": {
			mut(func(b *channel.Bundle) { b.SigningKeySig = endorsedBySigning }),
			channel.ErrSignature,
		},
		"garbage endorsement": {
			mut(func(b *channel.Bundle) { b.SigningKeySig = []byte("x") }),
			channel.ErrFormat,
		},
		"garbage signing key": {
			mut(func(b *channel.Bundle) { b.SigningKey = []byte("{}") }),
			channel.ErrSignature,
		},
		"garbage manifest sig": {
			mut(func(b *channel.Bundle) { b.ManifestSig = []byte(`{"schema":1}`) }),
			channel.ErrFormat,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := channel.Verify(
				[]channel.Anchor{c.anchor},
				tc.b,
				channel.Options{},
			); !errors.Is(
				err,
				tc.want,
			) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
	if _, _, err := channel.Verify(
		[]channel.Anchor{c.anchor},
		c.bundle,
		channel.Options{MinSequence: 9},
	); !errors.Is(
		err,
		channel.ErrRollback,
	) {
		t.Fatal(err)
	}
	// expiry: past, unparseable
	for exp, want := range map[string]bool{"2026-10-01T00:00:00Z": true, "soon": true, "2027-01-01T00:00:00Z": false} {
		var doc map[string]any
		_ = json.Unmarshal(c.bundle.Manifest, &doc)
		doc["expires"] = exp
		raw, _ := json.Marshal(doc)
		cc := newChain(t, raw)
		_, _, err := channel.Verify(
			[]channel.Anchor{cc.anchor},
			cc.bundle,
			channel.Options{Now: func() time.Time { return now }},
		)
		if errors.Is(err, channel.ErrExpired) != want {
			t.Fatalf("expires %s: %v", exp, err)
		}
	}
	// a correctly signed but malformed manifest
	bad := newChain(t, []byte(`{"schema":1,"channel":"x","protocol":{"min":0,"max":0}}`))
	if _, _, err := channel.Verify(
		[]channel.Anchor{bad.anchor},
		bad.bundle,
		channel.Options{},
	); !errors.Is(
		err,
		channel.ErrFormat,
	) {
		t.Fatal(err)
	}
}

func TestKeyFiles(t *testing.T) {
	if _, _, err := channel.GenerateKey(""); !errors.Is(err, channel.ErrFormat) {
		t.Fatal(err)
	}
	k, p, _ := channel.GenerateKey("signing")
	if _, err := channel.ParseAnchor("x", p); !errors.Is(err, channel.ErrFormat) {
		t.Fatal("non-root anchor accepted")
	}
	if _, err := channel.Endorse(k, p); !errors.Is(err, channel.ErrFormat) {
		t.Fatal("non-root endorsement accepted")
	}
	for _, bad := range [][]byte{[]byte("{"), []byte(`{"schema":1,"key_id":"a","public_key":"AAAA"}`)} {
		if _, _, err := channel.ParsePublicKey(bad); !errors.Is(err, channel.ErrFormat) {
			t.Fatal(err)
		}
		if _, _, err := channel.ParsePrivateKey(bad); !errors.Is(err, channel.ErrFormat) {
			t.Fatal(err)
		}
		if _, err := channel.Sign(bad, "c", nil); !errors.Is(err, channel.ErrFormat) {
			t.Fatal(err)
		}
		if _, err := channel.Endorse(bad, p); !errors.Is(err, channel.ErrFormat) {
			t.Fatal(err)
		}
		if _, err := channel.ParseAnchor("x", bad); !errors.Is(err, channel.ErrFormat) {
			t.Fatal(err)
		}
	}
	if _, _, err := channel.ParseSignature([]byte("{")); !errors.Is(err, channel.ErrFormat) {
		t.Fatal(err)
	}
	if string(channel.SigningMessage("ctx", []byte("d"))) != "ctx\x00d" {
		t.Fatal("signing message")
	}
	if _, err := channel.Parse(
		[]byte(
			`{"schema":1,"channel":"c","protocol":{"min":1,"max":1},"images":[{"repository":"r","digest":"bad"}]}`,
		),
	); !errors.Is(
		err,
		channel.ErrFormat,
	) {
		t.Fatal("bad image digest accepted")
	}
}

func writeBundle(t *testing.T, dir string, c chain) string {
	t.Helper()
	path := filepath.Join(dir, "stable.json")
	for name, data := range map[string][]byte{
		"stable.json": c.bundle.Manifest, "stable.json.sig": c.bundle.ManifestSig,
		channel.SigningKeyFile: c.bundle.SigningKey, channel.SigningKeySigFile: c.bundle.SigningKeySig,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestLoadFromFilesAndHTTP(t *testing.T) {
	ctx := context.Background()
	c := newChain(t, promoted(t))
	dir := t.TempDir()
	path := writeBundle(t, dir, c)
	b, err := channel.Load(ctx, path, nil)
	if err != nil || !bytes.Equal(b.Manifest, c.bundle.Manifest) ||
		!bytes.Equal(b.SigningKeySig, c.bundle.SigningKeySig) {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer srv.Close()
	b, err = channel.Load(ctx, srv.URL+"/stable.json", srv.Client())
	if err != nil || !bytes.Equal(b.ManifestSig, c.bundle.ManifestSig) {
		t.Fatal(err)
	}
	if _, err := channel.Load(
		ctx,
		srv.URL+"/missing.json",
		nil,
	); !errors.Is(
		err,
		channel.ErrFetch,
	) {
		t.Fatal(err)
	}
	if _, err := channel.Load(ctx, "http://127.0.0.1:1/x.json", nil); err == nil {
		t.Fatal("unreachable URL loaded")
	}
	if _, err := channel.Load(ctx, "http://bad host/x.json", nil); err == nil {
		t.Fatal("bad URL loaded")
	}
	if _, err := channel.Load(ctx, filepath.Join(dir, "missing.json"), nil); err == nil {
		t.Fatal("missing file loaded")
	}
	big := filepath.Join(dir, "big.json")
	_ = os.WriteFile(big, []byte(strings.Repeat("x", channel.MaxFileSize+1)), 0o600)
	if _, err := channel.Load(ctx, big, nil); !errors.Is(err, channel.ErrFormat) {
		t.Fatal(err)
	}
	if _, err := channel.Load(ctx, dir, nil); err == nil {
		t.Fatal("directory loaded as a manifest")
	}
}
