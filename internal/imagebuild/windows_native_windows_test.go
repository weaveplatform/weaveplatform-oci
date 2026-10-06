//go:build windows

package imagebuild

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"github.com/deploymenttheory/go-bindings-win32/bindings/win32/foundation"
	"github.com/deploymenttheory/go-bindings-win32/bindings/win32/security"
	hcs "github.com/deploymenttheory/go-bindings-win32/bindings/win32/system/hostcomputesystem"
	"golang.org/x/sys/windows"

	"github.com/weaveplatform/weaveplatform-oci/pkg/disk/vhd"
)

func TestNativeWindowsDiskAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := installWindowsNative(
		ctx,
		WindowsInstallRequest{Timeout: time.Minute},
	); err == nil {
		t.Fatal("cancelled installer ran")
	}
	root := t.TempDir()
	disk := filepath.Join(root, "disk.vhd")
	must(t, createWindowsDisk(disk))
	_, f, size, err := vhd.Raw(disk)
	must(t, err)
	must(t, f.Close())
	if size != 80<<30 {
		t.Fatal(size)
	}
	if err := createWindowsDisk(disk); err == nil {
		t.Fatal("replaced existing disk")
	}
	// A second installer must refuse the existing disk before any HCS VM call.
	if _, err := installWindowsNative(
		t.Context(),
		WindowsInstallRequest{Directory: root, Timeout: time.Minute},
	); err == nil {
		t.Fatal("reused disk")
	}
	must(t, os.WriteFile(filepath.Join(root, "blocker"), nil, 0o600))
	if err := createWindowsDisk(filepath.Join(root, "blocker", "disk")); err == nil {
		t.Fatal("invalid disk path")
	}
}

func TestNativeWindowsSerialConnection(t *testing.T) {
	pipe := `\\.\pipe\weaveoci-test-` + time.Now().Format("150405.000000000")
	listener, err := winio.ListenPipe(pipe, nil)
	must(t, err)
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err == nil {
			conn.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, err := connectWindowsSerial(ctx, pipe, make(chan struct{}))
	must(t, err)
	must(t, conn.Close())
	<-done
	cancel()
	if _, err := connectWindowsSerial(ctx, pipe+"-absent", make(chan struct{})); err == nil {
		t.Fatal("cancelled connection succeeded")
	}
	exited := make(chan struct{})
	close(exited)
	if _, err := connectWindowsSerial(t.Context(), pipe+"-absent", exited); err == nil {
		t.Fatal("connected to exited VM")
	}
}

func TestHCSOperationLifetime(t *testing.T) {
	for _, mode := range []string{"success", "allocation", "submit", "wait", "record"} {
		t.Run(mode, func(t *testing.T) {
			closed, freed, waited := false, false, false
			record, err := windows.UTF16FromString("native diagnostic")
			must(t, err)
			api := hcsOperationCalls{
				create: func() hcs.HCS_OPERATION {
					if mode == "allocation" {
						return 0
					}
					return 42
				},
				close: func(op hcs.HCS_OPERATION) {
					if op != 42 {
						t.Fatal(op)
					}
					closed = true
				},
				wait: func(op hcs.HCS_OPERATION, timeout uint32, out *foundation.PWSTR) error {
					waited = true
					if op != 42 || timeout != 60000 {
						t.Fatal("wrong operation wait")
					}
					if mode == "record" {
						*out = foundation.PWSTR(&record[0])
					}
					if mode == "wait" || mode == "record" {
						return ErrInput
					}
					return nil
				},
				free: func(p foundation.PWSTR) {
					if p != foundation.PWSTR(&record[0]) {
						t.Fatal("freed wrong allocation")
					}
					freed = true
				},
			}
			err = hcsOperationWith(func(op hcs.HCS_OPERATION) error {
				if op != 42 {
					t.Fatal(op)
				}
				if mode == "submit" {
					return ErrInput
				}
				return nil
			}, api)
			if (err == nil) != (mode == "success") {
				t.Fatal(mode, err)
			}
			if closed != (mode != "allocation") ||
				waited != (mode != "allocation" && mode != "submit") ||
				freed != (mode == "record") {
				t.Fatal("native resource lifetime", closed, waited, freed)
			}
			if mode == "record" && !strings.Contains(err.Error(), "native diagnostic") {
				t.Fatal("lost native diagnostic", err)
			}
		})
	}
}

func TestWindowsSDKHandleAndCallbackAdapter(t *testing.T) {
	var callback hcs.HCS_EVENT_CALLBACK
	starts, stops, closes, exits := 0, 0, 0, 0
	failCallback := false
	api := windowsNativeAPI(hcsSystemCalls{
		operation: func(f func(hcs.HCS_OPERATION) error) error { return f(17) },
		create: func(id, doc string, op hcs.HCS_OPERATION, sd *security.SECURITY_DESCRIPTOR, system *hcs.HCS_SYSTEM) error {
			if id != "id" || doc != "document" || op != 17 || sd != nil {
				t.Fatal("create arguments")
			}
			*system = 23
			return nil
		},
		observe: func(system hcs.HCS_SYSTEM, options hcs.HCS_EVENT_OPTIONS, ctx unsafe.Pointer, cb hcs.HCS_EVENT_CALLBACK) error {
			if system != 23 || options != hcs.HcsEventOptionEnableVmLifecycle || ctx != nil {
				t.Fatal("observe arguments")
			}
			if failCallback {
				return ErrInput
			}
			callback = cb
			return nil
		},
		start: func(system hcs.HCS_SYSTEM, op hcs.HCS_OPERATION, options *string) error {
			if system != 23 || op != 17 || options != nil {
				t.Fatal("start arguments")
			}
			starts++
			return nil
		},
		terminate: func(system hcs.HCS_SYSTEM, op hcs.HCS_OPERATION, options *string) error {
			if system != 23 || op != 17 || options != nil {
				t.Fatal("terminate arguments")
			}
			stops++
			return nil
		},
		close: func(system hcs.HCS_SYSTEM) {
			if system != 23 {
				t.Fatal(system)
			}
			closes++
		},
	})
	system, err := api.create("id", "document")
	must(t, err)
	must(t, api.observe(system, func() { exits++ }))
	syscall.SyscallN(uintptr(callback), 0, 0)
	event := hcs.HCS_EVENT{}
	syscall.SyscallN(uintptr(callback), uintptr(unsafe.Pointer(&event)), 0)
	if exits != 0 {
		t.Fatal("non-exit event treated as shutdown")
	}
	event.Type = hcs.HcsEventSystemExited
	syscall.SyscallN(uintptr(callback), uintptr(unsafe.Pointer(&event)), 0)
	if exits != 1 {
		t.Fatal("exit callback lost")
	}
	failCallback = true
	if err := api.observe(system, func() {}); err == nil {
		t.Fatal("callback registration failure ignored")
	}
	must(t, api.start(system))
	must(t, api.terminate(system))
	api.close(system)
	if starts != 1 || stops != 1 || closes != 1 {
		t.Fatal("native operations lost")
	}
}

func TestNativeHCSOperationSubmissionFailure(t *testing.T) {
	err := hcsOperation(func(hcs.HCS_OPERATION) error { return ErrInput })
	if !errors.Is(err, ErrInput) {
		t.Fatal("lost submission error", err)
	}
}
