package imagebuild

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/oci"

	"github.com/weaveplatform/weaveplatform-oci/pkg/chunk"
	"github.com/weaveplatform/weaveplatform-oci/pkg/conformance"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

// Acceptance records validation of the actual packed and unpacked bytes.
type Acceptance struct {
	SchemaVersion int                     `json:"schemaVersion"`
	IndexDigest   string                  `json:"indexDigest"`
	Tag           string                  `json:"tag"`
	Passed        bool                    `json:"passed"`
	Platforms     map[string][]BootResult `json:"platforms"`
}

// ValidateOptions selects the bundles and required architectures for a candidate.
type ValidateOptions struct {
	Bundles []string
	Out     string
	Arches  []string
	Timeout time.Duration
}

func validationBundles(o ValidateOptions) ([]pack.Bundle, string, error) {
	if len(o.Bundles) == 0 || len(o.Arches) == 0 {
		return nil, "", fmt.Errorf("%w: bundles and architectures required", ErrInput)
	}
	seen := map[string]bool{}
	var bundles []pack.Bundle
	tag, version, build := "", "", ""
	for _, dir := range o.Bundles {
		b, err := pack.LoadBundle(dir)
		if err != nil {
			return nil, "", fmt.Errorf("load bundle: %w", err)
		}
		g := b.File.Guest
		t := b.File.Annotations[spec.AnnotationVersion]
		if len(bundles) == 0 {
			tag, version, build = t, g.OSVersion, g.OSBuild
		}
		if g.OS != "linux" || g.Variant != "base" || b.File.Provisioning.Agent != nil ||
			seen[g.Arch] ||
			!slices.Contains(o.Arches, g.Arch) ||
			t == "" ||
			t != tag ||
			g.OSVersion != version ||
			g.OSBuild != build {
			return nil, "", fmt.Errorf(
				"%w: bundles must cover requested platforms once with matching base tier, tag and OS version/build",
				ErrInput,
			)
		}
		seen[g.Arch] = true
		bundles = append(bundles, b)
	}
	if len(seen) != len(o.Arches) {
		return nil, "", fmt.Errorf("%w: missing or duplicate architectures", ErrInput)
	}
	return bundles, tag, nil
}

// ValidateLinux packs, deeply verifies and boots two fresh clones per architecture.
func (t Tools) ValidateLinux(
	ctx context.Context,
	o ValidateOptions,
) (result Acceptance, err error) {
	bundles, tag, err := validationBundles(o)
	if err != nil {
		return result, err
	}
	if err := newDirectory(o.Out); err != nil {
		return result, err
	}
	store, err := pack.OpenLayout(ctx, filepath.Join(o.Out, "layout"))
	if err != nil {
		return result, fmt.Errorf("create layout: %w", err)
	}
	var children []ocispec.Descriptor
	var configs []pack.BundleFile
	for _, b := range bundles {
		desc, err := pack.Manifest(ctx, b, store, chunk.Options{TempDir: o.Out})
		if err != nil {
			return result, fmt.Errorf("pack candidate: %w", err)
		}
		children = append(children, desc)
		configs = append(configs, b.File)
	}
	root, err := pack.Index(ctx, store, children, nil)
	if err != nil {
		return result, fmt.Errorf("pack index: %w", err)
	}
	if err := store.Tag(ctx, root, tag); err != nil {
		return result, fmt.Errorf("tag index: %w", err)
	}
	report, err := deepCheck(ctx, store, root)
	if err != nil {
		return result, err
	}
	if err := writeJSON(filepath.Join(o.Out, "inspection.json"), report); err != nil {
		return result, err
	}
	if err := writeJSON(filepath.Join(o.Out, "bundles.json"), configs); err != nil {
		return result, err
	}
	result = Acceptance{
		SchemaVersion: 1,
		IndexDigest:   root.Digest.String(),
		Tag:           tag,
		Platforms:     map[string][]BootResult{},
	}
	defer func() { err = errors.Join(err, writeJSON(filepath.Join(o.Out, "acceptance.json"), result)) }()
	for i, b := range bundles {
		clones, err := t.validateClones(ctx, store, children[i], b.File.Guest.Arch, o)
		result.Platforms["linux/"+b.File.Guest.Arch] = clones
		if err != nil {
			return result, err
		}
	}
	result.Passed = true
	return result, nil
}

