package imagebuild

import "context"

// MacRestoreRequest is independent of any guestweave VM configuration or storage.
type MacRestoreRequest struct {
	IPSW, Directory string
	DiskSize        int64
}

// MacRestoreResult contains only the platform requirements carried by an OCI image.
type MacRestoreResult struct {
	HardwareModel                                    string
	CPUCountMin, CPUCount, MemorySizeMin, MemorySize int64
}

// MacRestoreFunc is the native Apple restore boundary, injectable for orchestration tests.
type MacRestoreFunc func(context.Context, MacRestoreRequest) (MacRestoreResult, error)
