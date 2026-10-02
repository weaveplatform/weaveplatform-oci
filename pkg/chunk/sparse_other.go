//go:build !windows

package chunk

import "os"

// markSparse is a no-op where files are sparse by default (APFS, ext4, XFS).
func markSparse(*os.File) {}
