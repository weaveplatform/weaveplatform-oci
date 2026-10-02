package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"

	"github.com/deploymenttheory/weaveplatform-oci/internal/testbundle"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/pack"
)

type world struct {
	t       *testing.T
	env     map[string]string
	vars    map[string]string
	root    string
	exit    int
	stdout  string
	stderr  string
	reg     *registry
	copyErr error
}

func newWorld(t *testing.T) *world { return &world{t: t} }

func (w *world) register(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		dir, err := os.MkdirTemp("", "weaveoci-scenario-")
		w.root, w.reg, w.copyErr = dir, nil, nil
		w.env = map[string]string{"WEAVEOCI_CACHE": filepath.Join(dir, "cache"), "DOCKER_CONFIG": filepath.Join(dir, "docker")}
		w.vars = map[string]string{"run": strings.ToLower(filepath.Base(dir)[len("weaveoci-scenario-"):])}
		return ctx, err
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
		_ = os.RemoveAll(w.root)
		return ctx, err
	})
	sc.Step(
		`^an? (darwin|windows|linux) (arm64|amd64) bundle "([^"]+)"( with a zero data disk)?$`,
		w.bundle,
	)
	sc.Step(
		`^a contract-size (darwin|windows|linux) (arm64|amd64) bundle "([^"]+)"$`,
		w.contractBundle,
	)
	sc.Step(`^I run weaveoci "([^"]*)"$`, w.runCLI)
	sc.Step(`^the exit code is (\d+)$`, w.exitCode)
	sc.Step(`^the output contains "([^"]*)"$`, w.outputContains)
	sc.Step(`^the error output contains "([^"]*)"$`, w.errorContains)
	sc.Step(`^the file "([^"]+)" equals the file "([^"]+)"$`, w.filesEqual)
	sc.Step(`^the file "([^"]+)" is sparse$`, w.fileSparse)
	sc.Step(`^bundle "([^"]+)" declares a machine identifier in its firmware$`, w.injectECID)
	sc.Step(`^I corrupt a disk chunk in layout "([^"]+)"$`, w.corruptChunk)
	sc.Step(`^I overwrite 1 MiB at offset (\d+) MiB of "([^"]+)"$`, w.overwrite)
	sc.Step(`^an? (weave-zot|distribution) registry is running$`, w.registryRunning)
	sc.Step(`^the registry container is healthy$`, w.registryHealthy)
	sc.Step(
		`^I copy layout "([^"]+)" tag "([^"]+)" to "([^"]+)" on the registry as "([^"]+)"$`,
		w.copyUp,
	)
	sc.Step(`^the copy (succeeds|is refused)$`, w.copyResult)
	sc.Step(`^I copy "([^"]+)" from the registry as "([^"]+)" into layout "([^"]+)"$`, w.copyDown)
	sc.Step(`^layout "([^"]+)" has the same index digest as layout "([^"]+)"$`, w.sameDigest)
	sc.Step(`^I attach a signature referrer to "([^"]+)" on the registry$`, w.attachReferrer)
	sc.Step(
		`^the registry (serves|does not serve) the referrers API for "([^"]+)"$`,
		w.referrersAPI,
	)
	sc.Step(`^a signature referrer is discoverable for "([^"]+)"$`, w.referrerDiscoverable)
	sc.Step(`^the environment variable "([^"]+)" is "([^"]*)"$`, w.setEnv)
	sc.Step(`^I use the registry as "([^"]+)"$`, w.useRegistryAs)
	sc.Step(`^a profile file "([^"]+)":$`, w.profileFile)
	sc.Step(`^cosign verifies "([^"]+)" with key "([^"]+)"$`, w.cosignVerifies)
	sc.Step(`^I remember the published digest as "([^"]+)"$`, w.rememberDigest)
}

// path maps a scenario-local name to a path under the scenario directory.
func (w *world) path(name string) string { return filepath.Join(w.root, name) }

var placeholder = regexp.MustCompile(`\{([A-Za-z0-9._/-]+)\}`)

