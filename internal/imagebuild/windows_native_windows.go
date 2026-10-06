//go:build windows

package imagebuild

import (
	"context"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"github.com/deploymenttheory/go-bindings-win32/bindings/win32/foundation"
	"github.com/deploymenttheory/go-bindings-win32/bindings/win32/security"
	hcs "github.com/deploymenttheory/go-bindings-win32/bindings/win32/system/hostcomputesystem"
	"golang.org/x/sys/windows"

	"github.com/weaveplatform/weaveplatform-oci/pkg/disk/vhd"
)

func installWindowsNative(
	ctx context.Context,
	r WindowsInstallRequest,
) (WindowsInstallResult, error) {
	return installWindowsWith(ctx, r, windowsNativeAPI(hcsSystemCalls{
		operation: hcsOperation,
		create:    hcs.HcsCreateComputeSystem,
		observe:   hcs.HcsSetComputeSystemCallback,
		start:     hcs.HcsStartComputeSystem,
		terminate: hcs.HcsTerminateComputeSystem,
		close:     hcs.HcsCloseComputeSystem,
	}))
}

// These signatures preserve the SDK's handle and callback types at the OS
// boundary while letting tests verify marshalling without creating a VM.
type hcsSystemCalls struct {
	operation func(func(hcs.HCS_OPERATION) error) error
	create    func(string, string, hcs.HCS_OPERATION, *security.SECURITY_DESCRIPTOR, *hcs.HCS_SYSTEM) error
	observe   func(hcs.HCS_SYSTEM, hcs.HCS_EVENT_OPTIONS, unsafe.Pointer, hcs.HCS_EVENT_CALLBACK) error
	start     func(hcs.HCS_SYSTEM, hcs.HCS_OPERATION, *string) error
	terminate func(hcs.HCS_SYSTEM, hcs.HCS_OPERATION, *string) error
	close     func(hcs.HCS_SYSTEM)
}

func windowsNativeAPI(api hcsSystemCalls) windowsNativeCalls {
	return windowsNativeCalls{
		createDisk:  createWindowsDisk,
		createState: hcs.HcsCreateEmptyGuestStateFile,
		grant:       hcs.HcsGrantVmAccess,
		create: func(id, document string) (uintptr, error) {
			var system hcs.HCS_SYSTEM
			err := api.operation(
				func(op hcs.HCS_OPERATION) error { return api.create(id, document, op, nil, &system) },
			)
			return uintptr(system), err
		},
		observe: func(system uintptr, exit func()) error {
			callback := syscall.NewCallback(func(event *hcs.HCS_EVENT, _ uintptr) uintptr {
				if event != nil && event.Type == hcs.HcsEventSystemExited {
					exit()
				}
				return 0
			})
			if err := api.observe(
				hcs.HCS_SYSTEM(system),
				hcs.HcsEventOptionEnableVmLifecycle,
				nil,
				hcs.HCS_EVENT_CALLBACK(callback),
			); err != nil {
				return fmt.Errorf("register HCS callback: %w", err)
			}
			return nil
		},
		start: func(system uintptr) error {
			return api.operation(
				func(op hcs.HCS_OPERATION) error { return api.start(hcs.HCS_SYSTEM(system), op, nil) },
			)
		},
		terminate: func(system uintptr) error {
			return api.operation(func(op hcs.HCS_OPERATION) error {
				return api.terminate(hcs.HCS_SYSTEM(system), op, nil)
			})
		},
		close:   func(system uintptr) { api.close(hcs.HCS_SYSTEM(system)) },
		connect: connectWindowsSerial,
	}
}

func createWindowsDisk(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("create fixed VHD: %w", err)
	}
	var returned uint32
	// FSCTL_SET_SPARSE keeps the empty fixed VHD from allocating 80 GiB up front.
	if err := windows.DeviceIoControl(
		windows.Handle(f.Fd()),
		windows.FSCTL_SET_SPARSE,
		nil,
		0,
		nil,
		0,
		&returned,
		nil,
	); err != nil {
		_ = f.Close()
		return fmt.Errorf("mark build disk sparse (NTFS/ReFS required): %w", err)
	}
	err = f.Truncate(80 << 30)
	closeErr := f.Close()
	if err != nil {
		return fmt.Errorf("size fixed VHD: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("close fixed VHD: %w", closeErr)
	}
	if err := vhd.Append(path, time.Now()); err != nil {
		return fmt.Errorf("write VHD footer: %w", err)
	}
	return nil
}

func connectWindowsSerial(
	ctx context.Context,
	pipe string,
	exited <-chan struct{},
) (net.Conn, error) {
	// DialPipeContext retries a busy pipe internally. A VM exit must interrupt
	// that wait as well as the retry delay between missing-pipe attempts.
	dialCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-exited:
			cancel()
		case <-dialCtx.Done():
		}
	}()
	for {
		conn, err := winio.DialPipeContext(dialCtx, pipe)
		if err == nil {
			return conn, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("connect build serial pipe: %w", ctx.Err())
		case <-exited:
			return nil, fmt.Errorf("%w: VM exited before serial connection", ErrInput)
		case <-time.After(time.Second):
		}
	}
}

func hcsOperation(start func(hcs.HCS_OPERATION) error) error {
	return hcsOperationWith(start, hcsOperationCalls{
		create: func() hcs.HCS_OPERATION { return hcs.HcsCreateOperation(nil, 0) },
		close:  hcs.HcsCloseOperation,
		wait:   hcs.HcsWaitForOperationResult,
		free:   func(record foundation.PWSTR) { _, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(record))) },
	})
}

type hcsOperationCalls struct {
	create func() hcs.HCS_OPERATION
	close  func(hcs.HCS_OPERATION)
	wait   func(hcs.HCS_OPERATION, uint32, *foundation.PWSTR) error
	free   func(foundation.PWSTR)
}

func hcsOperationWith(start func(hcs.HCS_OPERATION) error, api hcsOperationCalls) error {
	op := api.create()
	if op == 0 {
		return fmt.Errorf("%w: cannot create HCS operation", ErrInput)
	}
	defer api.close(op)
	if err := start(op); err != nil {
		return fmt.Errorf("submit HCS operation: %w", err)
	}
	var record foundation.PWSTR
	err := api.wait(op, 60000, &record)
	message := ""
	if record != nil {
		message = windows.UTF16PtrToString((*uint16)(record))
		// HCS result documents use LocalAlloc, not the COM task allocator.
		// https://learn.microsoft.com/virtualization/api/hcs/reference/hcswaitforoperationresult
		api.free(record)
	}
	if err != nil {
		return fmt.Errorf("complete HCS operation: %w: %s", err, message)
	}
	return nil
}
