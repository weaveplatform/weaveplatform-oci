package imagebuild

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/weaveclient"
	"github.com/weaveplatform/weaveplatform-oci/pkg/agentcheck"
	"github.com/weaveplatform/weaveplatform-oci/pkg/imagecheck"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
)

// LinuxAgentBoot supplies the concrete QEMU adapter for imagecheck.Validate.
// Each clone receives fresh disk/firmware and only its public host trust key.
// The private keys remain in the host process. No Guestweave CLI is involved.
func (t Tools) LinuxAgentBoot(lock Lock, consoleUser string, sequence uint64) imagecheck.BootClone {
	return func(ctx context.Context, clone imagecheck.Clone) (imagecheck.Boot, error) {
		return t.bootLinuxAgent(ctx, clone, lock, consoleUser, sequence, agentcheck.ProbeLifecycle)
	}
}

type linuxAgentProbe func(context.Context, agentcheck.Lifecycle, ed25519.PrivateKey, ed25519.PrivateKey, agentcheck.Expected, uint64) (agentcheck.LifecycleResult, error)

var consoleAccount = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,30}$`)

func (t Tools) bootLinuxAgent(
	ctx context.Context,
	c imagecheck.Clone,
	lock Lock,
	consoleUser string,
	sequence uint64,
	probe linuxAgentProbe,
) (result imagecheck.Boot, err error) {
	started := time.Now()
	progress := newNativeProgress(ctx, t.Log, "Linux agent acceptance")
	finish := progress.start()
	defer func() { finish(err) }()
	defer func() {
		result.ElapsedSeconds = time.Since(started).Seconds()
		if err != nil {
			result.Passed, result.Error = false, err.Error()
		}
	}()
	e, err := linuxAgentExpectations(c, lock, consoleUser)
	if err != nil {
		return result, err
	}
	b, err := pack.LoadBundle(c.Bundle)
	if err != nil {
		return result, fmt.Errorf("load acceptance clone: %w", err)
	}
	disk, err := systemDisk(b)
	if err != nil {
		return result, err
	}
	code, variables, err := firmware(e.Arch)
	if err != nil {
		return result, err
	}
	work, err := os.MkdirTemp("", "wa-")
	if err != nil {
		return result, fmt.Errorf("create agent acceptance workspace: %w", err)
	}
	defer os.RemoveAll(work)
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
		filepath.Join(work, "overlay.qcow2"),
	); err != nil {
		return result, err
	}
	if err := copyFileContext(ctx, variables, filepath.Join(work, "vars.fd")); err != nil {
		return result, err
	}
	// The ordinary seed helper's shutdown condition deliberately remains false:
	// only an authenticated power operation may shut down an acceptance VM.
	if err := t.seed(
		ctx,
		work,
		linuxAgentSeed(c, consoleUser),
		"agent-controlled-power",
		"",
	); err != nil {
		return result, err
	}
	result = imagecheck.Boot{
		Platform:    "linux/" + e.Arch,
		Profile:     imagecheck.ValidationProfile(c.Config),
		Accelerator: accelerator(e.Arch),
	}
	log, err := os.Create(filepath.Join(c.Report, "qemu.log"))
	if err != nil {
		return result, fmt.Errorf("create agent VM log: %w", err)
	}
	defer log.Close()
	serial, err := os.Create(filepath.Join(c.Report, "serial.log"))
	if err != nil {
		return result, fmt.Errorf("create agent serial log: %w", err)
	}
	defer serial.Close()
	vmCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	runner := t
	runner.Log = io.MultiWriter(log, progress)
	name, args := linuxAgentQEMU(work, code, e.Arch, result.Accelerator, e.Desktop)
	progress.step("starting QEMU; profile=" + result.Profile + " serial=" + serial.Name())
	go func() { done <- runner.run(vmCtx, io.MultiWriter(serial, progress), name, args...) }()
	finished := false
	defer func() {
		cancel()
		if !finished {
			<-done
		}
	}()
	progress.step("awaiting guest cloud-init trust provisioning")
	if err := waitLinuxAgentSeed(vmCtx, serial.Name(), c.Marker, done); err != nil {
		return result, err
	}
	progress.step("checking module operations, host trust and reboot persistence")
	vm := agentcheck.Lifecycle{
		Ready: func(ctx context.Context, client *weaveclient.Client) error {
			return agentcheck.WaitModules(ctx, client, e)
		},
		Dial: func(ctx context.Context) (io.ReadWriteCloser, error) {
			return dialLocal(ctx, filepath.Join(work, "agent.sock"))
		},
		Inspect: func(ctx context.Context, client *weaveclient.Client) (agentcheck.Snapshot, error) {
			observed, err := agentcheck.InspectLinux(ctx, client)
			if err != nil {
				return observed.Snapshot, fmt.Errorf("inspect agent clone: %w", err)
			}
			if observed.Version != c.Config.Guest.OSVersion ||
				observed.Build != c.Config.Guest.OSBuild ||
				observed.Challenge != c.Marker {
				return observed.Snapshot, fmt.Errorf(
					"%w: observed Linux release/serial/challenge differs from candidate",
					ErrInput,
				)
			}
			result.OSVersion, result.OSBuild = observed.Version, observed.Build
			result.Marker = observed.Challenge
			return observed.Snapshot, nil
		},
		ArmPower: func(ctx context.Context, restart bool) (func(context.Context) error, func(), error) {
			conn, err := dialLocal(ctx, filepath.Join(work, "qmp.sock"))
			if err != nil {
				return nil, nil, err
			}
			return armQEMUPower(ctx, conn, restart)
		},
	}
	observed, err := probe(vmCtx, vm, c.OwnKey, c.ForeignKey, e, sequence)
	if err != nil {
		return result, fmt.Errorf("linux agent lifecycle: %w", err)
	}
	select {
	case err := <-done:
		finished = true
		if err != nil {
			return result, fmt.Errorf("QEMU shutdown: %w", err)
		}
	case <-ctx.Done():
		return result, fmt.Errorf("await QEMU shutdown: %w", ctx.Err())
	}
	result.Checks = maps.Clone(observed.Checks)
	result.Operations, result.RebootOperations = observed.Operations, observed.RebootOperations
	result.Identities = maps.Clone(observed.First.Identity)
	result.Identities["agentStoreKeyDigest"] = observed.First.StoreKeyDigest
	result.MachineID = observed.First.Identity["machineID"]
	for _, name := range []string{"boot", "os-version", "fresh-firmware", "fresh-agent-store"} {
		result.Checks[name] = true
	}
	result.Passed = true
	progress.step("authenticated modules, reboot and shutdown passed")
	return result, nil
}

func linuxAgentExpectations(
	c imagecheck.Clone,
	lock Lock,
	consoleUser string,
) (agentcheck.Expected, error) {
	g := c.Config.Guest
	e := agentcheck.Expected{
		OS:          g.OS,
		Arch:        g.Arch,
		CoreVersion: lock.CoreVersion,
		Headless:    g.Variant == "agent",
		Desktop:     g.Variant == "desktop",
		ConsoleUser: consoleUser,
		Modules:     map[string]agentcheck.Module{},
	}
	if g.OS != "linux" || (g.Arch != "amd64" && g.Arch != "arm64") || (!e.Headless && !e.Desktop) ||
		c.Config.Provisioning.Agent == nil ||
		c.Config.Provisioning.Agent.Version != lock.CoreVersion ||
		len(c.OwnKey) != ed25519.PrivateKeySize ||
		len(c.ForeignKey) != ed25519.PrivateKeySize {
		return e, fmt.Errorf(
			"%w: Linux agent candidate, matching package lock and independent host keys required",
			ErrInput,
		)
	}
	if e.Desktop && !consoleAccount.MatchString(consoleUser) {
		return e, fmt.Errorf("%w: desktop acceptance needs a disposable console username", ErrInput)
	}
	entry, err := lock.RequirePackages("linux/" + g.Arch)
	if err != nil {
		return e, err
	}
	for _, m := range entry.Modules {
		e.Modules[strings.TrimPrefix(m.ID, "weave-linux-")] = agentcheck.Module{
			ID:      m.ID,
			Version: m.Version,
		}
	}
	return e, nil
}

func linuxAgentSeed(c imagecheck.Clone, consoleUser string) string {
	pub := base64.StdEncoding.EncodeToString(c.OwnKey.Public().(ed25519.PublicKey))
	console := "/dev/ttyS0"
	if c.Config.Guest.Arch == "arm64" {
		console = "/dev/ttyAMA0"
	}
	setup := ""
	if c.Config.Guest.Variant == "desktop" {
		setup = "if getent passwd " + shellQuote(
			consoleUser,
		) + " >/dev/null; then exit 1; fi\nuseradd --create-home --shell /bin/bash " + shellQuote(
			consoleUser,
		) + "\npasswd -l " + shellQuote(
			consoleUser,
		) + "\ninstall -d -m 0755 /etc/lightdm/lightdm.conf.d\nprintf '%s\\n' " + shellQuote(
			"[Seat:*]\nautologin-user="+consoleUser+"\nautologin-user-timeout=0\nuser-session=xfce",
		) + " > /etc/lightdm/lightdm.conf.d/99-weave-acceptance.conf\nsystemctl restart lightdm.service\n"
	}
	return "set -eu\n" + setup + "systemctl stop weave-agent.service\ntest ! -e /etc/weave/channel.pub\ninstall -d -m 0755 /etc/weave\nprintf '%s\\n' " + shellQuote(
		pub,
	) + " > /etc/weave/channel.pub\nchmod 0644 /etc/weave/channel.pub\ninstall -d -m 0700 /var/lib/weave\nprintf '%s\\n' " + shellQuote(
		c.Marker,
	) + " > /var/lib/weave/acceptance.challenge\nsystemctl start weave-agent.service\nprintf '\\n%s\\n' " + shellQuote(
		c.Marker+" agent-ready",
	) + " > " + console + "\n"
}

func waitLinuxAgentSeed(ctx context.Context, path, marker string, done chan error) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read agent readiness: %w", err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == marker+" agent-ready" {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for agent provisioning: %w", ctx.Err())
		case err := <-done:
			done <- err // Preserve the process result for the owning cleanup.
			return fmt.Errorf(
				"QEMU exited before agent provisioning: %w",
				errors.Join(ErrInput, err),
			)
		case <-ticker.C:
		}
	}
}

func linuxAgentQEMU(work, code, arch, accel string, desktop bool) (string, []string) {
	name, machine, cpu := "qemu-system-x86_64", "q35", "max"
	if arch == "arm64" {
		name, machine, cpu = "qemu-system-aarch64", "virt", "cortex-a72"
	}
	if accel != "tcg" {
		cpu = "host"
	}
	args := []string{
		"-machine",
		machine + ",accel=" + accel,
		"-cpu",
		cpu,
		"-smp",
		"2",
		"-m",
		"4096",
		"-drive",
		"if=pflash,format=raw,readonly=on,file=" + qemuPath(code),
		"-drive",
		"if=pflash,format=raw,file=" + qemuPath(filepath.Join(work, "vars.fd")),
		"-drive",
		"file=" + qemuPath(filepath.Join(work, "overlay.qcow2")) + ",if=virtio,format=qcow2",
		"-drive",
		"file=" + qemuPath(filepath.Join(work, "seed.iso")) + ",if=virtio,format=raw,readonly=on",
		"-nic",
		"user,model=virtio-net-pci",
		"-display",
		"none",
		"-serial",
		"stdio",
		"-monitor",
		"none",
		"-qmp",
		"unix:" + filepath.Join(work, "qmp.sock") + ",server=on,wait=off",
		"-device",
		"virtio-serial-pci",
		"-chardev",
		"socket,id=weave,path=" + qemuPath(
			filepath.Join(work, "agent.sock"),
		) + ",server=on,wait=off",
		"-device",
		"virtserialport,chardev=weave,name=org.weave.agent.0",
	}
	if desktop {
		args = append(args, "-device", "virtio-gpu-pci")
	}
	return name, args
}
