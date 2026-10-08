package handoff

import (
	"context"
	"fmt"
	"os"

	"oras.land/oras-go/v2/content/oci"

	"github.com/weaveplatform/weaveplatform-oci/pkg/conformance"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

// This receipt describes intended account behavior; passwords are never metadata.
type preparedResult struct {
	Parent         spec.BaseImage `json:"parent"`
	User           string         `json:"user"`
	AutomaticLogin bool           `json:"automaticLogin"`
	RemoteLogin    bool           `json:"remoteLogin"`
}

func preparedMetadata(r nativeResult, b build, f *pack.BundleFile) error {
	p := r.Prepared
	if r.FirstBoot != "desktop" || p.User != "weave" || !p.AutomaticLogin || !p.RemoteLogin ||
		p.Parent.Name == "" || p.Parent.Digest != "sha256:"+b.Data["source_sha256"] {
		return fmt.Errorf("%w: invalid prepared macOS receipt", ErrInput)
	}
	f.Guest.Variant = spec.TierPrepared
	f.Provisioning = spec.Provisioning{DefaultUser: "weave", CredentialHint: "baked"}
	f.Build.Base = &p.Parent
	f.Build.Template = "templates/macos/prepared/image.pkr.hcl"
	// The input is the parent image, not a second installation from its IPSW.
	f.Build.SourceMedia = []spec.SourceMedia{}
	return nil
}

func checkPreparedParent(ctx context.Context, o Options, f pack.BundleFile) error {
	if f.Guest.Variant != spec.TierPrepared {
		if o.ParentLayout != "" || o.ParentRef != "" || o.ParentName != "" {
			return fmt.Errorf("%w: parent options require a prepared image", ErrInput)
		}
		return nil
	}
	if o.ParentLayout == "" || o.ParentRef == "" || o.ParentName != f.Build.Base.Name {
		return fmt.Errorf(
			"%w: explicit parent layout, reference and matching repository required",
			ErrInput,
		)
	}
	info, err := os.Stat(o.ParentLayout)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%w: parent layout must be an existing directory", ErrInput)
	}
	store, err := oci.New(o.ParentLayout)
	if err != nil {
		return fmt.Errorf("open prepared parent: %w", err)
	}
	root, err := store.Resolve(ctx, o.ParentRef)
	if err != nil {
		return fmt.Errorf("resolve prepared parent: %w", err)
	}
	report, err := conformance.Check(ctx, store, root, conformance.Options{Deep: true})
	if err != nil || !report.OK() || root.MediaType != spec.MediaTypeIndex {
		return fmt.Errorf("%w: invalid prepared parent: %v; %v", ErrInput, err, report.Problems())
	}
	for _, child := range report.Children {
		if child.Descriptor.Digest.String() != f.Build.Base.Digest {
			continue
		}
		cfg := child.Description.Config
		if cfg.Guest.Variant != spec.TierBase || cfg.Provisioning.Agent != nil ||
			cfg.Provisioning.DefaultUser != "" || cfg.Provisioning.CredentialHint != "set-at-first-boot" ||
			cfg.Guest.OS != f.Guest.OS || cfg.Guest.Arch != f.Guest.Arch ||
			cfg.Guest.OSVersion != f.Guest.OSVersion || cfg.Guest.OSBuild != f.Guest.OSBuild ||
			cfg.Firmware != f.Firmware || cfg.Resources != f.Resources {
			return fmt.Errorf("%w: prepared guest does not match its restored base", ErrInput)
		}
		return nil
	}
	return fmt.Errorf("%w: parent platform digest is not in the supplied index", ErrInput)
}
