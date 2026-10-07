package imagebuild

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func repoInputs(t *testing.T) (Catalogue, Lock) {
	t.Helper()
	c, e := LoadCatalogue("../../images/catalogue.json")
	must(t, e)
	l, e := LoadLock("../../images/packages.lock.json", c)
	must(t, e)
	return c, l
}

func cloneLock(t *testing.T, l Lock) Lock {
	t.Helper()
	raw, e := json.Marshal(l)
	must(t, e)
	var v Lock
	must(t, json.Unmarshal(raw, &v))
	return v
}

func fakeAsset(repo, name string) Asset {
	return Asset{
		Name:   name,
		URI:    "https://github.com/" + repo + "/releases/download/v1.2.3/" + name,
		Digest: "sha256:" + strings.Repeat("a", 64),
		Size:   1,
	}
}

func TestCatalogueLock(t *testing.T) {
	c, l := repoInputs(t)
	if len(c.Matrix()) != 20 || len(l.Platforms) != 5 {
		t.Fatalf("matrix/platforms %d/%d", len(c.Matrix()), len(l.Platforms))
	}
	out := filepath.Join(t.TempDir(), "lock.json")
	must(t, WriteLock(out, l))
	if WriteLock(out, l) == nil {
		t.Fatal("overwrote lock")
	}
	_, err := LoadLock(out, c)
	must(t, err)
	if _, err := l.RequirePackages("linux/arm64"); err == nil {
		t.Fatal("missing installers accepted")
	}
	if _, err := l.RequirePackages("unknown"); err == nil {
		t.Fatal("unknown platform")
	}
	mutations := []func(*Lock){
		func(v *Lock) { v.SchemaVersion = 2 },
		func(v *Lock) { v.CoreVersion = "latest" },
		func(v *Lock) { delete(v.Platforms, "linux/arm64") },
		func(v *Lock) {
			v.Platforms["other/arm64"] = v.Platforms["linux/arm64"]
			delete(v.Platforms, "linux/arm64")
		},
		func(v *Lock) {
			p := v.Platforms["linux/arm64"]
			p.Modules = p.Modules[:1]
			v.Platforms["linux/arm64"] = p
		},
		func(v *Lock) {
			p := v.Platforms["linux/arm64"]
			p.Modules[1] = p.Modules[0]
			v.Platforms["linux/arm64"] = p
		},
		func(v *Lock) { p := v.Platforms["linux/arm64"]; p.Core.Size = 0; v.Platforms["linux/arm64"] = p },
	}
	for i, mutate := range mutations {
		v := cloneLock(t, l)
		mutate(&v)
		if v.Validate(c) == nil {
			t.Errorf("mutation %d accepted", i)
		}
	}
	for _, raw := range []string{`{`, `{}`, `{"schemaVersion":1,"images":[{}],"capabilities":["exec","exec"]}`, `{"schemaVersion":1,"images":[{}],"capabilities":["exec"]}`} {
		file := filepath.Join(t.TempDir(), "catalogue")
		must(t, os.WriteFile(file, []byte(raw), 0o600))
		if _, err := LoadCatalogue(file); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if _, err := LoadCatalogue("missing"); err == nil {
		t.Fatal("missing catalogue")
	}
	if _, err := LoadLock("missing", c); err == nil {
		t.Fatal("missing lock")
	}
}

func TestAssetValidation(t *testing.T) {
	a := fakeAsset(coreRepository, "core.deb")
	must(t, a.Validate())
	for _, uri := range []string{"%", strings.Replace(a.URI, "https:", "http:", 1), strings.Replace(a.URI, "github.com", "other.example", 1), a.URI + "?x=y", a.URI + "#x", strings.Replace(a.URI, "github.com", "user@github.com", 1), strings.Replace(a.URI, coreRepository, "other/repo", 1)} {
		v := a
		v.URI = uri
		if v.Validate() == nil {
			t.Errorf("accepted %s", uri)
		}
	}
	for _, name := range []string{"..", "../file", "bad\\name", "bad\nname"} {
		v := a
		v.Name = name
		if v.Validate() == nil {
			t.Errorf("accepted %s", name)
		}
	}
	a.Digest = "sha256:no"
	if a.Validate() == nil {
		t.Fatal("bad hash")
	}
}

func TestReleaseResolution(t *testing.T) {
	c, l := repoInputs(t)
	core := release{Tag: "v" + l.CoreVersion}
	modules := map[string]release{}
	appendAsset := func(r *release, a Asset) {
		for _, old := range r.Assets {
			if old.Name == a.Name {
				return
			}
		}
		r.Assets = append(
			r.Assets,
			releaseAsset{Name: a.Name, URI: a.URI, Digest: a.Digest, Size: a.Size},
		)
	}
	for _, p := range l.Platforms {
		appendAsset(&core, p.Core)
		for _, a := range p.CoreEvidence {
			appendAsset(&core, a)
		}
		for _, m := range p.Modules {
			tag := "modules/" + m.ID + "/v" + m.Version
			r := modules[tag]
			r.Tag = tag
			appendAsset(&r, m.Binary)
			appendAsset(&r, m.Manifest)
			if m.Package != nil {
				appendAsset(&r, *m.Package)
			}
			modules[tag] = r
		}
	}
	// Exercise the newly supported signed Linux installers in the resolver.
	for tag, r := range modules {
		if strings.Contains(tag, "weave-linux-") {
			for _, arch := range []string{"amd64", "arm64"} {
				parts := strings.Split(tag, "/")
				a := fakeAsset(
					modulesRepository,
					parts[1]+"_"+strings.TrimPrefix(parts[2], "v")+"_"+arch+".deb",
				)
				appendAsset(&r, a)
			}
			for _, name := range []string{"checksums.txt", "checksums.txt.sigstore.json"} {
				appendAsset(&r, fakeAsset(modulesRepository, name))
			}
			modules[tag] = r
		}
	}
	var moduleReleases []release
	for _, r := range modules {
		moduleReleases = append(moduleReleases, r)
	}
	runner := Tools{
		Run: func(_ context.Context, w, _ io.Writer, name string, args ...string) error {
			if name != "gh" {
				t.Fatalf("unexpected tool %s", name)
			}
			rs := moduleReleases
			if strings.Contains(args[len(args)-1], coreRepository) {
				rs = []release{core}
			}
			return json.NewEncoder(w).Encode([][]release{rs})
		},
	}
	resolved, err := ResolveLock(t.Context(), c, runner)
	must(t, err)
	must(t, resolved.Validate(c))
	if _, err := resolved.RequirePackages("linux/amd64"); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		rs           []release
		prefix, want string
	}{{[]release{{Tag: "v1.2.0"}, {Tag: "v1.10.0"}, {Tag: "v2.0.0", Prerelease: true}, {Tag: "v9.0.0", Draft: true}, {Tag: "junk"}}, "", "v1.10.0"}, {[]release{{Tag: "modules/x/v1.2.3"}}, "modules/x/", "modules/x/v1.2.3"}} {
		r, err := latest(test.rs, test.prefix)
		must(t, err)
		if r.Tag != test.want {
			t.Fatal(r)
		}
	}
	if _, err := latest(nil, ""); err == nil {
		t.Fatal("missing stable release")
	}
	if _, err := latest([]release{{Tag: "v999999999999999999999.1.0"}}, ""); err == nil {
		t.Fatal("overflow")
	}
	if _, err := core.asset("missing"); err == nil {
		t.Fatal("missing asset")
	}
	for _, r := range []Tools{{Run: func(context.Context, io.Writer, io.Writer, string, ...string) error { return errors.New("offline") }}, {Run: func(_ context.Context, w, _ io.Writer, _ string, _ ...string) error {
		_, err := fmt.Fprint(w, "bad")
		return err
	}}} {
		if _, err := ResolveLock(t.Context(), c, r); err == nil {
			t.Fatal("bad release response")
		}
	}
}

