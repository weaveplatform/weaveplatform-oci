package imagebuild

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	esdapi "github.com/deploymenttheory/go-sdk-winmediafoundry/esd/api/esd"
	"github.com/deploymenttheory/go-sdk-winmediafoundry/pkg/isoinspect"
	"github.com/deploymenttheory/go-sdk-winmediafoundry/pkg/wim"
)

func esdFixture() esdapi.ESDImage {
	return esdapi.ESDImage{
		FileName:     "26200.12.release_CLIENTBUSINESS_VOL_X64FRE_en-us.esd",
		Edition:      "Enterprise",
		Architecture: "x64",
		LanguageCode: "en-us",
		SizeBytes:    1024,
		SHA1:         strings.Repeat("a", 40),
		URL:          "http://dl.delivery.mp.microsoft.com/files/install.esd",
	}
}

func TestEnterpriseESDSelection(t *testing.T) {
	base := esdFixture()
	older := base
	older.FileName = strings.Replace(base.FileName, ".12.", ".9.", 1)
	arm := base
	arm.Architecture = "ARM64"
	arm.FileName = strings.Replace(base.FileName, "X64FRE", "A64FRE", 1)
	c := &esdapi.ESDCatalog{Images: []esdapi.ESDImage{older, base, arm}}
	for _, selector := range []string{"enterprise-25h2", "enterprise-latest", "enterprise-26200"} {
		for _, arch := range []string{"amd64", "arm64"} {
			s := WindowsSelection{selector, arch, "en-US"}
			sources, err := selectWindowsESD(c, s)
			must(t, err)
			if sources[0].BuildMajor != 26200 || !strings.HasPrefix(sources[0].URI, "https://") ||
				!strings.Contains(sources[0].FileName, ".12.") {
				t.Fatal(sources)
			}
			if !windowsInstalledVersionMatches(
				WindowsInstallResult{Build: "26200.12", Release: "25H2"},
				sources[0],
			) {
				t.Fatal("matching guest rejected")
			}
			if windowsInstalledVersionMatches(
				WindowsInstallResult{Build: "26100.12", Release: "24H2"},
				sources[0],
			) {
				t.Fatal("different guest accepted")
			}
		}
	}
	for _, selector := range []string{"enterprise-24h2", "enterprise-26h2", "education-25h2", "invalid-25h2"} {
		if _, err := selectWindowsESD(c, WindowsSelection{selector, "amd64", "en-US"}); err == nil {
			t.Fatal("fallback for " + selector)
		}
	}
	s := WindowsSelection{"enterprise-25h2", "amd64", "en-US"}
	sources, err := selectWindowsESD(c, s)
	must(t, err)
	source := sources[0]
	for _, mutate := range []func(*WindowsSource){func(s *WindowsSource) { s.URI = "https://example.com/media.esd" }, func(s *WindowsSource) { s.SHA1 = "bad" }, func(s *WindowsSource) { s.BuildMajor = 26100 }, func(s *WindowsSource) { s.Arch = "arm64" }, func(s *WindowsSource) { s.Release = "24h2" }, func(s *WindowsSource) { s.Release = "26100" }} {
		bad := source
		mutate(&bad)
		if bad.validateESD() == nil {
			t.Fatal("invalid ESD source accepted", bad)
		}
	}
	if windowsMediaKind(source) != "esd" {
		t.Fatal("lost ESD provenance")
	}
}

// storeCAB makes a single-file, uncompressed Microsoft Cabinet for exercising
// the real SDK catalogue decoder without a live Microsoft endpoint.
func storeCAB(data []byte) []byte {
	name := []byte("products.xml\x00")
	offset := 44 + 16 + len(name)
	b := make([]byte, offset+8+len(data))
	le := binary.LittleEndian
	copy(b, "MSCF")
	le.PutUint32(b[8:], uint32(len(b)))
	le.PutUint32(b[16:], 44)
	b[24], b[25] = 3, 1
	le.PutUint16(b[26:], 1)
	le.PutUint16(b[28:], 1)
	le.PutUint32(b[36:], uint32(offset))
	le.PutUint16(b[40:], 1)
	le.PutUint32(b[44:], uint32(len(data)))
	copy(b[60:], name)
	le.PutUint16(b[offset+4:], uint16(len(data)))
	le.PutUint16(b[offset+6:], uint16(len(data)))
	copy(b[offset+8:], data)
	return b
}