func (t Tools) validateClones(
	ctx context.Context,
	store *oci.Store,
	desc ocispec.Descriptor,
	arch string,
	o ValidateOptions,
) ([]BootResult, error) {
	tmp, err := os.MkdirTemp(o.Out, "unpack-"+arch+"-")
	if err != nil {
		return nil, fmt.Errorf("create unpack directory: %w", err)
	}
	defer os.RemoveAll(tmp)
	bundle := filepath.Join(tmp, "bundle")
	if _, err := pack.Unpack(ctx, store, desc, bundle, chunk.AssembleOptions{}); err != nil {
		return nil, fmt.Errorf("unpack candidate: %w", err)
	}
	var clones []BootResult
	for n := 1; n <= 2; n++ {
		result, err := t.BootLinux(
			ctx,
			BootOptions{
				Bundle:  bundle,
				Timeout: o.Timeout,
				Report:  filepath.Join(o.Out, "reports", fmt.Sprintf("%s-%d", arch, n)),
			},
		)
		clones = append(clones, result)
		if err != nil {
			return clones, err
		}
	}
	if clones[0].MachineID == clones[1].MachineID {
		return clones, fmt.Errorf("%w: fresh %s clones reused a machine-id", ErrInput, arch)
	}
	return clones, nil
}

func deepCheck(
	ctx context.Context,
	store *oci.Store,
	root ocispec.Descriptor,
) (conformance.Report, error) {
	report, err := conformance.Check(ctx, store, root, conformance.Options{Deep: true})
	if err != nil {
		return report, fmt.Errorf("inspect layout: %w", err)
	}
	if !report.OK() {
		return report, fmt.Errorf("%w: %s", ErrInput, strings.Join(report.Problems(), "; "))
	}
	return report, nil
}

func openCandidate(ctx context.Context, dir string) (*oci.Store, conformance.Report, error) {
	// OpenLayout can create a store; a parent input must already be a layout.
	if _, err := os.Stat(filepath.Join(dir, "oci-layout")); err != nil {
		return nil, conformance.Report{}, fmt.Errorf("read base layout: %w", err)
	}
	store, err := pack.OpenLayout(ctx, dir)
	if err != nil {
		return nil, conformance.Report{}, fmt.Errorf("open layout: %w", err)
	}
	var tags []string
	if err := store.Tags(
		ctx,
		"",
		func(page []string) error { tags = append(tags, page...); return nil },
	); err != nil {
		return nil, conformance.Report{}, fmt.Errorf("list layout tags: %w", err)
	}
	if len(tags) != 1 {
		return nil, conformance.Report{}, fmt.Errorf(
			"%w: base layout must have exactly one tag",
			ErrInput,
		)
	}
	root, err := store.Resolve(ctx, tags[0])
	if err != nil {
		return nil, conformance.Report{}, fmt.Errorf("resolve base: %w", err)
	}
	report, err := deepCheck(ctx, store, root)
	return store, report, err
}

// AgentOptions identifies the verified parent and new Linux agent candidate.
type AgentOptions struct {
	Base, BaseName, Arch, Cache, Out string
	Revision                         int
	Timeout                          time.Duration
	Lock                             Lock
}

