package imagebuild

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/weaveplatform/weaveplatform-oci/pkg/chunk"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

func desktopLockFixture() DesktopLock {
	l := DesktopLock{
		SchemaVersion: 1,
		OSVersion:     "26.04",
		Arch:          "arm64",
		ParentDigest:  "sha256:" + strings.Repeat("a", 64),
		Snapshot:      "20261001T000000Z",
	}
	for _, name := range []string{"xfce4", "lightdm", "xserver-xorg", "x11-xserver-utils", "dbus-x11"} {
		l.Packages = append(
			l.Packages,
			DesktopPackage{
				Name:    name,
				Version: "1:2.3-4ubuntu1",
				Arch:    "all",
				SHA256:  strings.Repeat("a", 64),
			},
		)
	}
	return l
}

func TestDesktopLockRejectsUnpinnedInputs(t *testing.T) {
	for name, change := range map[string]func(*DesktopLock){
		"schema":       func(l *DesktopLock) { l.SchemaVersion = 2 },
		"OS":           func(l *DesktopLock) { l.OSVersion = "24.04" },
		"arch":         func(l *DesktopLock) { l.Arch = "i386" },
		"parent":       func(l *DesktopLock) { l.ParentDigest = "latest" },
		"snapshot":     func(l *DesktopLock) { l.Snapshot = "latest" },
		"name":         func(l *DesktopLock) { l.Packages[0].Name = "xfce4;id" },
		"version":      func(l *DesktopLock) { l.Packages[0].Version = "$(id)" },
		"package arch": func(l *DesktopLock) { l.Packages[0].Arch = "amd64" },
		"checksum":     func(l *DesktopLock) { l.Packages[0].SHA256 = "none" },
		"duplicate":    func(l *DesktopLock) { l.Packages = append(l.Packages, l.Packages[0]) },
		"missing":      func(l *DesktopLock) { l.Packages = l.Packages[1:] },
	} {
		t.Run(name, func(t *testing.T) {
			l := desktopLockFixture()
			change(&l)
			if _, err := DesktopRecipe(l); err == nil {
				t.Fatal("accepted invalid input")
			}
		})
	}
	file := filepath.Join(t.TempDir(), "desktop.json")
	if _, err := LoadDesktopLock(file); err == nil {
		t.Fatal("accepted missing lock")
	}
	must(t, writeJSON(file, desktopLockFixture()))
	_, err := LoadDesktopLock(file)
	must(t, err)
	must(t, os.WriteFile(file, []byte(`{"schemaVersion":1,"unexpected":true}`), 0o600))
	if _, err := LoadDesktopLock(file); err == nil {
		t.Fatal("accepted invalid lock")
	}
}

func TestDesktopRecipePinsClosureAndSealsWithoutCredentials(t *testing.T) {
	for _, version := range []string{"20.04", "26.04"} {
		l := desktopLockFixture()
		l.OSVersion = version
		recipe, err := DesktopRecipe(l)
		must(t, err)
		cmd := exec.CommandContext(t.Context(), "sh", "-n")
		cmd.Stdin = strings.NewReader(recipe)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("shell syntax: %s %v", out, err)
		}
		for _, want := range []string{"signed-by=/usr/share/keyrings/ubuntu-archive-keyring.gpg", "https://snapshot.ubuntu.com/ubuntu/20261001T000000Z/", "AllowUnauthenticated=false", "--no-download", "Unpinned desktop dependency", "sha256sum", "test ! -e \"$work/seen-$identity\"", "graphical.target", "store.key", "cloud-init clean", "truncate -s 0 /etc/machine-id"} {
			if !strings.Contains(recipe, want) {
				t.Fatal("missing", want)
			}
		}
		for _, bad := range []string{"useradd", "autologin-user", "manifest.sequence", "--allow-unauthenticated"} {
			if strings.Contains(recipe, bad) {
				t.Fatal("unsafe recipe", bad)
			}
		}
		slices.Reverse(l.Packages)
		reordered, err := DesktopRecipe(l)
		must(t, err)
		if recipe != reordered {
			t.Fatal("package order changes recipe")
		}
	}
}

