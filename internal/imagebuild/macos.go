package imagebuild

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

// MacOptions selects an Apple restore candidate.
type MacOptions struct {
	Version, SourceLock string
	DiskSize            int64
	Revision            int
	Resume              bool
}

func (t Tools) macWorkspace(ctx context.Context) (string, error) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return "", fmt.Errorf("%w: macOS guests need an Apple silicon host", ErrInput)
	}
	work := os.Getenv("WEAVE_IMAGE_WORKSPACE")
	if work == "" || !filepath.IsAbs(work) {
		return "", fmt.Errorf("%w: use workspace.sh run to select mounted image storage", ErrInput)
	}
	mounts, err := t.output(ctx, "mount")
	if err != nil {
		return "", err
	}
	if !strings.Contains(string(mounts), " on "+work+" (apfs,") {
		return "", fmt.Errorf("%w: APFS image workspace is not mounted", ErrInput)
	}
	space, err := t.output(ctx, "df", "-Pk", work)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(string(space)), "\n")
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 4 {
		return "", fmt.Errorf("%w: cannot determine free space", ErrInput)
	}
	free, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return "", fmt.Errorf("decode free space: %w", err)
	}
	if free < 80*1024*1024 {
		return "", fmt.Errorf("%w: macOS restore needs 80 GiB free", ErrInput)
	}
	return work, nil
}

