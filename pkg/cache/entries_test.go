package cache_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"

	"github.com/weaveplatform/weaveplatform-oci/pkg/cache"
	"github.com/weaveplatform/weaveplatform-oci/pkg/client"
)

func TestEntriesAndRemove(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	refA, idxA := f.publish(t, "a", "1", 1)
	refB, idxB := f.publish(t, "b", "1", 2)
	clock := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	s, err := cache.Open(ctx, t.TempDir(), cache.Options{
		Now: func() time.Time { clock = clock.Add(time.Minute); return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Entries()) != 0 {
		t.Fatal("empty cache lists entries")
	}
	for _, ref := range []client.Reference{refA, refB} {
		if _, err := s.Pull(ctx, f.c, ref); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Pin(idxB.Digest, "vm-two"); err != nil {
		t.Fatal(err)
	}
	if err := s.Pin(idxB.Digest, "vm-one"); err != nil {
		t.Fatal(err)
	}
	es := s.Entries()
	if len(es) != 2 || es[0].Root != idxA.Digest || es[1].Root != idxB.Digest {
		t.Fatalf("order: %+v", es)
	}
	if len(es[0].PinnedBy) != 0 || len(es[1].PinnedBy) != 2 || es[1].PinnedBy[0] != "vm-one" ||
		len(es[0].Refs) == 0 {
		t.Fatalf("pins and refs: %+v", es)
	}

	if err := s.Remove(ctx, idxB.Digest); !errors.Is(err, cache.ErrPinned) {
		t.Fatalf("pinned image removed: %v", err)
	}
	if err := s.Remove(ctx, digest.FromString("nope")); !errors.Is(err, cache.ErrNotCached) {
		t.Fatalf("unknown image: %v", err)
	}
	before, _ := s.Usage()
	if err := s.Remove(ctx, idxA.Digest); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Usage()
	if after >= before || len(s.Entries()) != 1 {
		t.Fatalf("remove freed nothing: %d -> %d, %d entries", before, after, len(s.Entries()))
	}
	if _, err := s.Resolve(ctx, refA.String()); !errors.Is(err, cache.ErrNotCached) {
		t.Fatalf("removed image still resolves: %v", err)
	}
	// unpinning both owners makes it removable
	_ = s.Unpin("vm-one")
	_ = s.Unpin("vm-two")
	if err := s.Remove(ctx, idxB.Digest); err != nil {
		t.Fatal(err)
	}
}

// Two images last used at the same instant list in digest order, so the
// listing is stable.
func TestEntriesTieBreak(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	refA, _ := f.publish(t, "a", "1", 1)
	refB, _ := f.publish(t, "b", "1", 2)
	frozen := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	s, err := cache.Open(ctx, t.TempDir(), cache.Options{Now: func() time.Time { return frozen }})
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []client.Reference{refA, refB} {
		if _, err := s.Pull(ctx, f.c, ref); err != nil {
			t.Fatal(err)
		}
	}
	es := s.Entries()
	if len(es) != 2 || es[0].Root > es[1].Root {
		t.Fatalf("%+v", es)
	}
}