func desktopParent(t *testing.T, mutate func(*pack.BundleFile)) (string, string, Lock) {
	t.Helper()
	lock, _ := packageFixture(t)
	b, err := pack.LoadBundle(bootBundle(t, "arm64"))
	must(t, err)
	b.File.Guest.Variant = "agent"
	b.File.Guest.OSVersion = "26.04"
	b.File.Provisioning.Agent = &spec.Agent{Name: "weave-agent", Version: "1.2.3"}
	b.File.Build.Base = &spec.BaseImage{
		Name:   "ghcr.io/weaveplatform/ubuntu-base",
		Digest: "sha256:" + strings.Repeat("d", 64),
	}
	entry := lock.Platforms["linux/arm64"]
	b.File.Build.SourceMedia = []spec.SourceMedia{
		{Kind: "package", URI: entry.Core.URI, Digest: entry.Core.Digest},
	}
	for _, m := range entry.Modules {
		b.File.Build.SourceMedia = append(
			b.File.Build.SourceMedia,
			spec.SourceMedia{Kind: "package", URI: m.Package.URI, Digest: m.Package.Digest},
		)
	}
	if mutate != nil {
		mutate(&b.File)
	}
	must(t, pack.WriteBundleFile(b.Dir, b.File))
	b, err = pack.LoadBundle(b.Dir)
	must(t, err)
	dir := filepath.Join(t.TempDir(), "layout")
	store, err := pack.OpenLayout(t.Context(), dir)
	must(t, err)
	desc, err := pack.Manifest(t.Context(), b, store, chunk.Options{})
	must(t, err)
	root, err := pack.Index(t.Context(), store, []ocispec.Descriptor{desc}, nil)
	must(t, err)
	must(t, store.Tag(t.Context(), root, "candidate"))
	return dir, desc.Digest.String(), lock
}

func TestDesktopBuildPreservesAgentAndExactParent(t *testing.T) {
	fakeFirmware(t)
	base, parent, agentLock := desktopParent(t, nil)
	for _, mode := range []string{"valid", "options", "lock", "architecture", "layout", "parent", "output", "boot", "git", "metadata", "lock-output", "missing-packages", "wrong-module"} {
		t.Run(mode, func(t *testing.T) {
			l := desktopLockFixture()
			l.ParentDigest = parent
			out := filepath.Join(t.TempDir(), "desktop")
			o := DesktopOptions{
				AgentOptions: AgentOptions{
					Base:     base,
					BaseName: "ghcr.io/weaveplatform/ubuntu-agent",
					Arch:     "arm64",
					Cache:    t.TempDir(),
					Out:      out,
					Revision: 1,
					Timeout:  time.Second,
					Lock:     agentLock,
				},
				Desktop: l,
			}
			fake := &fakeQEMU{t: t}
			switch mode {
			case "missing-packages":
				o.Lock.Platforms = nil
			case "wrong-module":
				o.Lock = cloneLock(t, agentLock)
				entry := o.Lock.Platforms["linux/arm64"]
				entry.Modules[0].Package.Digest = "sha256:" + strings.Repeat("f", 64)
				o.Lock.Platforms["linux/arm64"] = entry
			case "options":
				o.Revision = 0
			case "lock":
				o.Desktop.Snapshot = "latest"
			case "architecture":
				o.Arch = "amd64"
			case "layout":
				o.Base = filepath.Join(t.TempDir(), "absent")
			case "parent":
				o.Desktop.ParentDigest = "sha256:" + strings.Repeat("c", 64)
			case "output":
				must(t, os.Mkdir(out, 0o700))
			case "boot":
				fake.fail = "qemu-system-aarch64"
			case "git":
				fake.fail = "git"
			}
			p := Packages{
				Tools: Tools{
					Run: func(ctx context.Context, w, e io.Writer, name string, args ...string) error {
						if name == "git" {
							if mode == "metadata" {
								must(
									t,
									os.Mkdir(filepath.Join(out, "bundle", "bundle.json"), 0o700),
								)
							}
							if mode == "lock-output" {
								must(t, os.Mkdir(filepath.Join(out, "desktop.lock.json"), 0o700))
							}
						}
						return fake.run(ctx, w, e, name, args...)
					},
				},
			}
			bundle, err := p.BuildLinuxDesktop(t.Context(), o)
			if mode != "valid" {
				if err == nil {
					t.Fatal("accepted", mode)
				}
				return
			}
			must(t, err)
			b, err := pack.LoadBundle(bundle)
			must(t, err)
			if b.File.Guest.Variant != "desktop" || b.File.Build.Base.Digest != parent ||
				b.File.Provisioning.Agent.Version != "1.2.3" ||
				len(b.File.Build.SourceMedia) != 2 {
				t.Fatal(b.File)
			}
			if !strings.HasPrefix(b.File.Annotations["io.weave.image.desktop-lock"], "sha256:") {
				t.Fatal("missing desktop lock binding")
			}
			raw, err := os.ReadFile(filepath.Join(out, "desktop.lock.json"))
			must(t, err)
			var saved DesktopLock
			must(t, json.Unmarshal(raw, &saved))
			if !slices.IsSortedFunc(
				saved.Packages,
				func(a, b DesktopPackage) int { return strings.Compare(a.Name, b.Name) },
			) {
				t.Fatal("noncanonical saved lock")
			}
		})
	}
}

