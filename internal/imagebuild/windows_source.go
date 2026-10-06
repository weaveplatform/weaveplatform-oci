package imagebuild

import (
	"context"
	"crypto/sha1" //nolint:gosec // Microsoft publishes SHA1 for ESDs; also calculate SHA256 for our lock.
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/deploymenttheory/go-sdk-winmediafoundry/softwaredownload"
	swdl "github.com/deploymenttheory/go-sdk-winmediafoundry/softwaredownload/api/softwaredownload"
	swconst "github.com/deploymenttheory/go-sdk-winmediafoundry/softwaredownload/constants"
	"github.com/deploymenttheory/go-sdk-winmediafoundry/softwaredownload/shared/models"
	"go.uber.org/zap"
)

// WindowsSelection matches guestweave's edition-release selector.
type WindowsSelection struct{ FromWindows, Arch, Language string }

// WindowsSource records the requested edition and exact Microsoft media. SHA256
// is filled after acquisition; Microsoft retail link metadata supplies no digest.
type WindowsSource struct {
	Kind       string `json:"kind,omitempty"`
	SHA1       string `json:"sha1,omitempty"`
	BuildMajor int    `json:"buildMajor,omitempty"`
	Release    string `json:"release"`
	Edition    string `json:"edition"`
	Arch       string `json:"arch"`
	Language   string `json:"language"`
	FileName   string `json:"fileName"`
	URI        string `json:"uri"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256,omitempty"`
}

var (
	windowsReleasePattern = regexp.MustCompile(`^[0-9]{2}h[12]$`)
	windowsBuildPattern   = regexp.MustCompile(`^[2-9][0-9]{4}$`)
	sha1Pattern           = regexp.MustCompile(`^[0-9a-f]{40}$`)
	windowsReleaseToken   = regexp.MustCompile(`(?i)_([0-9]{2}H[12])_`)
	languagePattern       = regexp.MustCompile(`^[a-zA-Z]{2,3}-[a-zA-Z]{2,4}$`)
)

// ParseWindowsSelection validates explicit edition, release, architecture and language.
func ParseWindowsSelection(s WindowsSelection) (string, string, error) {
	edition, release, ok := strings.Cut(strings.ToLower(s.FromWindows), "-")
	_, supported := windowsEditions[edition]
	if !ok || !supported ||
		(release != "latest" && !windowsReleasePattern.MatchString(release) && !windowsBuildPattern.MatchString(release)) {
		return "", "", fmt.Errorf(
			"%w: --from-windows must select pro, home, enterprise or education plus a release, base build, or latest",
			ErrInput,
		)
	}
	if (s.Arch != "amd64" && s.Arch != "arm64") || !languagePattern.MatchString(s.Language) {
		return "", "", fmt.Errorf(
			"%w: --arch amd64/arm64 and a language such as en-US required",
			ErrInput,
		)
	}
	return edition, release, nil
}

// ResolveWindows uses the same Microsoft software-download SDK as guestweave.
func (p Packages) ResolveWindows(ctx context.Context, s WindowsSelection) (WindowsSource, error) {
	edition, release, err := ParseWindowsSelection(s)
	if err != nil {
		return WindowsSource{}, err
	}
	if edition == "enterprise" || edition == "education" ||
		windowsBuildPattern.MatchString(release) {
		sources, err := p.WindowsESDSources(ctx, s)
		if err != nil {
			return WindowsSource{}, err
		}
		return sources[0], nil
	}
	client, err := p.windowsClient()
	if err != nil {
		return WindowsSource{}, fmt.Errorf("create Microsoft media client: %w", err)
	}
	arch := swconst.Arch("x64")
	if s.Arch == "arm64" {
		arch = swconst.Arch("ARM64")
	}
	language, err := p.windowsRetailLanguage(ctx, s.Language)
	if err != nil {
		return WindowsSource{}, err
	}
	link, _, err := client.GetByName(
		ctx,
		string(arch),
		swdl.WithArch(arch),
		swdl.WithLanguage(language),
	)
	if err != nil {
		return WindowsSource{}, fmt.Errorf("resolve Microsoft retail media: %w", err)
	}
	if !strings.EqualFold(link.Language.Name, language) &&
		!strings.EqualFold(link.Language.LocalizedName, language) {
		return WindowsSource{}, fmt.Errorf("%w: Microsoft returned a different language", ErrInput)
	}
	link.Language.Name = s.Language
	if link.SizeBytes <= 0 {
		link.SizeBytes, err = p.windowsMediaSize(ctx, link.URL)
		if err != nil {
			return WindowsSource{}, err
		}
	}
	return windowsSource(*link, s, edition, release)
}

