package imagebuild

import (
	"fmt"
	"strings"
)

// NativeAgentRecipe installs an authenticated payload inside the running guest,
// then stops the agent and removes its machine-bound state. The native builder
// must separately remove its temporary login account and generalize the OS.
// No channel public key or private key is included in these recipes.
func NativeAgentRecipe(l Lock, platform string) (string, error) {
	osName, _, _ := strings.Cut(platform, "/")
	if osName != "darwin" && osName != "windows" {
		return "", fmt.Errorf("%w: native recipe requires darwin or windows", ErrInput)
	}
	entry, err := l.RequirePackages(platform)
	if err != nil {
		return "", err
	}
	if !versionPattern.MatchString(l.CoreVersion) {
		return "", fmt.Errorf("%w: invalid core version", ErrInput)
	}
	if err := entry.Core.Validate(); err != nil {
		return "", err
	}
	for _, m := range entry.Modules {
		if !namePattern.MatchString(m.ID) || !versionPattern.MatchString(m.Version) ||
			!shaPattern.MatchString(m.Binary.Digest) {
			return "", fmt.Errorf("%w: invalid native module", ErrInput)
		}
		if err := m.Package.Validate(); err != nil {
			return "", err
		}
	}
	if osName == "darwin" {
		return macAgentRecipe(l.CoreVersion, entry), nil
	}
	return windowsAgentRecipe(l.CoreVersion, entry), nil
}

func macAgentRecipe(version string, e PlatformInputs) string {
	lines := make([]string, 0, 14+6*len(e.Modules))
	lines = append(lines, "#!/bin/sh", "set -eu", `packages=$1`, `test "$(id -u)" = 0`)
	assets := make([]Asset, 1, 1+len(e.Modules))
	assets[0] = e.Core
	for _, m := range e.Modules {
		assets = append(assets, *m.Package)
	}
	for _, a := range assets {
		path := `"$packages/"` + shellQuote(a.Name)
		lines = append(
			lines,
			`test "$(shasum -a 256 `+path+` | awk '{print $1}')" = `+shellQuote(
				strings.TrimPrefix(a.Digest, "sha256:"),
			),
			"pkgutil --check-signature "+path,
			"spctl --assess --type install "+path,
			"installer -pkg "+path+" -target /",
		)
	}
	lines = append(
		lines,
		`test "$(/usr/local/libexec/weave/weave-agent --version)" = `+shellQuote(version),
	)
	for _, m := range e.Modules {
		lines = append(
			lines,
			`test "$(pkgutil --pkg-info `+shellQuote(
				"run.weaveplatform.module."+m.ID,
			)+` | awk '/^version: / {print $2}')" = `+shellQuote(
				m.Version,
			),
			`test "$(shasum -a 256 `+shellQuote(
				"/usr/local/libexec/weave/modules/"+m.ID+"/"+m.ID,
			)+` | awk '{print $1}')" = `+shellQuote(
				strings.TrimPrefix(m.Binary.Digest, "sha256:"),
			),
		)
	}
	lines = append(
		lines,
		"launchctl bootout system/run.weaveplatform.agent || { if launchctl print system/run.weaveplatform.agent >/dev/null 2>&1; then exit 1; fi; }",
		"launchctl enable system/run.weaveplatform.agent",
		"rm -f /etc/weave/channel.pub",
		`for name in store.key store.db store.db-wal store.db-shm; do rm -f "/Library/Application Support/Weave/$name"; done`,
		"sync",
	)
	return strings.Join(lines, "\n") + "\n"
}

func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func windowsAgentRecipe(version string, e PlatformInputs) string {
	lines := make([]string, 0, 26+13*len(e.Modules))
	lines = append(lines,
		`param([Parameter(Mandatory=$true)][string]$Packages)`, `$ErrorActionPreference = 'Stop'`,
		`$work = Join-Path $env:TEMP ('weave-install-' + [guid]::NewGuid().ToString())`,
		`New-Item -ItemType Directory -Path $work | Out-Null`, "try {",
	)
	assets := make([]Asset, 1, 1+len(e.Modules))
	assets[0] = e.Core
	for _, m := range e.Modules {
		assets = append(assets, *m.Package)
	}
	for i, a := range assets {
		lines = append(
			lines,
			`$archive = Join-Path $Packages `+psQuote(a.Name),
			`if ((Get-FileHash -Algorithm SHA256 -LiteralPath $archive).Hash.ToLowerInvariant() -cne `+psQuote(
				strings.TrimPrefix(a.Digest, "sha256:"),
			)+`) { throw 'Installer hash differs from lock' }`,
			fmt.Sprintf(`$destination = Join-Path $work 'package-%d'`, i),
			`Expand-Archive -LiteralPath $archive -DestinationPath $destination`,
			`$install = Join-Path $destination 'install.ps1'`,
		)
		argument := "-NoReload"
		if i == 0 {
			argument = "-NoStart"
		}
		lines = append(
			lines,
			`& powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $install `+argument,
			`if ($LASTEXITCODE -ne 0) { throw 'Package installation failed' }`,
		)
	}
	lines = append(
		lines,
		`$agent = Join-Path $env:ProgramFiles 'Weave\weave-agent.exe'`,
		`if ((Get-AuthenticodeSignature -LiteralPath $agent).Status -ne 'Valid') { throw 'Core Authenticode verification failed' }`,
		`$version = & $agent --version`,
		`if ($LASTEXITCODE -ne 0 -or $version -cne `+psQuote(
			version,
		)+`) { throw 'Core version differs from lock' }`,
	)
	for _, m := range e.Modules {
		lines = append(
			lines,
			`$module = Join-Path $env:ProgramFiles `+psQuote(`Weave\modules\`+m.ID+`\`),
			`$binary = Join-Path $module `+psQuote(m.ID+".exe"),
			`if ((Get-AuthenticodeSignature -LiteralPath $binary).Status -ne 'Valid') { throw 'Module Authenticode verification failed' }`,
			`if ((Get-FileHash -Algorithm SHA256 -LiteralPath $binary).Hash.ToLowerInvariant() -cne `+psQuote(
				strings.TrimPrefix(m.Binary.Digest, "sha256:"),
			)+`) { throw 'Module hash differs from lock' }`,
			`$manifest = Get-Content -LiteralPath (Join-Path $module 'module.manifest.json') -Raw | ConvertFrom-Json`,
			`if ($manifest.id -cne `+psQuote(
				m.ID,
			)+` -or $manifest.version -cne `+psQuote(
				m.Version,
			)+`) { throw 'Module manifest differs from lock' }`,
		)
	}
	lines = append(
		lines,
		`Stop-Service -Name WeaveAgent -ErrorAction Stop`,
		`if ((Get-Service -Name WeaveAgent).Status -ne 'Stopped') { throw 'Agent did not stop before sealing' }`,
		`Set-Service -Name WeaveAgent -StartupType Automatic`,
		`foreach ($name in @('channel.pub','store.key','store.db','store.db-wal','store.db-shm')) {`,
		`  $path = Join-Path (Join-Path $env:ProgramData 'weave') $name`,
		`  if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path -Force }`,
		"}",
		"} finally {",
		`  Remove-Item -LiteralPath $work -Recurse -Force`,
		"}",
	)
	return strings.Join(lines, "\n") + "\n"
}
