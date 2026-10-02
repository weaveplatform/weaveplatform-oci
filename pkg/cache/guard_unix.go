//go:build unix

package cache

import (
	"fmt"

	"golang.org/x/sys/unix"
)

type fsGuard struct{}

func (fsGuard) Available(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	return int64(
		st.Bavail,
	) * int64(
		st.Bsize,
	), nil //nolint:gosec,unconvert // block counts fit in int64
}
