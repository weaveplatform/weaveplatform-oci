//go:build darwin && arm64

package imagebuild

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	vz "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/virtualization"
)

func TestNativeMacRestoreLifecycle(t *testing.T) {
	for _, mode := range []string{"success", "load", "unsupported", "small-disk", "oversized-disk", "existing-disk", "auxiliary", "attachment", "configuration", "install", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			ipsw := filepath.Join(dir, "restore.ipsw")
			must(t, os.WriteFile(ipsw, nil, 0o600))
			size := int64(64 << 30)
			if mode == "oversized-disk" {
				size = math.MaxInt64
			}
			if mode == "small-disk" {
				size = 1024
			}
			if mode == "existing-disk" {
				must(t, os.WriteFile(filepath.Join(dir, "disk0.img"), []byte("preserve"), 0o600))
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			cancelled := false
			started := false
			api := macNativeCalls{
				load: func(context.Context, string) (*vz.MacOSRestoreImage, error) {
					if mode == "load" {
						return nil, ErrInput
					}
					return &vz.MacOSRestoreImage{}, nil
				},
				requirements: func(*vz.MacOSRestoreImage) *vz.MacOSConfigurationRequirements {
					if mode == "unsupported" {
						return nil
					}
					return &vz.MacOSConfigurationRequirements{}
				},
				hardware: func(*vz.MacOSConfigurationRequirements) *vz.MacHardwareModel { return &vz.MacHardwareModel{} },
				data:     func(*vz.MacHardwareModel) []byte { return []byte("hardware") },
				cpu:      func(*vz.MacOSConfigurationRequirements) int { return 6 },
				memory:   func(*vz.MacOSConfigurationRequirements) uint64 { return 8 << 30 },
				auxiliary: func(string, *vz.MacHardwareModel, vz.MacAuxiliaryStorageInitializationOptions) (*vz.MacAuxiliaryStorage, error) {
					if mode == "auxiliary" {
						return nil, ErrInput
					}
					return &vz.MacAuxiliaryStorage{}, nil
				},
				disk: func(_ string, readOnly bool) (*vz.DiskImageStorageDeviceAttachment, error) {
					if readOnly {
						t.Fatal("restore disk read-only")
					}
					if mode == "attachment" {
						return nil, ErrInput
					}
					return &vz.DiskImageStorageDeviceAttachment{}, nil
				},
				configure: func(r MacRestoreResult, _ *vz.MacHardwareModel, _ *vz.MacAuxiliaryStorage, _ *vz.DiskImageStorageDeviceAttachment) (*vz.VirtualMachineConfiguration, error) {
					if r.CPUCount != 6 || r.MemorySize != 8<<30 {
						t.Fatal("ignored Apple's resource floor")
					}
					if mode == "configuration" {
						return nil, ErrInput
					}
					return &vz.VirtualMachineConfiguration{}, nil
				},
				start: func(*vz.VirtualMachineConfiguration, string) macInstallSession {
					started = true
					if mode == "cancel" {
						cancel()
					} else if mode == "install" {
						done <- ErrInput
					} else {
						done <- nil
					}
					return macInstallSession{
						done:     done,
						cancel:   func() { cancelled = true; done <- context.Canceled },
						fraction: func() float64 { return 0.375 },
					}
				},
			}
			var live bytes.Buffer
			result, err := restoreMacOSWith(
				ctx,
				MacRestoreRequest{IPSW: ipsw, Directory: dir, DiskSize: size, Log: &live},
				api,
			)
			if mode == "success" {
				must(t, err)
				if result.HardwareModel == "" || !started {
					t.Fatal(result)
				}
				for _, want := range []string{"loading Apple restore image", "restore=37.5%", "completed"} {
					if !strings.Contains(live.String(), want) {
						t.Fatal("missing restore progress", want, live.String())
					}
				}
			} else if err == nil {
				t.Fatal("ignored failure")
			}
			if err != nil && !strings.Contains(live.String(), "failed: ") {
				t.Fatal("missing restore error", live.String())
			}
			if mode == "cancel" && (!cancelled || !errors.Is(err, context.Canceled)) {
				t.Fatal("cancellation did not await native completion", err)
			}
			if mode == "existing-disk" {
				raw, err := os.ReadFile(filepath.Join(dir, "disk0.img"))
				must(t, err)
				if string(raw) != "preserve" {
					t.Fatal("overwrote disk")
				}
			}
		})
	}
}

func TestNativeMacConfigurationRejectsInvalidResources(t *testing.T) {
	// Public, non-machine-specific hardware model emitted by Apple's VirtualMac
	// restore metadata. No machine identifier or restored disk is stored here.
	const model = "YnBsaXN0MDDTAQIDBAQFXxAZRGF0YVJlcHJlc2VudGF0aW9uVmVyc2lvbl8QD1BsYXRmb3JtVmVyc2lvbl8QEk1pbmltdW1TdXBwb3J0ZWRPUxACowYHBxANEAAIDys9UlRYWgAAAAAAAAEBAAAAAAAAAAgAAAAAAAAAAAAAAAAAAABc"
	raw, err := base64.StdEncoding.DecodeString(model)
	must(t, err)
	hardware := vz.NewMACHardwareModelWithDataRepresentation(raw)
	if hardware == nil {
		t.Fatal("Apple hardware model could not be decoded")
	}
	dir := t.TempDir()
	aux, err := vz.NewMACAuxiliaryStorageCreatingStorageAtURLHardwareModelOptions(
		filepath.Join(dir, "auxstorage.bin"),
		hardware,
		0,
	)
	must(t, err)
	disk := filepath.Join(dir, "disk.img")
	must(t, os.WriteFile(disk, make([]byte, 1024), 0o600))
	attachment, err := vz.NewDiskImageStorageDeviceAttachmentWithURLReadOnly(disk, false)
	must(t, err)
	config, err := macNativeConfiguration(MacRestoreResult{}, hardware, aux, attachment)
	if err == nil || config != nil {
		t.Fatal("native API accepted zero CPU and memory")
	}
}
