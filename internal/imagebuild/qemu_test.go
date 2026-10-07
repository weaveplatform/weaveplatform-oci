package imagebuild

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/weaveplatform/weaveplatform-oci/internal/testbundle"
	"github.com/weaveplatform/weaveplatform-oci/pkg/chunk"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

func bootBundle(t *testing.T, arch string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "bundle")
	must(t, testbundle.Write(dir, testbundle.Options{OS: "linux", Arch: arch}))
	b, err := pack.LoadBundle(dir)
	must(t, err)
	b.File.Guest.Variant = "base"
	b.File.Provisioning.Agent = nil
	must(t, pack.WriteBundleFile(dir, b.File))
	return dir
}

func fakeFirmware(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"code", "vars"} {
		must(t, os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600))
	}
	t.Setenv("WEAVE_FIRMWARE_CODE", filepath.Join(dir, "code"))
	t.Setenv("WEAVE_FIRMWARE_VARS", filepath.Join(dir, "vars"))
}

type fakeQEMU struct {
	t                     *testing.T
	marker                string
	boots                 int
	sameID, missingMarker bool
	fail                  string
}

func (f *fakeQEMU) run(_ context.Context, w, _ io.Writer, name string, args ...string) error {
	if name == f.fail {
		return errors.New("native tool failed")
	}
	switch name {
	case "qemu-img":
		if args[0] == "info" {
			_, err := fmt.Fprint(w, `{"virtual-size":1048576}`)
			return err
		}
		if args[0] == "create" || args[0] == "convert" {
			return os.WriteFile(args[len(args)-1], []byte("disk"), 0o600)
		}
	case "hdiutil", "genisoimage":
		raw, err := os.ReadFile(filepath.Join(args[len(args)-1], "user-data"))
		if err != nil {
			return err
		}
		f.marker = regexp.MustCompile(`WEAVE-BOOT-OK-[a-f0-9]{24}`).FindString(string(raw))
		if f.marker == "" {
			f.t.Fatal("NoCloud seed lacks marker")
		}
	case "qemu-system-aarch64", "qemu-system-x86_64":
		f.boots++
		id := f.boots
		if f.sameID {
			id = 1
		}
		text := fmt.Sprintf("%s machine-id=%032x\r\n", f.marker, id)
		if f.missingMarker {
			text = "kernel panic"
		}
		if !strings.Contains(strings.Join(args, " "), "-serial stdio -monitor none") {
			f.t.Fatal("QEMU must stream serial output without a monitor", args)
		}
		_, err := io.WriteString(w, text)
		return err
	case "git":
		_, err := fmt.Fprint(w, "abcdef\n")
		return err
	default:
		f.t.Fatalf("unexpected native tool %s %v", name, args)
	}
	return nil
}

func TestBootLinuxDisposableState(t *testing.T) {
	fakeFirmware(t)
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			bundle := bootBundle(t, arch)
			original, err := os.ReadFile(filepath.Join(bundle, "disk0.img"))
			must(t, err)
			fake := &fakeQEMU{t: t}
			tools := Tools{Run: fake.run}
			out := t.TempDir()
			payload := t.TempDir()
			must(t, os.WriteFile(filepath.Join(payload, "a.deb"), []byte("deb"), 0o600))
			result, err := tools.BootLinux(
				t.Context(),
				BootOptions{
					Bundle:     bundle,
					Report:     filepath.Join(out, "report"),
					Timeout:    time.Second,
					OutputDisk: filepath.Join(out, "child.img"),
					Payload:    payload,
					Script:     "echo provisioning",
				},
			)
			must(t, err)
			if !result.Passed || result.MachineID != fmt.Sprintf("%032x", 1) {
				t.Fatal(result)
			}
			after, err := os.ReadFile(filepath.Join(bundle, "disk0.img"))
			must(t, err)
			if string(original) != string(after) {
				t.Fatal("base disk mutated")
			}
			var report BootResult
			must(t, readJSON(filepath.Join(out, "report", "result.json"), &report))
			if !report.Passed {
				t.Fatal(report)
			}
		})
	}
}

