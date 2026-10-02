package chunk

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
	"github.com/opencontainers/go-digest"

	"github.com/deploymenttheory/weaveplatform-oci/pkg/spec"
)

// Decode decompresses one chunk blob from r into dst and verifies it in the
// contract §5 rule 8 order: compressed size and digest against the
// descriptor, uncompressed size and digest against the annotations, and the
// frame content size against the chunk size. dst receives the bytes as they
// are decoded, so a caller writing in place must discard the range on error.
func Decode(r io.Reader, c spec.ChunkLayer, dst io.Writer) error {
	comp := &countingHash{Hash: sha256.New()}
	br := bufio.NewReaderSize(io.TeeReader(r, comp), 64<<10)
	head, _ := br.Peek(zstd.HeaderMaxSize)
	var h zstd.Header
	if err := h.Decode(head); err != nil {
		return fmt.Errorf("%w: chunk %d: frame header: %w", ErrVerify, c.Index, err)
	}
	if !h.HasFCS ||
		h.FrameContentSize != uint64(c.Size) { //nolint:gosec // c.Size is validated non-negative
		return fmt.Errorf("%w: chunk %d: frame content size %d (present %v), want %d",
			ErrVerify, c.Index, h.FrameContentSize, h.HasFCS, c.Size)
	}
	dec, err := zstd.NewReader(br, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return fmt.Errorf("zstd decoder: %w", err)
	}
	defer dec.Close()
	unc := &countingHash{Hash: sha256.New()}
	if _, err := io.Copy(io.MultiWriter(dst, unc), dec); err != nil {
		return fmt.Errorf("%w: chunk %d: decode: %w", ErrVerify, c.Index, err)
	}
	if _, err := io.Copy(io.Discard, br); err != nil {
		return fmt.Errorf("%w: chunk %d: read: %w", ErrVerify, c.Index, err)
	}
	if comp.n != c.Descriptor.Size || digest.NewDigest(digest.SHA256, comp) != c.Descriptor.Digest {
		return fmt.Errorf(
			"%w: chunk %d: compressed blob does not match its descriptor",
			ErrVerify,
			c.Index,
		)
	}
	if unc.n != c.Size || digest.NewDigest(digest.SHA256, unc) != c.UncompressedDigest {
		return fmt.Errorf(
			"%w: chunk %d: uncompressed bytes do not match the chunk annotations",
			ErrVerify,
			c.Index,
		)
	}
	return nil
}
