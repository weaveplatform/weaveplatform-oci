package pack_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/errdef"

	"github.com/deploymenttheory/weaveplatform-oci/internal/testbundle"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/chunk"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/pack"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/spec"
)

// selective fails Push or Fetch for one media type, or claims every push
// already exists.
type selective struct {
	*memory.Store
	pushFail, fetchFail string
	alreadyExists       bool
}

func (s selective) Push(ctx context.Context, d ocispec.Descriptor, r io.Reader) error {
	if d.MediaType == s.pushFail {
		return errBoom
	}
	if s.alreadyExists && d.MediaType == spec.MediaTypeDiskChunk {
		_ = s.Store.Push(ctx, d, r)
		return errdef.ErrAlreadyExists
	}
	return s.Store.Push(ctx, d, r) //nolint:wrapcheck // test double
}

func (s selective) Fetch(ctx context.Context, d ocispec.Descriptor) (io.ReadCloser, error) {
	if d.MediaType == s.fetchFail {
		return nil, errBoom
	}
	return s.Store.Fetch(ctx, d) //nolint:wrapcheck // test double
}

func TestStoreFailuresByMediaType(t *testing.T) {
	ctx := context.Background()
	b := bundle(t, testbundle.Options{OS: spec.OSDarwin, Arch: spec.ArchARM64})
	for _, mt := range []string{spec.MediaTypeConfig, spec.MediaTypeAuxStorage, spec.MediaTypeManifest} {
		if _, err := pack.Manifest(
			ctx,
			b,
			selective{Store: memory.New(), pushFail: mt},
			chunk.Options{},
		); err == nil {
			t.Fatalf("push failure of %s swallowed", mt)
		}
	}
	if _, err := pack.Manifest(
		ctx,
		b,
		selective{Store: memory.New(), fetchFail: spec.MediaTypeManifest},
		chunk.Options{},
	); err == nil {
		t.Fatal("manifest read-back failure swallowed")
	}
	// a store that answers "already exists" for chunks is fine
	s := selective{Store: memory.New(), alreadyExists: true}
	m, err := pack.Manifest(ctx, b, s, chunk.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pack.Index(
		ctx,
		selective{Store: s.Store, pushFail: spec.MediaTypeIndex},
		[]ocispec.Descriptor{m},
		nil,
	); err == nil {
		t.Fatal("index push failure swallowed")
	}
	if _, err := pack.Index(
		ctx,
		selective{Store: s.Store, fetchFail: spec.MediaTypeConfig},
		[]ocispec.Descriptor{m},
		nil,
	); err == nil {
		t.Fatal("config fetch failure swallowed")
	}
}

func TestUnreadableBundleFiles(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not restrict root or Windows")
	}
	ctx := context.Background()
	for _, name := range []string{"disk0.img", "nvram.bin"} {
		b := bundle(t, testbundle.Options{OS: spec.OSDarwin, Arch: spec.ArchARM64})
		p := filepath.Join(b.Dir, name)
		_ = os.Chmod(p, 0)
		t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
		if _, err := pack.Manifest(ctx, b, memory.New(), chunk.Options{}); err == nil {
			t.Fatalf("unreadable %s packed", name)
		}
	}
}

func TestUnpackDotfileStateAndBundleWriteFailure(t *testing.T) {
	ctx := context.Background()
	b := bundle(t, testbundle.Options{OS: spec.OSDarwin, Arch: spec.ArchARM64})
	_ = os.Rename(filepath.Join(b.Dir, "nvram.bin"), filepath.Join(b.Dir, ".nvram"))
	raw, _ := os.ReadFile(filepath.Join(b.Dir, pack.BundleFileName))
	var f pack.BundleFile
	_ = json.Unmarshal(raw, &f)
	f.State[0].Path = ".nvram"
	if err := pack.WriteBundleFile(b.Dir, f); err != nil {
		t.Fatal(err)
	}
	b, err := pack.LoadBundle(b.Dir)
	if err != nil {
		t.Fatal(err)
	}
	store := memory.New()
	m, err := pack.Manifest(ctx, b, store, chunk.Options{})
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if _, err := pack.Unpack(ctx, store, m, out, chunk.AssembleOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "auxstorage.bin")); err != nil {
		t.Fatal("dotfile title not replaced by the fallback name")
	}
	out = t.TempDir()
	_ = os.Mkdir(filepath.Join(out, pack.BundleFileName), 0o750)
	if _, err := pack.Unpack(ctx, store, m, out, chunk.AssembleOptions{}); err == nil {
		t.Fatal("bundle.json write failure swallowed")
	}
}

var _ = errors.New
