package imagebuild

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func nativePackageFixture(t *testing.T, osName string) (Lock, string) {
	t.Helper()
	l, cache := packageFixture(t)
	entry := l.Platforms["linux/arm64"]
	delete(l.Platforms, "linux/arm64")
	if osName == "darwin" {
		entry.Core = cachedAsset(
			t,
			cache,
			coreRepository,
			"weave-agent_1.2.3_darwin_arm64.pkg",
			[]byte("core pkg"),
		)
		entry.CoreEvidence = []Asset{
			cachedAsset(
				t,
				cache,
				coreRepository,
				entry.Core.Name+".sigstore.json",
				[]byte("signature"),
			),
		}
	} else {
		entry.Core = cachedAsset(
			t,
			cache,
			coreRepository,
			"weaveplatform-agent_1.2.3_windows_arm64.zip",
			[]byte("core zip"),
		)
		entry.CoreEvidence = evidence(
			t,
			cache,
			coreRepository,
			"weaveplatform-agent_1.2.3_",
			[]Asset{entry.Core},
		)
	}
	m := entry.Modules[0]
	m.ID = strings.Replace(
		m.ID,
		"linux",
		map[string]string{"darwin": "macos", "windows": "windows"}[osName],
		1,
	)
	m.Binary = cachedAsset(
		t,
		cache,
		modulesRepository,
		m.ID+"-"+osName+"-arm64",
		[]byte("native binary"),
	)
	raw, err := json.Marshal(
		map[string]any{
			"id":      m.ID,
			"version": m.Version,
			"artifacts": []map[string]any{
				{"os": osName, "arch": "arm64", "digest": m.Binary.Digest, "size": m.Binary.Size},
			},
		},
	)
	must(t, err)
	m.Manifest = cachedAsset(t, cache, modulesRepository, "module.manifest.json", raw)
	pkg := cachedAsset(
		t,
		cache,
		modulesRepository,
		m.ID+"_2.3.4_"+osName+"_arm64."+map[string]string{"darwin": "pkg", "windows": "zip"}[osName],
		[]byte("native module package"),
	)
	m.Package = &pkg
	m.PackageEvidence = evidence(
		t,
		cache,
		modulesRepository,
		"",
		[]Asset{pkg, m.Manifest, m.Binary},
	)
	entry.Modules = []ModuleInput{m}
	l.Platforms[osName+"/arm64"] = entry
	return l, cache
}

func TestNativePackagePreparation(t *testing.T) {
	for _, osName := range []string{"darwin", "windows"} {
		t.Run(osName, func(t *testing.T) {
			l, cache := nativePackageFixture(t, osName)
			calls := 0
			p := Packages{
				Tools: Tools{
					Run: func(_ context.Context, _, _ io.Writer, name string, args ...string) error {
						calls++
						if name != "cosign" || args[0] != "verify-blob" {
							t.Fatal(name, args)
						}
						joined := strings.Join(args, " ")
						if calls == 1 &&
							(!strings.Contains(joined, `release\.yml@(?:refs/tags/v1\.2\.3)`) || (osName == "darwin" && !strings.HasSuffix(joined, ".pkg"))) {
							t.Fatal(joined)
						}
						return nil
					},
				},
			}
			out := filepath.Join(t.TempDir(), "payload")
			assets, err := p.Prepare(t.Context(), l, osName+"/arm64", cache, out)
			must(t, err)
			if len(assets) != 2 || calls != 2 {
				t.Fatal(assets, calls)
			}
			if _, err := os.Stat(filepath.Join(out, "packages.lock.json")); err != nil {
				t.Fatal(err)
			}
			if err := p.moduleFor(
				t.Context(),
				l.Platforms[osName+"/arm64"].Modules[0],
				"linux",
				"arm64",
				cache,
			); err == nil {
				t.Fatal("accepted different OS binary")
			}
		})
	}
}

func TestNativePackageEvidenceFailures(t *testing.T) {
	for _, mode := range []string{"evidence absent", "wrong evidence", "package corrupt", "bundle corrupt", "bad signature", "unknown platform"} {
		t.Run(mode, func(t *testing.T) {
			l, cache := nativePackageFixture(t, "darwin")
			entry := l.Platforms["darwin/arm64"]
			platform := "darwin/arm64"
			p := Packages{
				Downloader: Downloader{
					Client: &http.Client{
						Transport: roundTripFunc(
							func(*http.Request) (*http.Response, error) { return nil, os.ErrPermission },
						),
					},
				},
				Tools: Tools{
					Run: func(context.Context, io.Writer, io.Writer, string, ...string) error { return nil },
				},
			}
			switch mode {
			case "evidence absent":
				entry.CoreEvidence = nil
			case "wrong evidence":
				entry.CoreEvidence[0].Name = "wrong.sigstore.json"
			case "package corrupt", "bundle corrupt":
				a := entry.Core
				if mode == "bundle corrupt" {
					a = entry.CoreEvidence[0]
				}
				must(
					t,
					os.WriteFile(
						filepath.Join(cache, strings.TrimPrefix(a.Digest, "sha256:"), a.Name),
						[]byte("corrupt"),
						0o600,
					),
				)
			case "bad signature":
				p.Tools.Run = func(context.Context, io.Writer, io.Writer, string, ...string) error { return os.ErrPermission }
			case "unknown platform":
				platform = "darwin/amd64"
			}
			l.Platforms["darwin/arm64"] = entry
			out := filepath.Join(t.TempDir(), "payload")
			if _, err := p.Prepare(t.Context(), l, platform, cache, out); err == nil {
				t.Fatal("accepted invalid package evidence")
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatal("created payload before verification", err)
			}
		})
	}
}

func TestNativePackagePreflightRequiresSignedModuleChecksums(t *testing.T) {
	for _, osName := range []string{"darwin", "windows"} {
		l, cache := nativePackageFixture(t, osName)
		entry := l.Platforms[osName+"/arm64"]
		entry.Modules[0].PackageEvidence = nil
		l.Platforms[osName+"/arm64"] = entry
		calls := 0
		p := Packages{
			Tools: Tools{
				Run: func(context.Context, io.Writer, io.Writer, string, ...string) error { calls++; return nil },
			},
		}
		if _, err := p.Prepare(
			t.Context(),
			l,
			osName+"/arm64",
			cache,
			filepath.Join(t.TempDir(), "payload"),
		); err == nil ||
			!strings.Contains(err.Error(), "signed checksums") {
			t.Fatal(err)
		}
		if calls != 0 {
			t.Fatal("started verification before preflight", calls)
		}
	}
}