func TestBootLinuxFailures(t *testing.T) {
	fakeFirmware(t)
	bundle := bootBundle(t, "arm64")
	for _, fail := range []string{"qemu-img", "hdiutil", "genisoimage", "qemu-system-aarch64", "marker"} {
		t.Run(fail, func(t *testing.T) {
			fake := &fakeQEMU{t: t, fail: fail, missingMarker: fail == "marker"}
			tools := Tools{Run: fake.run}
			_, err := tools.BootLinux(
				t.Context(),
				BootOptions{
					Bundle:  bundle,
					Report:  filepath.Join(t.TempDir(), "report"),
					Timeout: time.Second,
				},
			)
			// Only the host's ISO utility is called.
			if (fail == "hdiutil" || fail == "genisoimage") && err == nil {
				return
			}
			if err == nil {
				t.Fatal("failure accepted")
			}
		})
	}
	for _, o := range []BootOptions{{Bundle: "missing", Timeout: time.Second}, {Bundle: bundle}, {Bundle: bundle, Timeout: time.Second, Report: t.TempDir()}, {Bundle: bundle, Timeout: time.Second, OutputDisk: bundle}} {
		if _, err := (Tools{}).BootLinux(t.Context(), o); err == nil {
			t.Fatal("invalid boot accepted")
		}
	}
	for _, serial := range []string{"", "MARK machine-id=bad", "MARK machine-id=" + strings.Repeat("a", 32) + "\nMARK machine-id=" + strings.Repeat("b", 32)} {
		if _, err := bootIdentity(serial, "MARK"); err == nil {
			t.Fatal("invalid marker accepted")
		}
	}
}

func TestValidateLinuxPackedBytesAndFreshIdentities(t *testing.T) {
	fakeFirmware(t)
	for _, same := range []bool{false, true} {
		t.Run(fmt.Sprint(same), func(t *testing.T) {
			fake := &fakeQEMU{t: t, sameID: same}
			tools := Tools{Run: fake.run}
			out := filepath.Join(t.TempDir(), "candidate")
			result, err := tools.ValidateLinux(
				t.Context(),
				ValidateOptions{
					Bundles: []string{bootBundle(t, "arm64")},
					Arches:  []string{"arm64"},
					Out:     out,
					Timeout: time.Second,
				},
			)
			if same {
				if err == nil || result.Passed {
					t.Fatal("cloned identity accepted")
				}
			} else {
				must(t, err)
				if !result.Passed || !strings.HasPrefix(result.IndexDigest, "sha256:") {
					t.Fatal(result)
				}
			}
			var saved Acceptance
			must(t, readJSON(filepath.Join(out, "acceptance.json"), &saved))
			if saved.Passed == same {
				t.Fatal(saved)
			}
			_, _, err = openCandidate(t.Context(), filepath.Join(out, "layout"))
			must(t, err)
		})
	}
}

func TestValidationRefusesWrongTierAndMatrix(t *testing.T) {
	b := bootBundle(t, "arm64")
	for _, o := range []ValidateOptions{{}, {Bundles: []string{"missing"}, Arches: []string{"arm64"}}, {Bundles: []string{b, b}, Arches: []string{"arm64"}}, {Bundles: []string{b}, Arches: []string{"amd64"}}, {Bundles: []string{b}, Arches: []string{"arm64", "amd64"}}} {
		if _, _, err := validationBundles(o); err == nil {
			t.Fatal("invalid matrix accepted")
		}
	}
	loaded, err := pack.LoadBundle(b)
	must(t, err)
	loaded.File.Guest.Variant = "agent"
	loaded.File.Provisioning.Agent = &spec.Agent{Name: "weave-agent", Version: "1.2.3"}
	must(t, pack.WriteBundleFile(b, loaded.File))
	if _, _, err := validationBundles(
		ValidateOptions{Bundles: []string{b}, Arches: []string{"arm64"}},
	); err == nil {
		t.Fatal("agent accepted as base")
	}
	loaded.File.Disks = append(loaded.File.Disks, loaded.File.Disks[0])
	if _, err := systemDisk(loaded); err == nil {
		t.Fatal("two system disks")
	}
	loaded.File.Disks = nil
	if _, err := systemDisk(loaded); err == nil {
		t.Fatal("missing system disk")
	}
}

