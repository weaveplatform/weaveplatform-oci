package chunk

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"strconv"

	"github.com/klauspost/compress/zstd"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"

	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

// Sink receives compressed chunk blobs. A content store or a push session
// implements it.
type Sink interface {
	Exists(ctx context.Context, d ocispec.Descriptor) (bool, error)
	Push(ctx context.Context, d ocispec.Descriptor, r io.Reader) error
}

// Options tune Split.
type Options struct {
	// ChunkSize defaults to spec.ChunkSize. Smaller values exist for tests;
	// spec.Inspect rejects artifacts that use them.
	ChunkSize int64
	// Level is the zstd level (RFC 8878 numbering); default 3.
	Level int
	// Concurrency is the number of chunks processed at once; default 2.
	Concurrency int
	// TempDir holds compressed chunks before they are pushed; default os.TempDir().
	TempDir string
}

func (o Options) withDefaults() Options {
	if o.ChunkSize <= 0 {
		o.ChunkSize = spec.ChunkSize
	}
	if o.Level <= 0 {
		o.Level = 3
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 2
	}
	return o
}

const ioBlock = 1 << 20

// Split reads logicalSize bytes of a raw disk and pushes one compressed blob
// per chunk to sink, skipping blobs the sink already has. It returns the chunk
// layers in index order with every contract annotation set.
func Split(
	ctx context.Context,
	disk io.ReaderAt,
	logicalSize int64,
	name string,
	sink Sink,
	o Options,
) ([]spec.ChunkLayer, error) {
	o = o.withDefaults()
	if logicalSize <= 0 {
		return nil, fmt.Errorf("%w: disk %s has size %d", ErrInvalidInput, name, logicalSize)
	}
	count := (logicalSize + o.ChunkSize - 1) / o.ChunkSize
	out := make([]spec.ChunkLayer, count)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(o.Concurrency)
	for i := range count {
		g.Go(func() error {
			off := i * o.ChunkSize
			size := min(o.ChunkSize, logicalSize-off)
			c, err := splitOne(
				gctx,
				io.NewSectionReader(disk, off, size),
				i,
				off,
				size,
				name,
				sink,
				o,
			)
			if err != nil {
				return fmt.Errorf("disk %s chunk %d: %w", name, i, err)
			}
			out[i] = c
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err //nolint:wrapcheck // already wrapped per chunk
	}
	return out, nil
}

type countingHash struct {
	hash.Hash
	n int64
}

func (c *countingHash) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return c.Hash.Write(p) //nolint:wrapcheck // hash writes never fail
}

func splitOne(
	ctx context.Context,
	r io.Reader,
	idx, off, size int64,
	name string,
	sink Sink,
	o Options,
) (spec.ChunkLayer, error) {
	enc, err := zstd.NewWriter(
		nil,
		zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(o.Level)),
		zstd.WithEncoderConcurrency(1),
	)
	if err != nil {
		return spec.ChunkLayer{}, fmt.Errorf("zstd encoder: %w", err)
	}
	tmp, err := os.CreateTemp(o.TempDir, "weave-chunk-*")
	if err != nil {
		return spec.ChunkLayer{}, fmt.Errorf("temp file: %w", err)
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	comp := &countingHash{Hash: sha256.New()}
	enc.ResetContentSize(io.MultiWriter(tmp, comp), size)
	unc := sha256.New()
	allZero := true
	buf := make([]byte, ioBlock)
	var read int64
	for read < size {
		if err := ctx.Err(); err != nil {
			return spec.ChunkLayer{}, fmt.Errorf("split: %w", err)
		}
		n, rerr := io.ReadFull(r, buf[:min(int64(len(buf)), size-read)])
		if n > 0 {
			read += int64(n)
			_, _ = unc.Write(buf[:n])
			if allZero && !isZero(buf[:n]) {
				allZero = false
			}
			if _, err := enc.Write(buf[:n]); err != nil {
				return spec.ChunkLayer{}, fmt.Errorf("compress: %w", err)
			}
		}
		if rerr != nil && read < size {
			return spec.ChunkLayer{}, fmt.Errorf("read: %w", rerr)
		}
	}
	if err := enc.Close(); err != nil {
		return spec.ChunkLayer{}, fmt.Errorf("compress: %w", err)
	}
	layer := spec.ChunkLayer{
		Index:              idx,
		Offset:             off,
		Size:               size,
		UncompressedDigest: digest.NewDigest(digest.SHA256, unc),
		Zero:               allZero,
	}
	var body io.Reader
	if allZero {
		zc := spec.ZeroChunk(size)
		layer.Descriptor = ocispec.Descriptor{
			MediaType: spec.MediaTypeDiskChunk,
			Digest:    digest.FromBytes(zc),
			Size:      int64(len(zc)),
		}
		body = bytesReader(zc)
	} else {
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return spec.ChunkLayer{}, fmt.Errorf("rewind: %w", err)
		}
		layer.Descriptor = ocispec.Descriptor{
			MediaType: spec.MediaTypeDiskChunk,
			Digest:    digest.NewDigest(digest.SHA256, comp),
			Size:      comp.n,
		}
		body = tmp
	}
	layer.Descriptor.Annotations = Annotations(name, layer)
	exists, err := sink.Exists(ctx, layer.Descriptor)
	if err != nil {
		return spec.ChunkLayer{}, fmt.Errorf("exists: %w", err)
	}
	if !exists {
		if err := sink.Push(
			ctx,
			layer.Descriptor,
			body,
		); err != nil &&
			!errors.Is(err, ErrAlreadyExists) {
			return spec.ChunkLayer{}, fmt.Errorf("push: %w", err)
		}
	}
	return layer, nil
}

// Annotations returns the contract §5 annotations for a chunk layer.
func Annotations(disk string, c spec.ChunkLayer) map[string]string {
	a := map[string]string{
		spec.AnnotationTitle:       fmt.Sprintf("%s.chunk.%06d", disk, c.Index),
		spec.AnnotationDiskName:    disk,
		spec.AnnotationChunkIndex:  strconv.FormatInt(c.Index, 10),
		spec.AnnotationChunkOffset: strconv.FormatInt(c.Offset, 10),
		spec.AnnotationChunkSize:   strconv.FormatInt(c.Size, 10),
		spec.AnnotationChunkDigest: c.UncompressedDigest.String(),
	}
	if c.Zero {
		a[spec.AnnotationChunkZero] = "true"
	}
	return a
}

var zeroBlock = make([]byte, ioBlock)

func isZero(b []byte) bool {
	for len(b) > 0 {
		n := min(len(b), len(zeroBlock))
		if string(b[:n]) != string(zeroBlock[:n]) {
			return false
		}
		b = b[n:]
	}
	return true
}
