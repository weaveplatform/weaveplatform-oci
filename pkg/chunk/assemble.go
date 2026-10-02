package chunk

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"

	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

// Source supplies compressed chunk blobs (a cache or a registry).
type Source interface {
	Fetch(ctx context.Context, d ocispec.Descriptor) (io.ReadCloser, error)
}

// SparseWriter is the destination disk file.
type SparseWriter interface {
	io.WriterAt
	// PunchHole makes [off, off+length) read as zeros, deallocating it where
	// the file system allows.
	PunchHole(off, length int64) error
	Truncate(size int64) error
}

// AssembleOptions tune Assemble.
type AssembleOptions struct {
	Concurrency int // default 2
	Retries     int // extra attempts per chunk after a fetch or verification failure; default 2
	// Resume re-checks ranges already in the destination (which must then
	// implement io.ReaderAt) and skips those whose digest already matches.
	// Without Resume the destination must be new or empty.
	Resume bool
}

// Stats reports what Assemble did.
type Stats struct {
	Fetched int64 // chunks downloaded and written
	Zero    int64 // zero chunks skipped or punched
	Resumed int64 // chunks already present and verified
	Retried int64 // extra attempts made
}

// Assemble writes a disk of logicalSize bytes from its chunk layers.
func Assemble(
	ctx context.Context,
	logicalSize int64,
	chunks []spec.ChunkLayer,
	src Source,
	w SparseWriter,
	o AssembleOptions,
) (Stats, error) {
	if o.Concurrency <= 0 {
		o.Concurrency = 2
	}
	if o.Retries < 0 {
		o.Retries = 0
	} else if o.Retries == 0 {
		o.Retries = 2
	}
	var ra io.ReaderAt
	if o.Resume {
		r, ok := w.(io.ReaderAt)
		if !ok {
			return Stats{}, fmt.Errorf(
				"%w: resume needs a destination that can be read",
				ErrInvalidInput,
			)
		}
		ra = r
	}
	if err := w.Truncate(logicalSize); err != nil {
		return Stats{}, fmt.Errorf("truncate: %w", err)
	}
	var fetched, zero, resumed, retried atomic.Int64
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(o.Concurrency)
	for _, c := range chunks {
		g.Go(func() error {
			if c.Offset < 0 || c.Size <= 0 || c.Offset+c.Size > logicalSize {
				return fmt.Errorf(
					"%w: chunk %d range %d+%d outside disk of %d bytes",
					ErrInvalidInput,
					c.Index,
					c.Offset,
					c.Size,
					logicalSize,
				)
			}
			if ra != nil {
				ok, err := rangeMatches(ra, c)
				if err != nil {
					return err
				}
				if ok {
					resumed.Add(1)
					return nil
				}
			}
			if c.Zero {
				zero.Add(1)
				if ra == nil {
					return nil // a freshly truncated file already reads as zeros
				}
				if err := w.PunchHole(c.Offset, c.Size); err != nil {
					return fmt.Errorf("chunk %d: punch hole: %w", c.Index, err)
				}
				return nil
			}
			if ra != nil {
				// the range holds stale bytes; clear it so the sparse write is exact
				if err := w.PunchHole(c.Offset, c.Size); err != nil {
					return fmt.Errorf("chunk %d: clear stale range: %w", c.Index, err)
				}
			}
			var err error
			for attempt := 0; attempt <= o.Retries; attempt++ {
				if attempt > 0 {
					retried.Add(1)
				}
				if err = fetchInto(gctx, src, c, w); err == nil {
					fetched.Add(1)
					return nil
				}
				if gctx.Err() != nil {
					break
				}
			}
			return err
		})
	}
	err := g.Wait()
	return Stats{
		Fetched: fetched.Load(),
		Zero:    zero.Load(),
		Resumed: resumed.Load(),
		Retried: retried.Load(),
	}, err //nolint:wrapcheck // wrapped per chunk
}

func fetchInto(ctx context.Context, src Source, c spec.ChunkLayer, w SparseWriter) error {
	rc, err := src.Fetch(ctx, c.Descriptor)
	if err != nil {
		return fmt.Errorf("chunk %d: fetch: %w", c.Index, err)
	}
	defer func() { _ = rc.Close() }()
	if err := Decode(rc, c, &sparseWriter{w: w, off: c.Offset}); err != nil {
		// leave the range reading as zeros so a retry's sparse write is exact
		if perr := w.PunchHole(c.Offset, c.Size); perr != nil {
			return fmt.Errorf("%w (and clearing the range failed: %w)", err, perr)
		}
		return err
	}
	return nil
}

// rangeMatches reports whether the destination already holds chunk c.
func rangeMatches(ra io.ReaderAt, c spec.ChunkLayer) (bool, error) {
	h := sha256.New()
	n, err := io.Copy(h, io.NewSectionReader(ra, c.Offset, c.Size))
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("chunk %d: read existing range: %w", c.Index, err)
	}
	return n == c.Size && digest.NewDigest(digest.SHA256, h) == c.UncompressedDigest, nil
}