func TestEnterpriseSDKCatalogueAndDownload(t *testing.T) {
	payload := []byte("an ESD payload")
	hash := sha1.Sum(payload)
	entry := esdFixture()
	entry.SizeBytes = int64(len(payload))
	entry.SHA1 = hex.EncodeToString(hash[:])
	xml := fmt.Sprintf(
		`<MCT><Catalogs><Catalog><PublishedMedia><Files><File><FileName>%s</FileName><Edition>Enterprise</Edition><Architecture>x64</Architecture><LanguageCode>en-us</LanguageCode><Size>%d</Size><Sha1>%s</Sha1><FilePath>%s</FilePath></File></Files></PublishedMedia></Catalog></Catalogs></MCT>`,
		entry.FileName,
		entry.SizeBytes,
		entry.SHA1,
		entry.URL,
	)
	p := Packages{
		Downloader: Downloader{
			Client: &http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					body := payload
					if r.URL.Host == "go.microsoft.com" {
						body = storeCAB([]byte(xml))
					}
					return &http.Response{
						StatusCode:    200,
						Header:        http.Header{},
						Request:       r,
						Body:          io.NopCloser(bytes.NewReader(body)),
						ContentLength: int64(len(body)),
					}, nil
				}),
			},
		},
	}
	selection := WindowsSelection{"enterprise-25h2", "amd64", "en-US"}
	source, err := p.ResolveWindows(t.Context(), selection)
	must(t, err)
	acquired, file, err := p.AcquireWindows(t.Context(), source, selection, t.TempDir())
	must(t, err)
	if acquired.SHA256 == "" || acquired.SHA1 != entry.SHA1 {
		t.Fatal(acquired)
	}
	must(t, verifiedFile(file, Media{Size: acquired.Size, SHA256: acquired.SHA256}))
	source.SHA1 = strings.Repeat("b", 40)
	if _, _, err := p.AcquireWindows(t.Context(), source, selection, t.TempDir()); err == nil {
		t.Fatal("vendor checksum mismatch ignored")
	}
	if _, err := p.WindowsESDSources(t.Context(), WindowsSelection{}); err == nil {
		t.Fatal("bad selection")
	}
}

func syntheticEnterpriseESD(t *testing.T) string {
	t.Helper()
	media := t.TempDir()
	boot := filepath.Join(media, "efi", "microsoft", "boot")
	must(t, os.MkdirAll(boot, 0o750))
	for _, name := range []string{"efisys.bin", "efisys_noprompt.bin"} {
		must(t, os.WriteFile(filepath.Join(boot, name), make([]byte, 4096), 0o600))
	}
	path := filepath.Join(t.TempDir(), "install.esd")
	file, err := os.Create(path)
	must(t, err)
	writer, err := wim.NewWriter(file)
	must(t, err)
	must(t, writer.AddImage(media, "Windows Setup Media"))
	for _, name := range []string{"Microsoft Windows PE", "Microsoft Windows Setup", "Windows 11 Enterprise"} {
		dir := t.TempDir()
		must(t, os.WriteFile(filepath.Join(dir, "payload.txt"), []byte(name), 0o600))
		must(t, writer.AddImage(dir, name))
	}
	must(t, writer.Close())
	must(t, file.Close())
	updater, err := wim.OpenForUpdate(path)
	must(t, err)
	must(t, updater.SetProperty(4, "WINDOWS/EDITIONID", "Enterprise"))
	must(t, updater.Commit())
	must(t, updater.Close())
	return path
}

