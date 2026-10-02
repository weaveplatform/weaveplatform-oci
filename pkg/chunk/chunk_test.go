package chunk_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/deploymenttheory/weaveplatform-oci/pkg/chunk"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/spec"
)

const testChunk = 64 << 10

var errBoom = errors.New("boom")

// memStore is a Sink and a Source with fault injection.
type memStore struct {
	mu         sync.Mutex
	blobs      map[digest.Digest][]byte
	pushes     int
	existsErr  error
	pushErr    error
	fetchErr   error
	failFetch  map[digest.Digest]int // remaining failures per digest
	corrupt    map[digest.Digest]bool
	alreadyErr bool
}

func newStore() *memStore {
	return &memStore{
		blobs:     map[digest.Digest][]byte{},
		failFetch: map[digest.Digest]int{},
		corrupt:   map[digest.Digest]bool{},
	}
}

func (m *memStore) Exists(_ context.Context, d ocispec.Descriptor) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.existsErr != nil {
		return false, m.existsErr
	}
	_, ok := m.blobs[d.Digest]
	return ok && !m.alreadyErr, nil
}

func (m *memStore) Push(_ context.Context, d ocispec.Descriptor, r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pushErr != nil {
		return m.pushErr
	}
	if digest.FromBytes(b) != d.Digest || int64(len(b)) != d.Size {
		return fmt.Errorf("push %s: content mismatch", d.Digest)
	}
	m.pushes++
	if _, ok := m.blobs[d.Digest]; ok && m.alreadyErr {
		return chunk.ErrAlreadyExists
	}
	m.blobs[d.Digest] = b
	return nil
}

func (m *memStore) Fetch(_ context.Context, d ocispec.Descriptor) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fetchErr != nil {
		return nil, m.fetchErr
	}
	if n := m.failFetch[d.Digest]; n > 0 {
		m.failFetch[d.Digest] = n - 1
		return nil, errBoom
	}
	b, ok := m.blobs[d.Digest]
	if !ok {
		return nil, fmt.Errorf("%s: not found", d.Digest)
	}
	if m.corrupt[d.Digest] {
		b = append([]byte{}, b...)
		b[len(b)-1] ^= 0xff
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

// memDisk is a SparseWriter and io.ReaderAt over a byte slice.
type memDisk struct {
	mu       sync.Mutex
	b        []byte
	punched  int
	punchErr error
	truncErr error
	writeErr error
}

func (d *memDisk) WriteAt(p []byte, off int64) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.writeErr != nil {
		return 0, d.writeErr
	}
	copy(d.b[off:], p)
	return len(p), nil
}

