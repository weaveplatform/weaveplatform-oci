package cli_test

import (
	"strings"
	"testing"
)

func TestImageCommandsBelongToTheirOwner(t *testing.T) {
	code, out, stderr := run(t, "image")
	if code != 0 || !strings.Contains(out, "imageweave image") {
		t.Fatalf("help: %d %s %s", code, out, stderr)
	}
	for _, command := range []string{"verify-acceptance", "verify-published", "verify-candidate"} {
		if !strings.Contains(out, command) {
			t.Fatalf("missing verifier %s: %s", command, out)
		}
	}
	for _, command := range []string{"matrix", "check-lock", "lock", "ipsw", "windows", "build-windows", "build-linux-agent", "build-windows-agent", "build-linux-desktop", "prepare-agent", "boot-linux", "validate-linux", "validate-agent-linux", "export", "rebuild-plan"} {
		if code, _, _ := run(t, "image", command); code == 0 {
			t.Fatalf("retired builder command %s succeeded", command)
		}
	}
}
