package imagebuild

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	mediaiso "github.com/deploymenttheory/go-sdk-winmediafoundry/pkg/iso"
	swconst "github.com/deploymenttheory/go-sdk-winmediafoundry/softwaredownload/constants"
	"github.com/deploymenttheory/go-sdk-winmediafoundry/softwaredownload/shared/models"

	"github.com/weaveplatform/weaveplatform-oci/pkg/disk/vhd"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
)

func windowsFixture() (WindowsSelection, WindowsSource) {
	selection := WindowsSelection{FromWindows: "pro-26h2", Arch: "amd64", Language: "en-US"}
	s := WindowsSource{
		Release:  "26h2",
		Edition:  "pro",
		Arch:     "amd64",
		Language: "en-US",
		FileName: "Win11_26H2_English_x64.iso",
		Size:     512,
		SHA256:   strings.Repeat("a", 64),
	}
	s.URI = "https://software.download.prss.microsoft.com/" + s.FileName + "?token=example"
	return selection, s
}

func TestWindowsVersionSelectors(t *testing.T) {
	selection, source := windowsFixture()
	must(t, source.validate(selection, true))
	for _, value := range []string{"pro-26h2", "home-26h2", "pro-latest", "pro-25h2"} {
		s := selection
		s.FromWindows = value
		_, _, err := ParseWindowsSelection(s)
		must(t, err)
	}
	for _, s := range []WindowsSelection{{"pro-26h3", "amd64", "en-US"}, {"invalid-26h2", "amd64", "en-US"}, {"pro-26h2", "x64", "en-US"}, {"pro-26h2", "amd64", "../bad"}, {"26h2", "amd64", "en-US"}} {
		if _, _, err := ParseWindowsSelection(s); err == nil {
			t.Fatal(s)
		}
	}
	for _, mutate := range []func(*WindowsSource){
		func(s *WindowsSource) { s.Release = "25h2" }, func(s *WindowsSource) { s.Edition = "home" }, func(s *WindowsSource) { s.Arch = "arm64" }, func(s *WindowsSource) { s.Language = "de-DE" }, func(s *WindowsSource) { s.URI = "http://software.download.prss.microsoft.com/" + s.FileName }, func(s *WindowsSource) { s.URI = "https://example.com/" + s.FileName }, func(s *WindowsSource) { s.FileName = "../bad.iso" }, func(s *WindowsSource) { s.SHA256 = "" }, func(s *WindowsSource) { s.Size = 0 }, func(s *WindowsSource) {
			s.FileName = "Win11_25H2_English_x64.iso"
			s.URI = "https://software.download.prss.microsoft.com/" + s.FileName
		},
	} {
		s := source
		mutate(&s)
		if s.validate(selection, true) == nil {
			t.Fatal("accepted changed source", s)
		}
	}
	link := models.DownloadLink{
		FileName:  source.FileName,
		URL:       source.URI,
		SizeBytes: source.Size,
		Arch:      swconst.Arch("x64"),
		Language:  models.Language{Name: "en-US"},
	}
	got, err := windowsSource(link, selection, "pro", "26h2")
	must(t, err)
	if got.SHA256 != "" || got.Release != "26h2" {
		t.Fatal(got)
	}
	if _, err := windowsSource(link, selection, "pro", "25h2"); err == nil {
		t.Fatal("silent release fallback")
	}
	latest := selection
	latest.FromWindows = "pro-latest"
	_, err = windowsSource(link, latest, "pro", "latest")
	must(t, err)
	link.Arch = "ARM64"
	if _, err := windowsSource(link, selection, "pro", "26h2"); err == nil {
		t.Fatal("wrong architecture")
	}
	arm := selection
	arm.Arch = "arm64"
	_, err = windowsSource(link, arm, "pro", "26h2")
	must(t, err)
	link.FileName = "unknown.iso"
	if _, err := windowsSource(link, selection, "pro", "26h2"); err == nil {
		t.Fatal("unidentifiable media")
	}
}

