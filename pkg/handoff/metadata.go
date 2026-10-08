package handoff

import (
	"fmt"
	"slices"
	"strings"

	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

// Native result v1 is a JSON boundary, deliberately independent of builder SDKs.
type nativeResult struct {
	SchemaVersion int             `json:"schemaVersion"`
	Qualification string          `json:"qualification"`
	OSVersion     string          `json:"osVersion"`
	FirstBoot     string          `json:"firstBoot"`
	Prepared      *preparedResult `json:"prepared,omitempty"`
	Files         []string        `json:"files"`
	Firmware      struct {
		Type          string `json:"type"`
		SecureBoot    bool   `json:"secureBoot"`
		TPM           string `json:"tpm"`
		CloneIdentity string `json:"cloneIdentity"`
	} `json:"firmware"`
	Inputs struct {
		Family          string `json:"family"`
		Release         string `json:"release"`
		Arch            string `json:"arch"`
		SourcePath      string `json:"sourcePath"`
		SourceSHA256    string `json:"sourceSHA256"`
		SourceBuild     string `json:"sourceBuild"`
		Edition         string `json:"edition,omitempty"`
		Language        string `json:"language,omitempty"`
		OutputDirectory string `json:"outputDirectory"`
		Timeout         string `json:"timeout"`
	} `json:"inputs"`
	Mac *struct {
		HardwareModel                                    string
		CPUCountMin, CPUCount, MemorySizeMin, MemorySize int64
	} `json:"mac,omitempty"`
	Windows *struct {
		OSVersion   string `json:"osVersion"`
		Build       string `json:"build"`
		Edition     string `json:"edition"`
		Release     string `json:"release"`
		Arch        string `json:"arch"`
		Generalized bool   `json:"generalized"`
	} `json:"windows,omitempty"`
}

func linuxMetadata(b build, f *pack.BundleFile) error {
	d := b.Data
	if !slices.Contains([]string{"ubuntu", "fedora"}, d["family"]) ||
		!slices.Contains([]string{"amd64", "arm64"}, d["arch"]) || d["release"] == "" ||
		d["purpose"] != "guest-base" || d["target"] != "qemu" ||
		!sha.MatchString(d["firmware_code_sha256"]) || !sha.MatchString(d["firmware_vars_sha256"]) {
		return fmt.Errorf("%w: unsupported or incomplete Linux base metadata", ErrInput)
	}
	f.Guest.OS, f.Guest.Arch, f.Guest.Distro, f.Guest.OSVersion = "linux", d["arch"], d["family"], d["release"]
	f.Build.Template = "templates/qemu/image.pkr.hcl"
	f.Build.SourceMedia[0].Kind = "cloud-image"
	f.Provisioning.CredentialHint = "cloud-init"
	f.Annotations["io.weave.image.first-boot"] = "cloud-init"
	f.Annotations["io.weave.imageweave.firmware-code.digest"] = "sha256:" + d["firmware_code_sha256"]
	f.Annotations["io.weave.imageweave.firmware-vars.digest"] = "sha256:" + d["firmware_vars_sha256"]
	return nil
}

func nativeMetadata(raw []byte, b build, f *pack.BundleFile) error {
	var r nativeResult
	if err := decode(raw, &r); err != nil {
		return err
	}
	i := r.Inputs
	if (r.SchemaVersion != 1 && r.SchemaVersion != 2) || r.Qualification != "unverified" || r.OSVersion == "" ||
		i.SourceSHA256 != b.Data["source_sha256"] ||
		i.SourceBuild != b.Data["source_build"] ||
		!slices.Contains([]string{"amd64", "arm64"}, i.Arch) {
		return fmt.Errorf("%w: native result does not match manifest", ErrInput)
	}
	if (r.SchemaVersion == 1 && r.Prepared != nil) ||
		(r.SchemaVersion == 2 && (r.Prepared == nil || i.Family != "macos")) {
		return fmt.Errorf("%w: native v2 is reserved for prepared macOS images", ErrInput)
	}
	f.Guest.Arch, f.Guest.OSVersion = i.Arch, r.OSVersion
	f.Annotations["io.weave.image.first-boot"] = r.FirstBoot
	switch i.Family {
	case "macos":
		if r.Mac == nil || r.Windows != nil || i.Arch != "arm64" ||
			strings.Split(r.OSVersion, ".")[0] != i.Release ||
			(r.Prepared == nil && r.FirstBoot != "setup-assistant") ||
			r.Firmware.Type != "apple" ||
			r.Firmware.TPM != "none" ||
			r.Firmware.SecureBoot ||
			r.Firmware.CloneIdentity != "new-machine-identifier; copy auxiliary storage" ||
			!slices.Equal(r.Files, []string{"disk0.img", "auxstorage.bin"}) {
			return fmt.Errorf("%w: invalid Apple restore result", ErrInput)
		}
		f.Guest.OS = "darwin"
		f.Firmware = spec.Firmware{Type: "apple", TPM: "none", HardwareModel: r.Mac.HardwareModel}
		f.Resources = spec.Resources{
			CPU:    spec.MinDefault{Min: r.Mac.CPUCountMin, Default: r.Mac.CPUCount},
			Memory: spec.MinDefault{Min: r.Mac.MemorySizeMin, Default: r.Mac.MemorySize},
		}
		f.State = []pack.BundleState{
			{
				Name:      spec.StateAuxStorage,
				Path:      "auxstorage.bin",
				Semantics: spec.SemanticsCarry,
				Required:  true,
			},
		}
		f.Build.Template, f.Build.SourceMedia[0].Kind = "templates/macos/image.pkr.hcl", "ipsw"
		if r.Prepared != nil {
			if err := preparedMetadata(r, b, f); err != nil {
				return err
			}
		}
	case "windows-11":
		w := r.Windows
		edition := map[string]string{"pro": "Professional", "home": "Core", "enterprise": "Enterprise", "education": "Education"}[i.Edition]
		if w == nil || r.Mac != nil || edition == "" || !w.Generalized || w.Release != i.Release || w.Arch != i.Arch || w.Edition != edition ||
			w.Build != i.SourceBuild || w.OSVersion != r.OSVersion ||
			w.OSVersion != "10.0."+w.Build ||
			r.FirstBoot != "windows-oobe" ||
			r.Firmware.Type != "uefi" ||
			r.Firmware.TPM != "required" ||
			!r.Firmware.SecureBoot ||
			r.Firmware.CloneIdentity != "regenerate firmware and TPM state" ||
			!slices.Equal(r.Files, []string{"disk0.img"}) {
			return fmt.Errorf("%w: invalid generalized Windows result", ErrInput)
		}
		f.Guest.OS, f.Guest.Edition = "windows", w.Edition
		f.Firmware = spec.Firmware{Type: "uefi", TPM: "required", SecureBoot: true}
		f.Resources = spec.Resources{
			CPU:    spec.MinDefault{Min: 2, Default: 4},
			Memory: spec.MinDefault{Min: 4 << 30, Default: 8 << 30},
		}
		f.Annotations["io.weave.image.windows.release"] = w.Release
		f.Build.Template, f.Build.SourceMedia[0].Kind = "templates/windows/image.pkr.hcl", "iso"
	default:
		return fmt.Errorf("%w: unsupported native family", ErrInput)
	}
	return nil
}
