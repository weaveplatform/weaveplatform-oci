package imagebuild

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Packages verifies publisher evidence before creating an offline installation payload.
type Packages struct {
	MacRestore     MacRestoreFunc
	WindowsInstall WindowsInstallFunc
	Tools          Tools
	Downloader     Downloader
}

func (p Packages) signedChecksums(
	ctx context.Context,
	evidence, artifacts []Asset,
	repo string,
	refs []string,
	cache string,
) error {
	files := map[string]string{}
	var sums string
	for _, a := range evidence {
		path, err := p.Downloader.Asset(ctx, a, cache)
		if err != nil {
			return err
		}
		files[a.Name] = path
		if strings.HasSuffix(a.Name, "checksums.txt") {
			if sums != "" {
				return fmt.Errorf("%w: multiple checksum files", ErrInput)
			}
			sums = a.Name
		}
	}
	if sums == "" || files[sums+".sigstore.json"] == "" {
		return fmt.Errorf("%w: checksum file and Sigstore bundle required", ErrInput)
	}
	workflow := "module-release"
	if repo == coreRepository {
		workflow = "release"
	}
	escaped := make([]string, len(refs))
	for i, r := range refs {
		escaped[i] = regexp.QuoteMeta(r)
	}
	identity := `^https://github\.com/` + regexp.QuoteMeta(
		repo,
	) + `/\.github/workflows/` + workflow + `\.yml@(?:` + strings.Join(
		escaped,
		"|",
	) + `)$`
	cosign := os.Getenv("COSIGN")
	if cosign == "" {
		cosign = "cosign"
	}
	if err := p.Tools.run(
		ctx,
		nil,
		cosign,
		"verify-blob",
		"--bundle",
		files[sums+".sigstore.json"],
		"--certificate-identity-regexp",
		identity,
		"--certificate-oidc-issuer",
		"https://token.actions.githubusercontent.com",
		files[sums],
	); err != nil {
		return err
	}
	raw, err := os.ReadFile(files[sums])
	if err != nil {
		return fmt.Errorf("read signed checksums: %w", err)
	}
	return checkSums(string(raw), artifacts)
}

func checkSums(raw string, artifacts []Asset) error {
	sums := map[string]string{}
	pattern := regexp.MustCompile(`^([0-9a-f]{64}) [ *](.+)$`)
	for _, line := range strings.Split(strings.TrimSuffix(raw, "\n"), "\n") {
		match := pattern.FindStringSubmatch(line)
		if match == nil || sums[match[2]] != "" {
			return fmt.Errorf("%w: malformed or duplicate checksum entry", ErrInput)
		}
		sums[match[2]] = "sha256:" + match[1]
	}
	for _, a := range artifacts {
		if sums[a.Name] != a.Digest {
			return fmt.Errorf("%w: %s signed checksum differs from lock", ErrInput, a.Name)
		}
	}
	return nil
}

func (p Packages) module(ctx context.Context, m ModuleInput, arch, cache string) error {
	if err := p.signedChecksums(
		ctx,
		m.PackageEvidence,
		[]Asset{*m.Package, m.Manifest, m.Binary},
		modulesRepository,
		[]string{"refs/tags/modules/" + m.ID + "/v" + m.Version, "refs/heads/main"},
		cache,
	); err != nil {
		return err
	}
	file, err := p.Downloader.Asset(ctx, m.Manifest, cache)
	if err != nil {
		return err
	}
	var manifest struct {
		ID        string `json:"id"`
		Version   string `json:"version"`
		Artifacts []struct {
			OS     string `json:"os"`
			Arch   string `json:"arch"`
			Digest string `json:"digest"`
			Size   int64  `json:"size"`
		} `json:"artifacts"`
	}
	if err := readJSON(file, &manifest); err != nil {
		return err
	}
	if manifest.ID != m.ID || manifest.Version != m.Version {
		return fmt.Errorf("%w: manifest disagrees with module lock", ErrInput)
	}
	matches := 0
	for _, a := range manifest.Artifacts {
		if a.OS == "linux" && a.Arch == arch {
			matches++
			if a.Digest != m.Binary.Digest || a.Size != m.Binary.Size {
				return fmt.Errorf("%w: manifest binary differs from lock", ErrInput)
			}
		}
	}
	if matches != 1 {
		return fmt.Errorf("%w: manifest requires exactly one matching artifact", ErrInput)
	}
	return nil
}

