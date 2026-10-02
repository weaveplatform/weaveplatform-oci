// Package vhd reads and writes the 512-byte footer that turns a raw disk
// into a fixed VHD (Microsoft Virtual Hard Disk Image Format Specification,
// version 1.0).
//
// A fixed VHD is the raw disk followed by the footer, nothing else. That
// makes it the cheapest bridge between the weave contract's raw guest disks
// and Windows: guestweave-cli-windows appends a footer to an assembled raw
// disk and lets virtdisk convert the fixed VHD into a dynamic VHDX parent,
// and for push it has virtdisk flatten a VHDX into a fixed VHD whose first
// Size bytes are the raw disk. No VHDX writer is needed anywhere.
package vhd

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// FooterSize is the size of a VHD footer.
const FooterSize = 512

// MaxSize is the largest disk a VHD can describe: 2040 GiB.
const MaxSize int64 = 2040 << 30

var (
	// ErrSize reports a disk size a fixed VHD cannot hold.
	ErrSize = errors.New("vhd: size must be a positive multiple of 512 no larger than 2040 GiB")
	// ErrFooter reports bytes that are not a valid fixed-VHD footer.
	ErrFooter = errors.New("vhd: not a fixed VHD footer")
)

const (
	cookie       = "conectix"
	features     = 0x00000002 // reserved bit, always set
	formatV1     = 0x00010000
	fixedOffset  = 0xFFFFFFFFFFFFFFFF
	creatorApp   = "wvoc"
	creatorVer   = 0x00010000
	creatorHost  = 0x5769326B // "Wi2k"
	diskTypeFix  = 2
	vhdEpochUnix = 946684800 // 2000-01-01T00:00:00Z
)

// Geometry is the CHS geometry the specification derives from the size.
type Geometry struct {
	Cylinders       uint16
	Heads           uint8
	SectorsPerTrack uint8
}

// Footer is a parsed fixed-VHD footer.
type Footer struct {
	Size     int64
	Geometry Geometry
	UniqueID [16]byte
	Created  time.Time
}

// GeometryFor implements the specification's CHS algorithm (appendix
// "CHS Calculation"), which virtdisk and qemu both use.
func GeometryFor(size int64) Geometry {
	total := size / 512
	if total > 65535*16*255 {
		total = 65535 * 16 * 255
	}
	var spt, heads, cth int64
	if total >= 65535*16*63 {
		spt, heads = 255, 16
		cth = total / spt
	} else {
		spt = 17
		cth = total / spt
		heads = (cth + 1023) / 1024
		if heads < 4 {
			heads = 4
		}
		if cth >= heads*1024 || heads > 16 {
			spt, heads = 31, 16
			cth = total / spt
		}
		if cth >= heads*1024 {
			spt, heads = 63, 16
			cth = total / spt
		}
	}
	return Geometry{
		Cylinders:       uint16(cth / heads),
		Heads:           uint8(heads),
		SectorsPerTrack: uint8(spt),
	} //nolint:gosec // bounded by the algorithm
}

// NewFooter builds the footer for a fixed VHD of size bytes, with a random
// unique ID, stamped at now.
func NewFooter(size int64, now time.Time) ([]byte, error) {
	if size <= 0 || size%512 != 0 || size > MaxSize {
		return nil, ErrSize
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, fmt.Errorf("vhd: unique id: %w", err)
	}
	b := make([]byte, FooterSize)
	be := binary.BigEndian
	copy(b[0:8], cookie)
	be.PutUint32(b[8:12], features)
	be.PutUint32(b[12:16], formatV1)
	be.PutUint64(b[16:24], fixedOffset)
	be.PutUint32(
		b[24:28],
		uint32(max(now.Unix()-vhdEpochUnix, 0)),
	) //nolint:gosec // seconds since 2000 fit until 2136
	copy(b[28:32], creatorApp)
	be.PutUint32(b[32:36], creatorVer)
	be.PutUint32(b[36:40], creatorHost)
	be.PutUint64(b[40:48], uint64(size)) //nolint:gosec // positive, checked above
	be.PutUint64(b[48:56], uint64(size)) //nolint:gosec // positive, checked above
	g := GeometryFor(size)
	be.PutUint16(b[56:58], g.Cylinders)
	b[58], b[59] = g.Heads, g.SectorsPerTrack
	be.PutUint32(b[60:64], diskTypeFix)
	copy(b[68:84], id[:])
	be.PutUint32(b[64:68], checksum(b))
	return b, nil
}

