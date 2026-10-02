//go:build windows

package cache

import (
	"fmt"

	"golang.org/x/sys/windows"
)

type fsGuard struct{}

func (fsGuard) Available(path string) (int64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, fmt.Errorf("free space %s: %w", path, err)
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return 0, fmt.Errorf("free space %s: %w", path, err)
	}
	return int64(free), nil //nolint:gosec // free bytes fit in int64
}
