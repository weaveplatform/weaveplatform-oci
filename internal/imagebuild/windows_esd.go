package imagebuild

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/deploymenttheory/go-sdk-winmediafoundry/esd"
	esdapi "github.com/deploymenttheory/go-sdk-winmediafoundry/esd/api/esd"
	esdconst "github.com/deploymenttheory/go-sdk-winmediafoundry/esd/constants"
	"github.com/deploymenttheory/go-sdk-winmediafoundry/pkg/builder"
	"github.com/deploymenttheory/go-sdk-winmediafoundry/pkg/wim"
	"go.uber.org/zap"
)

type windowsEdition struct{ ID, Image, Key string }

// These public setup keys select an edition; they do not activate Windows.
var windowsEditions = map[string]windowsEdition{
	"pro":        {"Professional", "Windows 11 Pro", "VK7JG-NPHTM-C97JM-9MPGT-3V66T"},
	"home":       {"Core", "Windows 11 Home", "TX9XD-98N7V-6WMQ6-BX7FG-H8Q99"},
	"enterprise": {"Enterprise", "Windows 11 Enterprise", "NPPR9-FWDCX-D2C8J-H872K-2YT43"},
	"education":  {"Education", "Windows 11 Education", "NW6C2-QMPVW-D7KKK-3GKT6-VCFB2"},
}

// WindowsESDSources queries the SDK's full-install catalogue, including business
// editions. Numeric selectors pin the base build without guessing a release name.
func (p Packages) WindowsESDSources(
	ctx context.Context,
	s WindowsSelection,
) ([]WindowsSource, error) {
	if _, _, err := ParseWindowsSelection(s); err != nil {
		return nil, err
	}
	catalog, err := p.windowsCatalogue(ctx)
	if err != nil {
		return nil, err
	}
	return selectWindowsESD(catalog, s)
}

func (p Packages) windowsCatalogue(ctx context.Context) (*esdapi.ESDCatalog, error) {
	options := []esd.ClientOption{esd.WithLogger(zap.NewNop())}
	if c := p.Downloader.Client; c != nil {
		if c.Transport != nil {
			options = append(options, esd.WithTransport(c.Transport))
		}
		if c.Timeout > 0 {
			options = append(options, esd.WithTimeout(c.Timeout))
		}
	}
	client, err := esd.NewClient(options...)
	if err != nil {
		return nil, fmt.Errorf("create ESD catalogue client: %w", err)
	}
	catalog, _, err := client.Catalog(ctx, esdapi.WithProduct(esdapi.Windows11))
	if err != nil {
		return nil, fmt.Errorf("resolve Microsoft ESD catalogue: %w", err)
	}
	return catalog, nil
}

func (p Packages) windowsRetailLanguage(ctx context.Context, locale string) (string, error) {
	catalog, err := p.windowsCatalogue(ctx)
	if err != nil {
		return "", err
	}
	for _, entry := range catalog.Images {
		if strings.EqualFold(entry.LanguageCode, locale) && entry.Language != "" {
			return entry.Language, nil
		}
	}
	return "", fmt.Errorf("%w: Microsoft catalogue does not identify language %s", ErrInput, locale)
}

