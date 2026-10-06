package imagebuild

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
)

// BootOptions describes a disposable QEMU boot and optional provisioning step.
type BootOptions struct {
	Bundle, Report, Script, OutputDisk, Payload string
	Timeout                                     time.Duration
}

// BootResult records the guest's observed boot identity.
type BootResult struct {
	Platform       string  `json:"platform"`
	OSVersion      string  `json:"osVersion"`
	Accelerator    string  `json:"accelerator"`
	Passed         bool    `json:"passed"`
	Marker         string  `json:"marker"`
	MachineID      string  `json:"machineID,omitempty"` //nolint:tagliatelle // Existing acceptance report contract.
	Error          string  `json:"error,omitempty"`
	ElapsedSeconds float64 `json:"elapsedSeconds"`
}

func firmware(arch string) (string, string, error) {
	pairs := [][2]string{{os.Getenv("WEAVE_FIRMWARE_CODE"), os.Getenv("WEAVE_FIRMWARE_VARS")}}
	if arch == "arm64" {
		pairs = append(
			pairs,
			[2]string{
				"/opt/homebrew/share/qemu/edk2-aarch64-code.fd",
				"/opt/homebrew/share/qemu/edk2-arm-vars.fd",
			},
			[2]string{"/usr/share/AAVMF/AAVMF_CODE.fd", "/usr/share/AAVMF/AAVMF_VARS.fd"},
		)
	} else {
		pairs = append(
			pairs,
			[2]string{"/usr/share/OVMF/OVMF_CODE_4M.fd", "/usr/share/OVMF/OVMF_VARS_4M.fd"},
			[2]string{
				"/opt/homebrew/share/qemu/edk2-x86_64-code.fd",
				"/opt/homebrew/share/qemu/edk2-i386-vars.fd",
			},
		)
	}
	for _, pair := range pairs {
		a, e1 := os.Stat(pair[0]) //nolint:gosec // Explicit host firmware override.
		b, e2 := os.Stat(pair[1]) //nolint:gosec // Explicit host firmware override.
		if e1 == nil && e2 == nil && a.Mode().IsRegular() && b.Mode().IsRegular() {
			return pair[0], pair[1], nil
		}
	}
	return "", "", fmt.Errorf(
		"%w: UEFI firmware for %s unavailable; set WEAVE_FIRMWARE_CODE and WEAVE_FIRMWARE_VARS",
		ErrInput,
		arch,
	)
}

func accelerator(arch string) string {
	return acceleratorFor(runtime.GOOS, runtime.GOARCH, arch, os.OpenFile)
}

// Keep host policy separate from the device probe so cross-architecture and
// unavailable-KVM fallback can be verified on every test runner.
func acceleratorFor(
	hostOS, hostArch, guestArch string,
	openKVM func(string, int, os.FileMode) (*os.File, error),
) string {
	if guestArch != hostArch {
		return "tcg"
	}
	if hostOS == "darwin" {
		return "hvf"
	}
	if hostOS != "linux" {
		return "tcg"
	}
	f, err := openKVM("/dev/kvm", os.O_RDWR, 0)
	if err == nil {
		_ = f.Close()
		return "kvm"
	}
	return "tcg"
}

func systemDisk(b pack.Bundle) (string, error) {
	var disk string
	for _, d := range b.File.Disks {
		if d.Role == "system" {
			if disk != "" {
				return "", fmt.Errorf("%w: multiple system disks", ErrInput)
			}
			disk = d.Path
		}
	}
	if disk == "" {
		return "", fmt.Errorf("%w: missing system disk", ErrInput)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(b.Dir, disk))
	if err != nil {
		return "", fmt.Errorf("resolve disk: %w", err)
	}
	root, err := filepath.EvalSymlinks(b.Dir)
	if err != nil {
		return "", fmt.Errorf("resolve bundle: %w", err)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%w: system disk escapes bundle", ErrInput)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat disk: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: disk is not a regular file", ErrInput)
	}
	return resolved, nil
}

func bootScript(b pack.Bundle, marker, script string) string {
	console := "/dev/ttyS0"
	if b.File.Guest.Arch == "arm64" {
		console = "/dev/ttyAMA0"
	}
	check := "set -eu\n. /etc/os-release\ntest \"$VERSION_ID\" = " + shellQuote(
		b.File.Guest.OSVersion,
	) + "\ntest -s /etc/machine-id\nweave_machine_id=$(cat /etc/machine-id)\n"
	if b.File.Guest.Variant == "base" {
		check += "if command -v weave-agent; then exit 1; fi\ntest ! -d /usr/lib/weave/modules\n"
	}
	// The serial login prompt may not end with a newline. Start our marker on
	// its own line so console timing cannot hide a successful boot.
	return check + script + "\nprintf '\\n" + marker + " machine-id=%s\\n' \"$weave_machine_id\" > " + console + "\nsystemctl poweroff\n"
}