func TestWindowsPinnedMedia(t *testing.T) {
	selection, s := windowsFixture()
	data := []byte("verified Windows media")
	m := mediaFor(s.URI, data)
	s.SHA256, s.Size = m.SHA256, m.Size
	cache := t.TempDir()
	file := filepath.Join(cache, s.SHA256, s.FileName)
	must(t, os.MkdirAll(filepath.Dir(file), 0o750))
	must(t, os.WriteFile(file, data, 0o600))
	got, path, err := (Packages{}).AcquireWindows(t.Context(), s, selection, cache)
	must(t, err)
	if path != file || got != s {
		t.Fatal(got, path)
	}
	digest, err := hashSizedFile(file, s.Size)
	must(t, err)
	if digest != s.SHA256 {
		t.Fatal(digest)
	}
	if _, err := hashSizedFile(file, s.Size+1); err == nil {
		t.Fatal("wrong length")
	}
	if _, err := hashSizedFile(filepath.Join(cache, "missing"), 1); err == nil {
		t.Fatal("missing file")
	}
	must(t, os.WriteFile(file, []byte("corrupt Windows media!"), 0o600))
	if _, _, err := (Packages{}).AcquireWindows(t.Context(), s, selection, cache); err == nil {
		t.Fatal("cache corruption accepted")
	}
	s.Release = "25h2"
	if _, _, err := (Packages{}).AcquireWindows(t.Context(), s, selection, cache); err == nil {
		t.Fatal("selector ignored")
	}
}

func TestWindowsRecipeAndCompletion(t *testing.T) {
	_, s := windowsFixture()
	marker := "WEAVE-IMAGE-READY-0123456789abcdef01234567"
	for _, arch := range []string{"amd64", "arm64"} {
		for _, edition := range []string{"pro", "home", "enterprise", "education"} {
			s.Arch, s.Edition = arch, edition
			answer, script, err := windowsRecipe(s, marker)
			must(t, err)
			decoder := xml.NewDecoder(strings.NewReader(answer))
			for {
				_, err := decoder.Token()
				if err == io.EOF {
					break
				}
				must(t, err)
			}
			for _, want := range []string{`processorArchitecture="` + arch + `"`, `<Mode>Audit</Mode>`, `<WillWipeDisk>true</WillWipeDisk>`, `WEAVE-SEED`} {
				if !strings.Contains(answer, want) {
					t.Fatal(want)
				}
			}
			for _, want := range []string{"/generalize", "GENERALIZE_RESEAL_TO_OOBE", "bcdboot.exe", marker, "DisplayVersion", "FullyDecrypted"} {
				if !strings.Contains(script, want) {
					t.Fatal(want)
				}
			}
			for _, forbidden := range []string{"AutoLogon", "Password", "BypassTPM", "BypassSecureBoot"} {
				if strings.Contains(answer+script, forbidden) {
					t.Fatal(forbidden)
				}
			}
		}
	}
	if _, _, err := windowsRecipe(s, "predictable"); err == nil {
		t.Fatal("bad marker")
	}
	s.Arch = "other"
	if _, _, err := windowsRecipe(s, marker); err == nil {
		t.Fatal("bad architecture")
	}
	result := WindowsInstallResult{
		OSVersion:   "10.0.27000.1",
		Build:       "27000.1",
		Edition:     "Professional",
		Release:     "26H2",
		Generalized: true,
	}
	raw, err := json.Marshal(result)
	must(t, err)
	got, err := windowsReceipt(marker+" "+string(raw), marker)
	must(t, err)
	if got != result {
		t.Fatal(got)
	}
	for _, line := range []string{"wrong " + string(raw), marker + " invalid", marker + ` {"generalized":false}`} {
		if _, err := windowsReceipt(line, marker); err == nil {
			t.Fatal("accepted invalid receipt")
		}
	}
	_, s = windowsFixture()
	iso, err := windowsSeed(t.TempDir(), s, marker)
	must(t, err)
	stat, err := os.Stat(iso)
	must(t, err)
	if stat.Size() == 0 {
		t.Fatal("empty seed")
	}
}

func TestWindowsHCSIsolationDocument(t *testing.T) {
	d := windowsDocument("disk", "install", "seed", "firmware", "serial")
	vm := d["VirtualMachine"].(map[string]any)
	sec := vm["SecuritySettings"].(map[string]any)
	if sec["EnableTpm"] != true || sec["Isolation"].(map[string]any)["HclEnabled"] != true {
		t.Fatal(sec)
	}
	uefi := vm["Chipset"].(map[string]any)["Uefi"].(map[string]any)
	if uefi["ApplySecureBootTemplate"] != "Apply" ||
		uefi["SecureBootTemplateId"] != "1734c6e8-3154-4dda-ba5f-a874cc483422" {
		t.Fatal(uefi)
	}
	if _, ok := uefi["BootThis"]; ok {
		t.Fatal("explicit boot entry incompatible with isolated HCS")
	}
	if _, ok := vm["Devices"].(map[string]any)["NetworkAdapters"]; ok {
		t.Fatal("installer should be offline")
	}
	if d["ShouldTerminateOnLastHandleClosed"] != true {
		t.Fatal("unmanaged VM lifetime")
	}
}

