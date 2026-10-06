package imagebuild

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	coreRepository    = "weaveplatform/weaveplatform-agent-core"
	modulesRepository = "weaveplatform/weaveplatform-agent-modules"
)

var (
	shaPattern       = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	versionPattern   = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)$`)
	namePattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	imageNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)
)

// Catalogue describes the requested image matrix.
type Catalogue struct {
	SchemaVersion int          `json:"schemaVersion"`
	Registry      string       `json:"registry"`
	Capabilities  []string     `json:"capabilities"`
	Images        []Definition `json:"images"`
}

// Definition is one OS family/version and its architectures and tiers.
type Definition struct {
	Name          string   `json:"name"`
	OS            string   `json:"os"`
	OSVersion     string   `json:"osVersion"`
	Architectures []string `json:"architectures"`
	Builder       string   `json:"builder"`
	Tiers         []string `json:"tiers"`
	Visibility    string   `json:"visibility"`
	Provisioning  string   `json:"provisioning"`
	Edition       string   `json:"edition,omitempty"`
	Language      string   `json:"language,omitempty"`
	Distro        string   `json:"distro,omitempty"`
}

// MatrixEntry is one platform/tier build.
type MatrixEntry struct {
	Definition
	Tier       string `json:"tier"`
	Arch       string `json:"arch"`
	Repository string `json:"repository"`
}

// LoadCatalogue reads and validates the build matrix.
func LoadCatalogue(file string) (Catalogue, error) {
	var c Catalogue
	if err := readJSON(file, &c); err != nil {
		return c, err
	}
	if c.SchemaVersion != 1 || len(c.Images) == 0 || len(c.Capabilities) == 0 {
		return c, fmt.Errorf("%w: invalid catalogue", ErrInput)
	}
	seen := map[string]bool{}
	for _, capability := range c.Capabilities {
		if !namePattern.MatchString(capability) || seen[capability] {
			return c, fmt.Errorf("%w: invalid capability", ErrInput)
		}
		seen[capability] = true
	}
	for _, d := range c.Images {
		if !imageNamePattern.MatchString(d.Name) ||
			!slices.Contains([]string{"darwin", "linux", "windows"}, d.OS) ||
			len(d.Architectures) == 0 ||
			len(d.Tiers) == 0 {
			return c, fmt.Errorf("%w: invalid image definition %q", ErrInput, d.Name)
		}
	}
	return c, nil
}

// Matrix expands all requested platforms and tiers.
func (c Catalogue) Matrix() []MatrixEntry {
	var entries []MatrixEntry
	for _, d := range c.Images {
		for _, tier := range d.Tiers {
			for _, arch := range d.Architectures {
				entries = append(
					entries,
					MatrixEntry{
						Definition: d,
						Tier:       tier,
						Arch:       arch,
						Repository: d.Name + "-" + tier,
					},
				)
			}
		}
	}
	return entries
}

// Asset pins one release artifact by its immutable bytes.
type Asset struct {
	Name   string `json:"name"`
	URI    string `json:"uri"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// ModuleInput pins the binary, manifest and optional native installer.
type ModuleInput struct {
	ID              string  `json:"id"`
	Version         string  `json:"version"`
	Binary          Asset   `json:"binary"`
	Manifest        Asset   `json:"manifest"`
	Package         *Asset  `json:"package"`
	PackageEvidence []Asset `json:"packageEvidence,omitempty"`
}

// PlatformInputs is a complete core and module set for one platform.
type PlatformInputs struct {
	Core         Asset         `json:"core"`
	CoreEvidence []Asset       `json:"coreEvidence"`
	Modules      []ModuleInput `json:"modules"`
}

// Lock records the resolved release inputs for every requested platform.
type Lock struct {
	SchemaVersion int                       `json:"schemaVersion"`
	ResolvedAt    string                    `json:"resolvedAt"`
	CoreVersion   string                    `json:"coreVersion"`
	Platforms     map[string]PlatformInputs `json:"platforms"`
}

// LoadLock verifies the lock's matrix and artifact metadata.
func LoadLock(file string, c Catalogue) (Lock, error) {
	var l Lock
	if err := readJSON(file, &l); err != nil {
		return l, err
	}
	return l, l.Validate(c)
}

// Validate rejects incomplete module sets and unsafe or unpinned artifacts.
func (l Lock) Validate(c Catalogue) error {
	if l.SchemaVersion != 1 || !versionPattern.MatchString(l.CoreVersion) {
		return fmt.Errorf("%w: invalid lock version", ErrInput)
	}
	expected := map[string]bool{}
	for _, m := range c.Matrix() {
		expected[m.OS+"/"+m.Arch] = true
	}
	if len(expected) != len(l.Platforms) {
		return fmt.Errorf("%w: lock must cover every platform", ErrInput)
	}
	for platform, entry := range l.Platforms {
		if !expected[platform] {
			return fmt.Errorf("%w: unexpected platform %s", ErrInput, platform)
		}
		if err := validatePlatform(platform, entry, c.Capabilities); err != nil {
			return err
		}
	}
	return nil
}