// PrepareLinux checks all installers and their signed manifests before writing a payload.
func (p Packages) PrepareLinux(
	ctx context.Context,
	l Lock,
	arch, cache, out string,
) ([]Asset, error) {
	entry, err := l.RequirePackages("linux/" + arch)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: payload output already exists or is inaccessible", ErrInput)
	}
	if err := p.signedChecksums(
		ctx,
		entry.CoreEvidence,
		[]Asset{entry.Core},
		coreRepository,
		[]string{"refs/tags/v" + l.CoreVersion},
		cache,
	); err != nil {
		return nil, err
	}
	sources := []Asset{entry.Core}
	for _, m := range entry.Modules {
		if err := p.module(ctx, m, arch, cache); err != nil {
			return nil, err
		}
		sources = append(sources, *m.Package)
	}
	paths := make([]string, len(sources))
	for i, a := range sources {
		path, err := p.Downloader.Asset(ctx, a, cache)
		if err != nil {
			return nil, err
		}
		paths[i] = path
	}
	if err := newDirectory(out); err != nil {
		return nil, err
	}
	for i, a := range sources {
		if err := copyFile(paths[i], filepath.Join(out, a.Name)); err != nil {
			return nil, err
		}
	}
	return sources, writeJSON(filepath.Join(out, "packages.lock.json"), l)
}

// LinuxRecipe installs pinned packages and removes machine-specific identities.
func LinuxRecipe(l Lock, arch string) (string, error) {
	entry, err := l.RequirePackages("linux/" + arch)
	if err != nil {
		return "", err
	}
	lines := []string{
		"mkdir -p /mnt/weave-packages",
		"mount -o ro /dev/disk/by-label/cidata /mnt/weave-packages",
		"systemctl mask --runtime weave-agent.service",
		"dpkg -i /mnt/weave-packages/packages/*.deb",
		"test \"$(dpkg-query -W -f='${Version}' weave-agent)\" = " + shellQuote(l.CoreVersion),
	}
	for _, m := range entry.Modules {
		if !namePattern.MatchString(m.ID) || !shaPattern.MatchString(m.Binary.Digest) {
			return "", fmt.Errorf("%w: invalid module for provisioning", ErrInput)
		}
		lines = append(
			lines,
			"test \"$(dpkg-query -W -f='${Version}' "+shellQuote(
				m.ID,
			)+")\" = "+shellQuote(
				m.Version,
			),
			"printf '%s\\n' "+shellQuote(
				strings.TrimPrefix(
					m.Binary.Digest,
					"sha256:",
				)+"  /usr/lib/weave/modules/"+m.ID+"/"+m.ID,
			)+" | sha256sum -c -",
		)
	}
	lines = append(
		lines,
		"systemctl unmask --runtime weave-agent.service",
		"systemctl enable weave-agent.service",
		"systemctl stop weave-agent.service",
		"rm -f /etc/weave/channel.pub /var/lib/weave/store.key /var/lib/weave/store.db /var/lib/weave/store.db-wal /var/lib/weave/store.db-shm",
		"cloud-init clean --logs --seed",
		"rm -f /etc/ssh/ssh_host_* /var/lib/systemd/random-seed /var/lib/dbus/machine-id",
		"truncate -s 0 /etc/machine-id",
		"ln -s /etc/machine-id /var/lib/dbus/machine-id",
		"sync",
	)
	return strings.Join(lines, "\n") + "\n", nil
}
