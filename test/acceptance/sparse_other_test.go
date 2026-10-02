//go:build !unix

package acceptance

import "os"

func allocated(os.FileInfo) (int64, bool) { return 0, false }
