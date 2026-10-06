package imagebuild

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func appleFixture() AppleSource {
	return AppleSource{
		URL:     "https://updates.cdn-apple.com/path/restore.ipsw",
		Version: "26.6.2",
		Build:   "25G83",
		SHA256:  strings.Repeat("a", 64),
		Size:    100,
	}
}

func makeIPSW(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "restore.ipsw")
	f, e := os.Create(file)
	must(t, e)
	z := zip.NewWriter(f)
	w, e := z.Create("BuildManifest.plist")
	must(t, e)
	_, e = w.Write([]byte("plist"))
	must(t, e)
	must(t, z.Close())
	must(t, f.Close())
	return file
}

func TestAppleVersionSelection(t *testing.T) {
	latest := appleFixture()
	older := latest
	older.Version = "26.6.1"
	older.Build = "25G80"
	other := latest
	other.Version = "27.0.1"
	other.Build = "26A434"
	beta := latest
	beta.Build = "25G83a"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).
			Encode(map[string]any{"identifier": "VirtualMac2,1", "firmwares": []AppleSource{older, beta, other, latest}})
	}))
	defer server.Close()
	for _, version := range []string{"26", "26.6.2", "27"} {
		sources, e := macSources(t.Context(), server.Client(), server.URL, version)
		must(t, e)
		if !macVersionMatches(sources[0].Version, version) {
			t.Fatal(sources)
		}
	}
	sources, e := macSources(t.Context(), server.Client(), server.URL, "26")
	must(t, e)
	if len(sources) != 2 || sources[0].Version != "26.6.2" {
		t.Fatal(sources)
	}
	for _, version := range []string{"latest", "26.7", "26.6.2.1", "../../26"} {
		if _, e := macSources(t.Context(), server.Client(), server.URL, version); e == nil {
			t.Fatal("accepted " + version)
		}
	}
	for _, mutate := range []func(*AppleSource){func(s *AppleSource) { s.URL = "http://example.com/a.ipsw" }, func(s *AppleSource) { s.Version = "27.0" }, func(s *AppleSource) { s.Build = "../bad" }, func(s *AppleSource) { s.SHA256 = "bad" }} {
		s := latest
		mutate(&s)
		if _, e := s.validate("26", 1); e == nil {
			t.Fatal("invalid source accepted")
		}
	}
	if _, e := latest.validate("26.6.1", 1); e == nil {
		t.Fatal("exact version silently changed")
	}
}

func TestMacSourceLockResume(t *testing.T) {
	s := appleFixture()
	raw, e := json.Marshal(s)
	must(t, e)
	out := filepath.Join(t.TempDir(), "candidate")
	must(t, macCandidate(out, s, raw, false))
	must(t, macCandidate(out, s, raw, true))
	if macCandidate(out, s, raw, false) == nil {
		t.Fatal("overwrote candidate")
	}
	changed := s
	changed.Size++
	if macCandidate(out, changed, raw, true) == nil {
		t.Fatal("changed source accepted")
	}
	got, _, e := (Packages{}).appleSource(
		t.Context(),
		MacOptions{SourceLock: filepath.Join(out, "source-lock.json")},
	)
	must(t, e)
	if got != s {
		t.Fatal(got)
	}
	must(t, os.WriteFile(filepath.Join(out, "restore.log"), nil, 0o600))
	if macCandidate(out, s, raw, true) == nil {
		t.Fatal("resumed restored candidate")
	}
	if _, _, e := (Packages{}).appleSource(
		t.Context(),
		MacOptions{SourceLock: "missing"},
	); e == nil {
		t.Fatal("missing source")
	}
}

