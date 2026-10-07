package imagebuild

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const appleCatalogueURL = "https://api.ipsw.me/v4/device/VirtualMac2,1?type=ipsw"

var (
	macVersionPattern = regexp.MustCompile(`^[0-9]{1,2}(\.[0-9]{1,3}){0,2}$`)
	appleBuildPattern = regexp.MustCompile(`^[0-9]{2}[A-Z][0-9]+$`)
)

// AppleSource records exact Apple restore media selected from a version request.
type AppleSource struct {
	URL         string `json:"url"`
	Version     string `json:"version"`
	Build       string `json:"buildid"`
	SHA256      string `json:"sha256sum"`
	Size        int64  `json:"filesize"`
	Signed      bool   `json:"signed"`
	ReleaseDate string `json:"releasedate,omitempty"`
}

// ValidateMacVersion accepts the same major/exact version forms as guestweave.
func ValidateMacVersion(version string) error {
	if !macVersionPattern.MatchString(version) {
		return fmt.Errorf(
			"%w: macOS version must be a major release or exact dotted version",
			ErrInput,
		)
	}
	return nil
}

func (s AppleSource) validate(version string, revision int) (string, error) {
	if err := ValidateMacVersion(version); err != nil {
		return "", err
	}
	u, err := url.Parse(s.URL)
	if err != nil {
		return "", fmt.Errorf("parse Apple URL: %w", err)
	}
	hosts := []string{"updates.cdn-apple.com", "secure-appldnld.apple.com", "appldnld.apple.com"}
	if u.Scheme != "https" || !slices.Contains(hosts, u.Host) || u.User != nil ||
		u.Fragment != "" ||
		u.RawQuery != "" ||
		!strings.HasSuffix(u.Path, ".ipsw") {
		return "", fmt.Errorf("%w: restore media must use Apple's HTTPS CDN", ErrInput)
	}
	if !macVersionPattern.MatchString(s.Version) || !appleBuildPattern.MatchString(s.Build) ||
		!macVersionMatches(s.Version, version) ||
		revision < 1 ||
		s.Size <= 0 ||
		!shaPattern.MatchString("sha256:"+s.SHA256) {
		return "", fmt.Errorf(
			"%w: Apple source does not match requested version or lacks size/digest",
			ErrInput,
		)
	}
	return fmt.Sprintf("%s-%s-r%d", s.Version, s.Build, revision), nil
}

func macVersionMatches(actual, requested string) bool {
	if strings.Contains(requested, ".") {
		return actual == requested
	}
	return strings.Split(actual, ".")[0] == requested
}

// MacSources lists production restore images matching a major or exact version.
// The catalogue locates media; Apple's installer remains the restore authority.
func MacSources(ctx context.Context, client *http.Client, version string) ([]AppleSource, error) {
	return macSources(ctx, client, appleCatalogueURL, version)
}

func macSources(
	ctx context.Context,
	client *http.Client,
	endpoint, version string,
) ([]AppleSource, error) {
	if err := ValidateMacVersion(version); err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("catalogue request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch IPSW catalogue: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: IPSW catalogue HTTP %d", ErrInput, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024+1))
	if err != nil {
		return nil, fmt.Errorf("read IPSW catalogue: %w", err)
	}
	if len(raw) > 8*1024*1024 {
		return nil, fmt.Errorf("%w: IPSW catalogue exceeds 8 MiB", ErrInput)
	}
	var cat struct {
		Identifier string        `json:"identifier"`
		Firmwares  []AppleSource `json:"firmwares"`
	}
	if err := json.Unmarshal(raw, &cat); err != nil {
		return nil, fmt.Errorf("decode IPSW catalogue: %w", err)
	}
	if cat.Identifier != "VirtualMac2,1" {
		return nil, fmt.Errorf("%w: unexpected Apple catalogue device", ErrInput)
	}
	var result []AppleSource
	for _, s := range cat.Firmwares {
		if !macVersionPattern.MatchString(s.Version) || !appleBuildPattern.MatchString(s.Build) ||
			!macVersionMatches(s.Version, version) {
			continue
		}
		if _, err := s.validate(version, 1); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	slices.SortFunc(result, func(a, b AppleSource) int {
		if n := compareVersionNumbers(a.Version, b.Version); n != 0 {
			return -n
		}
		if a.Signed != b.Signed {
			if a.Signed {
				return -1
			}
			return 1
		}
		if n := strings.Compare(a.ReleaseDate, b.ReleaseDate); n != 0 {
			return -n
		}
		if n := strings.Compare(a.Build[:3], b.Build[:3]); n != 0 {
			return -n
		}
		x, _ := strconv.Atoi(a.Build[3:])
		y, _ := strconv.Atoi(b.Build[3:])
		if x != y {
			if x > y {
				return -1
			}
			return 1
		}
		return strings.Compare(a.URL, b.URL)
	})
	if len(result) == 0 {
		return nil, fmt.Errorf(
			"%w: no production Apple restore image matches %s",
			ErrInput,
			version,
		)
	}
	return result, nil
}

func compareVersionNumbers(a, b string) int {
	parse := func(s string) [3]int {
		var v [3]int
		parts := strings.Split(s, ".")
		for i := range v {
			if i < len(parts) {
				v[i], _ = strconv.Atoi(parts[i])
			}
		}
		return v
	}
	x, y := parse(a), parse(b)
	return slices.Compare(x[:], y[:])
}

// WriteSourceLock saves a resolved source without replacing an existing lock.
func WriteSourceLock(file string, source any) error { return writeNewJSON(file, source) }