// BuildLinuxAgent creates a sealed candidate; module/channel acceptance remains a separate gate.
func (p Packages) BuildLinuxAgent(ctx context.Context, o AgentOptions) (string, error) {
	entry, err := o.Lock.RequirePackages("linux/" + o.Arch)
	if err != nil {
		return "", err
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9./_-]*$`).MatchString(o.BaseName) || o.Revision < 1 {
		return "", fmt.Errorf("%w: valid base repository and positive revision required", ErrInput)
	}
	store, report, err := openCandidate(ctx, o.Base)
	if err != nil {
		return "", err
	}
	var selected []ocispec.Descriptor
	for _, child := range report.Children {
		g := child.Description.Config.Guest
		if g.OS == "linux" && g.Arch == o.Arch {
			selected = append(selected, child.Descriptor)
		}
	}
	if len(selected) != 1 {
		return "", fmt.Errorf("%w: base needs exactly one matching platform", ErrInput)
	}
	if err := newDirectory(o.Out); err != nil {
		return "", err
	}
	parent := filepath.Join(o.Out, "parent")
	if _, err := pack.Unpack(ctx, store, selected[0], parent, chunk.AssembleOptions{}); err != nil {
		return "", fmt.Errorf("unpack base: %w", err)
	}
	b, err := pack.LoadBundle(parent)
	if err != nil {
		return "", fmt.Errorf("load parent: %w", err)
	}
	if b.File.Guest.Variant != "base" || b.File.Provisioning.Agent != nil ||
		len(b.File.State) > 0 ||
		len(b.File.Disks) != 1 {
		return "", fmt.Errorf(
			"%w: parent must be a base with one disk and fresh UEFI state",
			ErrInput,
		)
	}
	sources, err := p.PrepareLinux(ctx, o.Lock, o.Arch, o.Cache, filepath.Join(o.Out, "packages"))
	if err != nil {
		return "", err
	}
	recipe, err := LinuxRecipe(o.Lock, o.Arch)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(
		filepath.Join(o.Out, "provision.sh"),
		[]byte(recipe),
		0o600,
	); err != nil {
		return "", fmt.Errorf("write recipe: %w", err)
	}
	bundle := filepath.Join(o.Out, "bundle")
	if err := newDirectory(bundle); err != nil {
		return "", err
	}
	if _, err := p.Tools.BootLinux(
		ctx,
		BootOptions{
			Bundle:     parent,
			Timeout:    o.Timeout,
			Report:     filepath.Join(o.Out, "provision-report"),
			Script:     recipe,
			OutputDisk: filepath.Join(bundle, "disk0.img"),
			Payload:    filepath.Join(o.Out, "packages"),
		},
	); err != nil {
		return "", err
	}
	commit, err := p.Tools.output(ctx, "git", "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	child := b.File
	child.Guest.Variant = "agent"
	child.Provisioning = spec.Provisioning{
		CredentialHint: "cloud-init",
		Agent:          &spec.Agent{Name: "weave-agent", Version: o.Lock.CoreVersion},
	}
	child.Disks = []pack.BundleDisk{{Name: "disk0", Role: "system", Path: "disk0.img"}}
	child.Build.Base = &spec.BaseImage{Name: o.BaseName, Digest: selected[0].Digest.String()}
	child.Build.Template = "linux-agent"
	child.Build.TemplateRef = "weaveplatform/weaveplatform-oci@" + strings.TrimSpace(string(commit))
	child.Build.Created = time.Now().UTC().Format(time.RFC3339)
	for _, s := range sources {
		child.Build.SourceMedia = append(
			child.Build.SourceMedia,
			spec.SourceMedia{Kind: "package", URI: s.URI, Digest: s.Digest},
		)
	}
	child.Annotations[spec.AnnotationVersion] = fmt.Sprintf(
		"%s-%s-agent%s-r%d",
		child.Guest.OSVersion,
		child.Guest.OSBuild,
		o.Lock.CoreVersion,
		o.Revision,
	)
	child.Annotations[spec.AnnotationRevision] = strings.TrimSpace(string(commit))
	if err := pack.WriteBundleFile(bundle, child); err != nil {
		return "", fmt.Errorf("write agent bundle: %w", err)
	}
	err = writeJSON(
		filepath.Join(o.Out, "inventory.json"),
		map[string]any{
			"coreVersion": o.Lock.CoreVersion,
			"modules":     entry.Modules,
			"acceptance":  "pending: module readiness and host channel tests",
		},
	)
	return bundle, err
}
