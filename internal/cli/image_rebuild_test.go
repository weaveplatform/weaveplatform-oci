package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"

	"github.com/weaveplatform/weaveplatform-oci/internal/imagebuild"
)

func TestRebuildCLI(t *testing.T) {
	dir := t.TempDir()
	input, accepted := filepath.Join(dir, "inputs.json"), filepath.Join(dir, "accepted.json")
	for _, args := range [][]string{{"image", "rebuild-plan"}, {"image", "rebuild-plan", input}, {"image", "verify-acceptance"}, {"image", "verify-acceptance", dir}, {"image", "verify-acceptance", dir, "--policy", "missing", "--tag", "r1", "--provenance", "b", "--acceptance", "a"}} {
		if code, _, _ := run(t, args...); code == 0 {
			t.Fatal(args)
		}
	}
	in := imagebuild.BuildInputs{
		Image:         "ubuntu-26.04-base",
		Platform:      "linux/arm64",
		Tier:          "base",
		SourceDigest:  digest.FromString("source").String(),
		RecipeDigest:  digest.FromString("recipe").String(),
		BuilderDigest: digest.FromString("builder").String(),
	}
	data, _ := json.Marshal([]imagebuild.BuildInputs{in})
	os.WriteFile(input, data, 0o600)
	if code, out, stderr := run(
		t,
		"image",
		"rebuild-plan",
		input,
	); code != 0 ||
		!strings.Contains(out, `"action":"build"`) {
		t.Fatal(code, out, stderr)
	}
	for _, raw := range []string{"missing", "bad", "[]", `[{"image":"bad"}]`} {
		if raw != "missing" {
			os.WriteFile(accepted, []byte(raw), 0o600)
		}
		code, _, stderr := run(t, "image", "rebuild-plan", input, "--accepted", accepted)
		if (code == 0) != (raw == "[]") {
			t.Fatal(raw, code, stderr)
		}
	}
	os.WriteFile(input, []byte("bad"), 0o600)
	if code, _, _ := run(t, "image", "rebuild-plan", input); code == 0 {
		t.Fatal("invalid JSON accepted")
	}
	os.WriteFile(input, []byte(`[{}]`), 0o600)
	if code, _, _ := run(t, "image", "rebuild-plan", input); code == 0 {
		t.Fatal("incomplete input accepted")
	}
}