func TestMissingReleaseInputs(t *testing.T) {
	c, _ := repoInputs(t)
	runner := Tools{Run: func(_ context.Context, w, _ io.Writer, _ string, _ ...string) error {
		return json.NewEncoder(w).Encode([][]release{{}})
	}}
	if _, err := ResolveLock(t.Context(), c, runner); err == nil {
		t.Fatal("no stable core")
	}
	calls := 0
	runner.Run = func(_ context.Context, w, _ io.Writer, _ string, _ ...string) error {
		calls++
		if calls == 2 {
			return errors.New("modules unavailable")
		}
		return json.NewEncoder(w).Encode([][]release{{{Tag: "v1.2.3"}}})
	}
	if _, err := ResolveLock(t.Context(), c, runner); err == nil {
		t.Fatal("modules unavailable")
	}
	runner.Run = func(_ context.Context, w, _ io.Writer, _ string, _ ...string) error {
		return json.NewEncoder(w).Encode([][]release{{{Tag: "v1.2.3"}}})
	}
	if _, err := ResolveLock(t.Context(), c, runner); err == nil {
		t.Fatal("assets missing")
	}
	core := release{Tag: "v1.2.3"}
	a := fakeAsset(coreRepository, "weave-agent_1.2.3_arm64.deb")
	core.Assets = []releaseAsset{releaseAsset(a)}
	if _, err := resolvePlatform(
		core,
		nil,
		[]string{"exec"},
		"linux",
		"arm64",
		"1.2.3",
	); err == nil {
		t.Fatal("evidence missing")
	}
	for _, name := range []string{"weaveplatform-agent_1.2.3_checksums.txt", "weaveplatform-agent_1.2.3_checksums.txt.sigstore.json"} {
		core.Assets = append(core.Assets, releaseAsset(fakeAsset(coreRepository, name)))
	}
	if _, err := resolvePlatform(
		core,
		nil,
		[]string{"exec"},
		"linux",
		"arm64",
		"1.2.3",
	); err == nil {
		t.Fatal("module missing")
	}
	r := release{Tag: "modules/weave-linux-exec/v1.2.3"}
	if _, err := resolveModule([]release{r}, "exec", "linux", "arm64"); err == nil {
		t.Fatal("binary missing")
	}
	r.Assets = append(
		r.Assets,
		releaseAsset(fakeAsset(modulesRepository, "weave-linux-exec-linux-arm64")),
	)
	if _, err := resolveModule([]release{r}, "exec", "linux", "arm64"); err == nil {
		t.Fatal("manifest missing")
	}
	r.Assets = append(r.Assets, releaseAsset(fakeAsset(modulesRepository, "module.manifest.json")))
	bad := fakeAsset(modulesRepository, "weave-linux-exec_1.2.3_arm64.deb")
	bad.Size = 0
	r.Assets = append(r.Assets, releaseAsset(bad))
	if _, err := resolveModule([]release{r}, "exec", "linux", "arm64"); err == nil {
		t.Fatal("bad installer pin")
	}
	r.Assets = r.Assets[:len(r.Assets)-1]
	bad = fakeAsset(modulesRepository, "checksums.txt")
	bad.Size = 0
	r.Assets = append(r.Assets, releaseAsset(bad))
	if _, err := resolveModule([]release{r}, "exec", "linux", "arm64"); err == nil {
		t.Fatal("bad evidence pin")
	}
}