func macCandidate(out string, s appleSource, raw []byte, resume bool) error {
	entries, err := os.ReadDir(out)
	if err == nil {
		if !resume || len(entries) != 1 || entries[0].Name() != "source-lock.json" {
			return fmt.Errorf("%w: resume only an interrupted download candidate", ErrInput)
		}
		var previous appleSource
		if err := readJSON(filepath.Join(out, "source-lock.json"), &previous); err != nil {
			return err
		}
		if previous != s {
			return fmt.Errorf("%w: resume source differs from candidate lock", ErrInput)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("read candidate: %w", err)
	}
	if err := newDirectory(out); err != nil {
		return err
	}
	//nolint:gosec // Candidate output uses a validated tag.
	if err := os.WriteFile(
		filepath.Join(out, "source-lock.json"),
		raw,
		0o600,
	); err != nil { //nolint:gosec // Candidate path uses a validated tag.
		return fmt.Errorf("write source lock: %w", err)
	}
	return nil
}

func (t Tools) verifyIPSW(ctx context.Context, file, work string, s appleSource) error {
	z, err := zip.OpenReader(file)
	if err != nil {
		return fmt.Errorf("open IPSW: %w", err)
	}
	defer z.Close()
	manifest, err := z.Open("BuildManifest.plist")
	if err != nil {
		return fmt.Errorf("open IPSW manifest: %w", err)
	}
	defer manifest.Close()
	// A corrupt archive must not make the metadata extraction consume arbitrary disk space.
	raw, err := io.ReadAll(io.LimitReader(manifest, 64*1024*1024+1))
	if err != nil {
		return fmt.Errorf("read IPSW manifest: %w", err)
	}
	if len(raw) > 64*1024*1024 {
		return fmt.Errorf("%w: IPSW manifest too large", ErrInput)
	}
	tmp, err := os.CreateTemp(work, "manifest-*.plist")
	if err != nil {
		return fmt.Errorf("create manifest file: %w", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := tmp.Write(raw); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	for key, want := range map[string]string{"ProductVersion": s.Version, "ProductBuildVersion": s.Build} {
		got, err := t.output(ctx, "plutil", "-extract", key, "raw", "-o", "-", tmp.Name())
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(got)) != want {
			return fmt.Errorf("%w: IPSW %s differs from source lock", ErrInput, key)
		}
	}
	return nil
}

// BuildMacOS restores verified Apple media and creates an agent-free raw bundle.
func (p Packages) BuildMacOS(ctx context.Context, o MacOptions) (string, error) {
	if o.DiskSize == 0 {
		o.DiskSize = 80 << 30
	}
	if o.DiskSize < 64<<30 || o.DiskSize%512 != 0 {
		return "", fmt.Errorf(
			"%w: macOS disk must be at least 64 GiB and a multiple of 512 bytes",
			ErrInput,
		)
	}
	work, err := p.Tools.macWorkspace(ctx)
	if err != nil {
		return "", err
	}
	p.Tools.progress("macOS: resolving Apple restore source for %s", o.Version)
	source, raw, err := p.appleSource(ctx, o)
	if err != nil {
		return "", err
	}
	tag, err := source.validate(o.Version, o.Revision)
	if err != nil {
		return "", err
	}
	out := filepath.Join(work, "builds", "macos-"+strings.Split(o.Version, ".")[0]+"-base", tag)
	if err := macCandidate(out, source, raw, o.Resume); err != nil {
		return "", err
	}
	u, err := url.Parse(source.URL)
	if err != nil {
		return "", fmt.Errorf("parse source URL: %w", err)
	}
	p.Tools.progress(
		"macOS %s (%s): acquiring verified IPSW; candidate=%s",
		source.Version,
		source.Build,
		out,
	)
	ipsw, err := p.Downloader.Download(
		ctx,
		Media{URI: source.URL, Size: source.Size, SHA256: source.SHA256},
		filepath.Join(work, "media", "ipsw", source.SHA256, filepath.Base(u.Path)),
	)
	if err != nil {
		return "", err
	}
	p.Tools.progress("macOS: checking IPSW version and build manifest")
	if err := p.Tools.verifyIPSW(ctx, ipsw, work, source); err != nil {
		return "", err
	}
	if err := writeJSON(
		filepath.Join(out, "source.json"),
		map[string]any{
			"kind":         "ipsw",
			"uri":          source.URL,
			"digest":       "sha256:" + source.SHA256,
			"verification": "apple-restore",
			"size":         source.Size,
		},
	); err != nil {
		return "", err
	}
	bundle := filepath.Join(out, "bundle")
	if err := newDirectory(bundle); err != nil {
		return "", err
	}
	restore := p.MacRestore
	if restore == nil {
		restore = restoreMacOSNative
	}
	result, err := restore(
		ctx,
		MacRestoreRequest{IPSW: ipsw, Directory: bundle, DiskSize: o.DiskSize, Log: p.Tools.Log},
	)
	if err != nil {
		return "", err
	}
	p.Tools.progress("macOS: restore completed; writing OCI bundle metadata")
	path, err := p.Tools.macBundle(
		ctx,
		bundle,
		result,
		source,
		strings.Split(o.Version, ".")[0],
		tag,
	)
	if err == nil {
		p.Tools.progress("macOS: candidate ready at %s", path)
	}
	return path, err
}

func (t Tools) macBundle(
	ctx context.Context,
	bundle string,
	c MacRestoreResult,
	s appleSource,
	major, tag string,
) (string, error) {
	commit, err := t.output(ctx, "git", "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	f := pack.BundleFile{
		SchemaVersion: 1,
		Guest: spec.Guest{
			OS:        "darwin",
			Arch:      "arm64",
			OSVersion: s.Version,
			OSBuild:   s.Build,
			Variant:   "base",
		},
		Firmware: spec.Firmware{
			Type:          "apple",
			TPM:           "none",
			HardwareModel: c.HardwareModel,
		},
		Resources: spec.Resources{
			CPU:    spec.MinDefault{Min: c.CPUCountMin, Default: c.CPUCount},
			Memory: spec.MinDefault{Min: c.MemorySizeMin, Default: c.MemorySize},
		},
		Provisioning: spec.Provisioning{ //nolint:gosec // Describes first-boot setup; contains no credentials.
			CredentialHint: "set-at-first-boot",
		},
		Build: spec.Build{
			Template:    "macos-" + major + "-base",
			TemplateRef: "weaveplatform/weaveplatform-oci@" + strings.TrimSpace(string(commit)),
			Created:     time.Now().UTC().Format(time.RFC3339),
			SourceMedia: []spec.SourceMedia{
				{Kind: "ipsw", URI: s.URL, Digest: "sha256:" + s.SHA256},
			},
		},
		Disks: []pack.BundleDisk{
			{Name: "disk0", Role: "system", Path: "disk0.img"},
		},
		State: []pack.BundleState{
			{
				Name:      "auxstorage",
				Path:      "auxstorage.bin",
				Semantics: spec.SemanticsCarry,
				Required:  true,
			},
		},
		Annotations: map[string]string{
			spec.AnnotationVersion:  tag,
			spec.AnnotationRevision: strings.TrimSpace(string(commit)),
			spec.AnnotationSource:   "https://github.com/weaveplatform/weaveplatform-oci",
		},
	}
	if err := pack.WriteBundleFile(bundle, f); err != nil {
		return "", fmt.Errorf("write macOS bundle: %w", err)
	}
	return bundle, nil
}

func (p Packages) appleSource(ctx context.Context, o MacOptions) (AppleSource, []byte, error) {
	var s AppleSource
	if o.SourceLock != "" {
		raw, err := os.ReadFile(o.SourceLock)
		if err != nil {
			return s, nil, fmt.Errorf("read Apple source lock: %w", err)
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			return s, nil, fmt.Errorf("decode Apple source lock: %w", err)
		}
		return s, raw, nil
	}
	sources, err := MacSources(ctx, p.Downloader.Client, o.Version)
	if err != nil {
		return s, nil, err
	}
	raw, err := json.MarshalIndent(sources[0], "", "  ")
	if err != nil {
		return s, nil, fmt.Errorf("encode Apple source lock: %w", err)
	}
	return sources[0], raw, nil
}
