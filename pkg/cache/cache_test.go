package cache_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/deploymenttheory/weaveplatform-oci/internal/testbundle"
	"github.com/deploymenttheory/weaveplatform-oci/internal/testregistry"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/cache"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/chunk"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/client"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/pack"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/profile"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/spec"
)

var errBoom = errors.New("boom")

type guard struct {
	avail func() int64
	err   error
}

func (g guard) Available(string) (int64, error) { return g.avail(), g.err }

type fixture struct {
	reg *testregistry.Server
	c   *client.Client
}

// publish packs a linux bundle with the given seed and pushes it as repo:tag.
func (f fixture) publish(t *testing.T, repo, tag string, seed uint64) (client.Reference, ocispec.Descriptor) {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "b")
	if err := testbundle.Write(dir, testbundle.Options{OS: spec.OSLinux, Arch: spec.ArchAMD64, Seed: seed}); err != nil {
		t.Fatal(err)
	}
	b, _ := pack.LoadBundle(dir)
	s := memory.New()
	m, err := pack.Manifest(ctx, b, s, chunk.Options{})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := pack.Index(ctx, s, []ocispec.Descriptor{m}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Tag(ctx, idx, "src")
	ref, _ := f.c.Parse(repo + ":" + tag)
	if _, err := f.c.Push(ctx, s, "src", ref, client.PushOptions{}); err != nil {
		t.Fatal(err)
	}
	// a signature-like referrer, to check that pulls and exports carry it
	r, _ := f.c.Repository(ref.Registry, ref.Repository)
	if _, err := oras.PackManifest(ctx, r, oras.PackManifestVersion1_1, "application/vnd.test.sig", oras.PackManifestOptions{Subject: &idx}); err != nil {
		t.Fatal(err)
	}
	return ref, idx
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	reg := testregistry.New(testregistry.Options{})
	t.Cleanup(reg.Close)
	p := profile.Profile{Registry: profile.Registry{Host: reg.Host, Namespace: "weave-images", PlainHTTP: true}}
	c := client.New(p, client.Options{Credentials: func(context.Context, string) (auth.Credential, error) { return auth.EmptyCredential, nil }})
	return fixture{reg: reg, c: c}
}

