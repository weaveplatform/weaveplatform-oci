package imagebuild

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWindowsRecipePowerShellSyntax(t *testing.T) {
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("PowerShell parser unavailable")
	}
	_, source := windowsFixture()
	_, script, err := windowsRecipe(source, "WEAVE-IMAGE-READY-0123456789abcdef01234567")
	must(t, err)
	dir := t.TempDir()
	seal := filepath.Join(dir, "seal.ps1")
	must(t, os.WriteFile(seal, []byte(script), 0o600))
	check := filepath.Join(dir, "check.ps1")
	must(t, os.WriteFile(check, []byte(`param([string]$Path)
$tokens = $null
$parseErrors = $null
$null = [System.Management.Automation.Language.Parser]::ParseFile($Path, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -gt 0) { $parseErrors | ForEach-Object { Write-Output $_ }; exit 1 }
`), 0o600))
	output, err := exec.CommandContext(t.Context(), pwsh, "-NoProfile", "-NonInteractive", "-File", check, "-Path", seal).
		CombinedOutput()
	if err != nil {
		t.Fatalf("generated sealing script is invalid: %v\n%s", err, output)
	}
}
