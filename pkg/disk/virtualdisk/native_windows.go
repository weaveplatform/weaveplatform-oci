//go:build windows

package virtualdisk

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	win32 "github.com/deploymenttheory/go-bindings-win32/bindings/runtime/win32"
	"github.com/deploymenttheory/go-bindings-win32/bindings/win32/foundation"
	"github.com/deploymenttheory/go-bindings-win32/bindings/win32/storage/vhd"
)

// The generated binding represents the SDK union as aligned byte storage.
type parametersV2 struct {
	UniqueID                                  win32.GUID
	MaximumSize                               uint64
	BlockSize, SectorSize, PhysicalSectorSize uint32
	_                                         uint32
	Parent, Source                            *uint16
	OpenFlags                                 uint32
	ParentType, SourceType                    vhd.VIRTUAL_STORAGE_TYPE
	ResiliencyGUID                            win32.GUID
}

func native(r request) error {
	storage := vhd.VIRTUAL_STORAGE_TYPE{
		DeviceId: vhd.VIRTUAL_STORAGE_TYPE_DEVICE_VHDX,
		VendorId: vhd.VIRTUAL_STORAGE_TYPE_VENDOR_MICROSOFT,
	}
	flags := vhd.CREATE_VIRTUAL_DISK_FLAG_NONE
	if r.Format == FixedVHD {
		storage.DeviceId = vhd.VIRTUAL_STORAGE_TYPE_DEVICE_VHD
		flags = vhd.CREATE_VIRTUAL_DISK_FLAG_FULL_PHYSICAL_ALLOCATION
	}
	params := vhd.CREATE_VIRTUAL_DISK_PARAMETERS{Version: vhd.CREATE_VIRTUAL_DISK_VERSION_2}
	p := (*parametersV2)(unsafe.Pointer(&params.Anonymous))
	p.MaximumSize, p.SectorSize = r.Size, 512
	if r.Source != "" {
		p.Source = win32.UTF16Ptr(r.Source)
		// Let virtdisk inspect the source container rather than its extension.
		p.SourceType.VendorId = vhd.VIRTUAL_STORAGE_TYPE_VENDOR_MICROSOFT
	}
	var handle foundation.HANDLE
	code := vhd.CreateVirtualDisk(
		&storage,
		r.Destination,
		vhd.VIRTUAL_DISK_ACCESS_NONE,
		0,
		flags,
		0,
		&params,
		nil,
		&handle,
	)
	runtime.KeepAlive(params)
	if code != 0 {
		return fmt.Errorf("CreateVirtualDisk: %w", syscall.Errno(code))
	}
	if err := foundation.CloseHandle(handle); err != nil {
		return fmt.Errorf("close virtual disk: %w", err)
	}
	return nil
}