func (w *world) bundle(osName, arch, name, zero string) error {
	return testbundle.Write(
		w.path(name),
		testbundle.Options{OS: osName, Arch: arch, ExtraDisk: zero != "", Seed: uint64(len(name))},
	)
}

func (w *world) contractBundle(osName, arch, name string) error {
	return testbundle.Write(
		w.path(name),
		testbundle.Options{OS: osName, Arch: arch, Size: testbundle.ContractSize},
	)
}

func (w *world) expand(s string) string {
	return placeholder.ReplaceAllStringFunc(s, func(m string) string {
		name := m[1 : len(m)-1]
		if name == "registry" && w.reg != nil {
			return w.reg.host
		}
		if v, ok := w.vars[name]; ok {
			return v
		}
		return w.path(name)
	})
}

func (w *world) environ() []string {
	env := os.Environ()
	for k, v := range w.env {
		env = append(env, k+"="+v)
	}
	return env
}

func (w *world) setEnv(k, v string) error {
	w.env[k] = w.expand(v)
	return nil
}

func (w *world) useRegistryAs(user string) error {
	if w.reg == nil || w.reg.kind != "weave-zot" {
		return nil // distribution runs without authentication
	}
	pw := map[string]string{"publisher": publisherPassword, "admin": adminPassword}[user]
	w.env["WEAVE_REGISTRY_USERNAME"], w.env["WEAVE_REGISTRY_PASSWORD"] = user, pw
	return nil
}

func (w *world) profileFile(name string, doc *godog.DocString) error {
	return os.WriteFile(w.path(name), []byte(w.expand(doc.Content)), 0o600)
}

var publishedDigest = regexp.MustCompile(`published \S+ (sha256:[0-9a-f]{64})`)

func (w *world) rememberDigest(name string) error {
	m := publishedDigest.FindStringSubmatch(w.stdout)
	if m == nil {
		return fmt.Errorf("no published digest in output:\n%s", w.stdout)
	}
	w.vars[name] = m[1]
	return nil
}

func (w *world) cosignVerifies(ref, key string) error {
	bin, err := exec.LookPath("cosign")
	if err != nil {
		home, _ := os.UserHomeDir()
		bin = filepath.Join(home, "go", "bin", "cosign")
		if _, err := os.Stat(bin); err != nil {
			return errors.New("cosign v3 is required for the interop check (go install github.com/sigstore/cosign/v3/cmd/cosign@v3.1.3)")
		}
	}
	args := []string{"verify", "--key", w.expand(key), "--insecure-ignore-tlog", "--allow-http-registry", "--allow-insecure-registry"}
	if w.reg.kind == "weave-zot" {
		args = append(args, "--registry-username", "publisher", "--registry-password", publisherPassword)
	}
	cmd := exec.Command(bin, append(args, w.expand(ref))...) //nolint:gosec // test tool
	cmd.Env = w.environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("cosign verify failed: %w\n%s", err, out)
	}
	return nil
}

func (w *world) runCLI(line string) error {
	line = w.expand(line)
	cmd := exec.Command(binary, strings.Fields(line)...) //nolint:gosec // test binary
	cmd.Env = w.environ()
	if coverDir != "" {
		cmd.Env = append(cmd.Env, "GOCOVERDIR="+coverDir)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	w.stdout, w.stderr, w.exit = out.String(), errb.String(), 0
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		w.exit = ee.ExitCode()
	case err != nil:
		return err
	}
	return nil
}

func (w *world) exitCode(want int) error {
	if w.exit != want {
		return fmt.Errorf(
			"exit code %d, want %d\nstdout:\n%s\nstderr:\n%s",
			w.exit,
			want,
			w.stdout,
			w.stderr,
		)
	}
	return nil
}

func (w *world) outputContains(s string) error {
	s = w.expand(s)
	if !strings.Contains(w.stdout, s) {
		return fmt.Errorf("stdout lacks %q:\n%s", s, w.stdout)
	}
	return nil
}

func (w *world) errorContains(s string) error {
	s = w.expand(s)
	if !strings.Contains(w.stderr, s) {
		return fmt.Errorf("stderr lacks %q:\n%s", s, w.stderr)
	}
	return nil
}