func validatePlatform(platform string, entry PlatformInputs, capabilities []string) error {
	osName, _, _ := strings.Cut(platform, "/")
	if osName == "darwin" {
		osName = "macos"
	}
	expected := map[string]bool{}
	for _, capability := range capabilities {
		expected["weave-"+osName+"-"+capability] = true
	}
	if len(entry.Modules) != len(expected) {
		return fmt.Errorf("%w: %s incomplete module set", ErrInput, platform)
	}
	assets := append([]Asset{entry.Core}, entry.CoreEvidence...)
	for _, m := range entry.Modules {
		if !expected[m.ID] || !versionPattern.MatchString(m.Version) {
			return fmt.Errorf("%w: invalid or duplicated module %s", ErrInput, m.ID)
		}
		delete(expected, m.ID)
		assets = append(assets, m.Binary, m.Manifest)
		assets = append(assets, m.PackageEvidence...)
		if m.Package != nil {
			assets = append(assets, *m.Package)
		}
	}
	for _, a := range assets {
		if err := a.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Validate restricts packages to pinned release artifacts in the two source repositories.
func (a Asset) Validate() error {
	u, err := url.Parse(a.URI)
	if err != nil {
		return fmt.Errorf("%w: invalid artifact URL: %w", ErrInput, err)
	}
	if !shaPattern.MatchString(a.Digest) || a.Size <= 0 || a.Name == "." || a.Name == ".." ||
		path.Base(a.Name) != a.Name ||
		strings.ContainsAny(a.Name, `\`+"\r\n") {
		return fmt.Errorf("%w: unsafe or unpinned artifact %q", ErrInput, a.Name)
	}
	allowed := strings.HasPrefix(u.Path, "/"+coreRepository+"/releases/download/") ||
		strings.HasPrefix(u.Path, "/"+modulesRepository+"/releases/download/")
	if u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" ||
		u.Fragment != "" ||
		!allowed ||
		path.Base(u.Path) != a.Name {
		return fmt.Errorf("%w: artifact is not a weaveplatform release asset", ErrInput)
	}
	return nil
}

// RequirePackages refuses a platform before any downloads if installers are absent.
func (l Lock) RequirePackages(platform string) (PlatformInputs, error) {
	entry, ok := l.Platforms[platform]
	if !ok {
		return entry, fmt.Errorf("%w: unknown platform %s", ErrInput, platform)
	}
	for _, m := range entry.Modules {
		if m.Package == nil {
			return entry, fmt.Errorf("%w: released installer missing for %s", ErrInput, m.ID)
		}
		if strings.HasPrefix(platform, "linux/") {
			names := map[string]bool{}
			for _, a := range m.PackageEvidence {
				names[a.Name] = true
			}
			if len(names) != 2 || !names["checksums.txt"] || !names["checksums.txt.sigstore.json"] {
				return entry, fmt.Errorf("%w: signed checksums missing for %s", ErrInput, m.ID)
			}
		}
	}
	return entry, nil
}

type releaseAsset struct {
	Name   string `json:"name"`
	URI    string `json:"browser_download_url"` //nolint:tagliatelle // GitHub API
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

type release struct {
	Tag        string         `json:"tag_name"` //nolint:tagliatelle // GitHub API
	Draft      bool           `json:"draft"`
	Prerelease bool           `json:"prerelease"`
	Assets     []releaseAsset `json:"assets"`
}

func (r release) asset(name string) (Asset, error) {
	for _, a := range r.Assets {
		if a.Name == name {
			result := Asset(a)
			return result, result.Validate()
		}
	}
	return Asset{}, fmt.Errorf("%w: %s is missing %s", ErrInput, r.Tag, name)
}

func latest(releases []release, prefix string) (release, error) {
	var result release
	best := [3]int64{-1, -1, -1}
	for _, r := range releases {
		if r.Draft || r.Prerelease || !strings.HasPrefix(r.Tag, prefix) {
			continue
		}
		match := versionPattern.FindStringSubmatch(strings.TrimPrefix(r.Tag, prefix))
		if match == nil {
			continue
		}
		var v [3]int64
		for i := range v {
			n, err := strconv.ParseInt(match[i+1], 10, 64)
			if err != nil {
				return release{}, fmt.Errorf("release version: %w", err)
			}
			v[i] = n
		}
		if slices.Compare(v[:], best[:]) > 0 {
			result, best = r, v
		}
	}
	if result.Tag == "" {
		return result, fmt.Errorf("%w: no stable release for %s", ErrInput, prefix)
	}
	return result, nil
}

func (t Tools) releases(ctx context.Context, repo string) ([]release, error) {
	raw, err := t.output(
		ctx,
		"gh",
		"api",
		"--paginate",
		"--slurp",
		"repos/"+repo+"/releases?per_page=100",
	)
	if err != nil {
		return nil, err
	}
	var pages [][]release
	if err := json.Unmarshal(raw, &pages); err != nil {
		return nil, fmt.Errorf("decode releases: %w", err)
	}
	var result []release
	for _, page := range pages {
		result = append(result, page...)
	}
	return result, nil
}

// ResolveLock selects stable releases once and pins every returned asset.
func ResolveLock(ctx context.Context, c Catalogue, t Tools) (Lock, error) {
	coreReleases, err := t.releases(ctx, coreRepository)
	if err != nil {
		return Lock{}, err
	}
	core, err := latest(coreReleases, "")
	if err != nil {
		return Lock{}, err
	}
	modules, err := t.releases(ctx, modulesRepository)
	if err != nil {
		return Lock{}, err
	}
	l := Lock{
		SchemaVersion: 1,
		ResolvedAt:    time.Now().UTC().Format(time.RFC3339),
		CoreVersion:   strings.TrimPrefix(core.Tag, "v"),
		Platforms:     map[string]PlatformInputs{},
	}
	for _, row := range c.Matrix() {
		platform := row.OS + "/" + row.Arch
		if _, ok := l.Platforms[platform]; ok {
			continue
		}
		entry, err := resolvePlatform(
			core,
			modules,
			c.Capabilities,
			row.OS,
			row.Arch,
			l.CoreVersion,
		)
		if err != nil {
			return Lock{}, err
		}
		l.Platforms[platform] = entry
	}
	return l, l.Validate(c)
}

func resolvePlatform(
	core release,
	releases []release,
	capabilities []string,
	osName, arch, version string,
) (PlatformInputs, error) {
	name := "weaveplatform-agent_" + version + "_windows_" + arch + ".zip"
	evidence := "weaveplatform-agent_" + version + "_checksums.txt"
	if osName == "linux" {
		name = "weave-agent_" + version + "_" + arch + ".deb"
	}
	if osName == "darwin" {
		name = "weave-agent_" + version + "_darwin_" + arch + ".pkg"
		evidence = name
	}
	entry := PlatformInputs{}
	var err error
	entry.Core, err = core.asset(name)
	if err != nil {
		return entry, err
	}
	evidenceNames := []string{evidence + ".sigstore.json"}
	if osName != "darwin" {
		evidenceNames = append(evidenceNames, evidence)
	}
	for _, n := range evidenceNames {
		a, err := core.asset(n)
		if err != nil {
			return entry, err
		}
		entry.CoreEvidence = append(entry.CoreEvidence, a)
	}
	for _, capability := range capabilities {
		m, err := resolveModule(releases, capability, osName, arch)
		if err != nil {
			return entry, err
		}
		entry.Modules = append(entry.Modules, m)
	}
	return entry, nil
}

func resolveModule(releases []release, capability, osName, arch string) (ModuleInput, error) {
	moduleOS := osName
	if osName == "darwin" {
		moduleOS = "macos"
	}
	id := "weave-" + moduleOS + "-" + capability
	r, err := latest(releases, "modules/"+id+"/")
	if err != nil {
		return ModuleInput{}, err
	}
	_, version, _ := strings.Cut(r.Tag, "/v")
	m := ModuleInput{ID: id, Version: version}
	name := id + "-" + osName + "-" + arch
	if osName == "windows" {
		name += ".exe"
	}
	m.Binary, err = r.asset(name)
	if err != nil {
		return m, err
	}
	m.Manifest, err = r.asset("module.manifest.json")
	if err != nil {
		return m, err
	}
	packageName := id + "_" + version + "_" + osName + "_" + arch + ".zip"
	if osName == "linux" {
		packageName = id + "_" + version + "_" + arch + ".deb"
	}
	if osName == "darwin" {
		packageName = id + "_" + version + "_darwin_" + arch + ".pkg"
	}
	for _, a := range r.Assets {
		if a.Name == packageName {
			p, err := r.asset(packageName)
			if err != nil {
				return m, err
			}
			m.Package = &p
		}
		if a.Name == "checksums.txt" || a.Name == "checksums.txt.sigstore.json" {
			e, err := r.asset(a.Name)
			if err != nil {
				return m, err
			}
			m.PackageEvidence = append(m.PackageEvidence, e)
		}
	}
	return m, nil
}

// WriteLock creates a new dependency lock without replacing an earlier candidate.
func WriteLock(file string, l Lock) error {
	return writeNewJSON(file, l)
}
