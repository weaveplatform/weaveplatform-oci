package imagebuild

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/deploymenttheory/go-sdk-winmediafoundry/pkg/isoinspect"
	"github.com/deploymenttheory/go-sdk-winmediafoundry/pkg/udf"

	"github.com/weaveplatform/weaveplatform-oci/pkg/disk/vhd"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

// WindowsOptions selects a Windows base image and its isolated build workspace.
type (
	WindowsOptions struct {
		Selection              WindowsSelection
		SourceLock, Out, Cache string
		Revision               int
		Timeout                time.Duration
	}
	// WindowsInstallRequest contains only build-owned paths and an unpredictable completion marker.
	WindowsInstallRequest struct {
		Directory, ISO, Seed, Marker string
		Timeout                      time.Duration
		Arch                         string
		Log                          io.Writer
	}
	// WindowsInstallResult is reported by the guest after successful generalization.
	WindowsInstallResult struct {
		OSVersion   string `json:"osVersion"`
		Build       string `json:"build"`
		Edition     string `json:"edition"`
		Release     string `json:"release"`
		Generalized bool   `json:"generalized"`
	}
	// WindowsInstallFunc isolates the native HCS boundary for orchestration tests.
	WindowsInstallFunc func(context.Context, WindowsInstallRequest) (WindowsInstallResult, error)
)

// BuildWindows creates a generalized Windows base using Microsoft media and native HCS APIs.
func (p Packages) BuildWindows(ctx context.Context, o WindowsOptions) (string, error) {
	if runtime.GOOS != "windows" && p.WindowsInstall == nil {
		return "", fmt.Errorf(
			"%w: Windows image installation requires a Windows HCS build host",
			ErrInput,
		)
	}
	if _, _, err := ParseWindowsSelection(o.Selection); err != nil {
		return "", err
	}
	if o.Revision < 1 || o.Timeout <= 0 || o.Out == "" || o.Cache == "" {
		return "", fmt.Errorf("%w: output, cache, positive revision and timeout required", ErrInput)
	}
	var pathErr error
	o.Out, pathErr = filepath.Abs(o.Out)
	if pathErr != nil {
		return "", fmt.Errorf("resolve output path: %w", pathErr)
	}
	if runtime.GOOS == "windows" && runtime.GOARCH != o.Selection.Arch {
		return "", fmt.Errorf("%w: HCS build host and guest architectures must match", ErrInput)
	}
	var source WindowsSource
	p.Tools.progress(
		"Windows %s: resolving %s media (%s)",
		o.Selection.Arch,
		o.Selection.FromWindows,
		o.Selection.Language,
	)
	var err error
	if o.SourceLock != "" {
		err = readJSON(o.SourceLock, &source)
		if err == nil {
			err = source.validate(o.Selection, true)
		}
	} else {
		source, err = p.ResolveWindows(ctx, o.Selection)
	}
	if err != nil {
		return "", err
	}
	if err := newDirectory(o.Out); err != nil {
		return "", err
	}
	p.Tools.progress("Windows %s: downloading and verifying source media", o.Selection.Arch)
	source, iso, err := p.AcquireWindows(ctx, source, o.Selection, o.Cache)
	if err != nil {
		return "", err
	}
	if err := writeJSON(filepath.Join(o.Out, "source-lock.json"), source); err != nil {
		return "", err
	}
	p.Tools.progress("Windows %s: preparing unattended installation media", o.Selection.Arch)
	installISO, err := p.prepareWindowsISO(ctx, source, iso, o.Out)
	if err != nil {
		return "", err
	}
	if err := isoinspect.RetargetElToritoUEFI(
		installISO,
		"efi/microsoft/boot/efisys_noprompt.bin",
	); err != nil {
		return "", fmt.Errorf("prepare native unattended boot media: %w", err)
	}
	marker := fmt.Sprintf("WEAVE-IMAGE-READY-%x", randomMarker())
	seed, err := windowsSeed(o.Out, source, marker)
	if err != nil {
		return "", err
	}
	install := p.WindowsInstall
	if install == nil {
		install = installWindowsNative
	}
	result, err := install(
		ctx,
		WindowsInstallRequest{
			Directory: o.Out,
			ISO:       installISO,
			Seed:      seed,
			Marker:    marker,
			Timeout:   o.Timeout,
			Arch:      o.Selection.Arch,
			Log:       p.Tools.Log,
		},
	)
	if err != nil {
		return "", err
	}
	if !result.Generalized || result.Build == "" || result.OSVersion != "10.0."+result.Build ||
		!windowsInstalledVersionMatches(result, source) {
		return "", fmt.Errorf(
			"%w: Windows guest did not confirm a generalized installation",
			ErrInput,
		)
	}
	expected := windowsEditions[source.Edition].ID
	if result.Edition != expected {
		return "", fmt.Errorf(
			"%w: installed Windows edition differs from requested edition",
			ErrInput,
		)
	}
	if err := writeJSON(filepath.Join(o.Out, "install-result.json"), result); err != nil {
		return "", err
	}
	p.Tools.progress(
		"Windows %s: verified generalized guest; exporting raw bundle",
		o.Selection.Arch,
	)
	bundle, err := p.Tools.windowsBundle(ctx, o, source, result)
	if err == nil {
		p.Tools.progress("Windows %s: candidate ready at %s", o.Selection.Arch, bundle)
	}
	return bundle, err
}

