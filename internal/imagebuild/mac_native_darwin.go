//go:build darwin && arm64

package imagebuild

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	foundation "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/foundation"
	vz "github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/virtualization"
	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/libraries/dispatch"
	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/obj"
	"github.com/deploymenttheory/go-bindings-macosplatform/bindings/runtime/purego"
	"github.com/deploymenttheory/go-bindings-macosplatform/opinionated/tools/grandcentraldispatch/serialqueue"
)

// The boundary is passed by value: tests can exercise asynchronous failures and
// cancellation without global overrides or creating a real VM.
type macNativeCalls struct {
	load         func(context.Context, string) (*vz.MacOSRestoreImage, error)
	requirements func(*vz.MacOSRestoreImage) *vz.MacOSConfigurationRequirements
	hardware     func(*vz.MacOSConfigurationRequirements) *vz.MacHardwareModel
	data         func(*vz.MacHardwareModel) []byte
	cpu          func(*vz.MacOSConfigurationRequirements) int
	memory       func(*vz.MacOSConfigurationRequirements) uint64
	auxiliary    func(string, *vz.MacHardwareModel, vz.MacAuxiliaryStorageInitializationOptions) (*vz.MacAuxiliaryStorage, error)
	disk         func(string, bool) (*vz.DiskImageStorageDeviceAttachment, error)
	configure    func(MacRestoreResult, *vz.MacHardwareModel, *vz.MacAuxiliaryStorage, *vz.DiskImageStorageDeviceAttachment) (*vz.VirtualMachineConfiguration, error)
	start        func(*vz.VirtualMachineConfiguration, string) macInstallSession
}

type macInstallSession struct {
	done   <-chan error
	cancel func()
	keep   []any
}

func restoreMacOSNative(ctx context.Context, r MacRestoreRequest) (MacRestoreResult, error) {
	return restoreMacOSWith(ctx, r, macNativeCalls{
		load:         vz.LoadFileURL,
		requirements: (*vz.MacOSRestoreImage).MostFeaturefulSupportedConfiguration,
		hardware:     (*vz.MacOSConfigurationRequirements).HardwareModel,
		data:         (*vz.MacHardwareModel).DataRepresentation,
		cpu:          (*vz.MacOSConfigurationRequirements).MinimumSupportedCPUCount,
		memory:       (*vz.MacOSConfigurationRequirements).MinimumSupportedMemorySize,
		auxiliary:    vz.NewMACAuxiliaryStorageCreatingStorageAtURLHardwareModelOptions,
		disk:         vz.NewDiskImageStorageDeviceAttachmentWithURLReadOnly,
		configure:    macNativeConfiguration,
		start:        macNativeStart,
	})
}

func restoreMacOSWith(
	ctx context.Context,
	r MacRestoreRequest,
	api macNativeCalls,
) (MacRestoreResult, error) {
	var result MacRestoreResult
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("restore cancelled: %w", err)
	}
	path, err := filepath.EvalSymlinks(r.IPSW)
	if err != nil {
		return result, fmt.Errorf("resolve IPSW: %w", err)
	}
	image, err := api.load(ctx, path)
	if err != nil {
		return result, fmt.Errorf("load Apple restore image: %w", err)
	}
	defer runtime.KeepAlive(image)
	requirements := api.requirements(image)
	if requirements == nil {
		return result, fmt.Errorf("%w: Apple restore image is unsupported on this host", ErrInput)
	}
	hardware := api.hardware(requirements)
	//nolint:gosec // Apple's host-supported memory requirement fits signed resource sizes.
	result = MacRestoreResult{
		HardwareModel: base64.StdEncoding.EncodeToString(api.data(hardware)),
		CPUCountMin:   int64(api.cpu(requirements)),
		MemorySizeMin: int64(api.memory(requirements)),
	}
	result.CPUCount = max(4, result.CPUCountMin)
	result.MemorySize = max(4<<30, result.MemorySizeMin)
	if r.DiskSize < 64<<30 {
		return result, fmt.Errorf("%w: macOS restore disk must be at least 64 GiB", ErrInput)
	}
	disk := filepath.Join(r.Directory, "disk0.img")
	f, err := os.OpenFile(disk, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return result, fmt.Errorf("create restore disk: %w", err)
	}
	err = f.Truncate(r.DiskSize)
	closeErr := f.Close()
	if err != nil {
		return result, fmt.Errorf("size restore disk: %w", err)
	}
	if closeErr != nil {
		return result, fmt.Errorf("close restore disk: %w", closeErr)
	}
	aux, err := api.auxiliary(filepath.Join(r.Directory, "auxstorage.bin"), hardware, 0)
	if err != nil {
		return result, fmt.Errorf("create Apple auxiliary storage: %w", err)
	}
	attachment, err := api.disk(disk, false)
	if err != nil {
		return result, fmt.Errorf("attach restore disk: %w", err)
	}
	config, err := api.configure(result, hardware, aux, attachment)
	if err != nil {
		return result, fmt.Errorf("validate native restore VM: %w", err)
	}
	session := api.start(config, path)
	defer runtime.KeepAlive(session.keep)
	select {
	case err := <-session.done:
		if err != nil {
			return result, fmt.Errorf("native Apple restore: %w", err)
		}
	case <-ctx.Done():
		session.cancel()
		<-session.done
		return result, fmt.Errorf("native Apple restore cancelled: %w", ctx.Err())
	}
	return result, nil
}