func TestMacDirectLibraryOrchestration(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("Apple host coordinator")
	}
	work := t.TempDir()
	t.Setenv("WEAVE_IMAGE_WORKSPACE", work)
	file := makeIPSW(t)
	raw, e := os.ReadFile(file)
	must(t, e)
	s := appleFixture()
	pin := mediaFor(s.URL, raw)
	s.SHA256, s.Size = pin.SHA256, pin.Size
	cached := filepath.Join(work, "media", "ipsw", s.SHA256)
	must(t, os.MkdirAll(cached, 0o750))
	must(t, copyFile(file, filepath.Join(cached, "restore.ipsw")))
	lock := filepath.Join(work, "source.json")
	must(t, writeJSON(lock, s))
	calls := 0
	mode := ""
	p := Packages{
		Tools: Tools{
			Run: func(_ context.Context, w, _ io.Writer, name string, args ...string) error {
				if mode == name {
					return ErrInput
				}
				switch name {
				case "mount":
					fmt.Fprintf(w, "disk on %s (apfs, local)", work)
				case "df":
					fmt.Fprint(w, "disk 999999999 0 999999999 0% /work")
				case "plutil":
					v := s.Version
					if args[1] == "ProductBuildVersion" {
						v = s.Build
					}
					fmt.Fprint(w, v)
				case "git":
					fmt.Fprint(w, "abcdef")
				default:
					t.Fatalf("unexpected executable dependency: %s", name)
				}
				return nil
			},
		},
		MacRestore: func(_ context.Context, r MacRestoreRequest) (MacRestoreResult, error) {
			if mode == "restore" {
				return MacRestoreResult{}, ErrInput
			}
			calls++
			if r.IPSW != filepath.Join(cached, "restore.ipsw") || r.DiskSize != 80<<30 {
				t.Fatal(r)
			}
			for _, name := range []string{"disk0.img", "auxstorage.bin"} {
				must(t, os.WriteFile(filepath.Join(r.Directory, name), []byte(name), 0o600))
			}
			return MacRestoreResult{
				HardwareModel: "bW9kZWw=",
				CPUCountMin:   2,
				CPUCount:      4,
				MemorySizeMin: 2 << 30,
				MemorySize:    4 << 30,
			}, nil
		},
	}
	bundle, e := p.BuildMacOS(
		t.Context(),
		MacOptions{Version: "26.6.2", SourceLock: lock, Revision: 1},
	)
	must(t, e)
	var f map[string]any
	must(t, readJSON(filepath.Join(bundle, "bundle.json"), &f))
	if calls != 1 {
		t.Fatal(calls)
	}
	if !strings.Contains(bundle, "macos-26-base") {
		t.Fatal(bundle)
	}
	if _, e := os.Stat(filepath.Join(bundle, "auxstorage.bin")); e != nil {
		t.Fatal(e)
	}
	for i, failure := range []string{"mount", "df", "plutil", "git", "restore", "native"} {
		mode = failure
		if failure == "native" {
			p.MacRestore = nil
		}
		if _, err := p.BuildMacOS(
			t.Context(),
			MacOptions{Version: "26", SourceLock: lock, Revision: i + 2},
		); err == nil {
			t.Fatal("ignored " + failure + " failure")
		}
	}
	mode = ""
	for _, o := range []MacOptions{{Version: "27", SourceLock: lock, Revision: 1}, {Version: "26", SourceLock: "missing", Revision: 1}, {Version: "26", SourceLock: lock, Revision: 1}} {
		if _, err := p.BuildMacOS(t.Context(), o); err == nil {
			t.Fatal("invalid/reused candidate accepted")
		}
	}
	must(t, os.WriteFile(filepath.Join(cached, "restore.ipsw"), []byte("corrupt"), 0o600))
	if _, err := p.BuildMacOS(
		t.Context(),
		MacOptions{Version: "26", SourceLock: lock, Revision: 10},
	); err == nil {
		t.Fatal("tampered media accepted")
	}
}

func TestNativeRestoreCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, e := restoreMacOSNative(ctx, MacRestoreRequest{}); e == nil {
		t.Fatal("cancelled native restore succeeded")
	}
	if _, err := restoreMacOSNative(t.Context(), MacRestoreRequest{IPSW: "missing"}); err == nil {
		t.Fatal("missing native media")
	}
}