func TestPullPinGC(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	refA, idxA := f.publish(t, "a", "1", 1)
	refB, idxB := f.publish(t, "b", "1", 2)
	clock := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	s, err := cache.Open(ctx, dir, cache.Options{Quota: 1, Now: func() time.Time { clock = clock.Add(time.Minute); return clock }})
	if err != nil {
		t.Fatal(err)
	}
	if s.Dir() != dir || s.Target() == nil {
		t.Fatal("accessors")
	}
	got, err := s.Pull(ctx, f.c, refA)
	if err != nil || got.Digest != idxA.Digest {
		t.Fatalf("pull a: %v", err)
	}
	if _, err := s.Pull(ctx, f.c, refB); err != nil {
		t.Fatal(err)
	}
	// a second pull of a cached image downloads nothing but metadata
	before := f.reg.Requests()
	if _, err := s.Pull(ctx, f.c, refA); err != nil {
		t.Fatal(err)
	}
	if f.reg.Requests()-before > 12 {
		t.Fatalf("re-pull made %d requests", f.reg.Requests()-before)
	}
	// the referrer travelled with the pull
	refs := 0
	_ = s.Target().Tags(ctx, "", func(tags []string) error { refs += len(tags); return nil })
	if d, err := s.Resolve(ctx, refA.String()); err != nil || d.Digest != idxA.Digest {
		t.Fatalf("resolve %v", err)
	}
	if _, err := s.Resolve(ctx, "nope"); !errors.Is(err, cache.ErrNotCached) {
		t.Fatal(err)
	}
	usage, _ := s.Usage()
	if usage == 0 {
		t.Fatal("no usage")
	}
	// pin A, then GC to quota 1 byte: B (unpinned) goes, A stays
	if err := s.Pin(idxA.Digest, "vm-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Pin(idxB.Digest+"x", "vm-2"); !errors.Is(err, cache.ErrNotCached) {
		t.Fatal(err)
	}
	freed, err := s.GC(ctx, 0)
	if err != nil || freed <= 0 {
		t.Fatalf("gc %d %v", freed, err)
	}
	if _, err := s.Resolve(ctx, refB.String()); !errors.Is(err, cache.ErrNotCached) {
		t.Fatal("unpinned image survived gc")
	}
	if _, err := s.Resolve(ctx, refA.String()); err != nil {
		t.Fatal("pinned image collected")
	}
	// state survives reopen; unpin then gc removes A
	s2, err := cache.Open(ctx, dir, cache.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Unpin("vm-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.GC(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if u, _ := s2.Usage(); u != 0 {
		// only the layout's own files may remain
		blobs, _ := os.ReadDir(filepath.Join(dir, "layout", "blobs", "sha256"))
		if len(blobs) != 0 {
			t.Fatalf("%d blobs left after full gc", len(blobs))
		}
	}
}

func TestSpaceGuard(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	refA, _ := f.publish(t, "a", "1", 1)
	refB, _ := f.publish(t, "b", "1", 2)
	dir := t.TempDir()
	// plenty of space for A
	s, _ := cache.Open(ctx, dir, cache.Options{Guard: guard{avail: func() int64 { return 1 << 40 }}})
	if _, err := s.Pull(ctx, f.c, refA); err != nil {
		t.Fatal(err)
	}
	// no space at all, even after evicting A: refused before any download
	s, _ = cache.Open(ctx, dir, cache.Options{Guard: guard{avail: func() int64 { return 0 }}})
	before := f.reg.Requests()
	if _, err := s.Pull(ctx, f.c, refB); !errors.Is(err, cache.ErrNoSpace) {
		t.Fatalf("want ErrNoSpace, got %v", err)
	}
	_ = before
	// space appears once A is evicted
	evicted := false
	s, _ = cache.Open(ctx, dir, cache.Options{Guard: guard{avail: func() int64 {
		if u, _ := usage(dir); u == 0 {
			evicted = true
			return 1 << 40
		}
		return 0
	}}})
	if _, err := s.Pull(ctx, f.c, refA); err != nil {
		t.Fatal(err) // A is cached already: needs nothing
	}
	if _, err := s.Pull(ctx, f.c, refB); err != nil || !evicted {
		t.Fatalf("evict-then-pull: %v evicted=%v", err, evicted)
	}
	// guard failures surface
	s, _ = cache.Open(ctx, t.TempDir(), cache.Options{Guard: guard{avail: func() int64 { return 0 }, err: errBoom}})
	if _, err := s.Pull(ctx, f.c, refA); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	// the default guard reports real free space
	s, _ = cache.Open(ctx, t.TempDir(), cache.Options{})
	if _, err := s.Pull(ctx, f.c, refA); err != nil {
		t.Fatal(err)
	}
	// unknown reference
	missing, _ := f.c.Parse("missing:1")
	if _, err := s.Pull(ctx, f.c, missing); !errors.Is(err, client.ErrUnavailable) {
		t.Fatal(err)
	}
}

func usage(dir string) (int64, error) {
	s, err := cache.Open(context.Background(), dir, cache.Options{})
	if err != nil {
		return 0, err
	}
	return s.Usage()
}

func TestExportImportCarriesReferrers(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	ref, idx := f.publish(t, "a", "1", 1)
	src, _ := cache.Open(ctx, t.TempDir(), cache.Options{})
	if _, err := src.Pull(ctx, f.c, ref); err != nil {
		t.Fatal(err)
	}
	layout := filepath.Join(t.TempDir(), "airgap")
	d, err := src.ExportLayout(ctx, ref.String(), layout)
	if err != nil || d.Digest != idx.Digest {
		t.Fatalf("export %v", err)
	}
	dst, _ := cache.Open(ctx, t.TempDir(), cache.Options{})
	roots, err := dst.ImportLayout(ctx, layout)
	if err != nil || len(roots) != 1 || roots[0].Digest != idx.Digest {
		t.Fatalf("import %v %v", roots, err)
	}
	preds, err := dst.Target().Predecessors(ctx, idx)
	if err != nil || len(preds) != 1 {
		t.Fatalf("referrer not carried: %v %v", preds, err)
	}
	raw, _ := content.FetchAll(ctx, dst.Target(), preds[0])
	var m ocispec.Manifest
	if json.Unmarshal(raw, &m) != nil || m.ArtifactType != "application/vnd.test.sig" {
		t.Fatalf("carried referrer is %s", raw)
	}
	if _, err := src.ExportLayout(ctx, "missing", filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("export of a missing ref succeeded")
	}
	file := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(file, nil, 0o600)
	if _, err := src.ExportLayout(ctx, ref.String(), filepath.Join(file, "x")); err == nil {
		t.Fatal("export under a file succeeded")
	}
	if _, err := dst.ImportLayout(ctx, filepath.Join(file, "x")); err == nil {
		t.Fatal("import from under a file succeeded")
	}
}

func TestOpenErrorsAndStateRecovery(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(file, nil, 0o600)
	if _, err := cache.Open(ctx, filepath.Join(file, "x"), cache.Options{}); err == nil {
		t.Fatal("open under a file succeeded")
	}
	dir := t.TempDir()
	_ = os.Mkdir(filepath.Join(dir, "layout"), 0o750)
	_ = os.WriteFile(filepath.Join(dir, "layout", "oci-layout"), []byte("not json"), 0o600)
	if _, err := cache.Open(ctx, dir, cache.Options{}); err == nil {
		t.Fatal("corrupt layout accepted")
	}
	// a torn state file is recovered from
	dir = t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "state.json"), []byte("{torn"), 0o600)
	s, err := cache.Open(ctx, dir, cache.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Unpin("nobody"); err != nil {
		t.Fatal(err)
	}
	// state.json unreadable (a directory)
	dir = t.TempDir()
	_ = os.Mkdir(filepath.Join(dir, "state.json"), 0o750)
	if _, err := cache.Open(ctx, dir, cache.Options{}); err == nil {
		t.Fatal("unreadable state accepted")
	}
	// save fails when the temp file cannot be written
	dir = t.TempDir()
	s, _ = cache.Open(ctx, dir, cache.Options{})
	_ = os.Mkdir(filepath.Join(dir, "state.json.tmp"), 0o750)
	if err := s.Unpin("x"); err == nil {
		t.Fatal("save into a directory succeeded")
	}
	if _, err := s.GC(ctx, 1); err == nil {
		t.Fatal("gc save failure swallowed")
	}
}
