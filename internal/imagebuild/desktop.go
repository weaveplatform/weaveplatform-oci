package imagebuild

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/weaveplatform/weaveplatform-oci/pkg/chunk"
	"github.com/weaveplatform/weaveplatform-oci/pkg/conformance"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

// DesktopLock pins the complete set of packages downloaded on top of an exact
// agent parent. APT authenticates Canonical's snapshot metadata; this lock then
// checks every archive before installation. An extra dependency fails closed.
type DesktopLock struct {
	SchemaVersion int              `json:"schemaVersion"`
	OSVersion     string           `json:"osVersion"`
	Arch          string           `json:"arch"`
	ParentDigest  string           `json:"parentDigest"`
	Snapshot      string           `json:"snapshot"`
	Packages      []DesktopPackage `json:"packages"`
}

type DesktopPackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Arch    string `json:"arch"`
	SHA256  string `json:"sha256"`
}

var (
	debVersion = regexp.MustCompile(`^[0-9][0-9A-Za-z.+:~_-]*$`)
	debName    = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)
)

func LoadDesktopLock(path string) (DesktopLock, error) {
	var l DesktopLock
	if err := readJSON(path, &l); err != nil {
		return l, err
	}
	return l, l.Validate()
}

func (l DesktopLock) Validate() error {
	if l.SchemaVersion != 1 || (l.OSVersion != "20.04" && l.OSVersion != "26.04") ||
		(l.Arch != "amd64" && l.Arch != "arm64") ||
		!shaPattern.MatchString(l.ParentDigest) {
		return fmt.Errorf(
			"%w: desktop lock needs a supported Ubuntu platform and exact agent parent",
			ErrInput,
		)
	}
	if _, err := time.Parse("20060102T150405Z", l.Snapshot); err != nil {
		return fmt.Errorf("%w: invalid Ubuntu snapshot ID", ErrInput)
	}
	seen := map[string]bool{}
	for _, p := range l.Packages {
		if !debName.MatchString(p.Name) || !debVersion.MatchString(p.Version) ||
			(p.Arch != l.Arch && p.Arch != "all") ||
			!shaPattern.MatchString("sha256:"+p.SHA256) ||
			seen[p.Name] {
			return fmt.Errorf("%w: invalid, duplicate or unpinned desktop package", ErrInput)
		}
		seen[p.Name] = true
	}
	for _, name := range []string{"xfce4", "lightdm", "xserver-xorg", "x11-xserver-utils", "dbus-x11"} {
		if !seen[name] {
			return fmt.Errorf("%w: desktop lock missing %s", ErrInput, name)
		}
	}
	return nil
}

