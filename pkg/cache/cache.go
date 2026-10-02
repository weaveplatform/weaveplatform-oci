// Package cache is the device-side content-addressed store for weave guest
// artifacts (decision 0008): one OCI image layout shared by every image on a
// device, so a chunk present in two images is stored once; in-use pins that
// garbage collection never removes; least-recently-used eviction down to a
// quota; a disk-space check before the first byte of a pull; and OCI-layout
// export and import for air-gapped sites, with referrers carried along.
//
// A Store serialises its own operations; separate processes sharing one
// cache directory must coordinate externally (open question in
// docs/research/12-open-questions.md).
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/errdef"

	"github.com/weaveplatform/weaveplatform-oci/pkg/client"
)

var (
	// ErrNoSpace reports that a pull cannot fit even after evicting
	// everything unpinned.
	ErrNoSpace = errors.New("not enough disk space")
	// ErrNotCached reports a reference the cache does not hold.
	ErrNotCached = errors.New("not in cache")
)

// Guard reports free bytes for the volume holding path. guestweave-cli-macos
// supplies an implementation that counts purgeable APFS space.
type Guard interface {
	Available(path string) (int64, error)
}

// Options configure a Store.
type Options struct {
	// Quota caps the bytes the cache keeps after a GC; default 100 GiB.
	Quota int64
	// Guard checks free space before a pull; nil uses the platform's
	// file-system statistics.
	Guard Guard
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
}

// Store is a cache directory.
type Store struct {
	root  string
	oci   *oci.Store
	quota int64
	guard Guard
	now   func() time.Time
	mu    sync.Mutex
	state state
}

// state is persisted as state.json beside the layout.
type state struct {
	Roots map[digest.Digest]*rootInfo `json:"roots"`
	Pins  map[string]digest.Digest    `json:"pins"`
}

type rootInfo struct {
	Refs       []string  `json:"refs"`
	LastAccess time.Time `json:"lastAccess"`
}

const stateFile = "state.json"

// DefaultQuota is 100 GiB.
const DefaultQuota int64 = 100 << 30

// Open opens or creates a cache at root.
func Open(ctx context.Context, root string, o Options) (*Store, error) {
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("cache: %w", err)
	}
	st, err := oci.NewWithContext(ctx, filepath.Join(root, "layout"))
	if err != nil {
		return nil, fmt.Errorf("cache: %w", err)
	}
	s := &Store{root: root, oci: st, quota: o.Quota, guard: o.Guard, now: o.Now}
	if s.quota <= 0 {
		s.quota = DefaultQuota
	}
	if s.guard == nil {
		s.guard = fsGuard{}
	}
	if s.now == nil {
		s.now = time.Now
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	s.state = state{Roots: map[digest.Digest]*rootInfo{}, Pins: map[string]digest.Digest{}}
	raw, err := os.ReadFile(filepath.Join(s.root, stateFile))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("cache state: %w", err)
	}
	var st state
	if err := json.Unmarshal(raw, &st); err != nil {
		// A torn write must not brick the cache: start over; blobs remain and
		// unreferenced ones are collected by the next GC.
		return nil //nolint:nilerr // recovery is the documented behaviour
	}
	if st.Roots != nil {
		s.state.Roots = st.Roots
	}
	if st.Pins != nil {
		s.state.Pins = st.Pins
	}
	return nil
}

// save writes state.json atomically.
func (s *Store) save() error {
	raw, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return fmt.Errorf("cache state: %w", err)
	}
	tmp := filepath.Join(s.root, stateFile+".tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("cache state: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(s.root, stateFile)); err != nil {
		return fmt.Errorf("cache state: %w", err)
	}
	return nil
}

// Target is the underlying OCI layout store, for pack.Unpack and chunk.Assemble.
func (s *Store) Target() *oci.Store { return s.oci }

// Dir is the cache root directory.
func (s *Store) Dir() string { return s.root }

