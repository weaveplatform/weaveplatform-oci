package imagebuild

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/weaveplatform/weaveplatform-oci/internal/testbundle"
	"github.com/weaveplatform/weaveplatform-oci/pkg/chunk"
	"github.com/weaveplatform/weaveplatform-oci/pkg/disk/vhd"
	"github.com/weaveplatform/weaveplatform-oci/pkg/disk/virtualdisk"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

func windowsAgentParent(t *testing.T, extra bool) (string, string, pack.BundleFile) {
	t.Helper()
	dir := t.TempDir()
	bundle := filepath.Join(dir, "base")
	must(
		t,
		testbundle.Write(
			bundle,
			testbundle.Options{OS: "windows", Arch: "arm64", Size: 1 << 20, ExtraDisk: extra},
		),
	)
	b, err := pack.LoadBundle(bundle)
	must(t, err)
	b.File.Guest.Variant = "base"
	store, err := pack.OpenLayout(t.Context(), filepath.Join(dir, "layout"))
	must(t, err)
	desc, err := pack.Manifest(t.Context(), b, store, chunk.Options{})
	must(t, err)
	root, err := pack.Index(t.Context(), store, []ocispec.Descriptor{desc}, nil)
	must(t, err)
	must(t, store.Tag(t.Context(), root, "base"))
	return filepath.Join(dir, "layout"), desc.Digest.String(), b.File
}

func TestWindowsAgentParentClone(t *testing.T) {
	for _, mode := range []string{"success", "input", "lock", "base", "platform", "extra-disk", "out", "verify", "install", "version", "edition", "not-generalized", "receipt-write", "bundle-create", "bridge", "state", "git", "bundle-write", "inventory"} {
		t.Run(mode, func(t *testing.T) {
			l, cache := nativePackageFixture(t, "windows")
			base, parent, cfg := windowsAgentParent(t, mode == "extra-disk")
			o := AgentOptions{
				Lock:     l,
				Cache:    cache,
				Base:     base,
				BaseName: "windows-base",
				Arch:     "arm64",
				Out:      filepath.Join(t.TempDir(), "build"),
				Timeout:  time.Second,
				Revision: 1,
			}
			switch mode {
			case "input":
				o.Revision = 0
			case "lock":
				o.Lock = Lock{}
			case "base":
				o.Base = "missing"
			case "platform":
				o.Arch = "amd64"
				o.Lock.Platforms["windows/amd64"] = o.Lock.Platforms["windows/arm64"]
			case "out":
				must(t, os.Mkdir(o.Out, 0o750))
			}
			installCalls := 0
			p := Packages{
				Tools: Tools{
					Run: func(_ context.Context, w, _ io.Writer, name string, _ ...string) error {
						if name == "git" {
							if mode == "git" {
								return os.ErrPermission
							}
							if mode == "bundle-write" {
								must(
									t,
									os.Mkdir(
										filepath.Join(o.Out, "bundle", pack.BundleFileName),
										0o750,
									),
								)
							}
							_, err := io.WriteString(w, "abcdef\n")
							return err
						}
						if name != "cosign" {
							t.Fatal(name)
						}
						if mode == "verify" {
							return os.ErrPermission
						}
						return nil
					},
				},
				WindowsInstall: func(_ context.Context, r WindowsInstallRequest) (WindowsInstallResult, error) {
					installCalls++
					if r.BaseDisk == "" || r.ISO != "" || r.Seed == "" || r.Arch != "arm64" {
						t.Fatal("agent build did not boot parent", r)
					}
					answer, err := os.ReadFile(
						filepath.Join(r.Directory, "seed", "autounattend.xml"),
					)
					must(t, err)
					decoder := xml.NewDecoder(bytes.NewReader(answer))
					for {
						if _, err := decoder.Token(); err != nil {
							if err != io.EOF {
								t.Fatal(err)
							}
							break
						}
					}
					if strings.Contains(string(answer), "windowsPE") ||
						strings.Contains(string(answer), "WillWipeDisk") {
						t.Fatal("derived builder can reinstall base")
					}
					if _, err := os.Stat(
						filepath.Join(r.Directory, "seed", "packages", "install-agent.ps1"),
					); err != nil {
						t.Fatal(err)
					}
					if mode == "install" {
						return WindowsInstallResult{}, os.ErrPermission
					}
					result := WindowsInstallResult{
						OSVersion:   cfg.Guest.OSVersion,
						Build:       cfg.Guest.OSBuild,
						Edition:     cfg.Guest.Edition,
						Generalized: true,
					}
					switch mode {
					case "version":
						result.OSVersion = "wrong"
					case "edition":
						result.Edition = "wrong"
					case "not-generalized":
						result.Generalized = false
					case "receipt-write":
						must(t, os.Mkdir(filepath.Join(r.Directory, "install-result.json"), 0o750))
					case "bundle-create":
						must(t, os.Mkdir(filepath.Join(r.Directory, "bundle"), 0o750))
					case "inventory":
						must(t, os.Mkdir(filepath.Join(r.Directory, "inventory.json"), 0o750))
					case "state":
						must(
							t,
							os.Remove(filepath.Join(r.Directory, "parent", "firmware-policy.json")),
						)
					}
					if mode != "bridge" {
						bridge := filepath.Join(r.Directory, "disk.vhd")
						must(t, copyFile(r.BaseDisk, bridge))
						must(t, vhd.Append(bridge, time.Now()))
					}
					return result, nil
				},
			}
			bundle, err := p.BuildWindowsAgent(t.Context(), o)
			if mode != "success" {
				if err == nil {
					t.Fatal("failure accepted", bundle)
				}
				return
			}
			must(t, err)
			b, err := pack.LoadBundle(bundle)
			must(t, err)
			if b.File.Build.Base.Digest != parent || b.File.Guest.Variant != "agent" ||
				b.File.Provisioning.Agent.Version != l.CoreVersion ||
				b.File.Build.Template != "windows-agent" ||
				installCalls != 1 {
				t.Fatal(b.File)
			}
			before, err := os.ReadFile(filepath.Join(o.Out, "parent", "disk0.img"))
			must(t, err)
			after, err := os.ReadFile(filepath.Join(bundle, "disk0.img"))
			must(t, err)
			if !bytes.Equal(before, after) {
				t.Fatal("export modified guest sectors")
			}
			if b.File.State[0].Semantics != spec.SemanticsRegenerate {
				t.Fatal("inherited build identity")
			}
		})
	}
	if runtime.GOOS != "windows" {
		if _, err := (Packages{}).BuildWindowsAgent(t.Context(), AgentOptions{}); err == nil {
			t.Fatal("native HCS accepted wrong host")
		}
	}
}