func (w *world) filesEqual(a, b string) error {
	fa, err := os.Open(w.path(a))
	if err != nil {
		return err
	}
	defer fa.Close()
	fb, err := os.Open(w.path(b))
	if err != nil {
		return err
	}
	defer fb.Close()
	ba, bb := make([]byte, 1<<20), make([]byte, 1<<20)
	for off := int64(0); ; off += int64(len(ba)) {
		na, ea := io.ReadFull(fa, ba)
		nb, eb := io.ReadFull(fb, bb)
		if na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return fmt.Errorf("%s and %s differ near offset %d", a, b, off)
		}
		if ea != nil || eb != nil {
			return nil
		}
	}
}

func (w *world) fileSparse(name string) error {
	st, err := os.Stat(w.path(name))
	if err != nil {
		return err
	}
	alloc, ok := allocated(st)
	if !ok {
		return nil // the platform does not report allocation
	}
	if alloc*2 > st.Size() {
		return fmt.Errorf("%s allocates %d of %d bytes; not sparse", name, alloc, st.Size())
	}
	return nil
}

func (w *world) injectECID(name string) error {
	p := filepath.Join(w.path(name), pack.BundleFileName)
	raw, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	raw = bytes.Replace(
		raw,
		[]byte(`"firmware": {`),
		[]byte(`"firmware": {"ecid": "AAAAAAAAAAA=", `),
		1,
	)
	return os.WriteFile(p, raw, 0o600)
}

func (w *world) corruptChunk(layout string) error {
	blobs, err := filepath.Glob(filepath.Join(w.path(layout), "blobs", "sha256", "*"))
	if err != nil {
		return err
	}
	for _, p := range blobs {
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if len(raw) > 4 && bytes.Equal(raw[:4], []byte{0x28, 0xb5, 0x2f, 0xfd}) &&
			len(raw) > 64<<10 {
			raw[len(raw)/2] ^= 0xff
			if err := os.Chmod(p, 0o600); err != nil {
				return err
			}
			return os.WriteFile(p, raw, 0o600)
		}
	}
	return errors.New("no data chunk found in the layout")
}

func (w *world) overwrite(mib int, name string) error {
	f, err := os.OpenFile(w.path(name), os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteAt(bytes.Repeat([]byte{0x5a}, 1<<20), int64(mib)<<20)
	return err
}

func (w *world) registryRunning(kind string) error {
	r, err := registries.get(context.Background(), kind)
	w.reg = r
	return err
}

func (w *world) registryHealthy() error {
	st, err := w.reg.container.State(context.Background())
	if err != nil {
		return err
	}
	if st.Health == nil {
		return errors.New("the image declares no HEALTHCHECK")
	}
	for i := 0; i < 30 && st.Health.Status == "starting"; i++ {
		if err := sleep(context.Background()); err != nil {
			return err
		}
		if st, err = w.reg.container.State(context.Background()); err != nil {
			return err
		}
	}
	if st.Health.Status != "healthy" {
		return fmt.Errorf("container health %q", st.Health.Status)
	}
	return nil
}

func (w *world) repo(name, user string) (*remote.Repository, error) {
	r, err := remote.NewRepository(w.reg.host + "/" + name)
	if err != nil {
		return nil, err
	}
	r.PlainHTTP = true
	pw := map[string]string{"publisher": publisherPassword, "admin": adminPassword}[user]
	r.Client = &auth.Client{
		Client: retry.DefaultClient,
		Cache:  auth.NewCache(),
		Credential: func(context.Context, string) (auth.Credential, error) {
			if user == "" || w.reg.kind != "weave-zot" {
				return auth.EmptyCredential, nil
			}
			return auth.Credential{Username: user, Password: pw}, nil
		},
	}
	return r, nil
}

func splitRef(ref string) (string, string) {
	i := strings.LastIndex(ref, ":")
	return ref[:i], ref[i+1:]
}

func (w *world) copyUp(layout, tag, ref, user string) error {
	src, err := pack.OpenLayout(context.Background(), w.path(layout))
	if err != nil {
		return err
	}
	name, dstTag := splitRef(ref)
	dst, err := w.repo(name, user)
	if err != nil {
		return err
	}
	_, w.copyErr = oras.Copy(context.Background(), src, tag, dst, dstTag, oras.DefaultCopyOptions)
	return nil
}

func (w *world) copyResult(want string) error {
	if want == "succeeds" && w.copyErr != nil {
		return fmt.Errorf("copy failed: %w", w.copyErr)
	}
	if want == "is refused" && w.copyErr == nil {
		return errors.New("copy succeeded, want refusal")
	}
	return nil
}

func (w *world) copyDown(ref, user, layout string) error {
	name, tag := splitRef(ref)
	src, err := w.repo(name, user)
	if err != nil {
		return err
	}
	dst, err := pack.OpenLayout(context.Background(), w.path(layout))
	if err != nil {
		return err
	}
	_, err = oras.Copy(context.Background(), src, tag, dst, tag, oras.DefaultCopyOptions)
	return err
}

func indexDigest(dir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		return "", err
	}
	var idx ocispec.Index
	if err := json.Unmarshal(raw, &idx); err != nil {
		return "", err
	}
	if len(idx.Manifests) == 0 {
		return "", errors.New("empty layout")
	}
	return idx.Manifests[0].Digest.String(), nil
}