func (t Tools) seed(ctx context.Context, work, check, marker, payload string) error {
	dir := filepath.Join(work, "seed")
	if err := os.Mkdir(dir, 0o750); err != nil {
		return fmt.Errorf("create seed: %w", err)
	}
	user, err := json.Marshal(
		map[string]any{"ssh_pwauth": false, "runcmd": [][]string{{"sh", "-c", check}}},
	)
	if err != nil {
		return fmt.Errorf("encode seed: %w", err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "user-data"),
		append([]byte("#cloud-config\n"), user...),
		0o600,
	); err != nil {
		return fmt.Errorf("write seed: %w", err)
	}
	if err := writeJSON(
		filepath.Join(dir, "meta-data"),
		map[string]string{"instance-id": marker, "local-hostname": "weave-test"},
	); err != nil {
		return err
	}
	if payload != "" {
		if err := copyTree(payload, filepath.Join(dir, "packages")); err != nil {
			return err
		}
	}
	out := filepath.Join(work, "seed.iso")
	if runtime.GOOS == "darwin" {
		return t.run(
			ctx,
			nil,
			"hdiutil",
			"makehybrid",
			"-o",
			out,
			"-iso",
			"-joliet",
			"-default-volume-name",
			"cidata",
			dir,
		)
	}
	return t.run(
		ctx,
		nil,
		"genisoimage",
		"-output",
		out,
		"-volid",
		"cidata",
		"-joliet",
		"-rock",
		dir,
	)
}

func copyTree(src, dst string) error {
	err := filepath.WalkDir(src, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return fmt.Errorf("resolve payload path: %w", err)
		}
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o750)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("%w: payload contains non-regular file", ErrInput)
		}
		return copyFile(path, filepath.Join(dst, rel))
	})
	if err != nil {
		return fmt.Errorf("copy payload: %w", err)
	}
	return nil
}

// BootLinux boots a fresh overlay with fresh firmware state; input disks stay immutable.
func (t Tools) BootLinux(ctx context.Context, o BootOptions) (BootResult, error) {
	var result BootResult
	b, err := pack.LoadBundle(o.Bundle)
	if err != nil {
		return result, fmt.Errorf("load boot bundle: %w", err)
	}
	arch := b.File.Guest.Arch
	if b.File.Guest.OS != "linux" || (arch != "amd64" && arch != "arm64") || o.Timeout <= 0 {
		return result, fmt.Errorf("%w: boot needs Linux amd64/arm64 and positive timeout", ErrInput)
	}
	disk, err := systemDisk(b)
	if err != nil {
		return result, err
	}
	code, variables, err := firmware(arch)
	if err != nil {
		return result, err
	}
	if o.OutputDisk != "" {
		if _, err := os.Lstat(o.OutputDisk); !os.IsNotExist(err) {
			return result, fmt.Errorf(
				"%w: output disk already exists or cannot be checked",
				ErrInput,
			)
		}
	}
	if err := newDirectory(o.Report); err != nil {
		return result, err
	}
	work, err := os.MkdirTemp("", "weave-boot-")
	if err != nil {
		return result, fmt.Errorf("create boot workspace: %w", err)
	}
	defer os.RemoveAll(work)
	t.progress(
		"boot linux/%s: preparing fresh disk overlay, firmware and cloud-init seed; bundle=%s report=%s",
		arch,
		o.Bundle,
		o.Report,
	)
	overlay := filepath.Join(work, "overlay.qcow2")
	if err := t.run(
		ctx,
		nil,
		"qemu-img",
		"create",
		"-q",
		"-f",
		"qcow2",
		"-F",
		"raw",
		"-b",
		disk,
		overlay,
	); err != nil {
		return result, err
	}
	raw, err := t.output(ctx, "qemu-img", "info", "--output=json", overlay)
	if err != nil {
		return result, err
	}
	var info struct {
		Size int64 `json:"virtual-size"` //nolint:tagliatelle // QEMU JSON field.
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return result, fmt.Errorf("decode disk size: %w", err)
	}
	if info.Size < 16*1024*1024*1024 {
		if err := t.run(ctx, nil, "qemu-img", "resize", "-q", overlay, "16G"); err != nil {
			return result, err
		}
	}
	if err := copyFile(variables, filepath.Join(work, "vars.fd")); err != nil {
		return result, err
	}
	marker := fmt.Sprintf("WEAVE-BOOT-OK-%x", randomMarker())
	if err := t.seed(ctx, work, bootScript(b, marker, o.Script), marker, o.Payload); err != nil {
		return result, err
	}
	result = BootResult{
		Platform:    "linux/" + arch,
		OSVersion:   b.File.Guest.OSVersion,
		Accelerator: accelerator(arch),
		Marker:      marker,
	}
	err = t.runGuest(ctx, o, work, code, arch, &result)
	if reportErr := writeJSON(filepath.Join(o.Report, "result.json"), result); reportErr != nil {
		return result, reportErr
	}
	if err != nil {
		return result, err
	}
	if o.OutputDisk != "" {
		t.progress("boot linux/%s: exporting provisioned disk to %s", arch, o.OutputDisk)
		if err := t.run(
			ctx,
			nil,
			"qemu-img",
			"convert",
			"-p",
			"-O",
			"raw",
			overlay,
			o.OutputDisk,
		); err != nil {
			return result, err
		}
	}
	return result, nil
}

