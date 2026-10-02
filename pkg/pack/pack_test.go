package pack_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"

	"github.com/deploymenttheory/weaveplatform-oci/pkg/chunk"
	"github.com/deploymenttheory/weaveplatform-oci/internal/testbundle"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/pack"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/spec"
)

var errBoom = errors.New("boom")

func bundle(t *testing.T, o testbundle.Options) pack.Bundle {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "bundle")
	if err := testbundle.Write(dir, o); err != nil {
		t.Fatal(err)
	}
	b, err := pack.LoadBundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPackDescribeUnpackRoundTrip(t *testing.T) {
	ctx := context.Background()
	for _, osName := range []string{spec.OSDarwin, spec.OSWindows, spec.OSLinux} {
		t.Run(osName, func(t *testing.T) {
			b := bundle(t, testbundle.Options{OS: osName, Arch: spec.ArchARM64, ExtraDisk: true})
			store, err := pack.OpenLayout(
				context.Background(),
				filepath.Join(t.TempDir(), "layout"),
			)
			if err != nil {
				t.Fatal(err)
			}
			m, err := pack.Manifest(ctx, b, store, chunk.Options{TempDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			d, err := pack.Describe(ctx, store, m)
			if err != nil {
				t.Fatal(err)
			}
			if len(d.Disks) != 2 || d.Disks[1].ZeroChunks != 1 || len(d.Disks[0].Chunks) != 1 {
				t.Fatalf("disks %+v", d.Disks)
			}
			out := filepath.Join(t.TempDir(), "out")
			r, err := pack.Unpack(ctx, store, m, out, chunk.AssembleOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if r.Stats.Fetched != 1 || r.Stats.Zero != 1 {
				t.Fatalf("stats %+v", r.Stats)
			}
			orig, _ := os.ReadFile(filepath.Join(b.Dir, "disk0.img"))
			got, _ := os.ReadFile(filepath.Join(out, "disk0.img"))
			if !bytes.Equal(orig, got) {
				t.Fatal("disk differs after unpack")
			}
			// repacking the unpacked bundle reproduces the manifest digest
			rb, err := pack.LoadBundle(out)
			if err != nil {
				t.Fatal(err)
			}
			again, err := pack.Manifest(ctx, rb, memory.New(), chunk.Options{TempDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			if again.Digest != m.Digest {
				t.Fatalf("repack digest %s, want %s", again.Digest, m.Digest)
			}
			// packing the same bundle into the same store finds every blob present
			if again2, err := pack.Manifest(
				ctx,
				b,
				store,
				chunk.Options{},
			); err != nil ||
				again2.Digest != m.Digest {
				t.Fatalf("second pack into the same store: %v", err)
			}
			// resume skips everything
			r, err = pack.Unpack(ctx, store, m, out, chunk.AssembleOptions{Resume: true})
			if err != nil || r.Stats.Fetched != 0 ||
				r.Stats.Resumed != int64(len(d.Disks[0].Chunks)+chunksOf(d, 1)) {
				t.Fatalf("resume %+v %v", r.Stats, err)
			}
		})
	}
}

func chunksOf(d spec.Description, i int) int {
	if i >= len(d.Disks) {
		return 0
	}
	return len(d.Disks[i].Chunks)
}

func TestIndexMultiArchAndErrors(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	var kids []ocispec.Descriptor
	for _, arch := range []string{spec.ArchARM64, spec.ArchAMD64} {
		m, err := pack.Manifest(
			ctx,
			bundle(t, testbundle.Options{OS: spec.OSLinux, Arch: arch, Seed: 7}),
			store,
			chunk.Options{},
		)
		if err != nil {
			t.Fatal(err)
		}
		kids = append(kids, m)
	}
	idx, err := pack.Index(ctx, store, kids, map[string]string{spec.AnnotationDescription: "test"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := content.FetchAll(ctx, store, idx)
	var got ocispec.Index
	_ = json.Unmarshal(raw, &got)
	if len(got.Manifests) != 2 || got.Manifests[1].Platform.Architecture != spec.ArchAMD64 ||
		got.Annotations[spec.AnnotationDescription] != "test" || got.Annotations[spec.AnnotationVersion] == "" {
		t.Fatalf("index %+v", got)
	}
	// mixing OS families is refused (rule 5)
	w, err := pack.Manifest(
		ctx,
		bundle(t, testbundle.Options{OS: spec.OSWindows, Arch: spec.ArchAMD64}),
		store,
		chunk.Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pack.Index(ctx, store, append(kids, w), nil); !errors.Is(err, spec.ErrInvalid) {
		t.Fatalf("mixed index accepted: %v", err)
	}
	// unknown child
	if _, err := pack.Index(
		ctx,
		store,
		[]ocispec.Descriptor{
			{MediaType: spec.MediaTypeManifest, Digest: digest.FromString("x"), Size: 1},
		},
		nil,
	); err == nil {
		t.Fatal("missing child accepted")
	}
}

func TestLoadBundleErrors(t *testing.T) {
	good := bundle(t, testbundle.Options{OS: spec.OSLinux, Arch: spec.ArchAMD64}).Dir
	edit := func(t *testing.T, f func(*pack.BundleFile)) string {
		t.Helper()
		dir := t.TempDir()
		for _, n := range []string{"disk0.img"} {
			b, _ := os.ReadFile(filepath.Join(good, n))
			_ = os.WriteFile(filepath.Join(dir, n), b, 0o600)
		}
		raw, _ := os.ReadFile(filepath.Join(good, pack.BundleFileName))
		var bf pack.BundleFile
		_ = json.Unmarshal(raw, &bf)
		f(&bf)
		if err := pack.WriteBundleFile(dir, bf); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	cases := map[string]string{
		"missing dir":    filepath.Join(t.TempDir(), "nope"),
		"schema version": edit(t, func(b *pack.BundleFile) { b.SchemaVersion = 2 }),
		"no disks":       edit(t, func(b *pack.BundleFile) { b.Disks = nil }),
		"abs path":       edit(t, func(b *pack.BundleFile) { b.Disks[0].Path = "/etc/passwd" }),
		"escape":         edit(t, func(b *pack.BundleFile) { b.Disks[0].Path = "../disk0.img" }),
		"empty path":     edit(t, func(b *pack.BundleFile) { b.Disks[0].Path = "" }),
		"missing file":   edit(t, func(b *pack.BundleFile) { b.Disks[0].Path = "other.img" }),
		"missing state": edit(t, func(b *pack.BundleFile) {
			b.State = []pack.BundleState{{Name: "uefivars", Path: "nvram.bin", Semantics: "carry"}}
		}),
	}
	for name, dir := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := pack.LoadBundle(dir); !errors.Is(err, pack.ErrBundle) {
				t.Fatalf("want ErrBundle, got %v", err)
			}
		})
	}
	bad := t.TempDir()
	_ = os.WriteFile(
		filepath.Join(bad, pack.BundleFileName),
		[]byte(`{"schemaVersion":1,"bogus":true}`),
		0o600,
	)
	if _, err := pack.LoadBundle(bad); !errors.Is(err, pack.ErrBundle) {
		t.Fatal("unknown field accepted")
	}
	if err := pack.WriteBundleFile(
		filepath.Join(t.TempDir(), "missing"),
		pack.BundleFile{},
	); err == nil {
		t.Fatal("write into a missing directory succeeded")
	}
}

func TestManifestErrors(t *testing.T) {
	ctx := context.Background()
	b := bundle(t, testbundle.Options{OS: spec.OSDarwin, Arch: spec.ArchARM64})
	cases := map[string]func(*pack.Bundle){
		"invalid config":  func(b *pack.Bundle) { b.File.Firmware.HardwareModel = "" },
		"unknown state":   func(b *pack.Bundle) { b.File.State[0].Name = "seed" },
		"state escapes":   func(b *pack.Bundle) { b.File.State[0].Path = "../x" },
		"disk escapes":    func(b *pack.Bundle) { b.File.Disks[0].Path = "../x" },
		"missing created": func(b *pack.Bundle) { b.File.Annotations = map[string]string{} },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			bb := b
			bb.File.State = append([]pack.BundleState{}, b.File.State...)
			bb.File.Disks = append([]pack.BundleDisk{}, b.File.Disks...)
			mut(&bb)
			if _, err := pack.Manifest(ctx, bb, memory.New(), chunk.Options{}); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	// oversized state file
	big := filepath.Join(b.Dir, "big.bin")
	f, _ := os.Create(big)
	_ = f.Truncate(pack.MaxStateSize + 1)
	_ = f.Close()
	bb := b
	bb.File.State = []pack.BundleState{
		{Name: "auxstorage", Path: "big.bin", Semantics: "carry", Required: true},
	}
	if _, err := pack.Manifest(
		ctx,
		bb,
		memory.New(),
		chunk.Options{},
	); !errors.Is(
		err,
		pack.ErrBundle,
	) {
		t.Fatalf("oversized state accepted: %v", err)
	}
	// store failures
	if _, err := pack.Manifest(
		ctx,
		b,
		failingStore{Storage: memory.New(), existsErr: errBoom},
		chunk.Options{},
	); !errors.Is(
		err,
		errBoom,
	) {
		t.Fatal(err)
	}
	if _, err := pack.Manifest(
		ctx,
		b,
		failingStore{Storage: memory.New(), pushErr: errBoom},
		chunk.Options{},
	); !errors.Is(
		err,
		errBoom,
	) {
		t.Fatal(err)
	}
	if _, err := pack.OpenLayout(
		context.Background(),
		filepath.Join(b.Dir, "disk0.img"),
	); err == nil {
		t.Fatal("a file opened as a layout")
	}
}

type failingStore struct {
	content.Storage
	existsErr, pushErr, fetchErr error
	fetchAfter                   int64
	fetches                      *atomic.Int64
}

func (f failingStore) Exists(ctx context.Context, d ocispec.Descriptor) (bool, error) {
	if f.existsErr != nil {
		return false, f.existsErr
	}
	return f.Storage.Exists(ctx, d) //nolint:wrapcheck // test double
}

func (f failingStore) Push(ctx context.Context, d ocispec.Descriptor, r io.Reader) error {
	if f.pushErr != nil {
		return f.pushErr
	}
	return f.Storage.Push(ctx, d, r) //nolint:wrapcheck // test double
}

func (f failingStore) Fetch(ctx context.Context, d ocispec.Descriptor) (io.ReadCloser, error) {
	if f.fetchErr != nil && f.fetches != nil {
		if f.fetches.Add(1) > f.fetchAfter {
			return nil, f.fetchErr
		}
	}
	return f.Storage.Fetch(ctx, d) //nolint:wrapcheck // test double
}

func TestUnpackErrors(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	b := bundle(t, testbundle.Options{OS: spec.OSDarwin, Arch: spec.ArchARM64})
	m, err := pack.Manifest(ctx, b, store, chunk.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pack.Unpack(
		ctx,
		store,
		ocispec.Descriptor{Digest: digest.FromString("x"), Size: 1},
		t.TempDir(),
		chunk.AssembleOptions{},
	); err == nil {
		t.Fatal("missing manifest accepted")
	}
	file := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(file, nil, 0o600)
	if _, err := pack.Unpack(
		ctx,
		store,
		m,
		filepath.Join(file, "sub"),
		chunk.AssembleOptions{},
	); err == nil {
		t.Fatal("unpack under a file succeeded")
	}
	// failing fetches at each stage: manifest, config, chunk, state
	for after := range int64(4) {
		var n atomic.Int64
		fs := failingStore{Storage: store, fetchErr: errBoom, fetchAfter: after, fetches: &n}
		if _, err := pack.Unpack(
			ctx,
			fs,
			m,
			t.TempDir(),
			chunk.AssembleOptions{Retries: -1},
		); err == nil {
			t.Fatalf("fetch failure after %d swallowed", after)
		}
	}
	// a disk path that is a directory cannot be opened
	out := t.TempDir()
	_ = os.Mkdir(filepath.Join(out, "disk0.img"), 0o750)
	if _, err := pack.Unpack(ctx, store, m, out, chunk.AssembleOptions{}); err == nil {
		t.Fatal("disk path that is a directory accepted")
	}
	// a state file that cannot be written
	out = t.TempDir()
	_ = os.Mkdir(filepath.Join(out, "nvram.bin"), 0o750)
	if _, err := pack.Unpack(ctx, store, m, out, chunk.AssembleOptions{}); err == nil {
		t.Fatal("state path that is a directory accepted")
	}
	// a manifest whose config is too large is refused before fetching it
	raw, _ := content.FetchAll(ctx, store, m)
	var man ocispec.Manifest
	_ = json.Unmarshal(raw, &man)
	man.Config.Size = spec.MaxConfigSize + 1
	big, _ := json.Marshal(man)
	bd := content.NewDescriptorFromBytes(spec.MediaTypeManifest, big)
	_ = store.Push(ctx, bd, bytes.NewReader(big))
	if _, err := pack.Describe(ctx, store, bd); !errors.Is(err, spec.ErrInvalid) {
		t.Fatalf("oversized config accepted: %v", err)
	}
	notJSON := []byte("not json")
	nd := content.NewDescriptorFromBytes(spec.MediaTypeManifest, notJSON)
	_ = store.Push(ctx, nd, bytes.NewReader(notJSON))
	if _, err := pack.Describe(
		ctx,
		store,
		nd,
	); err == nil ||
		!strings.Contains(err.Error(), "decode manifest") {
		t.Fatalf("non-JSON manifest: %v", err)
	}
}

// TestContractSizeRoundTrip packs and unpacks a disk with full 512 MiB chunks,
// a zero chunk and a short final chunk.
func TestContractSizeRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("contract-size disk")
	}
	ctx := context.Background()
	b := bundle(
		t,
		testbundle.Options{OS: spec.OSDarwin, Arch: spec.ArchARM64, Size: testbundle.ContractSize},
	)
	store := memory.New()
	m, err := pack.Manifest(ctx, b, store, chunk.Options{Concurrency: 3})
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	r, err := pack.Unpack(ctx, store, m, out, chunk.AssembleOptions{Concurrency: 3})
	if err != nil {
		t.Fatal(err)
	}
	disk := r.Description.Disks[0]
	if len(disk.Chunks) != 3 || disk.ZeroChunks != 1 || !disk.Chunks[1].Zero ||
		disk.Chunks[2].Size != spec.ChunkSize/2 {
		t.Fatalf("layout %+v", disk.Disk)
	}
	if r.Stats.Fetched != 2 || r.Stats.Zero != 1 {
		t.Fatalf("stats %+v", r.Stats)
	}
	same, err := filesEqual(filepath.Join(b.Dir, "disk0.img"), filepath.Join(out, "disk0.img"))
	if err != nil || !same {
		t.Fatalf("disk differs: %v", err)
	}
}

func filesEqual(a, b string) (bool, error) {
	fa, err := os.Open(a)
	if err != nil {
		return false, err
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false, err
	}
	defer fb.Close()
	ba, bb := make([]byte, 1<<20), make([]byte, 1<<20)
	for {
		na, ea := io.ReadFull(fa, ba)
		nb, eb := io.ReadFull(fb, bb)
		if na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return false, nil
		}
		if ea != nil || eb != nil {
			return errors.Is(ea, io.EOF) || errors.Is(ea, io.ErrUnexpectedEOF), nil
		}
	}
}