func selectWindowsESD(c *esdapi.ESDCatalog, s WindowsSelection) ([]WindowsSource, error) {
	edition, release, err := ParseWindowsSelection(s)
	if err != nil {
		return nil, err
	}
	build := 0
	if release != "latest" {
		if windowsBuildPattern.MatchString(release) {
			build, _ = strconv.Atoi(release)
		} else {
			var ok bool
			build, ok = esdconst.ReleaseBuild(esdconst.Release(release))
			if !ok {
				return nil, fmt.Errorf(
					"%w: Media Foundry has no build mapping for %s; use an exact base build selector or update the SDK",
					ErrInput,
					release,
				)
			}
		}
	}
	candidates := c.FilterBuildMajor(build, windowsEditions[edition].ID, "", s.Language)
	sources := make([]WindowsSource, 0, len(candidates))
	for _, entry := range candidates {
		// The SDK checks the architecture encoded in the filename, not merely the
		// catalogue row; Microsoft has shipped incorrectly labelled rows before.
		if (s.Arch == "arm64") != entry.IsARM64() {
			continue
		}
		if s.Arch == "amd64" && !strings.EqualFold(entry.Architecture, "x64") {
			continue
		}
		major := entry.BuildMajor()
		if major < 22000 {
			continue
		}
		actual := release
		if actual == "latest" {
			actual = strconv.Itoa(major)
			for _, known := range esdconst.Releases() {
				if number, _ := esdconst.ReleaseBuild(known); number == major {
					actual = strings.ToLower(string(known))
					break
				}
			}
		}
		uri, err := url.Parse(entry.URL)
		if err != nil {
			return nil, fmt.Errorf("parse ESD URL: %w", err)
		}
		// The catalogue also contains HTTP delivery URLs; retain only Microsoft's
		// host and upgrade the transport to TLS before any download.
		if uri.Scheme == "http" {
			uri.Scheme = "https"
		}
		source := WindowsSource{
			Kind:       "esd",
			Release:    actual,
			Edition:    edition,
			Arch:       s.Arch,
			Language:   s.Language,
			FileName:   entry.FileName,
			URI:        uri.String(),
			Size:       entry.SizeBytes,
			SHA1:       strings.ToLower(entry.SHA1),
			BuildMajor: major,
		}
		if err := source.validate(s, false); err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	slices.SortFunc(sources, func(a, b WindowsSource) int {
		if a.BuildMajor != b.BuildMajor {
			return b.BuildMajor - a.BuildMajor
		}
		return -compareESDVersions(a.FileName, b.FileName)
	})
	if len(sources) == 0 {
		return nil, fmt.Errorf(
			"%w: no Microsoft ESD matches %s/%s/%s; no edition or release fallback",
			ErrInput,
			s.FromWindows,
			s.Arch,
			s.Language,
		)
	}
	return sources, nil
}

func compareESDVersions(a, b string) int {
	// The first two filename components are base build and servicing revision.
	parts := func(s string) []int {
		tokens := strings.SplitN(s, ".", 3)
		out := make([]int, 2)
		for i := range out {
			if i < len(tokens) {
				out[i], _ = strconv.Atoi(tokens[i])
			}
		}
		return out
	}
	return slices.Compare(parts(a), parts(b))
}

func (s WindowsSource) validateESD() error {
	u, err := url.Parse(s.URI)
	if err != nil {
		return fmt.Errorf("parse ESD URI: %w", err)
	}
	if u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" ||
		(u.Hostname() != "dl.delivery.mp.microsoft.com" && u.Hostname() != "fg.ds.b1.download.windowsupdate.com") ||
		!strings.HasSuffix(strings.ToLower(s.FileName), ".esd") ||
		!sha1Pattern.MatchString(s.SHA1) ||
		s.BuildMajor < 22000 {
		return fmt.Errorf("%w: invalid Microsoft ESD source", ErrInput)
	}
	entry := esdapi.ESDImage{FileName: s.FileName, Architecture: "x64"}
	if s.Arch == "arm64" {
		entry.Architecture = "ARM64"
	}
	if entry.BuildMajor() != s.BuildMajor || (s.Arch == "arm64") != entry.IsARM64() {
		return fmt.Errorf("%w: ESD filename differs from pinned build/architecture", ErrInput)
	}
	if windowsBuildPattern.MatchString(s.Release) {
		if strconv.Itoa(s.BuildMajor) != s.Release {
			return fmt.Errorf("%w: ESD build differs from selector", ErrInput)
		}
	} else if major, ok := esdconst.ReleaseBuild(esdconst.Release(s.Release)); !ok || major != s.BuildMajor {
		return fmt.Errorf("%w: ESD release/build mapping differs", ErrInput)
	}
	return nil
}

func (p Packages) prepareWindowsISO(
	ctx context.Context,
	s WindowsSource,
	media, out string,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("prepare Windows media: %w", err)
	}
	dest := filepath.Join(out, "install.iso")
	if s.Kind != "esd" {
		return dest, copyFile(media, dest)
	}
	source, err := wim.Open(media)
	if err != nil {
		return "", fmt.Errorf("inspect ESD images: %w", err)
	}
	defer source.Close()
	found := false
	for _, image := range source.Images() {
		if strings.EqualFold(image.Edition, windowsEditions[s.Edition].ID) &&
			image.Name == windowsEditions[s.Edition].Image {
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf(
			"%w: ESD does not contain the requested Windows edition image",
			ErrInput,
		)
	}
	scratch := filepath.Join(out, "media-work")
	if err := newDirectory(scratch); err != nil {
		return "", err
	}
	if err := builder.BuildISO(
		media,
		dest,
		builder.Options{VolumeID: "WEAVE-WINDOWS", WorkDir: scratch, Progress: p.Tools.Log},
	); err != nil {
		return "", fmt.Errorf("build Microsoft ESD installation media: %w", err)
	}
	return dest, nil
}

func windowsInstalledVersionMatches(r WindowsInstallResult, s WindowsSource) bool {
	if s.BuildMajor > 0 && !strings.HasPrefix(r.Build, strconv.Itoa(s.BuildMajor)+".") {
		return false
	}
	if windowsBuildPattern.MatchString(s.Release) {
		return strings.HasPrefix(r.Build, s.Release+".")
	}
	return strings.EqualFold(r.Release, s.Release)
}

func windowsMediaKind(s WindowsSource) string {
	if s.Kind == "esd" {
		return "esd"
	}
	return "iso"
}