func macNativeConfiguration(
	r MacRestoreResult,
	hardware *vz.MacHardwareModel,
	aux *vz.MacAuxiliaryStorage,
	attachment *vz.DiskImageStorageDeviceAttachment,
) (*vz.VirtualMachineConfiguration, error) {
	platform := vz.NewMacPlatformConfiguration().
		WithHardwareModel(hardware).
		WithAuxiliaryStorage(aux).
		WithMachineIdentifier(vz.NewMacMachineIdentifier())
	display := vz.NewMACGraphicsDisplayConfigurationWithWidthInPixelsHeightInPixelsPixelsPerInch(
		1024,
		768,
		72,
	)
	//nolint:gosec // MemorySize is positive and selected from host-supported requirements.
	config := vz.NewVirtualMachineConfiguration().
		WithPlatform(platform).
		WithBootLoader(vz.NewMacOSBootLoader()).
		WithCPUCount(int(r.CPUCount)).
		WithMemorySize(uint64(r.MemorySize)).
		WithStorageDevices(vz.NewVirtioBlockDeviceConfigurationWithAttachment(vz.StorageDeviceAttachmentFromID(obj.ID(attachment)))).
		WithGraphicsDevices(vz.NewMacGraphicsDeviceConfiguration().WithDisplays(display)).
		WithNetworkDevices(vz.NewVirtioNetworkDeviceConfiguration().WithAttachment(vz.NewNATNetworkDeviceAttachment())).
		WithEntropyDevices(vz.NewVirtioEntropyDeviceConfiguration())
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("validate Apple configuration: %w", err)
	}
	return config, nil
}

func macNativeStart(config *vz.VirtualMachineConfiguration, path string) macInstallSession {
	queue := serialqueue.New("io.weaveplatform.oci.restore")
	var vm *vz.VirtualMachine
	var installer *vz.MacOSInstaller
	queue.Do(func() {
		vm = vz.NewVirtualMachineWithConfigurationQueue(
			config,
			dispatch.WrapQueue(purego.CFRef(purego.ID(queue.Handle()))),
		)
		installer = vz.NewMACOSInstallerWithVirtualMachineRestoreImageURL(vm, path)
	})
	done := make(chan error, 1)
	callback := purego.NewBlock(func(_ purego.Block, errID purego.ID) {
		if errID != 0 {
			done <- purego.NSErrorToError(errID)
		} else {
			done <- nil
		}
	})
	queue.Do(
		func() { obj.ID(installer).Send(purego.RegisterName("installWithCompletionHandler:"), callback) },
	)
	return macInstallSession{
		done:   done,
		cancel: func() { queue.Do(func() { foundation.ProgressFromID(obj.ID(installer.Progress())).Cancel() }) },
		keep:   []any{vm, installer, config, queue},
	}
}
