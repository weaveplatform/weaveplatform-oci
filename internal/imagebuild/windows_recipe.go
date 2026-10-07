package imagebuild

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"regexp"
	"strings"
)

var regexpMarker = regexp.MustCompile(`^[0-9a-f]{24}$`)

func windowsRecipe(s WindowsSource, marker string) (string, string, error) {
	if _, _, err := ParseWindowsSelection(
		WindowsSelection{
			FromWindows: s.Edition + "-" + s.Release,
			Arch:        s.Arch,
			Language:    s.Language,
		},
	); err != nil {
		return "", "", err
	}
	if !strings.HasPrefix(marker, "WEAVE-IMAGE-READY-") ||
		!regexpMarker.MatchString(strings.TrimPrefix(marker, "WEAVE-IMAGE-READY-")) {
		return "", "", fmt.Errorf("%w: invalid build completion marker", ErrInput)
	}
	edition := windowsEditions[s.Edition]
	image, key := edition.Image, edition.Key
	escape := func(v string) string { var b bytes.Buffer; _ = xml.EscapeText(&b, []byte(v)); return b.String() }
	component := func(name string) string {
		return `<component name="` + name + `" processorArchitecture="` + s.Arch + `" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS" xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State">`
	}
	command := `powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "$v=Get-Volume | Where-Object FileSystemLabel -eq 'WEAVE-SEED'; if(!$v){exit 1}; & ($v.DriveLetter+':\seal.ps1')"`
	answer := `<?xml version="1.0" encoding="utf-8"?><unattend xmlns="urn:schemas-microsoft-com:unattend"><settings pass="windowsPE">` + component(
		"Microsoft-Windows-International-Core-WinPE",
	) + `<SetupUILanguage><UILanguage>` + escape(
		s.Language,
	) + `</UILanguage></SetupUILanguage><InputLocale>` + escape(
		s.Language,
	) + `</InputLocale><SystemLocale>` + escape(
		s.Language,
	) + `</SystemLocale><UILanguage>` + escape(
		s.Language,
	) + `</UILanguage><UserLocale>` + escape(
		s.Language,
	) + `</UserLocale></component>` + component(
		"Microsoft-Windows-Setup",
	) + `
<DiskConfiguration><Disk wcm:action="add"><DiskID>0</DiskID><WillWipeDisk>true</WillWipeDisk><CreatePartitions>
<CreatePartition wcm:action="add"><Order>1</Order><Type>EFI</Type><Size>300</Size></CreatePartition>
<CreatePartition wcm:action="add"><Order>2</Order><Type>MSR</Type><Size>16</Size></CreatePartition>
<CreatePartition wcm:action="add"><Order>3</Order><Type>Primary</Type><Extend>true</Extend></CreatePartition>
</CreatePartitions><ModifyPartitions><ModifyPartition wcm:action="add"><Order>1</Order><PartitionID>1</PartitionID><Format>FAT32</Format></ModifyPartition><ModifyPartition wcm:action="add"><Order>2</Order><PartitionID>3</PartitionID><Format>NTFS</Format><Letter>C</Letter></ModifyPartition></ModifyPartitions></Disk></DiskConfiguration>
<ImageInstall><OSImage><InstallFrom><MetaData wcm:action="add"><Key>/IMAGE/NAME</Key><Value>` + image + `</Value></MetaData></InstallFrom><InstallTo><DiskID>0</DiskID><PartitionID>3</PartitionID></InstallTo></OSImage></ImageInstall><UserData><AcceptEula>true</AcceptEula><ProductKey><Key>` + key + `</Key></ProductKey></UserData></component></settings>
<settings pass="oobeSystem">` + component(
		"Microsoft-Windows-Deployment",
	) + `<Reseal><Mode>Audit</Mode></Reseal></component></settings>
<settings pass="auditUser">` + component(
		"Microsoft-Windows-Deployment",
	) + `<RunSynchronous><RunSynchronousCommand wcm:action="add"><Order>1</Order><Path>` + escape(
		command,
	) + `</Path></RunSynchronousCommand></RunSynchronous></component></settings></unattend>`
	return answer, windowsSealScript(marker, ""), nil
}

func windowsSealScript(marker, provision string) string {
	check := `if (Get-Service 'WeaveAgent' -ErrorAction SilentlyContinue) { throw 'Base image contains weave agent' }`
	if provision != "" {
		check = provision
	}
	return `$ErrorActionPreference = 'Stop'
$serial = New-Object System.IO.Ports.SerialPort 'COM1',115200,'None',8,'One'
$serial.Open()
function Write-BuildProgress([string]$message) { $serial.WriteLine("WEAVE-IMAGE-PROGRESS $([DateTime]::UtcNow.ToString('o')) $message") }
try {
Write-BuildProgress 'Audit mode reached; checking image state'
$cv = Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion'
` + check + `
$volume = $null
if (Get-Command Get-BitLockerVolume -ErrorAction SilentlyContinue) { $volume = Get-BitLockerVolume -MountPoint C: -ErrorAction SilentlyContinue }
if ($volume -and $volume.VolumeStatus -ne 'FullyDecrypted') { throw 'Refusing encrypted image' }
Write-BuildProgress 'Preparing EFI boot files for fresh firmware'
mountvol.exe S: /S
if ($LASTEXITCODE -ne 0) { throw 'EFI partition unavailable' }
bcdboot.exe C:\Windows /s S: /f UEFI
if ($LASTEXITCODE -ne 0) { throw 'Cannot prepare fresh-firmware boot' }
mountvol.exe S: /D
# Do not replay the build answer file's audit-mode reseal on a consumer's boot.
foreach ($path in @("$env:WINDIR\Panther\unattend.xml", "$env:WINDIR\Panther\Unattend\unattend.xml", "$env:WINDIR\System32\Sysprep\unattend.xml")) {
  if (Test-Path $path) { Remove-Item -LiteralPath $path -Force }
}
$finalAnswer = "$env:WINDIR\Temp\weave-image-seal.xml"
Set-Content -LiteralPath $finalAnswer -Encoding UTF8 -Value '<unattend xmlns="urn:schemas-microsoft-com:unattend" />'
Write-BuildProgress 'Starting Sysprep generalization'
$p = Start-Process "$env:WINDIR\System32\Sysprep\Sysprep.exe" -ArgumentList '/generalize','/oobe','/quit','/quiet',"/unattend:$finalAnswer" -PassThru -Wait
if ($p.ExitCode -ne 0) { throw "Sysprep failed: $($p.ExitCode)" }
Write-BuildProgress 'Sysprep completed; checking generalized image state'
Remove-Item -LiteralPath $finalAnswer -Force
$state = (Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Setup\State').ImageState
if ($state -ne 'IMAGE_STATE_GENERALIZE_RESEAL_TO_OOBE') { throw "Unexpected image state: $state" }
$result = @{osVersion="10.0.$($cv.CurrentBuildNumber).$($cv.UBR)";build="$($cv.CurrentBuildNumber).$($cv.UBR)";edition=$cv.EditionID;release=$cv.DisplayVersion;generalized=$true} | ConvertTo-Json -Compress
$serial.WriteLine("` + marker + ` " + $result)
shutdown.exe /s /t 0
} catch {
  $serial.WriteLine("WEAVE-IMAGE-ERROR $($_.Exception.Message)")
  throw
} finally {
  $serial.Close()
}
`
}
