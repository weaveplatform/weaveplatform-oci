package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/deploymenttheory/weaveplatform-oci/internal/cli"
	"github.com/deploymenttheory/weaveplatform-oci/internal/testbundle"
	"github.com/deploymenttheory/weaveplatform-oci/internal/testregistry"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/spec"
)

// env isolates a test from the user's profile, cache and docker credentials.
type env struct {
	dir  string
	reg  *testregistry.Server
	prof string
}

func newEnv(t *testing.T, o testregistry.Options) *env {
	t.Helper()
	e := &env{dir: t.TempDir()}
	e.reg = testregistry.New(o)
	t.Cleanup(e.reg.Close)
	t.Setenv("DOCKER_CONFIG", filepath.Join(e.dir, "docker"))
	t.Setenv("WEAVEOCI_CACHE", filepath.Join(e.dir, "cache"))
	t.Setenv("COSIGN_PASSWORD", "pw")
	t.Setenv("WEAVE_REGISTRY_USERNAME", o.Username)
	t.Setenv("WEAVE_REGISTRY_PASSWORD", o.Password)
	t.Setenv("WEAVE_REGISTRY_HOSTNAME", "")
	return e
}

func (e *env) p(name string) string { return filepath.Join(e.dir, name) }

// profile writes a profile file and points WEAVEOCI_PROFILES at it.
func (e *env) profile(t *testing.T, body string) {
	t.Helper()
	e.prof = e.p("profiles.yaml")
	body = strings.ReplaceAll(body, "{registry}", e.reg.Host)
	if err := os.WriteFile(e.prof, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEAVEOCI_PROFILES", e.prof)
}

const privateProfile = `schemaVersion: 1
default: org
profiles:
  - name: org
    kind: private
    registry: {host: "{registry}", namespace: weave-images, plainHTTP: true}
    signing: {provider: cosign-key, key: cosign.key}
    verify: {mode: signature, publicKeys: [cosign.pub]}
  - name: open
    kind: github
    registry: {host: "{registry}", namespace: weave-images, plainHTTP: true}
    signing: {provider: none}
    verify: {mode: none}
`

func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	code, out, errOut := run(t, args...)
	if code != cli.ExitOK {
		t.Fatalf("%v: exit %d\nstdout: %s\nstderr: %s", args, code, out, errOut)
	}
	return out
}

func expect(t *testing.T, want int, args ...string) (string, string) {
	t.Helper()
	code, out, errOut := run(t, args...)
	if code != want {
		t.Fatalf("%v: exit %d, want %d\nstdout: %s\nstderr: %s", args, code, want, out, errOut)
	}
	return out, errOut
}

var digestRE = regexp.MustCompile(`sha256:[0-9a-f]{64}`)

func TestProfileCommands(t *testing.T) {
	e := newEnv(t, testregistry.Options{})
	e.profile(t, privateProfile)
	if out := mustRun(t, "profile", "show"); !strings.Contains(out, "kind: private") {
		t.Fatal(out)
	}
	if out := mustRun(
		t,
		"profile",
		"show",
		"--profile",
		"open",
	); !strings.Contains(
		out,
		"kind: github",
	) {
		t.Fatal(out)
	}
	if out := mustRun(t, "profile", "validate"); !strings.Contains(out, "2 profile(s) valid") {
		t.Fatal(out)
	}
	expect(t, cli.ExitFailure, "profile", "show", "--profile", "nope")
	expect(t, cli.ExitFailure, "profile", "show", "--profiles", e.p("missing.yaml"))
	expect(t, cli.ExitFailure, "profile", "validate", "--profiles", e.p("missing.yaml"))
	_ = os.WriteFile(e.p("bad.yaml"), []byte("schemaVersion: 2\n"), 0o600)
	expect(t, cli.ExitFailure, "profile", "validate", "--profiles", e.p("bad.yaml"))
	expect(t, cli.ExitUsage, "profile", "show", "extra")
}

