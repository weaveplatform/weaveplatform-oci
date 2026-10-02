package conformance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"

	"github.com/deploymenttheory/weaveplatform-oci/pkg/chunk"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/conformance"
	"github.com/deploymenttheory/weaveplatform-oci/internal/testbundle"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/pack"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/spec"
)

func packed(t *testing.T, osName string) (*memory.Store, ocispec.Descriptor, ocispec.Descriptor) {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "b")
	if err := testbundle.Write(
		dir,
		testbundle.Options{OS: osName, Arch: spec.ArchAMD64, ExtraDisk: true},
	); err != nil {
		t.Fatal(err)
	}
	b, err := pack.LoadBundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	store := memory.New()
	m, err := pack.Manifest(ctx, b, store, chunk.Options{})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := pack.Index(ctx, store, []ocispec.Descriptor{m}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return store, idx, m
}

func TestCheckIndexAndManifestRoots(t *testing.T) {
	ctx := context.Background()
	store, idx, m := packed(t, spec.OSWindows)
	for _, root := range []ocispec.Descriptor{idx, m} {
		for _, deep := range []bool{false, true} {
			r, err := conformance.Check(ctx, store, root, conformance.Options{Deep: deep})
			if err != nil || !r.OK() || len(r.Problems()) != 0 || len(r.Children) != 1 {
				t.Fatalf("root %s deep %v: %v %v", root.MediaType, deep, err, r.Problems())
			}
		}
	}
}

// tamper wraps a fetcher and corrupts one blob.
type tamper struct {
	content.Fetcher
	target ocispec.Descriptor
	fail   bool
}

func (t tamper) Fetch(ctx context.Context, d ocispec.Descriptor) (io.ReadCloser, error) {
	if d.Digest == t.target.Digest {
		if t.fail {
			return nil, errors.New("gone")
		}
		return io.NopCloser(bytes.NewReader(make([]byte, d.Size))), nil
	}
	return t.Fetcher.Fetch(ctx, d) //nolint:wrapcheck // test double
}

func TestDeepFindsCorruptChunkAndState(t *testing.T) {
	ctx := context.Background()
	store, idx, m := packed(t, spec.OSWindows)
	d, err := pack.Describe(ctx, store, m)
	if err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]struct {
		d    ocispec.Descriptor
		rule int
		fail bool
	}{
		"chunk":         {d.Disks[0].Chunks[0].Descriptor, 15, false},
		"missing chunk": {d.Disks[0].Chunks[0].Descriptor, 15, true},
		"state":         {d.State[0].Descriptor, 16, false},
	} {
		t.Run(name, func(t *testing.T) {
			r, err := conformance.Check(
				ctx,
				tamper{Fetcher: store, target: target.d, fail: target.fail},
				idx,
				conformance.Options{Deep: true},
			)
			if err != nil || r.OK() {
				t.Fatalf("%v ok=%v", err, r.OK())
			}
			if r.Children[0].Problems[0].Rule != target.rule ||
				!strings.Contains(r.Problems()[0], m.Digest.String()) {
				t.Fatalf("%v", r.Problems())
			}
		})
	}
}

// blobs is a digest-keyed fetcher that accepts any bytes, including
// manifests a real store would refuse.
type blobs struct {
	content.Fetcher
	extra map[string][]byte
}

func (b blobs) Fetch(ctx context.Context, d ocispec.Descriptor) (io.ReadCloser, error) {
	if raw, ok := b.extra[d.Digest.String()]; ok {
		return io.NopCloser(bytes.NewReader(raw)), nil
	}
	return b.Fetcher.Fetch(ctx, d) //nolint:wrapcheck // test double
}

func push(t *testing.T, s blobs, mt string, b []byte) ocispec.Descriptor {
	t.Helper()
	d := content.NewDescriptorFromBytes(mt, b)
	s.extra[d.Digest.String()] = b
	return d
}

func TestCheckReportsStructuralProblems(t *testing.T) {
	ctx := context.Background()
	mem, idx, m := packed(t, spec.OSLinux)
	store := blobs{Fetcher: mem, extra: map[string][]byte{}}
	raw, _ := content.FetchAll(ctx, store, m)
	var man ocispec.Manifest
	_ = json.Unmarshal(raw, &man)

	notJSON := push(t, store, spec.MediaTypeManifest, []byte("nope"))
	r, err := conformance.Check(ctx, store, notJSON, conformance.Options{})
	if err != nil || r.OK() || r.IndexProblems[0].Rule != 1 {
		t.Fatalf("non-JSON root: %v %v", err, r.Problems())
	}
	// an index whose child is not JSON
	var ix ocispec.Index
	ixRaw, _ := content.FetchAll(ctx, store, idx)
	_ = json.Unmarshal(ixRaw, &ix)
	brokenChild := push(t, store, spec.MediaTypeManifest, []byte(`{"mediaType":"x"`))
	ix.Manifests[0].Digest, ix.Manifests[0].Size = brokenChild.Digest, brokenChild.Size
	b, _ := json.Marshal(ix)
	r, err = conformance.Check(
		ctx,
		store,
		push(t, store, spec.MediaTypeIndex, b),
		conformance.Options{},
	)
	if err != nil || r.OK() || r.Children[0].Problems[0].Rule != 6 {
		t.Fatalf("broken child: %v %v", err, r.Problems())
	}
	// config too large, config missing, config invalid
	for name, edit := range map[string]func(*ocispec.Manifest){
		"too large": func(m *ocispec.Manifest) { m.Config.Size = spec.MaxConfigSize + 1 },
		"missing":   func(m *ocispec.Manifest) { m.Config.Digest = idx.Digest; m.Config.Size = 7 },
		"invalid":   func(m *ocispec.Manifest) { m.Layers = m.Layers[1:] },
	} {
		mm := man
		mm.Layers = append([]ocispec.Descriptor{}, man.Layers...)
		edit(&mm)
		b, _ := json.Marshal(mm)
		r, err := conformance.Check(
			ctx,
			store,
			push(t, store, spec.MediaTypeManifest, b),
			conformance.Options{Deep: true},
		)
		if err != nil || r.OK() {
			t.Fatalf("%s: %v ok=%v", name, err, r.OK())
		}
	}
	// fetch failures are errors, not problems
	if _, err := conformance.Check(
		ctx,
		store,
		ocispec.Descriptor{Digest: idx.Digest, Size: idx.Size + 1},
		conformance.Options{},
	); err == nil {
		t.Fatal("root fetch failure swallowed")
	}
	ix.Manifests[0].Size++
	b, _ = json.Marshal(ix)
	if _, err := conformance.Check(
		ctx,
		store,
		push(t, store, spec.MediaTypeIndex, b),
		conformance.Options{},
	); err == nil {
		t.Fatal("child fetch failure swallowed")
	}
}
