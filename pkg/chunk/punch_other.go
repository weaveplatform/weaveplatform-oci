//go:build !darwin && !linux && !windows

package chunk

import "os"

// punch is unavailable here; PunchHole falls back to writing zeros.
func punch(*os.File, int64, int64) error { return errNoHoles }