func TestWindowsBundleDropsBuildIdentity(t *testing.T) {
	_, s := windowsFixture()
	out := t.TempDir()
	disk := filepath.Join(out, "disk.vhd")
	must(t, os.WriteFile(disk, make([]byte, 1024), 0o600))
	must(t, vhd.Append(disk, time.Now()))
	tools := Tools{Run: func(_ context.Context, w, _ io.Writer, _ string, _ ...string) error {
		_, err := fmt.Fprint(w, "abc123")
		return err
	}}
	bundle, err := tools.windowsBundle(
		t.Context(),
		WindowsOptions{Out: out, Revision: 2},
		s,
		WindowsInstallResult{
			OSVersion:   "10.0.27000.1",
			Build:       "27000.1",
			Edition:     "Professional",
			Release:     "26H2",
			Generalized: true,
		},
	)
	must(t, err)
	loaded, err := pack.LoadBundle(bundle)
	must(t, err)
	f := loaded.File
	if f.Guest.OS != "windows" || f.Guest.OSBuild != "27000.1" || len(f.State) != 1 ||
		string(f.State[0].Semantics) != "regenerate" {
		t.Fatal(f)
	}
	stat, err := os.Stat(filepath.Join(bundle, "disk0.img"))
	must(t, err)
	if stat.Size() != 1024 {
		t.Fatal("VHD footer retained")
	}
	if _, err := os.Stat(filepath.Join(bundle, "build.vmgs")); !os.IsNotExist(err) {
		t.Fatal("firmware identity exported")
	}
	if _, err := tools.windowsBundle(
		t.Context(),
		WindowsOptions{Out: out, Revision: 2},
		s,
		WindowsInstallResult{},
	); err == nil {
		t.Fatal("overwrote bundle")
	}
}

func TestWindowsSDKAcquisition(t *testing.T) {
	selection, s := windowsFixture()
	data := []byte("SDK media payload")
	s.Size = int64(len(data))
	s.SHA256 = ""
	calls := 0
	p := Packages{
		Downloader: Downloader{
			Client: &http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if r.URL.String() != s.URI {
						t.Fatal(r.URL)
					}
					return &http.Response{
						StatusCode:    200,
						Header:        http.Header{},
						Body:          io.NopCloser(bytes.NewReader(data)),
						ContentLength: int64(len(data)),
						Request:       r,
					}, nil
				}),
				Timeout: time.Second,
			},
		},
	}
	cache := t.TempDir()
	got, file, err := p.AcquireWindows(t.Context(), s, selection, cache)
	must(t, err)
	expected := mediaFor(s.URI, data)
	if got.SHA256 != expected.SHA256 || calls != 1 {
		t.Fatal(got, calls)
	}
	must(t, verifiedFile(file, expected))
	_, _, err = p.AcquireWindows(t.Context(), s, selection, cache)
	must(t, err)
	bad := s
	bad.Size++
	if _, _, err := p.AcquireWindows(t.Context(), bad, selection, t.TempDir()); err == nil {
		t.Fatal("short download accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (Packages{}).ResolveWindows(ctx, selection); err == nil {
		t.Fatal("cancelled resolution succeeded")
	}
	if _, err := p.ResolveWindows(t.Context(), WindowsSelection{}); err == nil {
		t.Fatal("invalid resolution succeeded")
	}
}

func makeWindowsISO(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "efi", "microsoft", "boot")
	must(t, os.MkdirAll(dir, 0o750))
	for _, name := range []string{"efisys.bin", "efisys_noprompt.bin"} {
		must(t, os.WriteFile(filepath.Join(dir, name), make([]byte, 4096), 0o600))
	}
	iso := filepath.Join(t.TempDir(), "install.iso")
	must(t, mediaiso.BuildWindowsUDF(root, iso, "TEST"))
	return iso
}

