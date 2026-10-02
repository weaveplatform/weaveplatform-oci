package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"

	"github.com/deploymenttheory/weaveplatform-oci/internal/cli"
)

func TestChannelCommands(t *testing.T) {
	dir := t.TempDir()
	p := func(n string) string { return filepath.Join(dir, n) }
	file := p("plainfile")
	_ = os.WriteFile(file, nil, 0o600)

	mustRun(t, "channel", "keygen", "root", p("root"))
	mustRun(t, "channel", "keygen", "signing-2026", p("ch/signing")) // nested directory created
	mustRun(t, "channel", "endorse", p("root.key"), p("ch/signing.pub"))
	mustRun(t, "channel", "new", "org", p("ch/stable.json"))
	d := digest.FromString("index")
	entry := `{"repository":"weave-images/ubuntu","tag":"1-r1","digest":"` + d.String() + `"}`
	_ = os.WriteFile(p("entry.json"), []byte(entry), 0o600)
	mustRun(t, "channel", "promote", p("ch/stable.json"), p("entry.json"))
	mustRun(t, "channel", "sign", p("ch/signing.key"), p("ch/stable.json"))

	out := mustRun(t, "channel", "verify", p("ch/stable.json"), "--anchor", p("root.pub"))
	if !strings.Contains(out, "channel org sequence 1 verified") || !strings.Contains(out, "1 image(s)") {
		t.Fatal(out)
	}
	out = mustRun(t, "channel", "verify", p("ch/stable.json"), "--anchor", p("root.pub"), "--digest", d.String(), "--repository", "weave-images/ubuntu")
	if !strings.Contains(out, "lists weave-images/ubuntu:1-r1") {
		t.Fatal(out)
	}

	// verify failures
	expect(t, cli.ExitUsage, "channel", "verify", p("ch/stable.json"))
	expect(t, cli.ExitFailure, "channel", "verify", p("ch/stable.json"), "--anchor", p("missing.pub"))
	expect(t, cli.ExitFailure, "channel", "verify", p("ch/stable.json"), "--anchor", p("ch/signing.pub")) // not a root key
	expect(t, cli.ExitFailure, "channel", "verify", p("missing.json"), "--anchor", p("root.pub"))
	expect(t, cli.ExitUsage, "channel", "verify", p("ch/stable.json"), "--anchor", p("root.pub"), "--digest", "nope")
	expect(t, cli.ExitFailure, "channel", "verify", p("ch/stable.json"), "--anchor", p("root.pub"), "--digest", digest.FromString("other").String())
	expect(t, cli.ExitFailure, "channel", "verify", p("ch/stable.json"), "--anchor", p("root.pub"), "--digest", d.String(), "--repository", "elsewhere")
	mustRun(t, "channel", "keygen", "root", p("otherroot"))
	expect(t, cli.ExitFailure, "channel", "verify", p("ch/stable.json"), "--anchor", p("otherroot.pub"))

	// keygen failures
	expect(t, cli.ExitFailure, "channel", "keygen", "", p("empty"))
	expect(t, cli.ExitFailure, "channel", "keygen", "x", filepath.Join(file, "k"))
	_ = os.Mkdir(p("dirkey.key"), 0o750)
	expect(t, cli.ExitFailure, "channel", "keygen", "x", p("dirkey"))
	_ = os.Mkdir(p("dirpub.pub"), 0o750)
	expect(t, cli.ExitFailure, "channel", "keygen", "x", p("dirpub"))

	// endorse failures
	expect(t, cli.ExitFailure, "channel", "endorse", p("missing.key"), p("ch/signing.pub"))
	expect(t, cli.ExitFailure, "channel", "endorse", p("root.key"), p("missing.pub"))
	expect(t, cli.ExitFailure, "channel", "endorse", p("ch/signing.key"), p("ch/signing.pub")) // not a root key

	// sign failures
	expect(t, cli.ExitFailure, "channel", "sign", p("missing.key"), p("ch/stable.json"))
	expect(t, cli.ExitFailure, "channel", "sign", p("entry.json"), p("ch/stable.json"))

	// promote failures
	_ = os.WriteFile(p("bad-entry.json"), []byte("{"), 0o600)
	expect(t, cli.ExitUsage, "channel", "promote", p("ch/stable.json"), p("bad-entry.json"))
	_ = os.WriteFile(p("invalid-entry.json"), []byte(`{"repository":"r","tag":"t","digest":"nope"}`), 0o600)
	expect(t, cli.ExitFailure, "channel", "promote", p("ch/stable.json"), p("invalid-entry.json"))
	expect(t, cli.ExitFailure, "channel", "promote", p("missing.json"), p("entry.json"))

	// new failures: the output path cannot be written
	expect(t, cli.ExitFailure, "channel", "new", "org", filepath.Join(file, "stable.json"))
	_ = os.Mkdir(p("dirout.json"), 0o750)
	expect(t, cli.ExitFailure, "channel", "new", "org", p("dirout.json"))
	expect(t, cli.ExitUsage, "channel", "new", "org")
}