func TestCloneWindowsDiskNeverModifiesParent(t *testing.T) {
	for _, mode := range []string{"success", "source", "destination", "footer", "convert", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			raw, dst := filepath.Join(dir, "raw"), filepath.Join(dir, "disk.vhdx")
			data := bytes.Repeat([]byte{7}, 512)
			if mode == "footer" {
				data = data[:511]
			}
			if mode != "source" {
				must(t, os.WriteFile(raw, data, 0o600))
			}
			if mode == "destination" {
				must(t, os.WriteFile(dst+".import.vhd", []byte("keep"), 0o600))
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			err := cloneWindowsDisk(
				ctx,
				raw,
				dst,
				func(_ context.Context, source, destination string, format virtualdisk.Format) error {
					if destination != dst || format != virtualdisk.VHDX {
						t.Fatal(destination, format)
					}
					if mode == "convert" {
						return os.ErrPermission
					}
					reader, f, size, err := vhd.Raw(source)
					must(t, err)
					defer f.Close()
					contents, err := io.ReadAll(reader)
					must(t, err)
					if size != int64(len(data)) || !bytes.Equal(data, contents) {
						t.Fatal("bridge changed sectors")
					}
					return nil
				},
			)
			if (err == nil) != (mode == "success") {
				t.Fatal(mode, err)
			}
			if mode != "source" {
				after, err := os.ReadFile(raw)
				must(t, err)
				if !bytes.Equal(data, after) {
					t.Fatal("parent changed")
				}
			}
			if mode != "destination" {
				if _, err := os.Stat(dst + ".import.vhd"); !os.IsNotExist(err) {
					t.Fatal("bridge leaked", err)
				}
			}
		})
	}
}

func TestWindowsAgentSeedFailuresAndNoInstallerDevice(t *testing.T) {
	for _, mode := range []string{"directory", "payload", "answer", "iso"} {
		t.Run(mode, func(t *testing.T) {
			out, payload := t.TempDir(), t.TempDir()
			if mode == "directory" {
				must(t, os.Mkdir(filepath.Join(out, "seed"), 0o750))
			}
			if mode == "payload" {
				payload = "missing"
			}
			if mode == "answer" {
				must(t, os.WriteFile(filepath.Join(payload, "bad"), nil, 0o600))
				must(t, os.Symlink("bad", filepath.Join(payload, "link")))
			}
			if mode == "iso" {
				must(t, os.WriteFile(filepath.Join(out, "seed.iso"), nil, 0o600))
			}
			if _, err := windowsAgentSeed(
				t.Context(),
				out,
				payload,
				"arm64",
				"WEAVE-IMAGE-READY-0123456789abcdef01234567",
			); err == nil {
				t.Fatal(mode)
			}
		})
	}
	doc := windowsDocument("disk", "", "seed", "state", "pipe")
	attachments := doc["VirtualMachine"].(map[string]any)["Devices"].(map[string]any)["Scsi"].(map[string]any)["0"].(map[string]any)["Attachments"].(map[string]any)
	if len(attachments) != 2 || attachments["1"] != nil {
		t.Fatal(fmt.Sprint(attachments))
	}
}
