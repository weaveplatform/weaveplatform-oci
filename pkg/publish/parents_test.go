package publish

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"

	"github.com/weaveplatform/weaveplatform-oci/pkg/channel"
	"github.com/weaveplatform/weaveplatform-oci/pkg/client"
	"github.com/weaveplatform/weaveplatform-oci/pkg/conformance"
	"github.com/weaveplatform/weaveplatform-oci/pkg/profile"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

func TestParentAuthorization(t *testing.T) {
	now := time.Now()
	parent := digest.FromString("parent platform").String()
	cfg := spec.Config{
		Guest: spec.Guest{OS: "linux", Arch: "amd64"},
		Build: spec.Build{
			Base: &spec.BaseImage{Name: "registry.invalid/images/base", Digest: parent},
		},
	}
	image := conformance.Report{
		Children: []conformance.Child{{Description: spec.Description{Config: cfg}}},
	}
	root, pub, err := channel.GenerateKey(channel.RootKeyID)
	if err != nil {
		t.Fatal(err)
	}
	signer, signingPub, err := channel.GenerateKey("signing")
	if err != nil {
		t.Fatal(err)
	}
	base, err := channel.New("org", now)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := channel.Promote(
		base,
		channel.Image{
			Repository: "images/base",
			Tag:        "r1",
			Digest:     digest.FromString("index").String(),
			Platforms:  []channel.Platform{{OS: "linux", Arch: "amd64", Digest: parent}},
		},
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := channel.Sign(signer, channel.ManifestContext, raw)
	if err != nil {
		t.Fatal(err)
	}
	endorsement, err := channel.Endorse(root, signingPub)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, content := range map[string][]byte{"root.pub": pub, "stable.json": raw, "stable.json.sig": signature, channel.SigningKeyFile: signingPub, channel.SigningKeySigFile: endorsement} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p := profile.Profile{
		Registry: profile.Registry{Host: "registry.invalid", Namespace: "images"},
		Channel: profile.Channel{
			Manifest: filepath.Join(dir, "stable.json"),
			Anchors:  []profile.Anchor{{Name: "org", PublicKey: filepath.Join(dir, "root.pub")}},
		},
	}
	request := Request{
		Client: client.New(p, client.Options{}),
		Layout: "accepted",
		Now:    func() time.Time { return now },
	}
	if err := checkParents(t.Context(), request, image); err != nil {
		t.Fatal(err)
	}
	request.Layout = ""
	if err := checkParents(t.Context(), request, image); err == nil {
		t.Fatal("unvalidated derived image accepted")
	}
	request.Layout = "accepted"
	p.Channel.Manifest = filepath.Join(dir, "missing.json")
	request.Client = client.New(p, client.Options{})
	if err := checkParents(t.Context(), request, image); err == nil {
		t.Fatal("missing channel accepted")
	}
	p.Channel.Manifest = filepath.Join(dir, "stable.json")
	request.Client = client.New(p, client.Options{})
	// A validly signed but different platform must not authorize this parent.
	image.Children[0].Description.Config.Guest.Arch = "arm64"
	if err := checkParents(t.Context(), request, image); err == nil {
		t.Fatal("wrong platform accepted")
	}
	// Tampering with the manifest after signing cannot authorize publication.
	var manifest channel.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Images[0].Platforms[0].Arch = "arm64"
	tampered, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stable.json"), tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkParents(t.Context(), request, image); err == nil {
		t.Fatal("unsigned changes accepted")
	}
}
