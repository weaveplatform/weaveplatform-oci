package agentcheck

import (
	"strings"
	"testing"

	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/weaveclient"
	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/weaveexec"
)

func linuxOutput() string {
	return "26.04\n20261001\nboot-uuid\n" + strings.Repeat(
		"a",
		32,
	) + "\nsha256:" + strings.Repeat(
		"b",
		64,
	) + "\n9\nsealed\nWEAVE-BOOT-OK-0123456789abcdef01234567\n"
}

func TestLinuxObservationUsesRunningGuest(t *testing.T) {
	for _, bad := range []bool{false, true} {
		output := linuxOutput()
		if bad {
			output = strings.Repeat("x", 4097)
		}
		conn := connection(t, nil, weaveexec.NewService(starter{output: output}))
		client := weaveclient.New(t.Context(), conn, weaveclient.Options{})
		defer client.Close()
		observation, err := InspectLinux(t.Context(), client)
		if bad {
			if err == nil {
				t.Fatal("oversized response accepted")
			}
			continue
		}
		if err != nil || observation.Version != "26.04" || observation.Build != "20261001" ||
			observation.ManifestSequence != 9 {
			t.Fatal(observation, err)
		}
	}
}

func TestLinuxObservationRejectsMalformedOrUnsealedGuest(t *testing.T) {
	for _, output := range []string{"", linuxOutput() + "extra", strings.ReplaceAll(linuxOutput(), "sealed", "unknown"), strings.ReplaceAll(linuxOutput(), "sealed", "build-credentials"), strings.ReplaceAll(linuxOutput(), "\n9\n", "\ninvalid\n"), strings.ReplaceAll(linuxOutput(), "sha256:", "raw:")} {
		if _, err := parseLinuxObservation(output); err == nil {
			t.Fatal("accepted", output)
		}
	}
}
