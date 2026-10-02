package chunk

import (
	"errors"
	"fmt"
	"os"
)

// File is a SparseWriter backed by an operating-system file.
type File struct {
	*os.File
}

// OpenFile opens or creates path for reading and writing.
func OpenFile(path string) (*File, error) {
	f, err := os.OpenFile(
		path,
		os.O_RDWR|os.O_CREATE,
		0o600,
	) //nolint:gosec // path is chosen by the caller
	if err != nil {
		return nil, fmt.Errorf("open disk: %w", err)
	}
	markSparse(f)
	return &File{File: f}, nil
}

// holeAlign is the block size hole punching is aligned to; edges outside the
// aligned middle are written as zeros, which every file system accepts.
const holeAlign = 4096

// errNoHoles reports that the platform or file system cannot punch holes.
var errNoHoles = errors.New("hole punching not supported")

// PunchHole makes [off, off+length) read as zeros.
func (f *File) PunchHole(off, length int64) error {
	start := (off + holeAlign - 1) / holeAlign * holeAlign
	end := (off + length) / holeAlign * holeAlign
	if end <= start {
		return writeZeros(f.File, off, length)
	}
	if err := writeZeros(f.File, off, start-off); err != nil {
		return err
	}
	if err := writeZeros(f.File, end, off+length-end); err != nil {
		return err
	}
	if err := punch(f.File, start, end-start); err != nil {
		if !errors.Is(err, errNoHoles) {
			return err
		}
		return writeZeros(f.File, start, end-start)
	}
	return nil
}

func writeZeros(f *os.File, off, length int64) error {
	for length > 0 {
		n := min(length, int64(len(zeroBlock)))
		if _, err := f.WriteAt(zeroBlock[:n], off); err != nil {
			return fmt.Errorf("write zeros: %w", err)
		}
		off += n
		length -= n
	}
	return nil
}