func (p Packages) windowsMediaSize(ctx context.Context, uri string) (int64, error) {
	if _, err := microsoftISOURL(uri); err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, uri, nil)
	if err != nil {
		return 0, fmt.Errorf("create media size request: %w", err)
	}
	client := p.Downloader.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("probe Windows media size: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength <= 0 {
		return 0, fmt.Errorf(
			"%w: Microsoft media size unavailable (HTTP %d)",
			ErrInput,
			resp.StatusCode,
		)
	}
	return resp.ContentLength, nil
}

func microsoftISOURL(uri string) (*url.URL, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("parse Windows media URL: %w", err)
	}
	host := strings.ToLower(u.Hostname())
	if u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" ||
		(host != "software.download.prss.microsoft.com" && host != "software-static.download.prss.microsoft.com") {
		return nil, fmt.Errorf("%w: invalid Microsoft ISO source", ErrInput)
	}
	return u, nil
}

func windowsSource(
	link models.DownloadLink,
	s WindowsSelection,
	edition, release string,
) (WindowsSource, error) {
	match := windowsReleaseToken.FindStringSubmatch(link.FileName)
	if len(match) != 2 {
		return WindowsSource{}, fmt.Errorf(
			"%w: Microsoft media filename does not identify its release",
			ErrInput,
		)
	}
	actual := strings.ToLower(match[1])
	if release != "latest" && actual != release {
		return WindowsSource{}, fmt.Errorf(
			"%w: Microsoft returned %s, requested %s; refusing release fallback",
			ErrInput,
			actual,
			release,
		)
	}
	source := WindowsSource{
		Release:  actual,
		Edition:  edition,
		Arch:     s.Arch,
		Language: s.Language,
		FileName: link.FileName,
		URI:      link.URL,
		Size:     link.SizeBytes,
	}
	expected := "x64"
	if s.Arch == "arm64" {
		expected = "arm64"
	}
	if !strings.EqualFold(string(link.Arch), expected) ||
		!strings.EqualFold(link.Language.Name, s.Language) {
		return source, fmt.Errorf(
			"%w: Microsoft returned a different architecture or language",
			ErrInput,
		)
	}
	return source, source.validate(s, false)
}

func (s WindowsSource) validate(selection WindowsSelection, pinned bool) error {
	edition, release, err := ParseWindowsSelection(selection)
	if err != nil {
		return err
	}
	if s.Edition != edition || s.Arch != selection.Arch ||
		!strings.EqualFold(s.Language, selection.Language) ||
		(release != "latest" && s.Release != release) ||
		(!windowsReleasePattern.MatchString(s.Release) && !windowsBuildPattern.MatchString(s.Release)) {
		return fmt.Errorf("%w: Windows source lock differs from selection", ErrInput)
	}
	if filepath.Base(s.FileName) != s.FileName || strings.ContainsAny(s.FileName, "\\\r\n") ||
		s.Size <= 0 {
		return fmt.Errorf("%w: invalid Windows media filename/size", ErrInput)
	}
	if pinned && !shaPattern.MatchString("sha256:"+s.SHA256) {
		return fmt.Errorf("%w: replay requires the acquired media SHA256", ErrInput)
	}
	if s.Kind == "esd" {
		return s.validateESD()
	}
	if s.Kind != "" && s.Kind != "iso" {
		return fmt.Errorf("%w: unknown Windows media kind", ErrInput)
	}
	u, err := microsoftISOURL(s.URI)
	if err != nil {
		return err
	}
	if !strings.HasSuffix(strings.ToLower(s.FileName), ".iso") ||
		filepath.Base(u.Path) != s.FileName {
		return fmt.Errorf("%w: invalid Microsoft ISO source", ErrInput)
	}
	match := windowsReleaseToken.FindStringSubmatch(s.FileName)
	if len(match) != 2 || !strings.EqualFold(match[1], s.Release) {
		return fmt.Errorf("%w: ISO filename differs from locked release", ErrInput)
	}
	return nil
}