// DesktopRecipe uses only a dated Canonical snapshot and the archive keyring
// supplied by the verified Ubuntu parent. Expiry is disabled only for that
// immutable snapshot; signature checking and all package hash checks remain on.
// No account, password or autologin configuration is baked into the image.
func DesktopRecipe(l DesktopLock) (string, error) {
	if err := l.Validate(); err != nil {
		return "", err
	}
	suite := map[string]string{"20.04": "focal", "26.04": "resolute"}[l.OSVersion]
	packages := slices.Clone(l.Packages)
	slices.SortFunc(
		packages,
		func(a, b DesktopPackage) int { return strings.Compare(a.Name, b.Name) },
	)
	lines := []string{
		"set -eu",
		"export DEBIAN_FRONTEND=noninteractive",
		"systemctl stop weave-agent.service",
		"systemctl mask --runtime weave-agent.service lightdm.service",
		"work=$(mktemp -d /var/tmp/weave-desktop.XXXXXX)",
		"trap 'rm -rf \"$work\"' EXIT",
		`mkdir -p "$work/archives/partial" "$work/lists/partial"`,
		`dpkg-query -W -f='${Package} ${Version}\n' 'weave*' > "$work/weave.unsorted"`,
		`sort "$work/weave.unsorted" > "$work/weave.before"`,
		`test -s "$work/weave.before"`,
		`cat > "$work/sources.list" <<'WEAVE_APT_SOURCES'`,
	}
	for _, suffix := range []string{"", "-updates", "-security"} {
		lines = append(
			lines,
			"deb [signed-by=/usr/share/keyrings/ubuntu-archive-keyring.gpg check-valid-until=no] https://snapshot.ubuntu.com/ubuntu/"+l.Snapshot+"/ "+suite+suffix+" main universe",
		)
	}
	lines = append(
		lines,
		"WEAVE_APT_SOURCES",
		`apt_locked() { apt-get -o Dir::Etc::sourcelist="$work/sources.list" -o Dir::Etc::sourceparts="-" -o Dir::State::lists="$work/lists" -o Dir::Cache::archives="$work/archives" -o Acquire::AllowInsecureRepositories=false -o APT::Get::AllowUnauthenticated=false "$@"; }`,
		"apt_locked update",
	)
	var install []string
	for _, p := range packages {
		install = append(install, shellQuote(p.Name+"="+p.Version))
	}
	command := "--yes --no-install-recommends --no-remove --reinstall install " + strings.Join(
		install,
		" ",
	)
	lines = append(
		lines,
		"apt_locked --download-only "+command,
		`count=0`,
		`for file in "$work/archives/"*.deb; do`,
		`  test -f "$file"`,
		`  identity="$(dpkg-deb -f "$file" Package):$(dpkg-deb -f "$file" Version):$(dpkg-deb -f "$file" Architecture)"`,
		`  case "$identity" in`,
	)
	for _, p := range packages {
		lines = append(
			lines,
			"    "+shellQuote(
				p.Name+":"+p.Version+":"+p.Arch,
			)+") expected="+shellQuote(
				p.SHA256,
			)+" ;;",
		)
	}
	lines = append(
		lines,
		`    *) echo "Unpinned desktop dependency: $identity" >&2; exit 1 ;;`,
		"  esac",
		`  test ! -e "$work/seen-$identity"`,
		`  touch "$work/seen-$identity"`,
		`  test "$(sha256sum "$file" | awk '{print $1}')" = "$expected"`,
		`  count=$((count+1))`,
		"done",
		fmt.Sprintf(`test "$count" -eq %d`, len(packages)),
		"apt_locked --no-download "+command,
	)
	for _, p := range packages {
		lines = append(
			lines,
			`test "$(dpkg-query -W -f='${Version}' `+shellQuote(
				p.Name,
			)+`)" = `+shellQuote(
				p.Version,
			),
		)
	}
	lines = append(
		lines,
		"test -f /usr/share/xsessions/xfce.desktop",
		`dpkg-query -W -f='${Package} ${Version}\n' 'weave*' > "$work/weave.unsorted"`,
		`sort "$work/weave.unsorted" > "$work/weave.after"`,
		`cmp "$work/weave.before" "$work/weave.after"`,
		"systemctl unmask --runtime weave-agent.service lightdm.service",
		"systemctl enable weave-agent.service lightdm.service",
		"systemctl set-default graphical.target",
		"systemctl stop weave-agent.service lightdm.service",
		"rm -f /etc/weave/channel.pub /var/lib/weave/store.key /var/lib/weave/store.db /var/lib/weave/store.db-wal /var/lib/weave/store.db-shm",
		"cloud-init clean --logs --seed",
		"rm -f /etc/ssh/ssh_host_* /var/lib/systemd/random-seed /var/lib/dbus/machine-id",
		"truncate -s 0 /etc/machine-id",
		"ln -s /etc/machine-id /var/lib/dbus/machine-id",
		"sync",
	)
	return strings.Join(lines, "\n") + "\n", nil
}

type DesktopOptions struct {
	AgentOptions
	Desktop DesktopLock
}

