package imagebuild

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cachedAsset(t *testing.T, cache, repo, name string, data []byte) Asset {
	t.Helper()
	m := mediaFor("", data)
	a := fakeAsset(repo, name)
	a.Size = m.Size
	a.Digest = "sha256:" + m.SHA256
	dir := filepath.Join(cache, m.SHA256)
	must(t, os.MkdirAll(dir, 0o750))
	must(t, os.WriteFile(filepath.Join(dir, name), data, 0o600))
	return a
}

func evidence(t *testing.T, cache, repo, prefix string, assets []Asset) []Asset {
	t.Helper()
	var sums strings.Builder
	for _, a := range assets {
		sums.WriteString(strings.TrimPrefix(a.Digest, "sha256:") + "  " + a.Name + "\n")
	}
	return []Asset{
		cachedAsset(t, cache, repo, prefix+"checksums.txt", []byte(sums.String())),
		cachedAsset(t, cache, repo, prefix+"checksums.txt.sigstore.json", []byte("signature")),
	}
}

func packageFixture(t *testing.T) (Lock, string) {
	t.Helper()
	cache := t.TempDir()
	core := cachedAsset(t, cache, coreRepository, "weave-agent_1.2.3_arm64.deb", []byte("core"))
	binary := cachedAsset(
		t,
		cache,
		modulesRepository,
		"weave-linux-exec-linux-arm64",
		[]byte("binary"),
	)
	raw, err := json.Marshal(
		map[string]any{
			"id":      "weave-linux-exec",
			"version": "2.3.4",
			"artifacts": []map[string]any{
				{"os": "linux", "arch": "arm64", "digest": binary.Digest, "size": binary.Size},
			},
		},
	)
	must(t, err)
	manifest := cachedAsset(t, cache, modulesRepository, "module.manifest.json", raw)
	pkg := cachedAsset(
		t,
		cache,
		modulesRepository,
		"weave-linux-exec_2.3.4_arm64.deb",
		[]byte("module package"),
	)
	m := ModuleInput{
		ID:              "weave-linux-exec",
		Version:         "2.3.4",
		Binary:          binary,
		Manifest:        manifest,
		Package:         &pkg,
		PackageEvidence: evidence(t, cache, modulesRepository, "", []Asset{pkg, manifest, binary}),
	}
	return Lock{
		SchemaVersion: 1,
		CoreVersion:   "1.2.3",
		Platforms: map[string]PlatformInputs{
			"linux/arm64": {
				Core: core,
				CoreEvidence: evidence(
					t,
					cache,
					coreRepository,
					"weaveplatform-agent_1.2.3_",
					[]Asset{core},
				),
				Modules: []ModuleInput{m},
			},
		},
	}, cache
}

