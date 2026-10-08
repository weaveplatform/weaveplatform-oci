package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/testing/data"

	"github.com/weaveplatform/weaveplatform-oci/internal/testbundle"
	"github.com/weaveplatform/weaveplatform-oci/pkg/channel"
)

func TestAdmissionCLILoadsOnlyReviewedPolicyAndSignedEvidence(t *testing.T) {
	dir := t.TempDir()
	b := writeBundle(t, testbundle.Options{OS: "linux", Arch: "arm64"})
	layout := filepath.Join(dir, "layout")
	if code, _, stderr := run(t, "pack", b, "--out", layout, "--tag", "r1"); code != 0 {
		t.Fatal(stderr)
	}
	root, _ := data.TrustedRoot(t, "public-good.json").MarshalJSON()
	_, pub, _ := channel.GenerateKey(channel.RootKeyID)
	policy := map[string]any{
		"registry":    "ghcr.io",
		"trustedRoot": "root.json",
		"channel":     "stable.json",
		"build": map[string]string{
			"issuer":        "https://token.actions.githubusercontent.com",
			"subjectRegexp": "^build$",
		},
		"acceptance": map[string]string{
			"issuer":        "https://token.actions.githubusercontent.com",
			"subjectRegexp": "^accept$",
		},
		"anchors": []any{map[string]string{"name": "org", "publicKey": "anchor.pub"}},
	}
	raw, _ := json.Marshal(policy)
	file := filepath.Join(dir, "policy.json")
	os.WriteFile(file, raw, 0o600)
	args := []string{
		"image",
		"verify-acceptance",
		layout,
		"--policy",
		file,
		"--tag",
		"r1",
		"--provenance",
		filepath.Join(dir, "build.json"),
		"--acceptance",
		filepath.Join(dir, "accept.json"),
	}
	for _, step := range []string{"root missing", "anchor missing", "anchor invalid", "channel missing", "channel loaded", "build loaded", "acceptance loaded"} {
		switch step {
		case "anchor missing":
			os.WriteFile(filepath.Join(dir, "root.json"), root, 0o600)
		case "anchor invalid":
			os.WriteFile(filepath.Join(dir, "anchor.pub"), []byte("bad"), 0o600)
		case "channel missing":
			os.WriteFile(filepath.Join(dir, "anchor.pub"), pub, 0o600)
		case "channel loaded":
			for _, name := range []string{"stable.json", "stable.json.sig", "signing.pub", "signing.pub.sig"} {
				os.WriteFile(filepath.Join(dir, name), []byte(`{}`), 0o600)
			}
		case "build loaded":
			os.WriteFile(filepath.Join(dir, "build.json"), []byte("bad"), 0o600)
		case "acceptance loaded":
			os.WriteFile(filepath.Join(dir, "accept.json"), []byte("bad"), 0o600)
		}
		if code, _, stderr := run(t, args...); code == 0 {
			t.Fatal(step, stderr)
		}
	}
	args[2] = filepath.Join(dir, "absent-layout")
	if code, _, _ := run(t, args...); code == 0 {
		t.Fatal("missing layout accepted")
	}
}
