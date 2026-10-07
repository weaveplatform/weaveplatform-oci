package imagebuild

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/weaveclient"
	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/weaveexec"
	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/weavemodule/weavemoduletest"
	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/weavewire"
	"github.com/weaveplatform/weaveplatform-oci/pkg/agentcheck"
	"github.com/weaveplatform/weaveplatform-oci/pkg/imagecheck"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

type inspectProcess struct{ output string }

func (p inspectProcess) Stdout() io.Reader         { return strings.NewReader(p.output) }
func (inspectProcess) Stderr() io.Reader           { return strings.NewReader("") }
func (inspectProcess) Stdin() io.WriteCloser       { return nil }
func (inspectProcess) PID() int                    { return 42 }
func (inspectProcess) Resize(uint16, uint16) error { return nil }
func (inspectProcess) Signal(string) error         { return nil }
func (inspectProcess) Wait() (int, string, error)  { return 0, "", nil }
func (inspectProcess) Close() error                { return nil }
func (p inspectProcess) Start(context.Context, weavewire.ExecRequest) (weaveexec.Process, error) {
	return p, nil
}

func agentCloneFixture(t *testing.T) (imagecheck.Clone, Lock) {
	t.Helper()
	lock, _ := packageFixture(t)
	c := imagecheck.Clone{
		Bundle: bootBundle(
			t,
			"arm64",
		),
		Report: t.TempDir(),
		Marker: "WEAVE-BOOT-OK-0123456789abcdef01234567",
		Config: spec.Config{
			Guest: spec.Guest{
				OS:        "linux",
				Arch:      "arm64",
				Variant:   "agent",
				OSVersion: "26.04",
				OSBuild:   "20261001",
			},
			Provisioning: spec.Provisioning{
				Agent: &spec.Agent{Name: "weave-agent", Version: lock.CoreVersion},
			},
		},
		OwnKey: ed25519.NewKeyFromSeed(
			make([]byte, 32),
		),
		ForeignKey: ed25519.NewKeyFromSeed([]byte(strings.Repeat("a", 32))),
	}
	return c, lock
}

func TestLinuxAgentCloneOwnsDisposableQEMUAndObservedIdentity(t *testing.T) {
	fakeFirmware(t)
	for _, mode := range []string{"valid", "probe", "inspect", "version", "challenge", "qemu-fails", "qemu-exits-early", "deadline", "copy-vars", "seed", "log", "serial", "create", "bundle", "disk", "temp", "power-dial"} {
		t.Run(mode, func(t *testing.T) {
			c, lock := agentCloneFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			end := make(chan struct{})
			ready := make(chan struct{})
			var agentListener, qmpListener net.Listener
			tools := Tools{
				Run: func(ctx context.Context, w, _ io.Writer, name string, args ...string) error {
					if name == "qemu-img" {
						if mode == "create" {
							return os.ErrPermission
						}
						if mode == "copy-vars" {
							return os.Mkdir(
								filepath.Join(filepath.Dir(args[len(args)-1]), "vars.fd"),
								0o700,
							)
						}
						return nil
					}
					if name == "hdiutil" || name == "genisoimage" {
						if mode == "seed" {
							return os.ErrPermission
						}
						seed, err := os.ReadFile(filepath.Join(args[len(args)-1], "user-data"))
						if err != nil {
							return err
						}
						if strings.Contains(string(seed), base64Private(c.OwnKey)) {
							return fmt.Errorf("private key leaked")
						}
						return nil
					}
					if mode == "qemu-exits-early" {
						return os.ErrPermission
					}
					for n, arg := range args {
						if arg == "-qmp" && mode != "power-dial" {
							path := strings.Split(strings.TrimPrefix(args[n+1], "unix:"), ",")[0]
							var err error
							qmpListener, err = net.Listen("unix", path)
							if err != nil {
								return err
							}
							defer qmpListener.Close()
						}
						if arg == "-chardev" {
							path := strings.Split(strings.Split(args[n+1], "path=")[1], ",")[0]
							var err error
							agentListener, err = net.Listen("unix", path)
							if err != nil {
								return err
							}
							defer agentListener.Close()
						}
					}
					_, _ = fmt.Fprintln(w, c.Marker+" agent-ready")
					close(ready)
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-end:
					}
					if mode == "qemu-fails" {
						return os.ErrPermission
					}
					return nil
				},
			}
			switch mode {
			case "bundle":
				c.Bundle += "-missing"
			case "disk":
				os.Remove(filepath.Join(c.Bundle, "disk0.img"))
			case "temp":
				t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
			case "log":
				os.Mkdir(filepath.Join(c.Report, "qemu.log"), 0o700)
			case "serial":
				os.Mkdir(filepath.Join(c.Report, "serial.log"), 0o700)
			}
			probe := func(ctx context.Context, vm agentcheck.Lifecycle, _, _ ed25519.PrivateKey, _ agentcheck.Expected, _ uint64) (agentcheck.LifecycleResult, error) {
				<-ready
				var r agentcheck.LifecycleResult
				if mode == "probe" {
					return r, os.ErrPermission
				}
				// Exercise the real SDK-backed inspection callback over framed exec.
				output := "26.04\n20261001\nboot-uuid\n" + strings.Repeat(
					"a",
					32,
				) + "\nsha256:" + strings.Repeat(
					"b",
					64,
				) + "\n9\nsealed\n" + c.Marker + "\n"
				if mode == "inspect" {
					output = "invalid"
				}
				if mode == "version" {
					output = strings.ReplaceAll(output, "26.04", "20.04")
				}
				if mode == "challenge" {
					output = strings.ReplaceAll(output, c.Marker, "wrong-challenge")
				}
				host, guest := net.Pipe()
				core := weavemoduletest.NewCore(guest, nil)
				core.Serve(t, weaveexec.NewService(inspectProcess{output}))
				go core.Run()
				defer core.Close()
				client := weaveclient.New(ctx, host, weaveclient.Options{})
				defer client.Close()
				snapshot, err := vm.Inspect(ctx, client)
				if err != nil {
					return r, err
				}
				r.First = snapshot
				r.Checks = map[string]bool{"shutdown": true}
				conn, err := vm.Dial(ctx)
				if err != nil {
					return r, err
				}
				conn.Close()
				if mode == "power-dial" {
					cancel()
					_, _, err = vm.ArmPower(ctx, true)
					return r, err
				}
				go func() {
					conn, err := qmpListener.Accept()
					if err != nil {
						return
					}
					defer conn.Close()
					enc, dec := json.NewEncoder(conn), json.NewDecoder(conn)
					_ = enc.Encode(map[string]any{"QMP": map[string]any{}})
					for range 2 {
						var cmd map[string]string
						if dec.Decode(&cmd) != nil {
							return
						}
						_ = enc.Encode(map[string]any{"return": map[string]any{}, "id": cmd["id"]})
					}
					_ = enc.Encode(
						map[string]any{"event": "SHUTDOWN", "data": map[string]bool{"guest": true}},
					)
				}()
				wait, closeObserver, err := vm.ArmPower(ctx, false)
				if err != nil {
					return r, err
				}
				defer closeObserver()
				if err := wait(ctx); err != nil {
					return r, err
				}
				if mode == "deadline" {
					cancel()
				} else {
					close(end)
				}
				return r, nil
			}
			result, err := tools.bootLinuxAgent(ctx, c, lock, "", 0, probe)
			if (err == nil) != (mode == "valid") {
				t.Fatal(mode, result, err)
			}
			if mode == "valid" &&
				(result.Marker != c.Marker || result.OSVersion != "26.04" || result.MachineID != strings.Repeat("a", 32) || !result.Checks["fresh-firmware"]) {
				t.Fatal(result)
			}
		})
	}
}