// AcquireWindows downloads through the shared SDK, verifies the complete size,
// and records SHA256. Replays use the pinned content-addressed download path.
func (p Packages) AcquireWindows(
	ctx context.Context,
	s WindowsSource,
	selection WindowsSelection,
	cache string,
) (WindowsSource, string, error) {
	if err := s.validate(selection, false); err != nil {
		return s, "", err
	}
	if s.SHA256 != "" {
		file, err := p.Downloader.Download(
			ctx,
			Media{URI: s.URI, Size: s.Size, SHA256: s.SHA256},
			filepath.Join(cache, s.SHA256, s.FileName),
		)
		return s, file, err
	}
	if err := os.MkdirAll(cache, 0o750); err != nil {
		return s, "", fmt.Errorf("create Windows cache: %w", err)
	}
	temp, err := os.MkdirTemp(cache, "acquire-")
	if err != nil {
		return s, "", fmt.Errorf("create media staging: %w", err)
	}
	defer os.RemoveAll(temp)
	client, err := p.windowsClient()
	if err != nil {
		return s, "", fmt.Errorf("create Microsoft download client: %w", err)
	}
	link := models.DownloadLink{FileName: s.FileName, URL: s.URI, SizeBytes: s.Size}
	if _, err := client.Download(
		ctx,
		link,
		temp,
		swdl.WithProgress(windowsProgress(p.Tools.Log)),
	); err != nil {
		return s, "", fmt.Errorf("download Microsoft ISO: %w", err)
	}
	file := filepath.Join(temp, s.FileName)
	digest, err := hashWindowsMedia(file, s.Size, s.SHA1)
	if err != nil {
		return s, "", err
	}
	s.SHA256 = digest
	dest := filepath.Join(cache, digest, s.FileName)
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return s, "", fmt.Errorf("create media cache: %w", err)
	}
	if _, err := os.Stat(dest); err == nil {
		return s, dest, verifiedFile(dest, Media{Size: s.Size, SHA256: digest})
	} else if !errors.Is(err, os.ErrNotExist) {
		return s, "", fmt.Errorf("inspect Windows media cache: %w", err)
	}
	if err := os.Rename(file, dest); err != nil {
		return s, "", fmt.Errorf("cache Microsoft ISO: %w", err)
	}
	return s, dest, nil
}

func hashSizedFile(path string, size int64) (string, error) {
	return hashWindowsMedia(path, size, "")
}

func hashWindowsMedia(path string, size int64, vendorSHA1 string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open media: %w", err)
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat media: %w", err)
	}
	if stat.Size() != size {
		return "", fmt.Errorf("%w: downloaded media size differs from source", ErrInput)
	}
	h := sha256.New()
	legacy := sha1.New() //nolint:gosec // Verify Microsoft's published ESD checksum alongside SHA256.
	if _, err := io.Copy(io.MultiWriter(h, legacy), f); err != nil {
		return "", fmt.Errorf("hash media: %w", err)
	}
	if vendorSHA1 != "" && hex.EncodeToString(legacy.Sum(nil)) != vendorSHA1 {
		return "", fmt.Errorf("%w: Microsoft ESD SHA1 mismatch", ErrInput)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (p Packages) windowsClient() (*softwaredownload.Client, error) {
	options := []softwaredownload.ClientOption{softwaredownload.WithLogger(zap.NewNop())}
	if c := p.Downloader.Client; c != nil {
		if c.Transport != nil {
			options = append(options, softwaredownload.WithTransport(c.Transport))
		}
		if c.Timeout > 0 {
			options = append(options, softwaredownload.WithTimeout(c.Timeout))
		}
	}
	client, err := softwaredownload.NewClient(options...)
	if err != nil {
		return nil, fmt.Errorf("create Microsoft media SDK: %w", err)
	}
	return client, nil
}

func windowsProgress(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}