func TestPrepareLinuxEvidenceAndSealing(t *testing.T) {
	l, cache := packageFixture(t)
	calls := 0
	p := Packages{
		Tools: Tools{
			Run: func(_ context.Context, _, _ io.Writer, name string, args ...string) error {
				calls++
				if name != "cosign" || args[0] != "verify-blob" ||
					!strings.Contains(
						strings.Join(args, " "),
						"https://token.actions.githubusercontent.com",
					) {
					t.Fatalf("bad signature verification: %s %v", name, args)
				}
				return nil
			},
		},
	}
	out := filepath.Join(t.TempDir(), "packages")
	sources, err := p.PrepareLinux(t.Context(), l, "arm64", cache, out)
	must(t, err)
	if calls != 2 || len(sources) != 2 {
		t.Fatalf("verification calls %d sources %d", calls, len(sources))
	}
	for _, a := range sources {
		raw, err := os.ReadFile(filepath.Join(out, a.Name))
		must(t, err)
		if len(raw) == 0 {
			t.Fatal("empty installer")
		}
	}
	if _, err := p.PrepareLinux(t.Context(), l, "arm64", cache, out); err == nil {
		t.Fatal("overwritten payload")
	}
	recipe, err := LinuxRecipe(l, "arm64")
	must(t, err)
	for _, want := range []string{"store.db-wal", "store.db-shm", "store.key", "channel.pub", "cloud-init clean", "truncate -s 0 /etc/machine-id", "sha256sum -c -", "systemctl mask --runtime"} {
		if !strings.Contains(recipe, want) {
			t.Errorf("recipe missing %s", want)
		}
	}
	if strings.Contains(recipe, "manifest.sequence") {
		t.Fatal("recipe deletes anti-rollback state")
	}
	failing := p
	failing.Tools.Run = func(context.Context, io.Writer, io.Writer, string, ...string) error {
		return errors.New("bad signature")
	}
	if _, err := failing.PrepareLinux(
		t.Context(),
		l,
		"arm64",
		cache,
		filepath.Join(t.TempDir(), "out"),
	); err == nil {
		t.Fatal("accepted invalid signature")
	}
	l.Platforms["linux/arm64"].Modules[0].PackageEvidence = nil
	if _, err := p.PrepareLinux(
		t.Context(),
		l,
		"arm64",
		cache,
		filepath.Join(t.TempDir(), "out"),
	); err == nil {
		t.Fatal("accepted missing evidence")
	}
	l.Platforms["linux/arm64"].Modules[0].Package = nil
	if _, err := LinuxRecipe(l, "arm64"); err == nil {
		t.Fatal("missing installer")
	}
}

func TestSignedChecksumsRejectMismatches(t *testing.T) {
	a := fakeAsset(coreRepository, "a.deb")
	line := strings.Repeat("a", 64) + "  a.deb\n"
	must(t, checkSums(line, []Asset{a}))
	for _, raw := range []string{line + line, "malformed", strings.ReplaceAll(line, "a.deb", "b.deb"), strings.Repeat("b", 64) + "  a.deb\n"} {
		if checkSums(raw, []Asset{a}) == nil {
			t.Fatal("bad checksums accepted")
		}
	}
	p := Packages{}
	if err := p.signedChecksums(
		t.Context(),
		nil,
		nil,
		coreRepository,
		nil,
		t.TempDir(),
	); err == nil {
		t.Fatal("missing evidence")
	}
	l, cache := packageFixture(t)
	entry := l.Platforms["linux/arm64"]
	p.Tools.Run = func(context.Context, io.Writer, io.Writer, string, ...string) error { return nil }
	bad := entry.Modules[0]
	bad.Version = "0.0.0"
	if err := p.module(t.Context(), bad, "arm64", cache); err == nil {
		t.Fatal("manifest version differs")
	}
	if err := p.module(t.Context(), entry.Modules[0], "amd64", cache); err == nil {
		t.Fatal("wrong manifest platform")
	}
}