func TestWindowsDirectLibraryOrchestration(t *testing.T) {
	selection, s := windowsFixture()
	if runtime.GOOS == "windows" {
		selection.Arch = runtime.GOARCH
		s.Arch = runtime.GOARCH
	}
	iso := makeWindowsISO(t)
	stat, err := os.Stat(iso)
	must(t, err)
	s.Size = stat.Size()
	s.SHA256, err = hashSizedFile(iso, s.Size)
	must(t, err)
	cache := t.TempDir()
	cached := filepath.Join(cache, s.SHA256, s.FileName)
	must(t, os.MkdirAll(filepath.Dir(cached), 0o750))
	must(t, copyFile(iso, cached))
	lock := filepath.Join(t.TempDir(), "source.json")
	must(t, WriteSourceLock(lock, s))
	if WriteSourceLock(lock, s) == nil {
		t.Fatal("overwrote source lock")
	}
	for _, mode := range []string{"success", "install-fails", "not-generalized", "wrong-release", "wrong-edition"} {
		t.Run(mode, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "candidate")
			p := Packages{
				Tools: Tools{
					Run: func(_ context.Context, w, _ io.Writer, name string, _ ...string) error {
						if name != "git" {
							t.Fatalf("unexpected executable %s", name)
						}
						_, err := fmt.Fprint(w, "abcdef")
						return err
					},
				},
				WindowsInstall: func(_ context.Context, r WindowsInstallRequest) (WindowsInstallResult, error) {
					result := WindowsInstallResult{
						OSVersion:   "10.0.27000.1",
						Build:       "27000.1",
						Edition:     "Professional",
						Release:     "26H2",
						Generalized: true,
					}
					if r.ISO == cached || !strings.HasPrefix(r.Marker, "WEAVE-IMAGE-READY-") {
						t.Fatal(r)
					}
					if mode == "install-fails" {
						return result, ErrInput
					}
					if mode == "not-generalized" {
						result.Generalized = false
					}
					if mode == "wrong-release" {
						result.Release = "25H2"
					}
					if mode == "wrong-edition" {
						result.Edition = "Core"
					}
					disk := filepath.Join(r.Directory, "disk.vhd")
					must(t, os.WriteFile(disk, make([]byte, 1024), 0o600))
					must(t, vhd.Append(disk, time.Now()))
					return result, nil
				},
			}
			bundle, err := p.BuildWindows(
				t.Context(),
				WindowsOptions{
					Selection:  selection,
					SourceLock: lock,
					Out:        out,
					Cache:      cache,
					Revision:   1,
					Timeout:    time.Minute,
				},
			)
			if mode == "success" {
				must(t, err)
				if _, err := pack.LoadBundle(bundle); err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("invalid install exported")
			}
			// The retargeted installer is a copy. Never mutate content-addressed media.
			must(t, verifiedFile(cached, Media{Size: s.Size, SHA256: s.SHA256}))
		})
	}
	p := Packages{
		WindowsInstall: func(context.Context, WindowsInstallRequest) (WindowsInstallResult, error) {
			t.Fatal("should fail preflight")
			return WindowsInstallResult{}, nil
		},
	}
	for _, o := range []WindowsOptions{{}, {Selection: selection}, {Selection: selection, Revision: 1, Timeout: time.Minute, Out: filepath.Join(t.TempDir(), "out"), Cache: cache, SourceLock: "missing"}} {
		if _, err := p.BuildWindows(t.Context(), o); err == nil {
			t.Fatal("invalid build accepted")
		}
	}
	if runtime.GOOS != "windows" {
		if _, err := (Packages{}).BuildWindows(t.Context(), WindowsOptions{}); err == nil {
			t.Fatal("unsupported host")
		}
		if _, err := installWindowsNative(t.Context(), WindowsInstallRequest{}); err == nil {
			t.Fatal("unsupported native install")
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWindowsRefusesIncompleteCandidates(t *testing.T) {
	selection, s := windowsFixture()
	if runtime.GOOS == "windows" {
		selection.Arch = runtime.GOARCH
		s.Arch = runtime.GOARCH
	}
	p := Packages{
		WindowsInstall: func(context.Context, WindowsInstallRequest) (WindowsInstallResult, error) {
			t.Fatal("must fail before HCS")
			return WindowsInstallResult{}, nil
		},
	}
	for _, mode := range []string{"existing-output", "invalid-lock", "wrong-edition", "corrupt-media", "blocked-cache"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			cache := filepath.Join(root, "cache")
			out := filepath.Join(root, "candidate")
			source := s
			data := []byte("not a bootable ISO")
			media := mediaFor(source.URI, data)
			source.Size, source.SHA256 = media.Size, media.SHA256
			switch mode {
			case "existing-output":
				must(t, os.Mkdir(out, 0o750))
			case "wrong-edition":
				source.Edition = "home"
			case "corrupt-media":
				dir := filepath.Join(cache, source.SHA256)
				must(t, os.MkdirAll(dir, 0o750))
				must(t, os.WriteFile(filepath.Join(dir, source.FileName), data, 0o600))
			case "blocked-cache":
				must(t, os.WriteFile(cache, nil, 0o600))
			}
			lock := filepath.Join(root, "source.json")
			must(t, writeJSON(lock, source))
			if mode == "invalid-lock" {
				must(t, os.WriteFile(lock, []byte("{"), 0o600))
			}
			if _, err := p.BuildWindows(
				t.Context(),
				WindowsOptions{
					Selection:  selection,
					SourceLock: lock,
					Out:        out,
					Cache:      cache,
					Revision:   1,
					Timeout:    time.Minute,
				},
			); err == nil {
				t.Fatal("invalid candidate accepted")
			}
			if _, err := os.Stat(filepath.Join(out, "bundle", "bundle.json")); !os.IsNotExist(err) {
				t.Fatal("exported failed candidate")
			}
		})
	}
	_, err := (Tools{}).windowsBundle(
		t.Context(),
		WindowsOptions{Out: t.TempDir()},
		s,
		WindowsInstallResult{},
	)
	if err == nil {
		t.Fatal("missing VHD exported")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := p.BuildWindows(
		ctx,
		WindowsOptions{
			Selection: selection,
			Out:       filepath.Join(t.TempDir(), "out"),
			Cache:     t.TempDir(),
			Revision:  1,
			Timeout:   time.Minute,
		},
	); err == nil {
		t.Fatal("cancelled resolution accepted")
	}
}

func TestWindowsBuildRefusesUnwritableOutputs(t *testing.T) {
	selection, source := windowsFixture()
	if runtime.GOOS == "windows" {
		selection.Arch = runtime.GOARCH
		source.Arch = runtime.GOARCH
	}
	iso := makeWindowsISO(t)
	data, err := os.ReadFile(iso)
	must(t, err)
	pin := mediaFor(source.URI, data)
	source.Size, source.SHA256 = pin.Size, pin.SHA256
	lock := filepath.Join(t.TempDir(), "lock.json")
	must(t, WriteSourceLock(lock, source))
	for _, blocked := range []string{"source-lock.json", "install.iso", "seed", "install-result.json"} {
		t.Run(blocked, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "candidate")
			p := Packages{
				Downloader: Downloader{
					Client: &http.Client{
						Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
							must(t, os.Mkdir(filepath.Join(out, blocked), 0o700))
							return &http.Response{
								StatusCode:    200,
								Header:        http.Header{},
								Request:       r,
								Body:          io.NopCloser(bytes.NewReader(data)),
								ContentLength: int64(len(data)),
							}, nil
						}),
					},
				},
				WindowsInstall: func(context.Context, WindowsInstallRequest) (WindowsInstallResult, error) {
					if blocked != "install-result.json" {
						t.Fatal("started VM before preparing outputs")
					}
					return WindowsInstallResult{
						OSVersion:   "10.0.27000.1",
						Build:       "27000.1",
						Edition:     "Professional",
						Release:     "26H2",
						Generalized: true,
					}, nil
				},
			}
			if _, err := p.BuildWindows(
				t.Context(),
				WindowsOptions{
					Selection:  selection,
					SourceLock: lock,
					Out:        out,
					Cache:      t.TempDir(),
					Revision:   1,
					Timeout:    time.Minute,
				},
			); err == nil {
				t.Fatal("ignored output write failure")
			}
			if _, err := os.Stat(filepath.Join(out, "bundle")); !os.IsNotExist(err) {
				t.Fatal("exported failed candidate")
			}
		})
	}
}