func (d *memDisk) ReadAt(p []byte, off int64) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if off >= int64(len(d.b)) {
		return 0, io.EOF
	}
	n := copy(p, d.b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (d *memDisk) PunchHole(off, n int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.punchErr != nil {
		return d.punchErr
	}
	clear(d.b[off : off+n])
	d.punched++
	return nil
}

func (d *memDisk) Truncate(n int64) error {
	if d.truncErr != nil {
		return d.truncErr
	}
	if int64(len(d.b)) < n {
		d.b = append(d.b, make([]byte, n-int64(len(d.b)))...)
	}
	d.b = d.b[:n]
	return nil
}

// noReader exposes only the SparseWriter methods, hiding ReadAt.
type noReader struct {
	chunk.SparseWriter
}

// testDisk is 3.5 chunks: data, zero, data, short data.
func testDisk() []byte {
	rng := rand.New(rand.NewPCG(1, 2))
	b := make([]byte, 3*testChunk+testChunk/2)
	for i := range testChunk {
		b[i] = byte(rng.IntN(256))
	}
	for i := 2 * testChunk; i < 2*testChunk+100; i++ {
		b[i] = 7
	}
	b[len(b)-1] = 9
	return b
}

func split(t *testing.T, disk []byte, s *memStore) []spec.ChunkLayer {
	t.Helper()
	chunks, err := chunk.Split(
		context.Background(),
		bytes.NewReader(disk),
		int64(len(disk)),
		"disk0",
		s,
		chunk.Options{ChunkSize: testChunk, Concurrency: 3, TempDir: t.TempDir()},
	)
	if err != nil {
		t.Fatal(err)
	}
	return chunks
}

func TestSplitAnnotatesAndDetectsZero(t *testing.T) {
	disk := testDisk()
	s := newStore()
	chunks := split(t, disk, s)
	if len(chunks) != 4 {
		t.Fatalf("%d chunks", len(chunks))
	}
	for i, c := range chunks {
		size := min(int64(testChunk), int64(len(disk))-int64(i)*testChunk)
		if c.Index != int64(i) || c.Offset != int64(i)*testChunk || c.Size != size {
			t.Fatalf("chunk %d: %+v", i, c)
		}
		if c.UncompressedDigest != digest.FromBytes(disk[c.Offset:c.Offset+c.Size]) {
			t.Fatalf("chunk %d uncompressed digest", i)
		}
		a := c.Descriptor.Annotations
		if a[spec.AnnotationChunkIndex] != strconv.Itoa(i) ||
			a[spec.AnnotationDiskName] != "disk0" ||
			a[spec.AnnotationTitle] != fmt.Sprintf("disk0.chunk.%06d", i) ||
			c.Descriptor.MediaType != spec.MediaTypeDiskChunk {
			t.Fatalf("chunk %d annotations %v", i, a)
		}
		var h zstd.Header
		if err := h.Decode(
			s.blobs[c.Descriptor.Digest],
		); err != nil || !h.HasFCS ||
			h.FrameContentSize != uint64(c.Size) {
			t.Fatalf("chunk %d frame header %+v %v", i, h, err)
		}
	}
	if !chunks[1].Zero || chunks[0].Zero ||
		chunks[1].Descriptor.Annotations[spec.AnnotationChunkZero] != "true" {
		t.Fatal("zero detection")
	}
	if _, ok := chunks[0].Descriptor.Annotations[spec.AnnotationChunkZero]; ok {
		t.Fatal("zero annotation on a data chunk")
	}
	if want, _ := spec.ZeroChunkDigest(testChunk); chunks[1].Descriptor.Digest != want {
		t.Fatal("zero chunk is not canonical")
	}
	// a second split pushes nothing new
	before := s.pushes
	split(t, disk, s)
	if s.pushes != before {
		t.Fatalf("re-split pushed %d blobs", s.pushes-before)
	}
}

func TestSplitErrors(t *testing.T) {
	ctx := context.Background()
	disk := testDisk()
	o := chunk.Options{ChunkSize: testChunk, TempDir: t.TempDir()}
	if _, err := chunk.Split(
		ctx,
		bytes.NewReader(disk),
		0,
		"d",
		newStore(),
		o,
	); !errors.Is(
		err,
		chunk.ErrInvalidInput,
	) {
		t.Fatal(err)
	}
	s := newStore()
	s.existsErr = errBoom
	if _, err := chunk.Split(
		ctx,
		bytes.NewReader(disk),
		int64(len(disk)),
		"d",
		s,
		o,
	); !errors.Is(
		err,
		errBoom,
	) {
		t.Fatal(err)
	}
	s = newStore()
	s.pushErr = errBoom
	if _, err := chunk.Split(
		ctx,
		bytes.NewReader(disk),
		int64(len(disk)),
		"d",
		s,
		o,
	); !errors.Is(
		err,
		errBoom,
	) {
		t.Fatal(err)
	}
	// size larger than the reader
	if _, err := chunk.Split(
		ctx,
		bytes.NewReader(disk),
		int64(len(disk))+10,
		"d",
		newStore(),
		o,
	); err == nil {
		t.Fatal("short read accepted")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := chunk.Split(
		cctx,
		bytes.NewReader(disk),
		int64(len(disk)),
		"d",
		newStore(),
		o,
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatal(err)
	}
	o.TempDir = filepath.Join(t.TempDir(), "missing")
	if _, err := chunk.Split(
		ctx,
		bytes.NewReader(disk),
		int64(len(disk)),
		"d",
		newStore(),
		o,
	); err == nil {
		t.Fatal("missing temp dir accepted")
	}
	// a sink that reports ErrAlreadyExists is fine
	s = newStore()
	split(t, disk, s)
	s.alreadyErr = true
	if _, err := chunk.Split(ctx, bytes.NewReader(disk), int64(len(disk)), "d", s,
		chunk.Options{ChunkSize: testChunk, TempDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
}

func TestSplitDefaultOptions(t *testing.T) {
	// default chunk size: a tiny disk is one short chunk
	s := newStore()
	chunks, err := chunk.Split(
		context.Background(),
		bytes.NewReader([]byte("abc")),
		3,
		"d",
		s,
		chunk.Options{},
	)
	if err != nil || len(chunks) != 1 || chunks[0].Size != 3 {
		t.Fatalf("%v %+v", err, chunks)
	}
}

func TestDecodeVerificationOrder(t *testing.T) {
	disk := testDisk()
	s := newStore()
	chunks := split(t, disk, s)
	c := chunks[0]
	blob := s.blobs[c.Descriptor.Digest]
	var out bytes.Buffer
	if err := chunk.Decode(
		bytes.NewReader(blob),
		c,
		&out,
	); err != nil ||
		!bytes.Equal(out.Bytes(), disk[:testChunk]) {
		t.Fatalf("decode: %v", err)
	}
	cases := map[string]struct {
		blob []byte
		c    func(spec.ChunkLayer) spec.ChunkLayer
	}{
		"garbage header": {[]byte("not zstd at all, not at all"), nil},
		"empty":          {nil, nil},
		"fcs mismatch":   {blob, func(c spec.ChunkLayer) spec.ChunkLayer { c.Size++; return c }},
		"compressed size": {
			blob,
			func(c spec.ChunkLayer) spec.ChunkLayer { c.Descriptor.Size++; return c },
		},
		"compressed digest": {blob, func(c spec.ChunkLayer) spec.ChunkLayer {
			c.Descriptor.Digest = digest.FromString("x")
			return c
		}},
		"uncompressed digest": {blob, func(c spec.ChunkLayer) spec.ChunkLayer {
			c.UncompressedDigest = digest.FromString("x")
			return c
		}},
		"truncated frame": {blob[:len(blob)/2], nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cc := c
			if tc.c != nil {
				cc = tc.c(c)
			}
			if err := chunk.Decode(
				bytes.NewReader(tc.blob),
				cc,
				io.Discard,
			); !errors.Is(
				err,
				chunk.ErrVerify,
			) {
				t.Fatalf("want ErrVerify, got %v", err)
			}
		})
	}
	// a frame without a content size is refused
	enc, _ := zstd.NewWriter(nil)
	nofcs := enc.EncodeAll([]byte("hello"), nil)
	if err := chunk.Decode(
		bytes.NewReader(nofcs),
		spec.ChunkLayer{Size: 5},
		io.Discard,
	); !errors.Is(
		err,
		chunk.ErrVerify,
	) {
		t.Fatalf("missing FCS accepted: %v", err)
	}
	// a read error from the source surfaces
	if err := chunk.Decode(
		io.MultiReader(bytes.NewReader(blob), iotestErr{}),
		c,
		io.Discard,
	); err == nil {
		t.Fatal("read error swallowed")
	}
}

type iotestErr struct{}

func (iotestErr) Read([]byte) (int, error) { return 0, errBoom }

func TestAssembleFreshResumeAndRetry(t *testing.T) {
	ctx := context.Background()
	disk := testDisk()
	s := newStore()
	chunks := split(t, disk, s)
	size := int64(len(disk))

	d := &memDisk{}
	st, err := chunk.Assemble(ctx, size, chunks, s, d, chunk.AssembleOptions{Concurrency: 2})
	if err != nil || !bytes.Equal(d.b, disk) || st.Fetched != 3 || st.Zero != 1 {
		t.Fatalf("fresh: %v %+v", err, st)
	}
	// resume: everything already present
	st, err = chunk.Assemble(ctx, size, chunks, s, d, chunk.AssembleOptions{Resume: true})
	if err != nil || st.Resumed != 4 || st.Fetched != 0 {
		t.Fatalf("resume: %v %+v", err, st)
	}
	// resume repairs a damaged data chunk and a damaged zero chunk
	d.b[10] ^= 1
	d.b[testChunk+5] = 1
	st, err = chunk.Assemble(ctx, size, chunks, s, d, chunk.AssembleOptions{Resume: true})
	if err != nil || !bytes.Equal(d.b, disk) || st.Fetched != 1 || st.Zero != 1 || st.Resumed != 2 {
		t.Fatalf("repair: %v %+v", err, st)
	}
	// retries recover from transient fetch failures and corrupt reads
	s.failFetch[chunks[0].Descriptor.Digest] = 2
	d2 := &memDisk{}
	st, err = chunk.Assemble(ctx, size, chunks, s, d2, chunk.AssembleOptions{})
	if err != nil || st.Retried != 2 || !bytes.Equal(d2.b, disk) {
		t.Fatalf("retry: %v %+v", err, st)
	}
	s.corrupt[chunks[2].Descriptor.Digest] = true
	_, err = chunk.Assemble(ctx, size, chunks, s, &memDisk{}, chunk.AssembleOptions{Retries: -1})
	if !errors.Is(err, chunk.ErrVerify) {
		t.Fatalf("corrupt chunk accepted: %v", err)
	}
}

func TestAssembleErrors(t *testing.T) {
	ctx := context.Background()
	disk := testDisk()
	s := newStore()
	chunks := split(t, disk, s)
	size := int64(len(disk))
	if _, err := chunk.Assemble(
		ctx,
		size,
		chunks,
		s,
		noReader{SparseWriter: &memDisk{}},
		chunk.AssembleOptions{Resume: true},
	); !errors.Is(
		err,
		chunk.ErrInvalidInput,
	) {
		t.Fatal(err)
	}
	if _, err := chunk.Assemble(
		ctx,
		size,
		chunks,
		s,
		&memDisk{truncErr: errBoom},
		chunk.AssembleOptions{},
	); !errors.Is(
		err,
		errBoom,
	) {
		t.Fatal(err)
	}
	if _, err := chunk.Assemble(
		ctx,
		size-1,
		chunks,
		s,
		&memDisk{},
		chunk.AssembleOptions{},
	); !errors.Is(
		err,
		chunk.ErrInvalidInput,
	) {
		t.Fatal(err)
	}
	s2 := newStore()
	s2.blobs = s.blobs
	s2.fetchErr = errBoom
	if _, err := chunk.Assemble(
		ctx,
		size,
		chunks,
		s2,
		&memDisk{},
		chunk.AssembleOptions{Retries: 1},
	); !errors.Is(
		err,
		errBoom,
	) {
		t.Fatal(err)
	}
	// punch failure on a stale zero range during resume
	d := &memDisk{b: bytes.Repeat([]byte{1}, len(disk))}
	d.punchErr = errBoom
	if _, err := chunk.Assemble(
		ctx,
		size,
		chunks,
		s,
		d,
		chunk.AssembleOptions{Resume: true},
	); !errors.Is(
		err,
		errBoom,
	) {
		t.Fatal(err)
	}
	// write failure while decoding, and the follow-up clear also failing
	if _, err := chunk.Assemble(
		ctx,
		size,
		chunks,
		s,
		&memDisk{writeErr: errBoom},
		chunk.AssembleOptions{Retries: -1},
	); !errors.Is(
		err,
		errBoom,
	) {
		t.Fatal(err)
	}
	if _, err := chunk.Assemble(
		ctx,
		size,
		chunks,
		s,
		&memDisk{writeErr: errBoom, punchErr: errBoom},
		chunk.AssembleOptions{Retries: -1},
	); err == nil {
		t.Fatal("double failure swallowed")
	}
	// reading the existing range fails
	if _, err := chunk.Assemble(
		ctx,
		size,
		chunks,
		s,
		failingReader{&memDisk{}},
		chunk.AssembleOptions{Resume: true},
	); !errors.Is(
		err,
		errBoom,
	) {
		t.Fatal(err)
	}
	// cancelled context stops retries
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	s3 := newStore()
	s3.blobs = s.blobs
	s3.failFetch[chunks[0].Descriptor.Digest] = 5
	if _, err := chunk.Assemble(
		cctx,
		size,
		chunks,
		s3,
		&memDisk{},
		chunk.AssembleOptions{},
	); err == nil {
		t.Fatal("cancelled assemble succeeded")
	}
}

type failingReader struct{ *memDisk }

func (failingReader) ReadAt([]byte, int64) (int, error) { return 0, errBoom }

func TestFilePunchHoleAndSparseUnpack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.img")
	f, err := chunk.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data := bytes.Repeat([]byte{0xab}, 64<<10)
	if _, err := f.WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}
	for _, r := range [][2]int64{{100, 50}, {1000, 20000}, {8192, 8192}, {0, 4096}} {
		if err := f.PunchHole(r[0], r[1]); err != nil {
			t.Fatalf("punch %v: %v", r, err)
		}
		got := make([]byte, r[1])
		if _, err := f.ReadAt(got, r[0]); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, make([]byte, r[1])) {
			t.Fatalf("range %v not zero after punch", r)
		}
	}
	if _, err := chunk.OpenFile(filepath.Join(t.TempDir(), "missing", "x")); err == nil {
		t.Fatal("open in a missing directory succeeded")
	}
	// a closed file reports write errors from the zero-writing edges
	ro, _ := chunk.OpenFile(filepath.Join(t.TempDir(), "ro"))
	_ = ro.Close()
	if err := ro.PunchHole(1, 10); err == nil {
		t.Fatal("punch on a closed file succeeded")
	}
	if err := ro.PunchHole(1, 3*4096); err == nil {
		t.Fatal("punch on a closed file succeeded")
	}
	if err := ro.PunchHole(4096, 4096+1); err == nil {
		t.Fatal("punch on a closed file succeeded")
	}
	if err := ro.PunchHole(0, 8192); err == nil {
		t.Fatal("punch on a closed file succeeded")
	}
}

func TestAssembleIntoRealFileStaysSparse(t *testing.T) {
	disk := testDisk()
	s := newStore()
	chunks := split(t, disk, s)
	path := filepath.Join(t.TempDir(), "out.img")
	f, err := chunk.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chunk.Assemble(
		context.Background(),
		int64(len(disk)),
		chunks,
		s,
		f,
		chunk.AssembleOptions{},
	); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, disk) {
		t.Fatal("bytes differ")
	}
}