func (w *world) sameDigest(a, b string) error {
	da, err := indexDigest(w.path(a))
	if err != nil {
		return err
	}
	db, err := indexDigest(w.path(b))
	if err != nil {
		return err
	}
	if da != db {
		return fmt.Errorf("digests differ: %s vs %s", da, db)
	}
	return nil
}

const sigArtifactType = "application/vnd.dev.sigstore.bundle.v0.3+json"

func (w *world) attachReferrer(ref string) error {
	ctx := context.Background()
	name, tag := splitRef(ref)
	r, err := w.repo(name, "publisher")
	if err != nil {
		return err
	}
	subject, err := r.Resolve(ctx, tag)
	if err != nil {
		return err
	}
	blob := []byte(
		`{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json","note":"acceptance placeholder"}`,
	)
	layer := content.NewDescriptorFromBytes(sigArtifactType, blob)
	if err := r.Push(ctx, layer, bytes.NewReader(blob)); err != nil {
		return err
	}
	_, err = oras.PackManifest(
		ctx,
		r,
		oras.PackManifestVersion1_1,
		sigArtifactType,
		oras.PackManifestOptions{
			Subject: &subject, Layers: []ocispec.Descriptor{layer},
		},
	)
	return err
}

func (w *world) referrersAPI(mode, ref string) error {
	ctx := context.Background()
	name, tag := splitRef(ref)
	r, err := w.repo(name, "publisher")
	if err != nil {
		return err
	}
	subject, err := r.Resolve(ctx, tag)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		fmt.Sprintf("http://%s/v2/%s/referrers/%s", w.reg.host, name, subject.Digest),
		nil,
	)
	if err != nil {
		return err
	}
	if w.reg.kind == "weave-zot" {
		req.SetBasicAuth("publisher", publisherPassword)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	serves := resp.StatusCode == http.StatusOK
	if serves != (mode == "serves") {
		return fmt.Errorf("referrers API returned %d", resp.StatusCode)
	}
	return nil
}

func (w *world) referrerDiscoverable(ref string) error {
	ctx := context.Background()
	name, tag := splitRef(ref)
	r, err := w.repo(name, "publisher")
	if err != nil {
		return err
	}
	subject, err := r.Resolve(ctx, tag)
	if err != nil {
		return err
	}
	found := 0
	err = r.Referrers(ctx, subject, sigArtifactType, func(rs []ocispec.Descriptor) error {
		found += len(rs)
		return nil
	})
	if err != nil {
		return err
	}
	if found != 1 {
		return fmt.Errorf("found %s referrers, want 1", strconv.Itoa(found))
	}
	return nil
}