func TestWindowsAcquisitionCacheFailures(t *testing.T) {
	selection, source := windowsFixture()
	source.SHA256 = ""
	data := []byte("media")
	source.Size = int64(len(data))
	pin := mediaFor(source.URI, data)
	for _, mode := range []string{"download", "cache-parent", "promotion"} {
		t.Run(mode, func(t *testing.T) {
			cache := t.TempDir()
			p := Packages{
				Downloader: Downloader{
					Client: &http.Client{
						Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
							if mode == "download" {
								return nil, context.Canceled
							}
							parent := filepath.Join(cache, pin.SHA256)
							if mode == "cache-parent" {
								must(t, os.WriteFile(parent, nil, 0o600))
							}
							if mode == "promotion" {
								must(t, os.Mkdir(parent, 0o700))
								dest := filepath.Join(parent, source.FileName)
								must(t, os.Symlink(dest, dest))
							}
							return &http.Response{
								StatusCode:    200,
								Request:       r,
								Header:        http.Header{},
								ContentLength: int64(len(data)),
								Body:          io.NopCloser(bytes.NewReader(data)),
							}, nil
						}),
					},
				},
			}
			if _, _, err := p.AcquireWindows(t.Context(), source, selection, cache); err == nil {
				t.Fatal("ignored " + mode)
			}
		})
	}
}

func TestWindowsSourceFilenameMatchesURL(t *testing.T) {
	selection, s := windowsFixture()
	s.URI = "https://software.download.prss.microsoft.com/different.iso"
	if err := s.validate(selection, true); err == nil {
		t.Fatal("lock filename differs from download")
	}
}