// Pull fetches ref into the cache (with referrers) unless its root is
// already present, checking free space first and evicting least-recently-used
// unpinned images when needed. It returns the root descriptor.
func (s *Store) Pull(
	ctx context.Context,
	c *client.Client,
	ref client.Reference,
) (ocispec.Descriptor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	need, err := c.Size(ctx, ref, s.oci)
	if err != nil {
		return ocispec.Descriptor{}, err //nolint:wrapcheck // client errors name the reference
	}
	if err := s.ensureSpace(ctx, need); err != nil {
		return ocispec.Descriptor{}, err
	}
	root, _, err := c.Pull(ctx, ref, s.oci, client.PullOptions{Tag: ref.String(), Referrers: true})
	if err != nil {
		return ocispec.Descriptor{}, err //nolint:wrapcheck // client errors name the reference
	}
	s.record(root.Digest, ref.String())
	return root, s.save()
}

func (s *Store) record(d digest.Digest, ref string) {
	ri := s.state.Roots[d]
	if ri == nil {
		ri = &rootInfo{}
		s.state.Roots[d] = ri
	}
	ri.LastAccess = s.now().UTC()
	for _, r := range ri.Refs {
		if r == ref {
			return
		}
	}
	ri.Refs = append(ri.Refs, ref)
	sort.Strings(ri.Refs)
}

// Resolve looks up a cached reference (as written by Pull or ImportLayout)
// or digest and marks it used.
func (s *Store) Resolve(ctx context.Context, ref string) (ocispec.Descriptor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.oci.Resolve(ctx, ref)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("%w: %s", ErrNotCached, ref)
	}
	if ri := s.state.Roots[d.Digest]; ri != nil {
		ri.LastAccess = s.now().UTC()
		if err := s.save(); err != nil {
			return ocispec.Descriptor{}, err
		}
	}
	return d, nil
}

// Pin keeps root alive for owner (a running VM, a clone, a hostweave attempt).
// A Windows parent disk must stay pinned while any differencing child exists.
func (s *Store) Pin(root digest.Digest, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.state.Roots[root]; !ok {
		return fmt.Errorf("%w: %s", ErrNotCached, root)
	}
	s.state.Pins[owner] = root
	return s.save()
}

// Unpin releases owner's pin; unknown owners are ignored.
func (s *Store) Unpin(owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.state.Pins, owner)
	return s.save()
}

// Usage returns the bytes held in the layout's blob store.
func (s *Store) Usage() (int64, error) {
	var n int64
	err := filepath.WalkDir(
		filepath.Join(s.root, "layout", "blobs"),
		func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				info, err := d.Info()
				if err != nil {
					return err
				}
				n += info.Size()
			}
			return nil
		},
	)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("cache usage: %w", err)
	}
	return n, nil
}

// GC evicts unpinned images, least recently used first, until usage is at
// or below keep (the quota when keep <= 0), then removes unreferenced blobs.
// It returns the bytes freed.
func (s *Store) GC(ctx context.Context, keep int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if keep <= 0 {
		keep = s.quota
	}
	return s.gc(ctx, func(used int64) bool { return used <= keep })
}

func (s *Store) gc(ctx context.Context, enough func(used int64) bool) (int64, error) {
	before, err := s.Usage()
	if err != nil {
		return 0, err
	}
	pinned := map[digest.Digest]bool{}
	for _, d := range s.state.Pins {
		pinned[d] = true
	}
	type cand struct {
		d  digest.Digest
		at time.Time
	}
	var order []cand
	for d, ri := range s.state.Roots {
		if !pinned[d] {
			order = append(order, cand{d, ri.LastAccess})
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i].at.Before(order[j].at) })
	used := before
	for _, c := range order {
		if enough(used) {
			break
		}
		if err := s.evict(ctx, c.d); err != nil {
			return 0, err
		}
		delete(s.state.Roots, c.d)
		if err := s.oci.GC(ctx); err != nil {
			return 0, fmt.Errorf("gc: %w", err)
		}
		if used, err = s.Usage(); err != nil {
			return 0, err
		}
	}
	if err := s.save(); err != nil {
		return 0, err
	}
	return before - used, nil
}

