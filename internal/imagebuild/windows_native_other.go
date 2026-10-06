//go:build !windows

package imagebuild

import (
	"context"
	"fmt"
)

func installWindowsNative(context.Context, WindowsInstallRequest) (WindowsInstallResult, error) {
	return WindowsInstallResult{}, fmt.Errorf(
		"%w: native Windows image installation requires HCS",
		ErrInput,
	)
}
