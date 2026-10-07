package cli_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weaveplatform/weaveplatform-oci/internal/imagebuild"
	"github.com/weaveplatform/weaveplatform-oci/internal/testbundle"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

func TestImageExportCLI(t *testing.T) {
	b := writeBundle(t, testbundle.Options{OS: spec.OSWindows, Arch: spec.ArchAMD64})
	layout := filepath.Join(t.TempDir(), "layout")
	if code, _, stderr := run(t, "pack", b, "--out", layout, "--tag", "r1"); code != 0 {
		t.Fatal(stderr)
	}
	store, err := pack.OpenLayout(t.Context(), layout)
	if err != nil {
		t.Fatal(err)
	}
	root, err := store.Resolve(t.Context(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "export")
	args := []string{
		"image",
		"export",
		layout,
		"--out",
		out,
		"--platform",
		"windows/amd64",
		"--expected-digest",
		root.Digest.String(),
		"--target",
		"aws",
	}
	code, stdout, stderr := run(t, args...)
	if code != 0 {
		t.Fatalf("%d %s", code, stderr)
	}
	var result imagebuild.ExportResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Acceptance != "pending" || result.IndexDigest != root.Digest.String() ||
		len(result.Files) != 1 {
		t.Fatal(stdout)
	}
	if code, _, _ := run(t, args...); code == 0 {
		t.Fatal("reused export destination")
	}
	if code, _, stderr := run(
		t,
		"image",
		"export",
		layout,
	); code != 2 ||
		!strings.Contains(stderr, "--target") {
		t.Fatal(code, stderr)
	}
}

func TestPrepareAgentUsageAndLockFailures(t *testing.T) {
	if code, _, stderr := run(t, "image", "prepare-agent"); code != 2 {
		t.Fatal(code, stderr)
	}
	args := []string{
		"image",
		"prepare-agent",
		"--platform",
		"linux/arm64",
		"--cache",
		t.TempDir(),
		"--out",
		filepath.Join(t.TempDir(), "payload"),
	}
	if code, _, _ := run(t, args...); code == 0 {
		t.Fatal("accepted missing default catalogue")
	}
	args = append(
		args,
		"--catalogue",
		"../../images/catalogue.json",
		"--lock",
		"../../images/packages.lock.json",
	)
	if code, _, stderr := run(
		t,
		args...); code == 0 ||
		!strings.Contains(stderr, "installer missing") {
		t.Fatal(code, stderr)
	}
}
