package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deploymenttheory/weaveplatform-oci/internal/cli"
	"github.com/deploymenttheory/weaveplatform-oci/internal/testbundle"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/spec"
)

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := cli.Main(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

func writeBundle(t *testing.T, o testbundle.Options) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), o.OS+"-"+o.Arch)
	if err := testbundle.Write(dir, o); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestVersionAndUsage(t *testing.T) {
	if code, out, _ := run(
		t,
		"version",
	); code != cli.ExitOK ||
		!strings.HasPrefix(out, "weaveoci devel") {
		t.Fatalf("%d %q", code, out)
	}
	usage := [][]string{
		{"version", "extra"},
		{"pack"},
		{"pack", "somewhere"},
		{"pack", "--bogus"},
		{"inspect"},
		{"unpack", "a"},
	}
	for _, args := range usage {
		if code, _, errOut := run(
			t,
			args...); code != cli.ExitUsage ||
			!strings.Contains(errOut, "usage") {
			t.Errorf("%v: %d %q", args, code, errOut)
		}
	}
	if code, _, _ := run(t, "no-such-command"); code != cli.ExitFailure {
		t.Fatalf("unknown command: %d", code)
	}
}

func TestPackInspectUnpack(t *testing.T) {
	b := writeBundle(
		t,
		testbundle.Options{OS: spec.OSDarwin, Arch: spec.ArchARM64, ExtraDisk: true},
	)
	layout := filepath.Join(t.TempDir(), "layout")
	code, out, errOut := run(t, "pack", b, "--out", layout, "--tag", "26.0-25A354-r1")
	if code != cli.ExitOK || !strings.HasPrefix(out, "index sha256:") ||
		!strings.Contains(out, "darwin/arm64 26.0") {
		t.Fatalf("pack: %d %q %q", code, out, errOut)
	}
	code, out, _ = run(t, "inspect", layout, "--strict", "--deep")
	if code != cli.ExitOK || !strings.Contains(out, ": conformant") ||
		!strings.Contains(out, "state auxstorage:carry") ||
		!strings.Contains(out, "disk disk1 1.00 MiB chunks=1 zero=1") {
		t.Fatalf("inspect: %d %q", code, out)
	}
	code, out, _ = run(t, "inspect", layout, "--json")
	var ir struct {
		OK       bool
		Problems []string
		Children []struct{ Platform string }
	}
	if code != cli.ExitOK || json.Unmarshal([]byte(out), &ir) != nil || !ir.OK ||
		ir.Children[0].Platform != "darwin/arm64 26.0" {
		t.Fatalf("inspect json: %d %q", code, out)
	}
	dst := filepath.Join(t.TempDir(), "out")
	code, out, _ = run(t, "unpack", layout, dst)
	if code != cli.ExitOK || !strings.Contains(out, "fetched=1 zero=1") {
		t.Fatalf("unpack: %d %q", code, out)
	}
	code, out, _ = run(t, "unpack", layout, dst, "--resume", "--json")
	var ur struct{ Resumed int64 }
	if code != cli.ExitOK || json.Unmarshal([]byte(out), &ur) != nil || ur.Resumed != 2 {
		t.Fatalf("resume: %d %q", code, out)
	}
	orig, _ := os.ReadFile(filepath.Join(b, "disk0.img"))
	got, _ := os.ReadFile(filepath.Join(dst, "disk0.img"))
	if !bytes.Equal(orig, got) {
		t.Fatal("unpacked disk differs")
	}
	// a second tag makes --ref necessary
	if code, _, _ := run(
		t,
		"pack",
		b,
		"--out",
		layout,
		"--tag",
		"latest",
		"--json",
	); code != cli.ExitOK {
		t.Fatal("second pack")
	}
	if code, _, errOut := run(
		t,
		"inspect",
		layout,
	); code != cli.ExitUsage ||
		!strings.Contains(errOut, "--ref") {
		t.Fatalf("ambiguous: %d %q", code, errOut)
	}
	if code, _, _ := run(t, "inspect", layout, "--ref", "latest"); code != cli.ExitOK {
		t.Fatal("--ref latest")
	}
	if code, _, _ := run(t, "inspect", layout, "--ref", "missing"); code != cli.ExitFailure {
		t.Fatal("missing ref")
	}
}

func TestMultiArchAndPlatformSelection(t *testing.T) {
	arm := writeBundle(t, testbundle.Options{OS: spec.OSLinux, Arch: spec.ArchARM64, Seed: 1})
	amd := writeBundle(t, testbundle.Options{OS: spec.OSLinux, Arch: spec.ArchAMD64, Seed: 2})
	layout := filepath.Join(t.TempDir(), "layout")
	code, out, _ := run(
		t,
		"pack",
		arm,
		amd,
		"--out",
		layout,
		"--tag",
		"24.04-20260915-r1",
		"--json",
	)
	var pr struct{ Children []struct{ Platform string } }
	if code != cli.ExitOK || json.Unmarshal([]byte(out), &pr) != nil || len(pr.Children) != 2 {
		t.Fatalf("pack: %d %q", code, out)
	}
	dst := t.TempDir()
	if code, _, errOut := run(
		t,
		"unpack",
		layout,
		dst,
	); code != cli.ExitUsage ||
		!strings.Contains(errOut, "--platform") {
		t.Fatalf("no platform: %d %q", code, errOut)
	}
	if code, out, _ := run(
		t,
		"unpack",
		layout,
		dst,
		"--platform",
		"linux/amd64",
	); code != cli.ExitOK ||
		!strings.Contains(out, "linux/amd64") {
		t.Fatalf("amd64: %d %q", code, out)
	}
	for _, p := range []string{"linux", "/amd64"} {
		if code, _, _ := run(t, "unpack", layout, dst, "--platform", p); code != cli.ExitUsage {
			t.Fatalf("bad platform %q accepted", p)
		}
	}
	if code, _, _ := run(
		t,
		"unpack",
		layout,
		dst,
		"--platform",
		"windows/amd64",
	); code != cli.ExitFailure {
		t.Fatal("unmatched platform accepted")
	}
	// mixing OS families fails at index time
	win := writeBundle(t, testbundle.Options{OS: spec.OSWindows, Arch: spec.ArchAMD64})
	if code, _, errOut := run(
		t,
		"pack",
		arm,
		win,
		"--out",
		filepath.Join(t.TempDir(), "l"),
	); code != cli.ExitFailure ||
		!strings.Contains(errOut, "rule 5") {
		t.Fatalf("mixed: %d %q", code, errOut)
	}
}

