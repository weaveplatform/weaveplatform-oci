package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weaveplatform/weaveplatform-oci/pkg/handoff"
)

func TestImageweaveImportCLI(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "disk.raw"), make([]byte, 512), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := `{"builds":[{"name":"guest","builder_type":"qemu","build_time":1791379681,"files":[{"name":"disk.raw","size":512}],"artifact_id":"VM","packer_run_uuid":"run","custom_data":{"qualification":"unverified","family":"ubuntu","release":"26.04","arch":"arm64","purpose":"guest-base","target":"qemu","source_build":"20260927","source_sha256":"HASH","firmware_code_sha256":"HASH","firmware_vars_sha256":"HASH"}}],"last_run_uuid":"run"}`
	file := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(
		file,
		[]byte(strings.ReplaceAll(manifest, "HASH", strings.Repeat("b", 64))),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	args := []string{
		"bundle",
		"import-imageweave",
		file,
		"--recipe-commit",
		strings.Repeat("a", 40),
		"--source-uri",
		"https://vendor.example/source.img",
		"--version",
		"26.04-test-r1",
		"--out",
		filepath.Join(dir, "bundle"),
	}
	code, out, stderr := run(t, args...)
	if code != 0 {
		t.Fatalf("%d %s", code, stderr)
	}
	var receipt handoff.Receipt
	if err := json.Unmarshal([]byte(out), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Qualification != "unverified" || receipt.Bundle.Guest.OS != "linux" {
		t.Fatal(receipt)
	}
	if code, _, _ := run(t, args...); code == 0 {
		t.Fatal("overwrote bundle")
	}
	if code, _, _ := run(t, "bundle", "import-imageweave", file); code != 2 {
		t.Fatal("required flags", code)
	}
	if code, _, _ := run(t, "image", "build-macos"); code == 0 {
		t.Fatal("retired builder still active")
	}
}
