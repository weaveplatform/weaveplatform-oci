package handoff

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/weaveplatform/weaveplatform-oci/pkg/chunk"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

func preparedInput(t *testing.T) (Options, manifest, nativeResult) {
	t.Helper()
	base, _ := fixture(t, "macos")
	_, err := Import(t.Context(), base)
	must(t, err)
	bundle, err := pack.LoadBundle(base.Out)
	must(t, err)
	path := filepath.Join(t.TempDir(), "parent")
	store, err := pack.OpenLayout(t.Context(), path)
	must(t, err)
	desc, err := pack.Manifest(t.Context(), bundle, store, chunk.Options{})
	must(t, err)
	root, err := pack.Index(t.Context(), store, []ocispec.Descriptor{desc}, nil)
	must(t, err)
	must(t, store.Tag(t.Context(), root, "base"))
	o, m := fixture(t, "macos")
	m.Builds[0].Builder = "imageweave-macos-prepared"
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(o.Manifest), "build-result.json"))
	must(t, err)
	var r nativeResult
	must(t, json.Unmarshal(raw, &r))
	r.SchemaVersion, r.FirstBoot = 2, "desktop"
	r.Prepared = &preparedResult{
		Parent: spec.BaseImage{
			Name:   "ghcr.io/weaveplatform/weave-images/macos-26-base",
			Digest: desc.Digest.String(),
		},
		User:           "weave",
		AutomaticLogin: true,
		RemoteLogin:    true,
	}
	r.Inputs.SourceSHA256 = strings.TrimPrefix(desc.Digest.String(), "sha256:")
	m.Builds[0].Data["source_sha256"] = r.Inputs.SourceSHA256
	o.ParentLayout, o.ParentRef, o.ParentName = path, "base", r.Prepared.Parent.Name
	o.SourceURI = ""
	return o, m, r
}

func savePreparedInput(t *testing.T, o Options, m manifest, r nativeResult) {
	t.Helper()
	data := encode(t, r)
	must(t, os.WriteFile(filepath.Join(filepath.Dir(o.Manifest), "build-result.json"), data, 0o600))
	for i := range m.Builds[0].Files {
		if filepath.Base(m.Builds[0].Files[i].Name) == "build-result.json" {
			m.Builds[0].Files[i].Size = int64(len(data))
		}
	}
	must(t, os.WriteFile(o.Manifest, encode(t, m), 0o600))
}

func TestImportPreparedMacOS(t *testing.T) {
	o, m, r := preparedInput(t)
	savePreparedInput(t, o, m, r)
	receipt, err := Import(t.Context(), o)
	must(t, err)
	f := receipt.Bundle
	if f.Guest.Variant != spec.TierPrepared || f.Provisioning.DefaultUser != "weave" ||
		f.Provisioning.CredentialHint != "baked" ||
		f.Build.Base.Digest != r.Prepared.Parent.Digest ||
		len(f.Build.SourceMedia) != 0 ||
		receipt.Qualification != "unverified" {
		t.Fatal(f)
	}
	b, err := pack.LoadBundle(o.Out)
	must(t, err)
	store, err := pack.OpenLayout(t.Context(), filepath.Join(t.TempDir(), "layout"))
	must(t, err)
	desc, err := pack.Manifest(t.Context(), b, store, chunk.Options{})
	must(t, err)
	description, err := pack.Describe(t.Context(), store, desc)
	must(t, err)
	if description.Config.Build.Base.Digest != r.Prepared.Parent.Digest {
		t.Fatal("lost parent lineage")
	}
}

func TestPreparedImportRejectsInvalidClaims(t *testing.T) {
	for name, mutate := range map[string]func(*Options, *manifest, *nativeResult){
		"schema":      func(_ *Options, _ *manifest, r *nativeResult) { r.SchemaVersion = 1 },
		"no prepared": func(_ *Options, _ *manifest, r *nativeResult) { r.Prepared = nil },
		"first boot":  func(_ *Options, _ *manifest, r *nativeResult) { r.FirstBoot = "setup-assistant" },
		"user":        func(_ *Options, _ *manifest, r *nativeResult) { r.Prepared.User = "admin" },
		"login":       func(_ *Options, _ *manifest, r *nativeResult) { r.Prepared.AutomaticLogin = false },
		"ssh":         func(_ *Options, _ *manifest, r *nativeResult) { r.Prepared.RemoteLogin = false },
		"digest": func(_ *Options, _ *manifest, r *nativeResult) {
			r.Prepared.Parent.Digest = "sha256:" + strings.Repeat("0", 64)
		},
		"repository":         func(_ *Options, _ *manifest, r *nativeResult) { r.Prepared.Parent.Name = "" },
		"parent missing":     func(o *Options, _ *manifest, _ *nativeResult) { o.ParentLayout = "" },
		"parent nonexistent": func(o *Options, _ *manifest, _ *nativeResult) { o.ParentLayout = filepath.Join(t.TempDir(), "missing") },
		"parent file":        func(o *Options, _ *manifest, _ *nativeResult) { o.ParentLayout = o.Manifest },
		"parent ref":         func(o *Options, _ *manifest, _ *nativeResult) { o.ParentRef = "absent" },
		"parent name":        func(o *Options, _ *manifest, _ *nativeResult) { o.ParentName = "other" },
		"parent platform": func(_ *Options, m *manifest, r *nativeResult) {
			r.Inputs.SourceSHA256 = strings.Repeat("0", 64)
			m.Builds[0].Data["source_sha256"] = r.Inputs.SourceSHA256
			r.Prepared.Parent.Digest = "sha256:" + r.Inputs.SourceSHA256
		},
		"different version": func(_ *Options, _ *manifest, r *nativeResult) { r.OSVersion = "26.9" },
		"different model":   func(_ *Options, _ *manifest, r *nativeResult) { r.Mac.HardwareModel = "b3RoZXI=" },
		"corrupt parent": func(o *Options, _ *manifest, _ *nativeResult) {
			must(t, os.WriteFile(filepath.Join(o.ParentLayout, "index.json"), []byte("bad"), 0o600))
		},
	} {
		t.Run(name, func(t *testing.T) {
			o, m, r := preparedInput(t)
			mutate(&o, &m, &r)
			savePreparedInput(t, o, m, r)
			if _, err := Import(t.Context(), o); err == nil {
				t.Fatal("accepted invalid prepared input")
			}
			if _, err := os.Stat(o.Out); !os.IsNotExist(err) {
				t.Fatal("invalid import created output", err)
			}
		})
	}
	o, _ := fixture(t, "macos")
	o.ParentName = "unexpected"
	if _, err := Import(t.Context(), o); err == nil {
		t.Fatal("ignored parent options")
	}
}
