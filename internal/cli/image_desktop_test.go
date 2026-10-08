package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weaveplatform/weaveplatform-oci/internal/imagebuild"
)

func TestDesktopCommandRequiresLockAndExactParent(t *testing.T) {
	for _, mode := range []string{"missing lock", "invalid lock", "valid lock"} {
		t.Run(mode, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "desktop.json")
			l := imagebuild.DesktopLock{
				SchemaVersion: 1,
				OSVersion:     "26.04",
				Arch:          "arm64",
				Snapshot:      "20261001T000000Z",
				ParentDigest:  "sha256:" + strings.Repeat("a", 64),
			}
			for _, name := range []string{"xfce4", "lightdm", "xserver-xorg", "x11-xserver-utils", "dbus-x11"} {
				l.Packages = append(
					l.Packages,
					imagebuild.DesktopPackage{
						Name:    name,
						Version: "1.0",
						Arch:    "all",
						SHA256:  strings.Repeat("b", 64),
					},
				)
			}
			if mode == "invalid lock" {
				l.Snapshot = "latest"
			}
			if mode != "missing lock" {
				raw, err := json.Marshal(l)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cmd := newImageDesktop(
				imagebuild.Packages{},
				func() (imagebuild.Lock, error) { return imagebuild.Lock{CoreVersion: "1.2.3"}, nil },
				func(any) error { t.Fatal("emitted failed build"); return nil },
			)
			cmd.SetContext(t.Context())
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(
				[]string{
					"--desktop-lock",
					file,
					"--base",
					filepath.Join(t.TempDir(), "missing"),
					"--base-name",
					"ghcr.io/weaveplatform/ubuntu-agent",
					"--arch",
					"arm64",
					"--cache",
					t.TempDir(),
					"--out",
					filepath.Join(t.TempDir(), "out"),
				},
			)
			err := cmd.Execute()
			if err == nil {
				t.Fatal("accepted missing parent")
			}
			if mode == "valid lock" && !strings.Contains(err.Error(), "read base layout") {
				t.Fatal(err)
			}
			if mode != "valid lock" && !strings.Contains(err.Error(), "desktop lock") {
				t.Fatal(err)
			}
		})
	}
}