func TestSeedUsesJSON(t *testing.T) {
	b := pack.Bundle{
		File: pack.BundleFile{
			Guest: spec.Guest{OSVersion: "26.04", Arch: "arm64", Variant: "base"},
		},
	}
	script := bootScript(b, "marker", "echo 'custom'")
	if !strings.Contains(script, "if command -v weave-agent; then exit 1; fi") ||
		!strings.Contains(script, "/dev/ttyAMA0") {
		t.Fatal(script)
	}
	work := t.TempDir()
	tools := Tools{
		Run: func(context.Context, io.Writer, io.Writer, string, ...string) error { return nil },
	}
	must(t, tools.seed(t.Context(), work, script, "marker", ""))
	raw, err := os.ReadFile(filepath.Join(work, "seed", "user-data"))
	must(t, err)
	var cloud map[string]any
	must(t, json.Unmarshal(raw[len("#cloud-config\n"):], &cloud))
	power, ok := cloud["power_state"].(map[string]any)
	if !ok || power["mode"] != "poweroff" || power["delay"] != "now" ||
		power["timeout"] != float64(120) {
		t.Fatalf("shutdown must wait for cloud-final: %s", raw)
	}
	condition, err := json.Marshal(power["condition"])
	must(t, err)
	if string(condition) != `["test","-f","/run/marker"]` ||
		!strings.HasSuffix(script, "\ntouch '/run/marker'\n") ||
		strings.Contains(script, "systemctl poweroff") {
		t.Fatalf("shutdown must follow successful checks without interrupting cloud-init: %s", raw)
	}
}

