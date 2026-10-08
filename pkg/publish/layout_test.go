package publish_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/weaveplatform/weaveplatform-oci/internal/testregistry"
	"github.com/weaveplatform/weaveplatform-oci/pkg/chunk"
	"github.com/weaveplatform/weaveplatform-oci/pkg/imagecheck"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
	"github.com/weaveplatform/weaveplatform-oci/pkg/profile"
	"github.com/weaveplatform/weaveplatform-oci/pkg/publish"
)

func prepared(t *testing.T) publish.Request {
	t.Helper()
	c, dirs, signer := setup(t, profile.SigningCosignKey, testregistry.Options{NoReferrers: true})
	layout := filepath.Join(t.TempDir(), "layout")
	store, err := pack.OpenLayout(t.Context(), layout)
	if err != nil {
		t.Fatal(err)
	}
	var children []ocispec.Descriptor
	report := imagecheck.Report{
		SchemaVersion: 1,
		Tag:           "r1",
		Passed:        true,
		Platforms:     map[string][]imagecheck.Boot{},
	}
	for _, dir := range dirs {
		b, err := pack.LoadBundle(dir)
		if err != nil {
			t.Fatal(err)
		}
		b.File.Guest.Variant = "base"
		child, err := pack.Manifest(t.Context(), b, store, chunk.Options{})
		if err != nil {
			t.Fatal(err)
		}
		children = append(children, child)
		platform := b.File.Guest.OS + "/" + b.File.Guest.Arch
		for _, identity := range []string{"a", "b"} {
			report.Platforms[platform] = append(
				report.Platforms[platform],
				imagecheck.Boot{
					Platform:       platform,
					OSVersion:      b.File.Guest.OSVersion,
					Passed:         true,
					Marker:         "WEAVE-BOOT-OK-" + strings.Repeat(identity, 24),
					MachineID:      strings.Repeat(identity, 32),
					ElapsedSeconds: 1,
				},
			)
		}
	}
	root, err := pack.Index(t.Context(), store, children, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Tag(t.Context(), root, "validated"); err != nil {
		t.Fatal(err)
	}
	report.IndexDigest = root.Digest.String()
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	return publish.Request{
		Client:         c,
		Layout:         layout,
		LayoutRef:      "validated",
		ExpectedDigest: root.Digest.String(),
		Acceptance:     data,
		Repository:     "base",
		Tag:            "r1",
		Signer:         signer,
	}
}

func TestPublishExactValidatedLayout(t *testing.T) {
	r := prepared(t)
	before, err := os.ReadFile(filepath.Join(r.Layout, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := publish.Run(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	if res.Index.Digest.String() != r.ExpectedDigest || res.Promotion.Digest != r.ExpectedDigest ||
		len(res.Children) != 2 {
		t.Fatal("repacked accepted image", res)
	}
	after, err := os.ReadFile(filepath.Join(r.Layout, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("mutated validator's layout")
	}
	assertPromotionPlatforms(t, res)
}

func TestPublishRefusesInvalidLayoutEvidence(t *testing.T) {
	r := prepared(t)
	for name, change := range map[string]func(*publish.Request){
		"bundles mixed":     func(r *publish.Request) { r.Bundles = []string{"unused"} },
		"no reference":      func(r *publish.Request) { r.LayoutRef = "" },
		"no digest":         func(r *publish.Request) { r.ExpectedDigest = "" },
		"bad digest":        func(r *publish.Request) { r.ExpectedDigest = "bad" },
		"wrong digest":      func(r *publish.Request) { r.ExpectedDigest = digest.FromString("different").String() },
		"missing layout":    func(r *publish.Request) { r.Layout += "-missing" },
		"missing reference": func(r *publish.Request) { r.LayoutRef = "absent" },
		"no report":         func(r *publish.Request) { r.Acceptance = nil },
		"bad report":        func(r *publish.Request) { r.Acceptance = []byte("bad") },
		"failed report": func(r *publish.Request) {
			r.Acceptance = []byte(strings.ReplaceAll(string(r.Acceptance), `"passed":true`, `"passed":false`))
		},
		"wrong tag":                       func(r *publish.Request) { r.Tag = "r2" },
		"layout arguments without layout": func(r *publish.Request) { r.Layout = ""; r.Bundles = []string{"unused"} },
	} {
		t.Run(name, func(t *testing.T) {
			copyR := r
			change(&copyR)
			if _, err := publish.Run(t.Context(), copyR); err == nil {
				t.Fatal("accepted invalid publication")
			}
		})
	}
	// Negative cases must fail before publishing any tag.
	ref, err := r.Client.Parse("base:r1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Client.Resolve(t.Context(), ref); err == nil {
		t.Fatal("failed evidence pushed an image")
	}
	store, err := pack.OpenLayout(t.Context(), r.Layout)
	if err != nil {
		t.Fatal(err)
	}
	root, err := store.Resolve(t.Context(), r.LayoutRef)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the index: acceptance cannot stand in for integrity verification.
	blob := filepath.Join(
		r.Layout,
		"blobs",
		root.Digest.Algorithm().String(),
		root.Digest.Encoded(),
	)
	if err := os.Chmod(blob, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blob, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := publish.Run(t.Context(), r); !errors.Is(err, publish.ErrSelfCheck) {
		t.Fatal(err)
	}
}

func TestPublishRefusesSingleManifestLayout(t *testing.T) {
	r := prepared(t)
	store, err := pack.OpenLayout(t.Context(), r.Layout)
	if err != nil {
		t.Fatal(err)
	}
	root, err := store.Resolve(t.Context(), r.LayoutRef)
	if err != nil {
		t.Fatal(err)
	}
	f, err := store.Fetch(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var index ocispec.Index
	if err := json.NewDecoder(f).Decode(&index); err != nil {
		t.Fatal(err)
	}
	r.LayoutRef = index.Manifests[0].Digest.String()
	r.ExpectedDigest = r.LayoutRef
	if _, err := publish.Run(t.Context(), r); !errors.Is(err, publish.ErrRequest) {
		t.Fatal(err)
	}
}
