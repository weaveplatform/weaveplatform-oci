//go:build !darwin && !linux

package chunk

import "os"

// punch is unavailable here; PunchHole falls back to writing zeros. The
// Windows consumer marks its files sparse in guestweave-cli-windows.
func punch(*os.File, int64, int64) error { return errNoHoles }
