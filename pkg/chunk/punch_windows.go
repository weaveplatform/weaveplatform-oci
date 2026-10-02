//go:build windows

package chunk

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// NTFS keeps a file's unwritten ranges unallocated only when the file is
// marked sparse; without the flag, extending it with Truncate allocates and
// zero-fills the whole range, and a 64 GiB guest disk costs 64 GiB however
// empty it is. markSparse runs on every disk OpenFile opens.
//
// The error is ignored on purpose: FAT and some network shares have no
// sparse files, and the disk is still correct there, only larger.
func markSparse(f *os.File) {
	var n uint32
	_ = windows.DeviceIoControl(
		windows.Handle(f.Fd()),
		windows.FSCTL_SET_SPARSE,
		nil,
		0,
		nil,
		0,
		&n,
		nil,
	)
}

// fileZeroDataInformation is FILE_ZERO_DATA_INFORMATION.
type fileZeroDataInformation struct {
	FileOffset      int64
	BeyondFinalZero int64
}

func punch(f *os.File, off, length int64) error {
	in := fileZeroDataInformation{FileOffset: off, BeyondFinalZero: off + length}
	var n uint32
	err := windows.DeviceIoControl(
		windows.Handle(f.Fd()),
		windows.FSCTL_SET_ZERO_DATA,
		(*byte)(unsafe.Pointer(&in)), //nolint:gosec // the ioctl's input struct
		uint32(unsafe.Sizeof(in)),
		nil, 0, &n, nil,
	)
	if err != nil {
		return fmt.Errorf("FSCTL_SET_ZERO_DATA: %w", err)
	}
	return nil
}