func randomMarker() []byte { b := make([]byte, 12); _, _ = rand.Read(b); return b }

func (t Tools) runGuest(
	ctx context.Context,
	o BootOptions,
	work, code, arch string,
	result *BootResult,
) error {
	executable, machine := "qemu-system-x86_64", "q35"
	if arch == "arm64" {
		executable, machine = "qemu-system-aarch64", "virt"
	}
	cpu := "max"
	if result.Accelerator != "tcg" {
		cpu = "host"
	}
	log, err := os.Create(filepath.Join(o.Report, "qemu.log"))
	if err != nil {
		return fmt.Errorf("create QEMU log: %w", err)
	}
	defer log.Close()
	serialLog, err := os.Create(filepath.Join(o.Report, "serial.log"))
	if err != nil {
		return fmt.Errorf("create serial log: %w", err)
	}
	defer serialLog.Close()
	bootCtx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	started := time.Now()
	live := t.Log
	if live == nil {
		live = io.Discard
	}
	progress := &bootProgress{
		out:         live,
		started:     started,
		platform:    "linux/" + arch,
		accelerator: result.Accelerator,
		timeout:     o.Timeout,
	}
	t.progress(
		"boot linux/%s: starting %s; timeout=%s serial=%s diagnostics=%s",
		arch,
		executable,
		o.Timeout,
		serialLog.Name(),
		log.Name(),
	)
	progress.status(started, "starting QEMU")
	ticks := time.NewTicker(30 * time.Second)
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() { defer close(stopped); progress.watch(ticks.C, stop) }()
	runner := t
	runner.Log = io.MultiWriter(log, bootStream{progress: progress})
	err = runner.run(
		bootCtx,
		io.MultiWriter(serialLog, bootStream{progress: progress, serial: true}),
		executable,
		"-machine",
		machine+",accel="+result.Accelerator,
		"-cpu",
		cpu,
		"-smp",
		"2",
		"-m",
		"2048",
		"-drive",
		"if=pflash,format=raw,readonly=on,file="+qemuPath(code),
		"-drive",
		"if=pflash,format=raw,file="+qemuPath(filepath.Join(work, "vars.fd")),
		"-drive",
		"file="+qemuPath(filepath.Join(work, "overlay.qcow2"))+",if=virtio,format=qcow2",
		"-drive",
		"file="+qemuPath(filepath.Join(work, "seed.iso"))+",if=virtio,format=raw,readonly=on",
		"-nic",
		"user,model=virtio-net-pci",
		"-display",
		"none",
		"-serial",
		"stdio",
		"-monitor",
		"none",
		"-no-reboot",
	)
	ticks.Stop()
	close(stop)
	<-stopped
	result.ElapsedSeconds = time.Since(started).Seconds()
	if err != nil {
		if bootCtx.Err() != nil {
			err = fmt.Errorf("%w: %w", bootCtx.Err(), err)
		}
		progress.status(time.Now(), "failed: "+err.Error())
		result.Error = err.Error()
		return fmt.Errorf("boot failed; see %s: %w", o.Report, err)
	}
	serial, err := os.ReadFile(filepath.Join(o.Report, "serial.log"))
	if err != nil {
		t.progress("boot linux/%s: cannot read serial log: %v", arch, err)
		return fmt.Errorf("read serial log: %w", err)
	}
	result.MachineID, err = bootIdentity(string(serial), result.Marker)
	result.Passed = err == nil
	if err != nil {
		progress.status(time.Now(), "failed: "+err.Error())
		result.Error = err.Error()
		return fmt.Errorf("boot failed; see %s: %w", o.Report, err)
	}
	progress.status(
		time.Now(),
		"passed: guest shut down and boot identity verified; machine-id="+result.MachineID,
	)
	return nil
}

func qemuPath(path string) string { return strings.ReplaceAll(path, ",", ",,") }

func bootIdentity(serial, marker string) (string, error) {
	pattern := regexp.MustCompile(
		`(?m)^` + regexp.QuoteMeta(marker) + ` machine-id=([0-9a-f]{32})\r*$`,
	)
	matches := pattern.FindAllStringSubmatch(serial, -1)
	if len(matches) != 1 {
		return "", fmt.Errorf("%w: expected exactly one valid guest boot marker", ErrInput)
	}
	return matches[0][1], nil
}