func TestKeygenSignPushPullVerifyPublish(t *testing.T) {
	e := newEnv(t, testregistry.Options{Username: "u", Password: "p"})
	e.profile(t, privateProfile)
	// keygen into a nested directory that does not exist yet
	mustRun(t, "keygen", e.p("keys/other"))
	mustRun(t, "keygen", e.p("cosign"))
	for _, f := range []string{"cosign.key", "cosign.pub", "keys/other.key", "keys/other.pub"} {
		if _, err := os.Stat(e.p(f)); err != nil {
			t.Fatal(err)
		}
	}
	file := e.p("plainfile")
	_ = os.WriteFile(file, nil, 0o600)
	expect(t, cli.ExitFailure, "keygen", filepath.Join(file, "x"))
	expect(t, cli.ExitUsage, "keygen")

	arm := writeBundle(t, testbundle.Options{OS: spec.OSLinux, Arch: spec.ArchARM64, Seed: 1})
	amd := writeBundle(t, testbundle.Options{OS: spec.OSLinux, Arch: spec.ArchAMD64, Seed: 2})

	// publish in the private profile (signs with the profile key)
	out := mustRun(
		t,
		"publish",
		arm,
		amd,
		"--repository",
		"ubuntu",
		"--tag",
		"1-r1",
		"--promotion-out",
		e.p("entry.json"),
	)
	if !strings.Contains(out, "signature sha256:") || !strings.Contains(out, "linux/amd64") {
		t.Fatal(out)
	}
	if raw, err := os.ReadFile(e.p("entry.json")); err != nil || !json.Valid(raw) {
		t.Fatal(err)
	}
	expect(t, cli.ExitFailure, "publish", arm, "--repository", "ubuntu", "--tag", "1-r1")
	expect(t, cli.ExitUsage, "publish", arm, "--repository", "ubuntu")
	expect(
		t,
		cli.ExitFailure,
		"publish",
		arm,
		"--repository",
		"ubuntu",
		"--tag",
		"2-r1",
		"--key",
		e.p("missing.key"),
	)
	expect(
		t,
		cli.ExitFailure,
		"publish",
		arm,
		"--repository",
		"ubuntu",
		"--tag",
		"3-r1",
		"--promotion-out",
		filepath.Join(file, "x"),
	)
	// a profile that does not sign
	out = mustRun(t, "publish", arm, "--repository", "plain", "--tag", "1-r1", "--profile", "open")
	if strings.Contains(out, "signature") {
		t.Fatal(out)
	}
	expect(
		t,
		cli.ExitFailure,
		"publish",
		arm,
		"--repository",
		"x",
		"--tag",
		"1",
		"--profiles",
		e.p("missing.yaml"),
	)

	// pull: signature verified, unpack one platform, resume
	out = mustRun(t, "pull", "ubuntu:1-r1", "--to", e.p("out"), "--platform", "linux/amd64")
	if !strings.Contains(out, "signature: cosign-key") || !strings.Contains(out, "unpacked") {
		t.Fatal(out)
	}
	mustRun(t, "pull", "ubuntu:1-r1", "--to", e.p("out"), "--platform", "linux/amd64", "--resume")
	expect(t, cli.ExitUsage, "pull", "ubuntu:1-r1", "--to", e.p("out2"))
	expect(
		t,
		cli.ExitFailure,
		"pull",
		"ubuntu:1-r1",
		"--to",
		e.p("out3"),
		"--platform",
		"windows/amd64",
	)
	expect(
		t,
		cli.ExitFailure,
		"pull",
		"plain:1-r1",
	) // unsigned under signature mode
	mustRun(t, "pull", "plain:1-r1", "--verify", "none")                     // explicitly off
	expect(t, cli.ExitFailure, "pull", "plain:1-r1", "--verify", "trust-me") // bad mode
	expect(t, cli.ExitFailure, "pull", "missing:1")                          // unknown ref
	expect(t, cli.ExitUsage, "pull", "UPPER:1")                              // bad ref
	expect(t, cli.ExitFailure, "pull", "ubuntu:1-r1", "--profile", "nope")   // bad profile
	expect(
		t,
		cli.ExitFailure,
		"pull",
		"ubuntu:1-r1",
		"--to",
		file,
		"--platform",
		"linux/amd64",
		"--verify",
		"none",
	)
	t.Setenv("WEAVEOCI_CACHE", filepath.Join(file, "cache"))
	expect(t, cli.ExitFailure, "pull", "ubuntu:1-r1")
	t.Setenv("WEAVEOCI_CACHE", e.p("cache"))

	// verify remote and cached
	if out := mustRun(t, "verify", "ubuntu:1-r1"); !strings.Contains(out, "verified") {
		t.Fatal(out)
	}
	mustRun(t, "verify", "ubuntu:1-r1", "--cached")
	expect(t, cli.ExitFailure, "verify", "plain:1-r1")
	expect(t, cli.ExitFailure, "verify", "missing:1")
	expect(t, cli.ExitFailure, "verify", "missing:1", "--cached")
	expect(t, cli.ExitFailure, "verify", "ubuntu:1-r1", "--verify", "trust-me")
	expect(t, cli.ExitUsage, "verify", "UPPER:1")
	expect(t, cli.ExitFailure, "verify", "ubuntu:1-r1", "--profile", "nope")
	t.Setenv("WEAVEOCI_CACHE", filepath.Join(file, "cache"))
	expect(t, cli.ExitFailure, "verify", "ubuntu:1-r1", "--cached")
	t.Setenv("WEAVEOCI_CACHE", e.p("cache"))

	// sign the unsigned artifact, with the profile key and with --key
	mustRun(t, "sign", "plain:1-r1")
	mustRun(t, "pull", "plain:1-r1")
	mustRun(t, "sign", "plain:1-r1", "--key", e.p("keys/other.key"))
	expect(t, cli.ExitFailure, "sign", "plain:1-r1", "--key", e.p("missing.key"))
	expect(t, cli.ExitUsage, "sign", "UPPER:1")
	expect(t, cli.ExitFailure, "sign", "missing:1")
	expect(t, cli.ExitUsage, "sign", "plain:1-r1", "--profile", "open")
	expect(t, cli.ExitFailure, "sign", "plain:1-r1", "--profile", "nope")

	// push a packed layout; the build tag is then immutable
	layout := e.p("layout")
	mustRun(t, "pack", arm, "--out", layout, "--tag", "t")
	if out := mustRun(t, "push", layout, "pushed:1-r1"); !digestRE.MatchString(out) {
		t.Fatal(out)
	}
	expect(t, cli.ExitFailure, "push", layout, "pushed:1-r1")
	mustRun(t, "push", layout, "pushed:1-r1", "--allow-existing")
	mustRun(t, "push", layout, "pushed:2-r1", "--ref", "t")
	expect(t, cli.ExitUsage, "push", layout, "UPPER:1")
	expect(t, cli.ExitFailure, "push", layout, "pushed:3", "--ref", "missing")
	expect(t, cli.ExitFailure, "push", layout, "pushed:3", "--profile", "nope")

	// export, import, gc
	out = mustRun(t, "export-layout", "ubuntu:1-r1", e.p("airgap"))
	if !strings.Contains(out, "exported") {
		t.Fatal(out)
	}
	expect(t, cli.ExitFailure, "export-layout", "never-pulled:1", e.p("airgap2"))
	expect(t, cli.ExitUsage, "export-layout", "UPPER:1", e.p("airgap2"))
	expect(t, cli.ExitFailure, "export-layout", "ubuntu:1-r1", e.p("airgap3"), "--profile", "nope")
	t.Setenv("WEAVEOCI_CACHE", e.p("site-cache"))
	if out := mustRun(t, "import-layout", e.p("airgap")); !digestRE.MatchString(out) {
		t.Fatal(out)
	}
	mustRun(t, "verify", "ubuntu:1-r1", "--cached")
	expect(t, cli.ExitFailure, "import-layout", filepath.Join(file, "x"))
	if out := mustRun(t, "gc", "--keep", "1"); !strings.Contains(out, "freed") {
		t.Fatal(out)
	}
	mustRun(t, "gc")
	expect(t, cli.ExitUsage, "gc", "--keep", "lots")
	t.Setenv("WEAVEOCI_CACHE", filepath.Join(file, "cache"))
	expect(t, cli.ExitFailure, "gc")
	expect(t, cli.ExitFailure, "import-layout", e.p("airgap"))
	expect(t, cli.ExitFailure, "export-layout", "ubuntu:1-r1", e.p("airgap4"))
}

func TestDefaultCacheDirectory(t *testing.T) {
	e := newEnv(t, testregistry.Options{})
	e.profile(t, privateProfile)
	t.Setenv("WEAVEOCI_CACHE", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("LocalAppData", filepath.Join(home, "AppData"))
	mustRun(t, "gc")
	var want string
	switch runtime.GOOS {
	case "darwin":
		want = filepath.Join(home, "Library", "Caches", "weave", "oci-cache")
	case "windows":
		want = filepath.Join(home, "AppData", "weave", "oci-cache")
	default:
		want = filepath.Join(home, ".cache", "weave", "oci-cache")
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("default cache not created at %s: %v", want, err)
	}
	// no home directory at all: the cache cannot be located
	if runtime.GOOS != "windows" {
		t.Setenv("HOME", "")
		t.Setenv("XDG_CACHE_HOME", "")
		expect(t, cli.ExitFailure, "gc")
	}
}