func TestAppleCatalogueRefusalsAndOrdering(t *testing.T) {
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{`, 200}, {`{"identifier":"iPhone1,1"}`, 200}, {`{"identifier":"VirtualMac2,1","firmwares":[]}`, 200}, {`{}`, 503}, {strings.Repeat("x", 8*1024*1024+1), 200},
	} {
		client := &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: tc.status,
					Body:       io.NopCloser(strings.NewReader(tc.body)),
					Header:     http.Header{},
					Request:    r,
				}, nil
			}),
		}
		if _, err := MacSources(t.Context(), client, "26"); err == nil {
			t.Fatal("invalid catalogue accepted")
		}
	}
	if _, err := macSources(t.Context(), nil, "://invalid", "26"); err == nil {
		t.Fatal("bad URL")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := MacSources(ctx, nil, "26"); err == nil {
		t.Fatal("cancelled request")
	}
	base := appleFixture()
	versions := make([]AppleSource, 5)
	for i := range versions {
		versions[i] = base
	}
	versions[0].Build = "25G99"
	versions[1].Build = "25G100"
	versions[2].Signed = true
	versions[3].Build = "25H1"
	versions[4].ReleaseDate = "2026-10-01"
	raw, err := json.Marshal(map[string]any{"identifier": "VirtualMac2,1", "firmwares": versions})
	must(t, err)
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(bytes.NewReader(raw)),
			Header:     http.Header{},
			Request:    r,
		}, nil
	})}
	sources, err := MacSources(t.Context(), client, "26")
	must(t, err)
	if !sources[0].Signed || sources[1].ReleaseDate == "" || sources[2].Build != "25H1" ||
		sources[3].Build != "25G100" {
		t.Fatal(sources)
	}
	p := Packages{Downloader: Downloader{Client: client}}
	source, _, err := p.appleSource(t.Context(), MacOptions{Version: "26"})
	must(t, err)
	if !source.Signed {
		t.Fatal(source)
	}
	versions[0].URL = "https://example.com/wrong.ipsw"
	raw, err = json.Marshal(map[string]any{"identifier": "VirtualMac2,1", "firmwares": versions})
	must(t, err)
	if _, _, err := p.appleSource(t.Context(), MacOptions{Version: "26"}); err == nil {
		t.Fatal("bad source")
	}
	bad := base
	bad.URL = "://bad"
	if _, err := bad.validate("26", 1); err == nil {
		t.Fatal("bad URL")
	}
	if _, err := base.validate("bad", 1); err == nil {
		t.Fatal("bad selector")
	}
	lock := filepath.Join(t.TempDir(), "bad.json")
	must(t, os.WriteFile(lock, []byte("{"), 0o600))
	if _, _, err := p.appleSource(t.Context(), MacOptions{SourceLock: lock}); err == nil {
		t.Fatal("bad lock")
	}
}

func TestMacWorkspaceAndManifestRefusals(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		if _, err := (Tools{}).macWorkspace(t.Context()); err == nil {
			t.Fatal("unsupported host")
		}
		return
	}
	work := t.TempDir()
	t.Setenv("WEAVE_IMAGE_WORKSPACE", "")
	if _, err := (Tools{}).macWorkspace(t.Context()); err == nil {
		t.Fatal("missing workspace")
	}
	t.Setenv("WEAVE_IMAGE_WORKSPACE", work)
	for _, mode := range []string{"mount-error", "wrong-mount", "df-error", "short-df", "bad-free", "no-space"} {
		tools := Tools{
			Run: func(_ context.Context, w, _ io.Writer, name string, _ ...string) error {
				if name == "mount" {
					if mode == "mount-error" {
						return ErrInput
					}
					if mode != "wrong-mount" {
						fmt.Fprintf(w, "disk on %s (apfs, local)", work)
					}
					return nil
				}
				switch mode {
				case "df-error":
					return ErrInput
				case "short-df":
					fmt.Fprint(w, "short")
				case "bad-free":
					fmt.Fprint(w, "disk 99 1 invalid")
				default:
					fmt.Fprint(w, "disk 99 1 0")
				}
				return nil
			},
		}
		if _, err := tools.macWorkspace(t.Context()); err == nil {
			t.Fatal(mode)
		}
	}
	s := appleFixture()
	if err := (Tools{}).verifyIPSW(t.Context(), "missing", work, s); err == nil {
		t.Fatal("missing IPSW")
	}
	iso := makeIPSW(t)
	for _, fail := range []bool{false, true} {
		tools := Tools{Run: func(_ context.Context, w, _ io.Writer, _ string, _ ...string) error {
			if fail {
				return ErrInput
			}
			fmt.Fprint(w, "wrong")
			return nil
		}}
		if err := tools.verifyIPSW(t.Context(), iso, work, s); err == nil {
			t.Fatal("mismatched manifest")
		}
	}
	for _, size := range []int64{-1, 1024, 64<<30 + 1} {
		if _, err := (Packages{}).BuildMacOS(t.Context(), MacOptions{DiskSize: size}); err == nil {
			t.Fatal("invalid disk size")
		}
	}
}

func TestIPSWManifestFailures(t *testing.T) {
	for _, mode := range []string{"missing", "oversized", "crc"} {
		t.Run(mode, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "image.ipsw")
			var archive bytes.Buffer
			z := zip.NewWriter(&archive)
			name := "BuildManifest.plist"
			if mode == "missing" {
				name = "other"
			}
			w, err := z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
			must(t, err)
			data := []byte("metadata")
			if mode == "oversized" {
				data = bytes.Repeat([]byte("x"), 64*1024*1024+1)
			}
			_, err = w.Write(data)
			must(t, err)
			must(t, z.Close())
			raw := archive.Bytes()
			if mode == "crc" {
				offset := bytes.Index(raw, []byte("metadata"))
				raw[offset] = 'X'
			}
			must(t, os.WriteFile(file, raw, 0o600))
			if err := (Tools{}).verifyIPSW(
				t.Context(),
				file,
				t.TempDir(),
				appleFixture(),
			); err == nil {
				t.Fatal("accepted invalid manifest")
			}
		})
	}
}

func TestMacBuildRefusesUnwritableOutputs(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("Apple host coordinator")
	}
	for _, mode := range []string{"source-output", "bundle-output", "invalid-media"} {
		t.Run(mode, func(t *testing.T) {
			work := t.TempDir()
			t.Setenv("WEAVE_IMAGE_WORKSPACE", work)
			s := appleFixture()
			file := makeIPSW(t)
			data, err := os.ReadFile(file)
			must(t, err)
			if mode == "invalid-media" {
				data = []byte("not a zip")
			}
			pin := mediaFor(s.URL, data)
			s.SHA256, s.Size = pin.SHA256, pin.Size
			cached := filepath.Join(work, "media", "ipsw", s.SHA256, "restore.ipsw")
			must(t, os.MkdirAll(filepath.Dir(cached), 0o700))
			must(t, os.WriteFile(cached, data, 0o600))
			lock := filepath.Join(work, "lock.json")
			must(t, writeJSON(lock, s))
			tag, err := s.validate("26", 1)
			must(t, err)
			candidate := filepath.Join(work, "builds", "macos-26-base", tag)
			p := Packages{
				Tools: Tools{
					Run: func(_ context.Context, w, _ io.Writer, name string, args ...string) error {
						switch name {
						case "mount":
							fmt.Fprintf(w, "disk on %s (apfs, local)", work)
						case "df":
							fmt.Fprint(w, "disk 999999999 0 999999999")
						case "plutil":
							target := "source.json"
							if mode == "bundle-output" {
								target = "bundle"
							}
							must(t, os.MkdirAll(filepath.Join(candidate, target), 0o700))
							value := s.Version
							if args[1] == "ProductBuildVersion" {
								value = s.Build
							}
							fmt.Fprint(w, value)
						default:
							t.Fatal("unexpected tool", name)
						}
						return nil
					},
				},
				MacRestore: func(context.Context, MacRestoreRequest) (MacRestoreResult, error) {
					t.Fatal("restore started after output failure")
					return MacRestoreResult{}, nil
				},
			}
			if _, err := p.BuildMacOS(
				t.Context(),
				MacOptions{Version: "26", SourceLock: lock, Revision: 1},
			); err == nil {
				t.Fatal("ignored " + mode)
			}
		})
	}
}

func TestAppleBuildOrderingIsIndependentOfCatalogueOrder(t *testing.T) {
	a := appleFixture()
	a.Build = "25G99"
	b := a
	b.Build = "25G100"
	c := a
	c.URL = "https://updates.cdn-apple.com/another.ipsw"
	for _, entries := range [][]AppleSource{{b, a, c}, {c, b, a}, {a, c, b}} {
		raw, err := json.Marshal(
			map[string]any{"identifier": "VirtualMac2,1", "firmwares": entries},
		)
		must(t, err)
		client := &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: 200,
					Header:     http.Header{},
					Request:    r,
					Body:       io.NopCloser(bytes.NewReader(raw)),
				}, nil
			}),
		}
		sources, err := MacSources(t.Context(), client, "26")
		must(t, err)
		if sources[0] != b || sources[1] != c || sources[2] != a {
			t.Fatal("unstable source ordering", sources)
		}
	}
}
