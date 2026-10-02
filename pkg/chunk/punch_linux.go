//go:build linux

package chunk

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func punch(f *os.File, off, length int64) error {
	err := unix.Fallocate(
		int(f.Fd()),
		unix.FALLOC_FL_PUNCH_HOLE|unix.FALLOC_FL_KEEP_SIZE,
		off,
		length,
	) //nolint:gosec // fd fits in int
	if err != nil {
		if errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOSYS) {
			return errNoHoles
		}
		return fmt.Errorf("fallocate punch hole: %w", err)
	}
	return nil
}
