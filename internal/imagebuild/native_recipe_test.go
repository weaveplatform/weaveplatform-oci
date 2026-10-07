package imagebuild

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeRecipesInstallAndSealPinnedPackages(t *testing.T) {
	for _, osName := range []string{"darwin", "windows"} {
		t.Run(osName, func(t *testing.T) {
			l, _ := nativePackageFixture(t, osName)
			recipe, err := NativeAgentRecipe(l, osName+"/arm64")
			must(t, err)
			entry := l.Platforms[osName+"/arm64"]
			for _, want := range []string{entry.Core.Name, strings.TrimPrefix(entry.Core.Digest, "sha256:"), entry.Modules[0].Package.Name, strings.TrimPrefix(entry.Modules[0].Binary.Digest, "sha256:"), "1.2.3", "2.3.4", "store.db", "store.db-wal", "store.db-shm", "store.key", "channel.pub"} {
				if !strings.Contains(recipe, want) {
					t.Errorf("missing pinned install or sealing assertion %q", want)
				}
			}
			if strings.Contains(recipe, "manifest.sequence") {
				t.Fatal("recipe touches anti-rollback state")
			}
			stop, remove := "launchctl bootout", "rm -f /etc/weave/channel.pub"
			if osName == "windows" {
				stop, remove = "Stop-Service", "foreach ($name"
			}
			if strings.Index(recipe, stop) > strings.Index(recipe, remove) {
				t.Fatal("seals while agent can still write state")
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "recipe")
			must(t, os.WriteFile(path, []byte(recipe), 0o600))
			if osName == "darwin" {
				sh, err := exec.LookPath("sh")
				if err != nil {
					t.Skip("shell parser unavailable")
				}
				output, err := exec.CommandContext(t.Context(), sh, "-n", path).CombinedOutput()
				if err != nil {
					t.Fatalf("shell syntax: %v %s", err, output)
				}
			} else {
				pwsh, err := exec.LookPath("pwsh")
				if err != nil {
					t.Skip("PowerShell parser unavailable")
				}
				check := filepath.Join(dir, "check.ps1")
				must(t, os.WriteFile(check, []byte(`param([string]$Path)
$tokens = $null
$parseErrors = $null
$null = [System.Management.Automation.Language.Parser]::ParseFile($Path, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -gt 0) { $parseErrors | Write-Output; exit 1 }
`), 0o600))
				output, err := exec.CommandContext(t.Context(), pwsh, "-NoProfile", "-NonInteractive", "-File", check, "-Path", path).
					CombinedOutput()
				if err != nil {
					t.Fatalf("PowerShell syntax: %v %s", err, output)
				}
			}
		})
	}
}

func TestNativeRecipeRefusesUnsafeOrIncompleteInputs(t *testing.T) {
	for _, mode := range []string{"platform", "absent platform", "missing installer", "core version", "core name", "module ID", "module version", "module digest", "package name"} {
		t.Run(mode, func(t *testing.T) {
			l, _ := nativePackageFixture(t, "windows")
			e := l.Platforms["windows/arm64"]
			platform := "windows/arm64"
			switch mode {
			case "platform":
				platform = "linux/arm64"
			case "absent platform":
				platform = "windows/amd64"
			case "missing installer":
				e.Modules[0].Package = nil
			case "core version":
				l.CoreVersion = "1'; evil"
			case "core name":
				e.Core.Name = "../installer.zip"
			case "module ID":
				e.Modules[0].ID = "../exec"
			case "module version":
				e.Modules[0].Version = "1'; evil"
			case "module digest":
				e.Modules[0].Binary.Digest = "bad"
			case "package name":
				e.Modules[0].Package.Name = "../module.zip"
			}
			l.Platforms["windows/arm64"] = e
			if _, err := NativeAgentRecipe(l, platform); err == nil {
				t.Fatal("accepted unsafe recipe input")
			}
		})
	}
	if psQuote("path's name") != "'path''s name'" {
		t.Fatal("unsafe PowerShell quoting")
	}
}