func base64Private(key ed25519.PrivateKey) string { return base64.StdEncoding.EncodeToString(key) }

func TestLinuxAgentInputAndQEMUChoices(t *testing.T) {
	c, lock := agentCloneFixture(t)
	for _, mode := range []string{"os", "arch", "variant", "agent", "version", "key", "foreign", "packages"} {
		changed := c
		changed.Config = c.Config
		l := lock
		switch mode {
		case "os":
			changed.Config.Guest.OS = "windows"
		case "arch":
			changed.Config.Guest.Arch = "s390x"
		case "variant":
			changed.Config.Guest.Variant = "base"
		case "agent":
			changed.Config.Provisioning.Agent = nil
		case "version":
			l.CoreVersion = "0.0.0"
		case "key":
			changed.OwnKey = nil
		case "foreign":
			changed.ForeignKey = nil
		case "packages":
			l.Platforms = nil
		}
		if _, err := (Tools{}).LinuxAgentBoot(l, "", 0)(t.Context(), changed); err == nil {
			t.Fatal(mode)
		}
	}
	for _, arch := range []string{"arm64", "amd64"} {
		for _, accel := range []string{"tcg", "kvm"} {
			name, args := linuxAgentQEMU("work", "code", arch, accel, true)
			if name == "" || strings.Contains(strings.Join(args, " "), "-no-reboot") ||
				!strings.Contains(strings.Join(args, " "), "virtio-gpu-pci") {
				t.Fatal(name, args)
			}
			c.Config.Guest.Arch = arch
			if !strings.Contains(linuxAgentSeed(c), "agent-ready") {
				t.Fatal("missing readiness")
			}
		}
	}
}

func TestAgentReadinessFailsOnMissingOutputCancellationAndEarlyExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serial")
	done := make(chan error, 1)
	if err := waitLinuxAgentSeed(t.Context(), path, "marker", done); err == nil {
		t.Fatal("missing serial")
	}
	os.WriteFile(path, []byte("unrelated output"), 0o600)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := waitLinuxAgentSeed(ctx, path, "marker", done); err == nil {
		t.Fatal("cancelled")
	}
	done <- nil
	if err := waitLinuxAgentSeed(t.Context(), path, "marker", done); err == nil {
		t.Fatal("early exit")
	}
}
