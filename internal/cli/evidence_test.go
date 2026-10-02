package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deploymenttheory/weaveplatform-oci/internal/cli"
	"github.com/deploymenttheory/weaveplatform-oci/internal/testbundle"
	"github.com/deploymenttheory/weaveplatform-oci/internal/testregistry"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/spec"
)

const channelProfile = `schemaVersion: 1
profiles:
  - name: org
    kind: private
    registry: {host: "{registry}", namespace: weave-images, plainHTTP: true}
    signing: {provider: cosign-key, key: cosign.key}
    verify: {mode: both, publicKeys: [cosign.pub]}
    channel:
      manifest: ch/stable.json
      anchors: [{name: org-root, publicKey: root.pub}]
`

func TestChannelEvidenceOnPullAndVerify(t *testing.T) {
	e := newEnv(t, testregistry.Options{})
	e.profile(t, channelProfile)
	mustRun(t, "keygen", e.p("cosign"))
	mustRun(t, "channel", "keygen", "root", e.p("root"))
	mustRun(t, "channel", "keygen", "signing", e.p("ch/signing"))
	mustRun(t, "channel", "endorse", e.p("root.key"), e.p("ch/signing.pub"))
	mustRun(t, "channel", "new", "org", e.p("ch/stable.json"))
	b := writeBundle(t, testbundle.Options{OS: spec.OSLinux, Arch: spec.ArchARM64})
	mustRun(t, "publish", b, "--repository", "ubuntu", "--tag", "1-r1", "--promotion-out", e.p("entry.json"))
	mustRun(t, "channel", "promote", e.p("ch/stable.json"), e.p("entry.json"))
	mustRun(t, "channel", "sign", e.p("ch/signing.key"), e.p("ch/stable.json"))
	for _, args := range [][]string{{"pull", "ubuntu:1-r1"}, {"verify", "ubuntu:1-r1"}} {
		out := mustRun(t, args...)
		if !strings.Contains(out, "channel: org (anchor org-root, sequence 1)") || !strings.Contains(out, "signature: cosign-key") {
			t.Fatalf("%v: %s", args, out)
		}
	}
	// single-platform index: --to without --platform picks the only child
	mustRun(t, "pull", "ubuntu:1-r1", "--to", e.p("out"))
}

func TestKeygenWriteFailures(t *testing.T) {
	newEnv(t, testregistry.Options{})
	dir := t.TempDir()
	_ = os.Mkdir(filepath.Join(dir, "a.key"), 0o750)
	expect(t, cli.ExitFailure, "keygen", filepath.Join(dir, "a"))
	_ = os.Mkdir(filepath.Join(dir, "b.pub"), 0o750)
	expect(t, cli.ExitFailure, "keygen", filepath.Join(dir, "b"))
}