func TestGuestCPUCompatibility(t *testing.T) {
	for _, tc := range []struct{ arch, accelerator, cpu string }{
		{"arm64", "tcg", "cortex-a72"},
		{"amd64", "tcg", "max"},
		{"arm64", "hvf", "host"},
		{"arm64", "kvm", "host"},
		{"amd64", "kvm", "host"},
	} {
		t.Run(tc.arch+"/"+tc.accelerator, func(t *testing.T) {
			fake := &fakeQEMU{t: t, marker: "MARKER"}
			tools := Tools{
				Run: func(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error {
					command := strings.Join(args, " ")
					if !strings.Contains(command, "-cpu "+tc.cpu+" ") ||
						!strings.Contains(command, ",accel="+tc.accelerator+" ") {
						t.Fatalf("unexpected CPU/accelerator: %s", command)
					}
					return fake.run(ctx, stdout, stderr, name, args...)
				},
			}
			result := BootResult{Accelerator: tc.accelerator, Marker: fake.marker}
			must(
				t,
				tools.runGuest(t.Context(), BootOptions{Report: t.TempDir(), Timeout: time.Second},
					t.TempDir(), "code.fd", tc.arch, &result),
			)
			if !result.Passed {
				t.Fatal(result)
			}
		})
	}
}

func TestAgentCandidatePreservesParentManifest(t *testing.T) {
	fakeFirmware(t)
	fake := &fakeQEMU{t: t}
	tools := Tools{Run: fake.run}
	base := filepath.Join(t.TempDir(), "candidate")
	_, err := tools.ValidateLinux(
		t.Context(),
		ValidateOptions{
			Bundles: []string{bootBundle(t, "arm64")},
			Arches:  []string{"arm64"},
			Out:     base,
			Timeout: time.Second,
		},
	)
	must(t, err)
	l, cache := packageFixture(t)
	p := Packages{
		Tools: Tools{
			Run: func(ctx context.Context, w, e io.Writer, name string, args ...string) error {
				if name == "cosign" {
					return nil
				}
				return fake.run(ctx, w, e, name, args...)
			},
		},
	}
	o := AgentOptions{
		Base:     filepath.Join(base, "layout"),
		BaseName: "ghcr.io/weaveplatform/weave-images/ubuntu-base",
		Arch:     "arm64",
		Cache:    cache,
		Out:      filepath.Join(t.TempDir(), "agent"),
		Revision: 1,
		Timeout:  time.Second,
		Lock:     l,
	}
	bundle, err := p.BuildLinuxAgent(t.Context(), o)
	must(t, err)
	b, err := pack.LoadBundle(bundle)
	must(t, err)
	_, report, err := openCandidate(t.Context(), o.Base)
	must(t, err)
	if b.File.Build.Base.Digest != report.Children[0].Descriptor.Digest.String() ||
		b.File.Build.Base.Digest == report.Root.Digest.String() {
		t.Fatal("parent is not platform manifest")
	}
	if b.File.Guest.Variant != "agent" || b.File.Provisioning.Agent.Version != l.CoreVersion ||
		len(b.File.Build.SourceMedia) != 2 {
		t.Fatal(b.File)
	}
	for _, mutation := range []func(*AgentOptions){func(o *AgentOptions) { o.Arch = "unknown" }, func(o *AgentOptions) { o.BaseName = "tag:unsafe" }, func(o *AgentOptions) { o.Revision = 0 }, func(o *AgentOptions) { o.Base = "missing" }, func(o *AgentOptions) { o.Out = base }} {
		bad := o
		mutation(&bad)
		if _, err := p.BuildLinuxAgent(t.Context(), bad); err == nil {
			t.Fatal("invalid agent candidate accepted")
		}
	}
	fake.fail = "qemu-system-aarch64"
	o.Out = filepath.Join(t.TempDir(), "failed-agent")
	if _, err := p.BuildLinuxAgent(t.Context(), o); err == nil {
		t.Fatal("failed provision accepted")
	}
}

func TestBootSetupRefusals(t *testing.T) {
	fakeFirmware(t)
	bundle := bootBundle(t, "arm64")
	for _, failure := range []string{"disk-info", "bad-info", "resize", "missing-payload", "missing-serial", "convert"} {
		t.Run(failure, func(t *testing.T) {
			fake := &fakeQEMU{t: t}
			runner := Tools{
				Run: func(ctx context.Context, w, e io.Writer, name string, args ...string) error {
					if name == "qemu-img" {
						if args[0] == "info" && failure == "disk-info" {
							return errors.New("info failed")
						}
						if args[0] == "info" && failure == "bad-info" {
							_, err := fmt.Fprint(w, "not JSON")
							return err
						}
						if args[0] == failure {
							return errors.New("tool failed")
						}
					}
					if strings.HasPrefix(name, "qemu-system") && failure == "missing-serial" {
						return nil
					}
					return fake.run(ctx, w, e, name, args...)
				},
			}
			opts := BootOptions{
				Bundle:  bundle,
				Report:  filepath.Join(t.TempDir(), "report"),
				Timeout: time.Second,
			}
			if failure == "missing-payload" {
				opts.Payload = "missing"
			}
			if failure == "convert" {
				opts.OutputDisk = filepath.Join(t.TempDir(), "child")
			}
			if _, err := runner.BootLinux(t.Context(), opts); err == nil {
				t.Fatal("accepted " + failure)
			}
		})
	}
	b, err := pack.LoadBundle(bundle)
	must(t, err)
	b.File.Disks[0].Path = "missing"
	if _, err := systemDisk(b); err == nil {
		t.Fatal("missing disk")
	}
	b.File.Disks[0].Path = "."
	if _, err := systemDisk(b); err == nil {
		t.Fatal("directory disk")
	}
	b.File.Guest.OS = "windows"
	must(t, pack.WriteBundleFile(bundle, b.File))
	if _, err := (Tools{}).BootLinux(
		t.Context(),
		BootOptions{Bundle: bundle, Timeout: time.Second},
	); err == nil {
		t.Fatal("wrong guest OS")
	}
}

func TestSerialMarkerStartsFreshLine(t *testing.T) {
	b := pack.Bundle{File: pack.BundleFile{Guest: spec.Guest{Arch: "arm64", OSVersion: "26.04"}}}
	if !strings.Contains(bootScript(b, "MARKER", ""), "printf '\\nMARKER") {
		t.Fatal("marker could be appended to an unfinished login prompt")
	}
	id, err := bootIdentity(
		"weave-test login: \r\nMARKER machine-id="+strings.Repeat("a", 32)+"\r\r\n",
		"MARKER",
	)
	must(t, err)
	if id != strings.Repeat("a", 32) {
		t.Fatal(id)
	}
}

func TestLinuxAgentFailureBoundaries(t *testing.T) {
	fakeFirmware(t)
	fake := &fakeQEMU{t: t}
	base := filepath.Join(t.TempDir(), "base")
	_, err := (Tools{Run: fake.run}).ValidateLinux(
		t.Context(),
		ValidateOptions{
			Bundles: []string{bootBundle(t, "arm64")},
			Arches:  []string{"arm64"},
			Out:     base,
			Timeout: time.Second,
		},
	)
	must(t, err)
	for _, mode := range []string{"platform", "evidence", "recipe-output", "bundle-output", "git", "bundle-metadata"} {
		t.Run(mode, func(t *testing.T) {
			l, cache := packageFixture(t)
			out := filepath.Join(t.TempDir(), "agent")
			o := AgentOptions{
				Base:     filepath.Join(base, "layout"),
				BaseName: "ghcr.io/weaveplatform/base",
				Arch:     "arm64",
				Cache:    cache,
				Out:      out,
				Revision: 1,
				Timeout:  time.Second,
				Lock:     l,
			}
			if mode == "platform" {
				o.Arch = "amd64"
				l.Platforms["linux/amd64"] = l.Platforms["linux/arm64"]
			}
			p := Packages{
				Tools: Tools{
					Run: func(ctx context.Context, w, e io.Writer, name string, args ...string) error {
						if name == "cosign" {
							if mode == "evidence" {
								return ErrInput
							}
							for _, entry := range []struct{ mode, name string }{{"recipe-output", "provision.sh"}, {"bundle-output", "bundle"}} {
								if mode == entry.mode {
									must(t, os.MkdirAll(filepath.Join(out, entry.name), 0o700))
								}
							}
							return nil
						}
						if name == "git" {
							if mode == "git" {
								return ErrInput
							}
							if mode == "bundle-metadata" {
								must(
									t,
									os.Mkdir(filepath.Join(out, "bundle", "bundle.json"), 0o700),
								)
							}
						}
						return fake.run(ctx, w, e, name, args...)
					},
				},
			}
			if _, err := p.BuildLinuxAgent(t.Context(), o); err == nil {
				t.Fatal("accepted " + mode)
			}
			if _, err := os.Stat(filepath.Join(out, "inventory.json")); err == nil {
				t.Fatal("published failed agent inventory")
			}
		})
	}
}

func TestAgentRefusesNonBaseParent(t *testing.T) {
	l, cache := packageFixture(t)
	parent := bootBundle(t, "arm64")
	b, err := pack.LoadBundle(parent)
	must(t, err)
	b.File.Guest.Variant = "agent"
	b.File.Provisioning.Agent = &spec.Agent{Name: "weave-agent", Version: "1.2.3"}
	b.File.Build.Base = &spec.BaseImage{
		Name:   "ghcr.io/weaveplatform/base",
		Digest: "sha256:" + strings.Repeat("a", 64),
	}
	must(t, pack.WriteBundleFile(parent, b.File))
	b, err = pack.LoadBundle(parent)
	must(t, err)
	layout := filepath.Join(t.TempDir(), "layout")
	store, err := pack.OpenLayout(t.Context(), layout)
	must(t, err)
	desc, err := pack.Manifest(t.Context(), b, store, chunk.Options{})
	must(t, err)
	root, err := pack.Index(t.Context(), store, []ocispec.Descriptor{desc}, nil)
	must(t, err)
	must(t, store.Tag(t.Context(), root, "parent"))
	out := filepath.Join(t.TempDir(), "child")
	if _, err := (Packages{}).BuildLinuxAgent(
		t.Context(),
		AgentOptions{
			Base:     layout,
			BaseName: "ghcr.io/weaveplatform/base",
			Arch:     "arm64",
			Cache:    cache,
			Out:      out,
			Revision: 1,
			Timeout:  time.Second,
			Lock:     l,
		},
	); err == nil {
		t.Fatal("agent accepted as base")
	}
	if _, err := os.Stat(filepath.Join(out, "packages")); !os.IsNotExist(err) {
		t.Fatal("prepared packages for invalid parent")
	}
}

func TestAcceleratorHostPolicy(t *testing.T) {
	for _, tc := range []struct {
		os, host, guest, want string
		available, probe      bool
	}{
		{"darwin", "arm64", "amd64", "tcg", true, false},
		{"darwin", "arm64", "arm64", "hvf", false, false},
		{"windows", "amd64", "amd64", "tcg", true, false},
		{"linux", "amd64", "arm64", "tcg", true, false},
		{"linux", "amd64", "amd64", "tcg", false, true},
		{"linux", "arm64", "arm64", "kvm", true, true},
	} {
		t.Run(tc.os+"/"+tc.host+"/"+tc.guest, func(t *testing.T) {
			called := false
			var device *os.File
			got := acceleratorFor(
				tc.os,
				tc.host,
				tc.guest,
				func(path string, flags int, mode os.FileMode) (*os.File, error) {
					if path != "/dev/kvm" || flags != os.O_RDWR || mode != 0 {
						t.Fatal("incorrect KVM probe")
					}
					called = true
					if !tc.available {
						return nil, os.ErrPermission
					}
					var err error
					device, err = os.Create(filepath.Join(t.TempDir(), "device"))
					return device, err
				},
			)
			if got != tc.want || called != tc.probe {
				t.Fatal(got, called)
			}
			if device != nil {
				if _, err := device.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
					t.Fatal("KVM probe leaked handle", err)
				}
			}
		})
	}
}
