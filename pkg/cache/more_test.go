package cache_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"

	"github.com/weaveplatform/weaveplatform-oci/pkg/cache"
)

func writeState(t *testing.T, dir string, roots map[string]any) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"roots": roots, "pins": map[string]string{}})
	if err := os.WriteFile(filepath.Join(dir, "state.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGCOrderingAndEarlyStop(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	refA, _ := f.publish(t, "a", "1", 1)
	refB, _ := f.publish(t, "b", "1", 2)
	clock := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	s, _ := cache.Open(
		ctx,
		t.TempDir(),
		cache.Options{Now: func() time.Time { clock = clock.Add(time.Minute); return clock }},
	)
	if u, err := s.Usage(); err != nil || u != 0 {
		t.Fatalf("fresh usage %d %v", u, err)
	}
	if _, err := s.Pull(ctx, f.c, refA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pull(ctx, f.c, refB); err != nil {
		t.Fatal(err)
	}
	// keep everything: nothing evicted
	if freed, err := s.GC(ctx, 1<<40); err != nil || freed != 0 {
		t.Fatalf("%d %v", freed, err)
	}
	// keep almost nothing: the older image (A) goes first, then B
	if _, err := s.GC(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(ctx, refA.String()); !errors.Is(err, cache.ErrNotCached) {
		t.Fatal("A survived")
	}
}

func TestEvictUntrackedAndUntaggableRoots(t *testing.T) {
	ctx := context.Background()
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	// a root the layout does not know, with no refs: evicted quietly
	dir := t.TempDir()
	writeState(
		t,
		dir,
		map[string]any{
			digest.FromString("ghost").String(): map[string]any{
				"refs":       []string{},
				"lastAccess": old,
			},
		},
	)
	s, err := cache.Open(ctx, dir, cache.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(dir, "layout", "blobs", "sha256"), 0o750)
	_ = os.WriteFile(filepath.Join(dir, "layout", "blobs", "sha256", "junk"), []byte("xx"), 0o600)
	if _, err := s.GC(ctx, 1); err != nil {
		t.Fatal(err)
	}
	// with the blob directory gone, usage is zero
	_ = os.RemoveAll(filepath.Join(dir, "layout", "blobs"))
	if u, err := s.Usage(); err != nil || u != 0 {
		t.Fatalf("%d %v", u, err)
	}
	// a root whose ref does not exist in the layout: untag fails
	dir = t.TempDir()
	writeState(
		t,
		dir,
		map[string]any{
			digest.FromString("ghost").String(): map[string]any{
				"refs":       []string{"nope:1"},
				"lastAccess": old,
			},
		},
	)
	s, _ = cache.Open(ctx, dir, cache.Options{Quota: 1})
	_ = os.MkdirAll(filepath.Join(dir, "layout", "blobs", "sha256"), 0o750)
	_ = os.WriteFile(filepath.Join(dir, "layout", "blobs", "sha256", "junk"), []byte("x"), 0o600)
	_, _ = s.GC(ctx, 0) // oras treats untagging a missing ref as a no-op
}

func TestStateSaveFailures(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	ref, idx := f.publish(t, "a", "1", 1)
	dir := t.TempDir()
	s, _ := cache.Open(ctx, dir, cache.Options{})
	if _, err := s.Pull(ctx, f.c, ref); err != nil {
		t.Fatal(err)
	}
	// rename onto a non-empty directory fails
	_ = os.Remove(filepath.Join(dir, "state.json"))
	_ = os.MkdirAll(filepath.Join(dir, "state.json", "x"), 0o750)
	if err := s.Pin(idx.Digest, "vm"); err == nil {
		t.Fatal("rename onto a directory succeeded")
	}
	if _, err := s.Resolve(ctx, ref.String()); err == nil {
		t.Fatal("resolve save failure swallowed")
	}
}

func TestUnreadableBlobsAndGuardErrors(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not restrict root or Windows")
	}
	ctx := context.Background()
	f := newFixture(t)
	refA, _ := f.publish(t, "a", "1", 1)
	refB, _ := f.publish(t, "b", "1", 2)
	dir := t.TempDir()
	s, _ := cache.Open(ctx, dir, cache.Options{})
	if _, err := s.Pull(ctx, f.c, refA); err != nil {
		t.Fatal(err)
	}
	s2, err := cache.Open(ctx, dir, cache.Options{Guard: guard{avail: func() int64 { return 0 }}})
	if err != nil {
		t.Fatal(err)
	}
	blobs := filepath.Join(dir, "layout", "blobs", "sha256")
	if err := os.Chmod(blobs, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blobs, 0o750) })
	if _, err := s.Usage(); err == nil {
		t.Fatal("unreadable blobs counted")
	}
	if _, err := s.GC(ctx, 1); err == nil {
		t.Fatal("gc over unreadable blobs succeeded")
	}
	if _, err := s2.Pull(ctx, f.c, refB); err == nil {
		t.Fatal("eviction over unreadable blobs succeeded")
	}
	_ = os.Chmod(blobs, 0o750)

	// the guard answers once, then fails
	calls := 0
	s3, err := cache.Open(ctx, t.TempDir(), cache.Options{Guard: flaky{calls: &calls}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s3.Pull(ctx, f.c, refA); err == nil {
		t.Fatal("second guard failure swallowed")
	}
	// the default guard on a vanished directory
	gone := t.TempDir()
	s4, _ := cache.Open(ctx, gone, cache.Options{})
	_ = os.RemoveAll(gone)
	if _, err := s4.Pull(ctx, f.c, refA); err == nil {
		t.Fatal("statfs on a removed directory succeeded")
	}
}

type flaky struct{ calls *int }

func (f flaky) Available(string) (int64, error) {
	*f.calls++
	if *f.calls > 1 {
		return 0, errBoom
	}
	return 0, nil
}

func TestImportOfBrokenLayout(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	ref, _ := f.publish(t, "a", "1", 1)
	src, _ := cache.Open(ctx, t.TempDir(), cache.Options{})
	if _, err := src.Pull(ctx, f.c, ref); err != nil {
		t.Fatal(err)
	}
	layout := filepath.Join(t.TempDir(), "x")
	if _, err := src.ExportLayout(ctx, ref.String(), layout); err != nil {
		t.Fatal(err)
	}
	// remove the chunk blobs: the import's copy fails
	entries, _ := os.ReadDir(filepath.Join(layout, "blobs", "sha256"))
	for _, e := range entries {
		p := filepath.Join(layout, "blobs", "sha256", e.Name())
		if info, _ := os.Stat(p); info != nil && info.Size() > 4096 {
			_ = os.Remove(p)
		}
	}
	dst, _ := cache.Open(ctx, t.TempDir(), cache.Options{})
	if _, err := dst.ImportLayout(ctx, layout); err == nil {
		t.Fatal("broken layout imported")
	}
}