func (t Tools) windowsBundle(
	ctx context.Context,
	o WindowsOptions,
	s WindowsSource,
	r WindowsInstallResult,
) (string, error) {
	bundle := filepath.Join(o.Out, "bundle")
	if err := newDirectory(bundle); err != nil {
		return "", err
	}
	disk := filepath.Join(o.Out, "disk.vhd")
	_, f, size, err := vhd.Raw(disk)
	if err != nil {
		return "", fmt.Errorf("validate native fixed VHD: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close VHD: %w", err)
	}
	// A fixed VHD contains raw disk bytes followed by a footer. The VM is stopped
	// and its handles are closed before removing that footer in place.
	if err := os.Truncate(disk, size); err != nil {
		return "", fmt.Errorf("remove VHD footer: %w", err)
	}
	if err := os.Rename(disk, filepath.Join(bundle, "disk0.img")); err != nil {
		return "", fmt.Errorf("move raw disk: %w", err)
	}
	policy := "firmware-policy.json"
	if err := writeJSON(
		filepath.Join(bundle, policy),
		map[string]any{"schemaVersion": 1, "secureBoot": true, "tpm": "required", "generation": 2},
	); err != nil {
		return "", err
	}
	commit, err := t.output(ctx, "git", "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	tag := fmt.Sprintf("%s-%s-r%d", s.Release, r.Build, o.Revision)
	bf := pack.BundleFile{
		SchemaVersion: 1,
		Guest: spec.Guest{
			OS:        "windows",
			Arch:      s.Arch,
			OSVersion: r.OSVersion,
			OSBuild:   r.Build,
			Edition:   r.Edition,
			Variant:   "base",
		},
		Firmware: spec.Firmware{Type: "uefi", TPM: "required", SecureBoot: true},
		Resources: spec.Resources{
			CPU:    spec.MinDefault{Min: 2, Default: 4},
			Memory: spec.MinDefault{Min: 4 << 30, Default: 8 << 30},
		},
		//nolint:gosec // This hint describes first-boot provisioning, not a credential.
		Provisioning: spec.Provisioning{
			CredentialHint: "set-at-first-boot",
		},
		Build: spec.Build{
			Template:    "windows-" + s.Release + "-base",
			TemplateRef: "weaveplatform/weaveplatform-oci@" + strings.TrimSpace(string(commit)),
			Created:     time.Now().UTC().Format(time.RFC3339),
			SourceMedia: []spec.SourceMedia{
				{Kind: windowsMediaKind(s), URI: s.URI, Digest: "sha256:" + s.SHA256},
			},
		},
		Disks: []pack.BundleDisk{{Name: "disk0", Role: "system", Path: "disk0.img"}},
		State: []pack.BundleState{
			{
				Name:      "firmware-policy",
				Path:      policy,
				Semantics: spec.SemanticsRegenerate,
				Required:  true,
			},
		},
		Annotations: map[string]string{
			spec.AnnotationVersion:  tag,
			spec.AnnotationRevision: strings.TrimSpace(string(commit)),
			spec.AnnotationSource:   "https://github.com/weaveplatform/weaveplatform-oci",
		},
	}
	if err := pack.WriteBundleFile(bundle, bf); err != nil {
		return "", fmt.Errorf("write Windows bundle: %w", err)
	}
	return bundle, nil
}

func windowsSeed(out string, s WindowsSource, marker string) (string, error) {
	dir := filepath.Join(out, "seed")
	if err := newDirectory(dir); err != nil {
		return "", err
	}
	answer, script, err := windowsRecipe(s, marker)
	if err != nil {
		return "", err
	}
	for name, data := range map[string]string{"autounattend.xml": answer, "seal.ps1": script} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			return "", fmt.Errorf("write Windows seed: %w", err)
		}
	}
	iso := filepath.Join(out, "seed.iso")
	f, err := os.OpenFile(
		iso,
		os.O_CREATE|os.O_EXCL|os.O_RDWR,
		0o600,
	) //nolint:gosec // Build-owned seed output.
	if err != nil {
		return "", fmt.Errorf("create Windows seed ISO: %w", err)
	}
	// Use Media Foundry's UDF writer directly, as for Windows installation
	// media. Its ISO9660 writer retains open staging handles, which leaves
	// directory-entry sizes stale on Windows when finalizing the image.
	if err := errors.Join(udf.Write(f, dir, "WEAVE-SEED"), f.Close()); err != nil {
		return "", fmt.Errorf("build Windows seed ISO: %w", err)
	}
	return iso, nil
}

func windowsReceipt(line, marker string) (WindowsInstallResult, error) {
	var result WindowsInstallResult
	prefix := marker + " "
	if !strings.HasPrefix(line, prefix) {
		return result, fmt.Errorf("%w: Windows completion marker absent", ErrInput)
	}
	if err := json.Unmarshal(
		[]byte(strings.TrimSpace(strings.TrimPrefix(line, prefix))),
		&result,
	); err != nil {
		return result, fmt.Errorf("decode Windows receipt: %w", err)
	}
	if !result.Generalized {
		return result, fmt.Errorf("%w: Windows guest was not generalized", ErrInput)
	}
	return result, nil
}