// evict untags root and deletes the manifests that refer to it (signatures,
// attestations), depth first. Deleting referrers explicitly also avoids an
// oras-go v2.6.2 bug: oci.Store.GC loops forever on a referrer whose subject
// is no longer tagged (the loop in gcIndex shadows its own variable).
func (s *Store) evict(ctx context.Context, root digest.Digest) error {
	for _, ref := range s.state.Roots[root].Refs {
		if err := s.oci.Untag(ctx, ref); err != nil {
			return fmt.Errorf("evict %s: %w", ref, err)
		}
	}
	desc, err := s.oci.Resolve(ctx, root.String())
	if err != nil {
		return nil //nolint:nilerr // already untracked by the layout: nothing refers to it
	}
	return s.deleteReferrers(ctx, desc)
}

func (s *Store) deleteReferrers(ctx context.Context, d ocispec.Descriptor) error {
	preds, err := s.oci.Predecessors(ctx, d)
	if err != nil {
		return fmt.Errorf("evict: referrers of %s: %w", d.Digest, err)
	}
	for _, p := range preds {
		if err := s.deleteReferrers(ctx, p); err != nil {
			return err
		}
		if err := s.oci.Delete(ctx, p); err != nil && !errors.Is(err, errdef.ErrNotFound) {
			return fmt.Errorf("evict: delete referrer %s: %w", p.Digest, err)
		}
	}
	return nil
}

// ensureSpace evicts until need bytes fit on the volume, or fails before any
// byte is downloaded.
func (s *Store) ensureSpace(ctx context.Context, need int64) error {
	avail, err := s.guard.Available(s.root)
	if err != nil {
		return fmt.Errorf("check free space: %w", err)
	}
	if avail >= need {
		return nil
	}
	if _, err := s.gc(ctx, func(int64) bool {
		a, err := s.guard.Available(s.root)
		return err == nil && a >= need
	}); err != nil {
		return err
	}
	// the guard is authoritative: freed bytes may not translate into free space
	a, err := s.guard.Available(s.root)
	if err != nil {
		return fmt.Errorf("check free space: %w", err)
	}
	if a < need {
		return fmt.Errorf(
			"%w: need %d bytes, %d available after evicting unpinned images",
			ErrNoSpace,
			need,
			a,
		)
	}
	return nil
}

// ExportLayout writes root and its referrers to a new OCI layout at dir,
// tagged ref, for transfer to an air-gapped site.
func (s *Store) ExportLayout(ctx context.Context, ref, dir string) (ocispec.Descriptor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dst, err := oci.NewWithContext(ctx, dir)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("export: %w", err)
	}
	d, err := oras.ExtendedCopy(ctx, s.oci, ref, dst, ref, oras.DefaultExtendedCopyOptions)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("export %s: %w", ref, err)
	}
	return d, nil
}

// ImportLayout copies every tagged root in the layout at dir, with
// referrers, into the cache. Digests are unchanged, so channel and signature
// verification still apply after the transfer.
func (s *Store) ImportLayout(ctx context.Context, dir string) ([]ocispec.Descriptor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, err := oci.NewWithContext(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("import: %w", err)
	}
	var tags []string
	if err := src.Tags(ctx, "", func(t []string) error {
		tags = append(tags, t...)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("import: %w", err)
	}
	out := make([]ocispec.Descriptor, 0, len(tags))
	for _, t := range tags {
		d, err := oras.ExtendedCopy(ctx, src, t, s.oci, t, oras.DefaultExtendedCopyOptions)
		if err != nil {
			return nil, fmt.Errorf("import %s: %w", t, err)
		}
		s.record(d.Digest, t)
		out = append(out, d)
	}
	return out, s.save()
}
