//go:build unix

package acceptance

import (
	"os"
	"syscall"
)

func allocated(st os.FileInfo) (int64, bool) {
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return s.Blocks * 512, true
}