// BuildLinuxDesktop derives Xfce/X11 from an agent image and retains its pinned
// core/modules. Acceptance provisions a disposable console account separately.
func (p Packages) BuildLinuxDesktop(ctx context.Context, o DesktopOptions) (string, error) {
	if err := validateNativeAgentOptions(o.AgentOptions); err != nil {
		return "", err
	}
	recipe, err := DesktopRecipe(o.Desktop)
	if err != nil {
		return "", err
	}
	if o.Arch != o.Desktop.Arch {
		return "", fmt.Errorf("%w: desktop architecture differs from lock", ErrInput)
	}
	store, report, err := openCandidate(ctx, o.Base)
	if err != nil {
		return "", err
	}
	selected, err := desktopParentDescriptor(report, o)
	if err != nil {
		return "", err
	}
	if err := newDirectory(o.Out); err != nil {
		return "", err
	}
	parent := filepath.Join(o.Out, "parent")
	if _, err := pack.Unpack(ctx, store, selected, parent, chunk.AssembleOptions{}); err != nil {
		return "", fmt.Errorf("unpack desktop parent: %w", err)
	}
	b, err := pack.LoadBundle(parent)
	if err != nil {
		return "", fmt.Errorf("load desktop parent: %w", err)
	}
	if len(b.File.Disks) != 1 || len(b.File.State) != 0 {
		return "", fmt.Errorf("%w: desktop parent requires one disk and fresh firmware", ErrInput)
	}
	bundle := filepath.Join(o.Out, "bundle")
	if err := newDirectory(bundle); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(o.Out, "desktop.sh"), []byte(recipe), 0o600); err != nil {
		return "", fmt.Errorf("write desktop recipe: %w", err)
	}
	if _, err := p.Tools.BootLinux(
		ctx,
		BootOptions{
			Bundle:     parent,
			Report:     filepath.Join(o.Out, "provision-report"),
			Timeout:    o.Timeout,
			Script:     recipe,
			OutputDisk: filepath.Join(bundle, "disk0.img"),
		},
	); err != nil {
		return "", err
	}
	commit, err := p.Tools.output(ctx, "git", "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	o.Desktop.Packages = slices.Clone(o.Desktop.Packages)
	slices.SortFunc(
		o.Desktop.Packages,
		func(a, b DesktopPackage) int { return strings.Compare(a.Name, b.Name) },
	)
	lockBytes, err := json.Marshal(o.Desktop)
	if err != nil {
		return "", fmt.Errorf("encode desktop lock: %w", err)
	}
	child := b.File
	child.Guest.Variant = "desktop"
	child.Disks = []pack.BundleDisk{{Name: "disk0", Role: "system", Path: "disk0.img"}}
	child.Build.Base = &spec.BaseImage{Name: o.BaseName, Digest: selected.Digest.String()}
	child.Build.Template = "linux-desktop-xfce-x11"
	child.Build.TemplateRef = "weaveplatform/weaveplatform-oci@" + strings.TrimSpace(string(commit))
	child.Build.Created = time.Now().UTC().Format(time.RFC3339)
	child.Annotations["io.weave.image.desktop-lock"] = digest.FromBytes(lockBytes).String()
	child.Annotations[spec.AnnotationVersion] = fmt.Sprintf(
		"%s-%s-agent%s-xfce-r%d",
		child.Guest.OSVersion,
		child.Guest.OSBuild,
		o.Lock.CoreVersion,
		o.Revision,
	)
	child.Annotations[spec.AnnotationRevision] = strings.TrimSpace(string(commit))
	if err := pack.WriteBundleFile(bundle, child); err != nil {
		return "", fmt.Errorf("write desktop bundle: %w", err)
	}
	if err := writeJSON(filepath.Join(o.Out, "desktop.lock.json"), o.Desktop); err != nil {
		return "", err
	}
	return bundle, nil
}

func desktopParentDescriptor(
	report conformance.Report,
	o DesktopOptions,
) (ocispec.Descriptor, error) {
	inputs, err := o.Lock.RequirePackages("linux/" + o.Arch)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	packages := []Asset{inputs.Core}
	for _, module := range inputs.Modules {
		packages = append(packages, *module.Package)
	}
	var selected []ocispec.Descriptor
	for _, child := range report.Children {
		cfg := child.Description.Config
		if cfg.Guest.OS == "linux" && cfg.Guest.Arch == o.Arch && cfg.Guest.Variant == "agent" &&
			cfg.Provisioning.Agent != nil &&
			cfg.Provisioning.Agent.Version == o.Lock.CoreVersion &&
			cfg.Guest.OSVersion == o.Desktop.OSVersion &&
			child.Descriptor.Digest.String() == o.Desktop.ParentDigest {
			for _, pkg := range packages {
				if !slices.ContainsFunc(cfg.Build.SourceMedia, func(s spec.SourceMedia) bool {
					return s.Kind == "package" && s.Digest == pkg.Digest && s.URI == pkg.URI
				}) {
					return ocispec.Descriptor{}, fmt.Errorf(
						"%w: desktop parent does not contain locked package %s",
						ErrInput,
						pkg.Name,
					)
				}
			}
			selected = append(selected, child.Descriptor)
		}
	}
	if len(selected) != 1 {
		return ocispec.Descriptor{}, fmt.Errorf(
			"%w: desktop requires its exact locked Linux agent parent",
			ErrInput,
		)
	}
	return selected[0], nil
}