func TestModuleManifestIntegrityFailures(t *testing.T) {
	for _, mode := range []string{"invalid-json", "wrong-binary", "no-artifacts"} {
		t.Run(mode, func(t *testing.T) {
			l, cache := packageFixture(t)
			m := l.Platforms["linux/arm64"].Modules[0]
			raw := []byte(`{`)
			if mode != "invalid-json" {
				artifacts := []map[string]any{}
				if mode == "wrong-binary" {
					artifacts = append(
						artifacts,
						map[string]any{
							"os":     "linux",
							"arch":   "arm64",
							"digest": "sha256:" + strings.Repeat("0", 64),
							"size":   m.Binary.Size,
						},
					)
				}
				var err error
				raw, err = json.Marshal(
					map[string]any{"id": m.ID, "version": m.Version, "artifacts": artifacts},
				)
				must(t, err)
			}
			m.Manifest = cachedAsset(t, cache, modulesRepository, "module.manifest.json", raw)
			m.PackageEvidence = evidence(
				t,
				cache,
				modulesRepository,
				"",
				[]Asset{*m.Package, m.Manifest, m.Binary},
			)
			p := Packages{
				Tools: Tools{
					Run: func(context.Context, io.Writer, io.Writer, string, ...string) error { return nil },
				},
			}
			if err := p.module(t.Context(), m, "arm64", cache); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
	l, cache := packageFixture(t)
	p := Packages{
		Tools: Tools{
			Run: func(context.Context, io.Writer, io.Writer, string, ...string) error { return nil },
		},
	}
	entry := l.Platforms["linux/arm64"]
	duplicate := append(entry.CoreEvidence, entry.CoreEvidence[0])
	if err := p.signedChecksums(
		t.Context(),
		duplicate,
		nil,
		coreRepository,
		nil,
		cache,
	); err == nil {
		t.Fatal("duplicate evidence")
	}
	entry.Modules[0].ID = "unsafe/id"
	if _, err := LinuxRecipe(l, "arm64"); err == nil {
		t.Fatal("unsafe recipe accepted")
	}
}

func TestPackageEvidenceFilesystemFailures(t *testing.T) {
	for _, mode := range []string{"duplicate-checksums", "removed-checksums", "corrupt-manifest-cache", "corrupt-package-cache", "output-parent"} {
		t.Run(mode, func(t *testing.T) {
			l, cache := packageFixture(t)
			entry := l.Platforms["linux/arm64"]
			out := filepath.Join(t.TempDir(), "payload")
			if mode == "duplicate-checksums" {
				entry.CoreEvidence = append(entry.CoreEvidence, entry.CoreEvidence[0])
				l.Platforms["linux/arm64"] = entry
			}
			if mode == "corrupt-package-cache" {
				a := entry.Core
				must(
					t,
					os.WriteFile(
						filepath.Join(cache, strings.TrimPrefix(a.Digest, "sha256:"), a.Name),
						[]byte("tampered"),
						0o600,
					),
				)
			}
			if mode == "output-parent" {
				parent := filepath.Join(t.TempDir(), "file")
				out = filepath.Join(parent, "payload")
			}
			calls := 0
			p := Packages{
				Tools: Tools{
					Run: func(_ context.Context, _, _ io.Writer, _ string, args ...string) error {
						calls++
						if mode == "output-parent" && calls == 2 {
							must(t, os.WriteFile(filepath.Dir(out), nil, 0o600))
						}
						if mode == "removed-checksums" {
							must(t, os.Remove(args[len(args)-1]))
						}
						if mode == "corrupt-manifest-cache" && calls == 2 {
							a := entry.Modules[0].Manifest
							must(
								t,
								os.WriteFile(
									filepath.Join(
										cache,
										strings.TrimPrefix(a.Digest, "sha256:"),
										a.Name,
									),
									[]byte("tampered"),
									0o600,
								),
							)
						}
						return nil
					},
				},
			}
			if _, err := p.PrepareLinux(t.Context(), l, "arm64", cache, out); err == nil {
				t.Fatal("accepted " + mode)
			}
			if _, err := os.Stat(filepath.Join(out, "packages.lock.json")); err == nil {
				t.Fatal("published failed payload")
			}
		})
	}
}

func TestPackageEvidenceRejectsCorruptCache(t *testing.T) {
	l, cache := packageFixture(t)
	entry := l.Platforms["linux/arm64"]
	a := entry.CoreEvidence[0]
	must(
		t,
		os.WriteFile(
			filepath.Join(cache, strings.TrimPrefix(a.Digest, "sha256:"), a.Name),
			[]byte("tampered"),
			0o600,
		),
	)
	p := Packages{}
	if err := p.signedChecksums(
		t.Context(),
		entry.CoreEvidence,
		[]Asset{entry.Core},
		coreRepository,
		nil,
		cache,
	); err == nil {
		t.Fatal("corrupt evidence accepted")
	}
	m := entry.Modules[0]
	m.PackageEvidence = nil
	if err := p.module(t.Context(), m, "arm64", cache); err == nil {
		t.Fatal("unsigned module accepted")
	}
}