func TestDesktopRefusesBaseOrWrongCoreParent(t *testing.T) {
	for _, mode := range []string{"base", "core", "disks"} {
		t.Run(mode, func(t *testing.T) {
			base, parent, agentLock := desktopParent(t, func(b *pack.BundleFile) {
				switch mode {
				case "base":
					b.Guest.Variant = "base"
					b.Provisioning.Agent = nil
					b.Build.Base = nil
				case "core":
					b.Provisioning.Agent.Version = "0.9.0"
				case "disks":
					b.Disks = append(
						b.Disks,
						pack.BundleDisk{Name: "disk1", Role: "data", Path: "disk0.img"},
					)
				}
			})
			l := desktopLockFixture()
			l.ParentDigest = parent
			_, err := (Packages{}).BuildLinuxDesktop(
				t.Context(),
				DesktopOptions{
					AgentOptions: AgentOptions{
						Base:     base,
						BaseName: "ghcr.io/weaveplatform/ubuntu-agent",
						Arch:     "arm64",
						Cache:    t.TempDir(),
						Out:      filepath.Join(t.TempDir(), "out"),
						Revision: 1,
						Timeout:  time.Second,
						Lock:     agentLock,
					},
					Desktop: l,
				},
			)
			if err == nil {
				t.Fatal("accepted", mode)
			}
		})
	}
}

// Run the archive gate itself with fake dpkg metadata. Corrupt, extra, missing
// and duplicate archives must be rejected before the install command can run.
func TestDesktopArchiveGateExecutesBeforeInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Ubuntu shell payload is executed on Unix test runners")
	}
	for _, mode := range []string{"valid", "corrupt", "extra", "missing", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			l := desktopLockFixture()
			work := t.TempDir()
			archives := filepath.Join(work, "archives")
			must(t, os.Mkdir(archives, 0o700))
			for n := range l.Packages {
				p := &l.Packages[n]
				data := []byte(p.Name + ":" + p.Version + ":" + p.Arch + "\n")
				p.SHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
				if mode == "missing" && n == 0 {
					continue
				}
				if mode == "corrupt" && n == 0 {
					data = append(data, []byte("corrupt\n")...)
				}
				must(t, os.WriteFile(filepath.Join(archives, p.Name+".deb"), data, 0o600))
			}
			if mode == "extra" {
				must(
					t,
					os.WriteFile(
						filepath.Join(archives, "extra.deb"),
						[]byte("unknown:1.0:all\n"),
						0o600,
					),
				)
			}
			if mode == "duplicate" {
				p := l.Packages[0]
				must(
					t,
					os.WriteFile(
						filepath.Join(archives, "duplicate.deb"),
						[]byte(p.Name+":"+p.Version+":"+p.Arch+"\n"),
						0o600,
					),
				)
			}
			// The version may contain a Debian epoch colon. Return the metadata columns
			// without treating that epoch as a field separator.
			shim := `#!/bin/sh
case "$3" in
Package) sed -n '1s/:.*//p' "$2" ;;
Version) sed -n '1s/^[^:]*:\(.*\):[^:]*$/\1/p' "$2" ;;
Architecture) sed -n '1s/.*://p' "$2" ;;
esac
`
			must(t, os.WriteFile(filepath.Join(work, "dpkg-deb"), []byte(shim), 0o700))
			recipe, err := DesktopRecipe(l)
			must(t, err)
			start := strings.Index(recipe, "count=0\n")
			end := strings.Index(recipe, "apt_locked --no-download")
			if start < 0 || end < start {
				t.Fatal("archive gate not before installation")
			}
			cmd := exec.CommandContext(
				t.Context(),
				"sh",
				"-c",
				"set -eu\nwork="+shellQuote(work)+"\n"+recipe[start:end],
			)
			cmd.Env = append(os.Environ(), "PATH="+work+":"+os.Getenv("PATH"))
			output, err := cmd.CombinedOutput()
			if (err == nil) != (mode == "valid") {
				t.Fatalf("gate %s: %s %v", mode, output, err)
			}
		})
	}
}