func TestInspectNonConformantAndPackFailures(t *testing.T) {
	b := writeBundle(t, testbundle.Options{OS: spec.OSWindows, Arch: spec.ArchAMD64})
	layout := filepath.Join(t.TempDir(), "layout")
	if code, _, _ := run(t, "pack", b, "--out", layout, "--tag", "t"); code != cli.ExitOK {
		t.Fatal("pack")
	}
	// corrupt every blob that is not JSON metadata: the chunk and the state file
	blobs, _ := filepath.Glob(filepath.Join(layout, "blobs", "sha256", "*"))
	for _, p := range blobs {
		raw, _ := os.ReadFile(p)
		if len(raw) > 0 && raw[0] != '{' {
			raw[len(raw)-1] ^= 0xff
			if err := os.Chmod(p, 0o600); err != nil { // layout blobs are written read-only
				t.Fatal(err)
			}
			if err := os.WriteFile(p, raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	code, out, errOut := run(t, "inspect", layout, "--deep")
	if code != cli.ExitOK || !strings.Contains(out, "NOT conformant") ||
		!strings.Contains(errOut, "warning") {
		t.Fatalf("lenient: %d %q %q", code, out, errOut)
	}
	if code, _, errOut := run(
		t,
		"inspect",
		layout,
		"--deep",
		"--strict",
	); code != cli.ExitFailure ||
		!strings.Contains(errOut, "not conformant") {
		t.Fatalf("strict: %d %q", code, errOut)
	}
	if code, _, _ := run(t, "unpack", layout, t.TempDir()); code != cli.ExitFailure {
		t.Fatal("unpack of a corrupt layout succeeded")
	}
	// pack failures: missing bundle, layout path is a file, invalid bundle content
	if code, _, _ := run(
		t,
		"pack",
		filepath.Join(t.TempDir(), "none"),
		"--out",
		layout,
	); code != cli.ExitFailure {
		t.Fatal("missing bundle")
	}
	file := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(file, nil, 0o600)
	if code, _, _ := run(t, "pack", b, "--out", file); code != cli.ExitFailure {
		t.Fatal("layout is a file")
	}
	if code, _, _ := run(t, "inspect", file); code != cli.ExitFailure {
		t.Fatal("inspect of a file")
	}
	raw, _ := os.ReadFile(filepath.Join(b, "bundle.json"))
	_ = os.WriteFile(
		filepath.Join(b, "bundle.json"),
		bytes.Replace(raw, []byte(`"tpm": "required"`), []byte(`"tpm": "maybe"`), 1),
		0o600,
	)
	if code, _, errOut := run(
		t,
		"pack",
		b,
		"--out",
		filepath.Join(t.TempDir(), "l2"),
	); code != cli.ExitFailure ||
		!strings.Contains(errOut, "rule 8") {
		t.Fatalf("invalid bundle: %d %q", code, errOut)
	}
}

func TestEmptyLayoutNeedsRef(t *testing.T) {
	layout := t.TempDir()
	if code, _, errOut := run(
		t,
		"inspect",
		layout,
	); code != cli.ExitUsage ||
		!strings.Contains(errOut, "0 tags") {
		t.Fatalf("%d %q", code, errOut)
	}
}

func TestContractSizeThroughCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("contract-size disk")
	}
	b := writeBundle(
		t,
		testbundle.Options{OS: spec.OSLinux, Arch: spec.ArchARM64, Size: testbundle.ContractSize},
	)
	layout := filepath.Join(t.TempDir(), "layout")
	code, out, _ := run(t, "pack", b, "--out", layout, "--tag", "t", "--concurrency", "3")
	if code != cli.ExitOK || !strings.Contains(out, "chunks=3 zero=1 size=1.25 GiB") {
		t.Fatalf("%d %q", code, out)
	}
}

func TestHealthcheck(t *testing.T) {
	ok := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
		),
	)
	defer ok.Close()
	bad := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) },
		),
	)
	defer bad.Close()
	if code, out, _ := run(
		t,
		"healthcheck",
		"--url",
		ok.URL,
	); code != cli.ExitOK ||
		!strings.HasPrefix(out, "ok ") {
		t.Fatalf("%d %q", code, out)
	}
	if code, _, errOut := run(
		t,
		"healthcheck",
		"--url",
		bad.URL,
	); code != cli.ExitFailure ||
		!strings.Contains(errOut, "503") {
		t.Fatalf("%d %q", code, errOut)
	}
	if code, _, _ := run(
		t,
		"healthcheck",
		"--url",
		"http://127.0.0.1:1/readyz",
		"--timeout",
		"200ms",
	); code != cli.ExitFailure {
		t.Fatal("unreachable endpoint healthy")
	}
	if code, _, _ := run(t, "healthcheck", "--url", "://bad"); code != cli.ExitUsage {
		t.Fatal("bad url accepted")
	}
}
