//go:build !darwin || !arm64

package imagebuild

import (
	"context"
	"fmt"
)

func restoreMacOSNative(context.Context, MacRestoreRequest) (MacRestoreResult, error) {
	return MacRestoreResult{}, fmt.Errorf(
		"%w: Apple restore requires a signed darwin/arm64 builder",
		ErrInput,
	)
}
