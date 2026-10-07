// Package virtualdisk uses Windows' virtual-disk APIs for local VHDX storage.
// Fixed VHD is an import/export bridge to raw OCI sectors, never the VM's
// working disk. Conversion creates a new file and leaves the source intact.
package virtualdisk

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

var (
	ErrRequest = errors.New("invalid virtual disk request")
	ErrHost    = errors.New("virtual disk operations require Windows")
)

// Format selects a local disk container, not the OCI layer encoding.
type Format string

const (
	VHDX     Format = "vhdx"
	FixedVHD Format = "vhd"
)

type request struct {
	Source, Destination string
	Size                uint64
	Format              Format
}

// Create makes an empty, dynamic VHDX with 512-byte logical sectors.
func Create(ctx context.Context, path string, size uint64) error {
	return run(ctx, request{Destination: path, Size: size, Format: VHDX}, native)
}

// Convert flattens a stopped disk into a standalone container. The source
// must not be attached to a running VM. Existing destinations are refused.
func Convert(ctx context.Context, source, destination string, format Format) error {
	return run(ctx, request{Source: source, Destination: destination, Format: format}, native)
}

func run(ctx context.Context, r request, call func(request) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.Destination == "" || strings.ContainsRune(r.Destination+r.Source, '\x00') ||
		(r.Format != VHDX && r.Format != FixedVHD) ||
		(r.Source == "" && (r.Size == 0 || r.Size%512 != 0 || r.Format != VHDX)) {
		return ErrRequest
	}
	if _, err := os.Lstat(r.Destination); !os.IsNotExist(err) {
		if err == nil {
			err = os.ErrExist
		}
		return fmt.Errorf("destination %s: %w", r.Destination, err)
	}
	if r.Source != "" {
		info, err := os.Stat(r.Source)
		if err != nil {
			return fmt.Errorf("source: %w", err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: source must be a regular disk file", ErrRequest)
		}
	}
	if err := call(r); err != nil {
		return fmt.Errorf("virtual disk %s: %w", r.Destination, err)
	}
	return ctx.Err()
}
