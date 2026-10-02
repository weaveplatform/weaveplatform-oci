package spec

import (
	"crypto/sha256"
	"encoding/binary"
	"sync"

	"github.com/opencontainers/go-digest"
)

// The canonical zero chunk (contract §5 rule 5) is defined byte for byte so
// that its digest never depends on an encoder version:
//
//	magic            28 b5 2f fd
//	frame header     c0            (8-byte content size, no single segment,
//	                               no checksum, no dictionary)
//	window           38            (window log 17 = 128 KiB)
//	content size     8 bytes, little endian
//	blocks           ceil(n / 131072) RLE blocks of byte 0x00, each a 3-byte
//	                 header (size<<3 | type RLE<<1 | last) followed by 00;
//	                 every block holds 131072 bytes except the last
const zeroBlockMax = 128 << 10

// ZeroChunk returns the canonical zstd encoding of n zero bytes. n must be
// positive.
func ZeroChunk(n int64) []byte {
	if n <= 0 {
		return nil
	}
	blocks := (n + zeroBlockMax - 1) / zeroBlockMax
	buf := make([]byte, 0, 14+blocks*4)
	buf = append(buf, 0x28, 0xb5, 0x2f, 0xfd, 0xc0, 0x38)
	buf = binary.LittleEndian.AppendUint64(buf, uint64(n))
	for rest := n; rest > 0; {
		size := min(rest, int64(zeroBlockMax))
		rest -= size
		hdr := uint32(size)<<3 | 1<<1
		if rest == 0 {
			hdr |= 1
		}
		buf = append(buf, byte(hdr), byte(hdr>>8), byte(hdr>>16), 0x00)
	}
	return buf
}

// Precomputed digests of the full-size canonical zero chunk; spec tests
// recompute and compare them.
const (
	ZeroChunkCompressedDigest   digest.Digest = "sha256:bc5ab29610eed538b180edfbdf41019a8f3d47c96a5f366fc737ecb461ecbc75"
	ZeroChunkUncompressedDigest digest.Digest = "sha256:9acca8e8c22201155389f65abbf6bc9723edc7384ead80503839f49dcc56d767"
)

var zeroDigests sync.Map // int64 -> [2]digest.Digest

// ZeroChunkDigest returns the compressed and uncompressed digests of the
// canonical zero chunk of length n.
func ZeroChunkDigest(n int64) (compressed, uncompressed digest.Digest) {
	if v, ok := zeroDigests.Load(n); ok {
		d := v.([2]digest.Digest) //nolint:forcetypeassert // only this function stores values
		return d[0], d[1]
	}
	compressed = digest.FromBytes(ZeroChunk(n))
	h := sha256.New()
	block := make([]byte, 1<<20)
	for rest := n; rest > 0; {
		k := min(rest, int64(len(block)))
		_, _ = h.Write(block[:k])
		rest -= k
	}
	uncompressed = digest.NewDigest(digest.SHA256, h)
	zeroDigests.Store(n, [2]digest.Digest{compressed, uncompressed})
	return compressed, uncompressed
}