// checksum is the one's complement of the byte sum, with the checksum field
// itself counted as zero.
func checksum(b []byte) uint32 {
	var sum uint32
	for i, c := range b {
		if i >= 64 && i < 68 {
			continue
		}
		sum += uint32(c)
	}
	return ^sum
}

// ParseFooter validates a fixed-VHD footer: cookie, checksum, fixed disk
// type and a size the footer's own fields agree on.
func ParseFooter(b []byte) (Footer, error) {
	if len(b) != FooterSize || !bytes.Equal(b[0:8], []byte(cookie)) {
		return Footer{}, fmt.Errorf("%w: no conectix cookie", ErrFooter)
	}
	be := binary.BigEndian
	if be.Uint32(b[64:68]) != checksum(b) {
		return Footer{}, fmt.Errorf("%w: checksum mismatch", ErrFooter)
	}
	if t := be.Uint32(b[60:64]); t != diskTypeFix {
		return Footer{}, fmt.Errorf("%w: disk type %d is not fixed", ErrFooter, t)
	}
	size := int64(be.Uint64(b[48:56])) //nolint:gosec // validated below
	if size <= 0 || size%512 != 0 {
		return Footer{}, fmt.Errorf("%w: current size %d", ErrFooter, size)
	}
	f := Footer{
		Size:     size,
		Geometry: Geometry{Cylinders: be.Uint16(b[56:58]), Heads: b[58], SectorsPerTrack: b[59]},
		Created:  time.Unix(int64(be.Uint32(b[24:28]))+vhdEpochUnix, 0).UTC(),
	}
	copy(f.UniqueID[:], b[68:84])
	return f, nil
}

// Append makes the raw disk at path a fixed VHD by writing a footer after
// its last byte. The raw size must be a multiple of 512.
func Append(path string, now time.Time) error {
	fh, err := os.OpenFile(path, os.O_RDWR, 0) //nolint:gosec // the caller names the disk
	if err != nil {
		return fmt.Errorf("vhd: %w", err)
	}
	defer func() { _ = fh.Close() }()
	info, err := fh.Stat()
	if err != nil {
		return fmt.Errorf("vhd: %w", err)
	}
	footer, err := NewFooter(info.Size(), now)
	if err != nil {
		return err
	}
	if _, err := fh.WriteAt(footer, info.Size()); err != nil {
		return fmt.Errorf("vhd: write footer: %w", err)
	}
	return nil
}

// Raw returns a reader over the raw disk inside the fixed VHD at path (the
// file without its footer) and the raw size. The caller closes the file.
func Raw(path string) (*io.SectionReader, *os.File, int64, error) {
	fh, err := os.Open(path) //nolint:gosec // the caller names the disk
	if err != nil {
		return nil, nil, 0, fmt.Errorf("vhd: %w", err)
	}
	info, err := fh.Stat()
	if err != nil {
		_ = fh.Close()
		return nil, nil, 0, fmt.Errorf("vhd: %w", err)
	}
	if info.Size() < FooterSize {
		_ = fh.Close()
		return nil, nil, 0, fmt.Errorf("%w: file is %d bytes", ErrFooter, info.Size())
	}
	b := make([]byte, FooterSize)
	if _, err := fh.ReadAt(b, info.Size()-FooterSize); err != nil {
		_ = fh.Close()
		return nil, nil, 0, fmt.Errorf("vhd: read footer: %w", err)
	}
	f, err := ParseFooter(b)
	if err != nil {
		_ = fh.Close()
		return nil, nil, 0, err
	}
	if f.Size != info.Size()-FooterSize {
		_ = fh.Close()
		return nil, nil, 0, fmt.Errorf(
			"%w: footer says %d bytes, file holds %d",
			ErrFooter,
			f.Size,
			info.Size()-FooterSize,
		)
	}
	return io.NewSectionReader(fh, 0, f.Size), fh, f.Size, nil
}