func TestEnterpriseMediaAssembly(t *testing.T) {
	path := syntheticEnterpriseESD(t)
	p := Packages{Tools: Tools{Log: io.Discard}}
	source := WindowsSource{Kind: "esd", Edition: "enterprise"}
	out := t.TempDir()
	iso, err := p.prepareWindowsISO(t.Context(), source, path, out)
	must(t, err)
	must(t, isoinspect.RetargetElToritoUEFI(iso, "efi/microsoft/boot/efisys_noprompt.bin"))
	info, err := isoinspect.Inspect(iso)
	must(t, err)
	if info.ElTorito == nil || info.ElTorito.UEFI == nil {
		t.Fatal("missing UEFI boot entry")
	}
	source.Edition = "education"
	if _, err := p.prepareWindowsISO(t.Context(), source, path, t.TempDir()); err == nil {
		t.Fatal("invented missing edition")
	}
	if _, err := p.prepareWindowsISO(t.Context(), source, "missing", t.TempDir()); err == nil {
		t.Fatal("missing ESD")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := p.prepareWindowsISO(ctx, source, path, t.TempDir()); err == nil {
		t.Fatal("cancelled preparation")
	}
}

func TestESDRejectsMalformedCatalogueAndLockedPaths(t *testing.T) {
	p := Packages{
		Downloader: Downloader{
			Client: &http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: 200,
						Header:     http.Header{},
						Request:    r,
						Body:       io.NopCloser(strings.NewReader("invalid cabinet")),
					}, nil
				}),
			},
		},
	}
	if _, err := p.WindowsESDSources(
		t.Context(),
		WindowsSelection{"enterprise-latest", "amd64", "en-US"},
	); err == nil {
		t.Fatal("malformed catalogue accepted")
	}
	path := syntheticEnterpriseESD(t)
	out := t.TempDir()
	must(t, os.Mkdir(filepath.Join(out, "media-work"), 0o750))
	if _, err := p.prepareWindowsISO(
		t.Context(),
		WindowsSource{Kind: "esd", Edition: "enterprise"},
		path,
		out,
	); err == nil {
		t.Fatal("reused media assembly directory")
	}
	sources, err := selectWindowsESD(
		&esdapi.ESDCatalog{Images: []esdapi.ESDImage{esdFixture()}},
		WindowsSelection{"enterprise-latest", "amd64", "en-US"},
	)
	must(t, err)
	for _, uri := range []string{"://bad", "https://example.com/esd"} {
		bad := esdFixture()
		bad.URL = uri
		if _, err := selectWindowsESD(
			&esdapi.ESDCatalog{Images: []esdapi.ESDImage{bad}},
			WindowsSelection{"enterprise-latest", "amd64", "en-US"},
		); err == nil {
			t.Fatal("invalid URL accepted")
		}
	}
	if !windowsInstalledVersionMatches(
		WindowsInstallResult{Build: "26200.1", Release: "25H2"},
		sources[0],
	) {
		t.Fatal("valid version")
	}
}

func TestESDCatalogueRejectsMislabeledAndOldMedia(t *testing.T) {
	base := esdFixture()
	old := base
	old.FileName = strings.Replace(base.FileName, "26200", "19041", 1)
	mislabeled := base
	mislabeled.Architecture = "ARM64"
	newer := base
	newer.FileName = strings.Replace(base.FileName, "26200", "29000", 1)
	selection := WindowsSelection{"enterprise-latest", "amd64", "en-US"}
	sources, err := selectWindowsESD(
		&esdapi.ESDCatalog{Images: []esdapi.ESDImage{base, old, mislabeled, newer}},
		selection,
	)
	must(t, err)
	if len(sources) != 2 || sources[0].BuildMajor != 29000 || sources[0].Release != "29000" {
		t.Fatal(sources)
	}
	bad := sources[0]
	bad.URI = "://invalid"
	if err := bad.validateESD(); err == nil {
		t.Fatal("invalid locked URI")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p := Packages{Downloader: Downloader{Client: &http.Client{Timeout: time.Second}}}
	if _, err := p.ResolveWindows(ctx, selection); err == nil {
		t.Fatal("cancelled ESD resolution")
	}
	out := t.TempDir()
	must(t, os.Mkdir(filepath.Join(out, "install.iso"), 0o700))
	if _, err := p.prepareWindowsISO(
		t.Context(),
		WindowsSource{Kind: "esd", Edition: "enterprise"},
		syntheticEnterpriseESD(t),
		out,
	); err == nil {
		t.Fatal("replaced installation media")
	}
}
