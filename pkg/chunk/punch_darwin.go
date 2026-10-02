//go:build darwin

package chunk

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// punch uses fcntl(F_PUNCHHOLE). unix.Fstore_t begins with the same four
// fields as struct fpunchhole (flags, reserved, offset, length), so the libc
// fcntl binding can carry it.
func punch(f *os.File, off, length int64) error {
	arg := unix.Fstore_t{Offset: off, Length: length}
	if err := unix.FcntlFstore(f.Fd(), unix.F_PUNCHHOLE, &arg); err != nil {
		if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EINVAL) {
			return errNoHoles
		}
		return fmt.Errorf("F_PUNCHHOLE: %w", err)
	}
	return nil
}
