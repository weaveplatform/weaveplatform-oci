package cli_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/weaveplatform-oci/internal/cli"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
)

// cloudImageServer serves a raw "cloud image" with a SHA256SUMS signed by
// a fresh key, the way Ubuntu publishes them. It returns the base URL, the
// armored keyring path and the key's fingerprint.
func cloudImageServer(t *testing.T, image []byte) (string, string, string) {
	t.Helper()
	e, err := openpgp.NewEntity(
		"ubuntu",
		"",
		"cdimage@example.invalid",
		&packet.Config{Algorithm: packet.PubKeyAlgoEdDSA},
	)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(image)
	sums := []byte(hex.EncodeToString(h[:]) + " *noble-server-cloudimg-amd64.img\n")
	var sig, ring bytes.Buffer
	if err := openpgp.DetachSign(&sig, e, bytes.NewReader(sums), nil); err != nil {
		t.Fatal(err)
	}
	w, _ := armor.Encode(&ring, openpgp.PublicKeyType, nil)
	_ = e.Serialize(w)
	_ = w.Close()
	keyring := filepath.Join(t.TempDir(), "ubuntu.asc")
	_ = os.WriteFile(keyring, ring.Bytes(), 0o600)
	files := map[string][]byte{
		"noble-server-cloudimg-amd64.img": image,
		"SHA256SUMS":                      sums,
		"SHA256SUMS.gpg":                  sig.Bytes(),
	}
	r := chi.NewRouter()
	r.Get("/{file}", func(w http.ResponseWriter, req *http.Request) {
		b, ok := files[chi.URLParam(req, "file")]
		if !ok {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write(b)
	})
	s := httptest.NewServer(r)
	t.Cleanup(s.Close)
	return s.URL + "/", keyring, strings.ToUpper(hex.EncodeToString(e.PrimaryKey.Fingerprint))
}

func TestSourceFetchBundleInitPack(t *testing.T) {
	image := make([]byte, 3<<20)
	copy(image[1<<20:], bytes.Repeat([]byte("ext4"), 1024))
	base, keyring, fpr := cloudImageServer(t, image)
	dir := t.TempDir()
	raw, record := filepath.Join(dir, "work", "disk.raw"), filepath.Join(dir, "source.json")

	code, out, errOut := run(t, "source", "fetch", base+"noble-server-cloudimg-amd64.img",
		"--checksums", base+"SHA256SUMS", "--signature", base+"SHA256SUMS.gpg",
		"--keyring", keyring, "--fingerprint", fpr, "--out", raw, "--record", record)
	if code != cli.ExitOK || !strings.Contains(out, "openpgp-detached by "+fpr) {
		t.Fatalf("fetch: %d %s %s", code, out, errOut)
	}
	var rec struct{ Kind, Digest, Signer string }
	b, _ := os.ReadFile(record)
	if err := json.Unmarshal(
		b,
		&rec,
	); err != nil || rec.Kind != "cloud-image" ||
		rec.Signer != fpr {
		t.Fatalf("record %s", b)
	}

	bdir := filepath.Join(dir, "bundle-amd64")
	code, out, errOut = run(
		t,
		"bundle",
		"init",
		bdir,
		"--disk",
		raw,
		"--source",
		record,
		"--os",
		"linux",
		"--arch",
		"amd64",
		"--os-version",
		"24.04",
		"--os-build",
		"20260926",
		"--distro",
		"ubuntu",
		"--memory",
		"4GiB",
		"--memory-min",
		"512MiB",
		"--cpu",
		"2",
		"--template",
		"images/linux/ubuntu-24.04",
		"--template-ref",
		"weaveplatform/weaveplatform-oci@test",
		"--created",
		"2026-10-02T00:00:00Z",
		"--image-version",
		"24.04-20260926-r1",
		"--revision",
		"abc123",
		"--source-url",
		"https://github.com/weaveplatform/weaveplatform-oci",
		"--annotation",
		"org.opencontainers.image.vendor=weaveplatform",
	)
	if code != cli.ExitOK ||
		!strings.Contains(out, "linux/amd64 24.04 (20260926), 1 disk(s), 1 source(s)") {
		t.Fatalf("bundle init: %d %s %s", code, out, errOut)
	}
	if _, err := os.Stat(raw); err == nil {
		t.Fatal("the disk was copied, not moved")
	}
	bf, err := pack.LoadBundle(bdir)
	if err != nil {
		t.Fatal(err)
	}
	if bf.File.Resources.Memory.Default != 4<<30 || bf.File.Resources.Memory.Min != 512<<20 ||
		bf.File.Build.SourceMedia[0].Digest != rec.Digest || bf.File.Disks[0].Role != "system" {
		t.Fatalf("bundle %+v", bf.File)
	}

	layout := filepath.Join(dir, "layout")
	if code, out, errOut = run(
		t,
		"pack",
		bdir,
		"--out",
		layout,
		"--tag",
		"24.04-20260926-r1",
	); code != cli.ExitOK {
		t.Fatalf("pack: %d %s %s", code, out, errOut)
	}
	if code, out, errOut = run(t, "inspect", layout, "--strict", "--deep"); code != cli.ExitOK {
		t.Fatalf("inspect: %d %s %s", code, out, errOut)
	}
}

func TestSourceFetchRefusalsAndUsage(t *testing.T) {
	base, keyring, fpr := cloudImageServer(t, []byte("image"))
	_, otherRing, _ := cloudImageServer(t, []byte("image"))
	out := filepath.Join(t.TempDir(), "img")
	img := base + "noble-server-cloudimg-amd64.img"
	signed := []string{
		"--checksums",
		base + "SHA256SUMS",
		"--signature",
		base + "SHA256SUMS.gpg",
		"--out",
		out,
	}
	for name, c := range map[string]struct {
		args []string
		code int
	}{
		"wrong keyring":   {append([]string{"source", "fetch", img, "--keyring", otherRing, "--fingerprint", fpr}, signed...), cli.ExitFailure},
		"unsigned":        {[]string{"source", "fetch", img, "--checksums", base + "SHA256SUMS", "--out", out}, cli.ExitUsage},
		"bad kind":        {append([]string{"source", "fetch", img, "--kind", "floppy", "--keyring", keyring, "--fingerprint", fpr}, signed...), cli.ExitUsage},
		"missing keyring": {append([]string{"source", "fetch", img, "--keyring", filepath.Join(t.TempDir(), "nope"), "--fingerprint", fpr}, signed...), cli.ExitUsage},
		"no args":         {[]string{"source", "fetch"}, cli.ExitUsage},
		"unwritable record": {append([]string{
			"source", "fetch", img, "--keyring", keyring, "--fingerprint", fpr,
			"--record", filepath.Join(t.TempDir(), "missing", "r.json"),
		}, signed...), cli.ExitFailure},
	} {
		t.Run(name, func(t *testing.T) {
			if code, o, e := run(t, c.args...); code != c.code {
				t.Fatalf("got %d, want %d: %s %s", code, c.code, o, e)
			}
		})
	}
	// unsigned, allowed explicitly
	code, o, e := run(
		t,
		"source",
		"fetch",
		img,
		"--checksums",
		base+"SHA256SUMS",
		"--allow-unsigned",
		"--out",
		out,
	)
	if code != cli.ExitOK || !strings.Contains(o, "checksum-only)") {
		t.Fatalf("%d %s %s", code, o, e)
	}
}

func TestBundleInitUsage(t *testing.T) {
	dir := t.TempDir()
	disk := func() string {
		p := filepath.Join(t.TempDir(), "d.raw")
		_ = os.WriteFile(p, make([]byte, 1<<20), 0o600)
		return p
	}
	notRecord := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(notRecord, []byte(`{"kind":""}`), 0o600)
	base := []string{
		"--os",
		"linux",
		"--arch",
		"amd64",
		"--os-version",
		"1",
		"--os-build",
		"1",
		"--template",
		"t",
		"--template-ref",
		"r",
		"--image-version",
		"1",
		"--revision",
		"r",
		"--source-url",
		"https://example.invalid",
	}
	for name, c := range map[string]struct {
		args []string
		code int
	}{
		"no disk":          {append([]string{"bundle", "init", filepath.Join(dir, "a")}, base...), cli.ExitUsage},
		"no template":      {[]string{"bundle", "init", filepath.Join(dir, "b"), "--disk", disk()}, cli.ExitUsage},
		"no annotations":   {[]string{"bundle", "init", filepath.Join(dir, "b2"), "--disk", disk(), "--template", "t", "--template-ref", "r"}, cli.ExitUsage},
		"bad created":      {append([]string{"bundle", "init", filepath.Join(dir, "c"), "--disk", disk(), "--created", "today"}, base...), cli.ExitUsage},
		"bad memory":       {append([]string{"bundle", "init", filepath.Join(dir, "d"), "--disk", disk(), "--memory", "lots"}, base...), cli.ExitUsage},
		"bad memory-min":   {append([]string{"bundle", "init", filepath.Join(dir, "e"), "--disk", disk(), "--memory-min", "0"}, base...), cli.ExitUsage},
		"bad annotation":   {append([]string{"bundle", "init", filepath.Join(dir, "f"), "--disk", disk(), "--annotation", "nokey"}, base...), cli.ExitUsage},
		"missing source":   {append([]string{"bundle", "init", filepath.Join(dir, "g"), "--disk", disk(), "--source", filepath.Join(dir, "nope")}, base...), cli.ExitUsage},
		"not a record":     {append([]string{"bundle", "init", filepath.Join(dir, "h"), "--disk", disk(), "--source", notRecord}, base...), cli.ExitUsage},
		"missing disk":     {append([]string{"bundle", "init", filepath.Join(dir, "i"), "--disk", filepath.Join(dir, "nope")}, base...), cli.ExitFailure},
		"dir is a file":    {append([]string{"bundle", "init", notRecord, "--disk", disk()}, base...), cli.ExitFailure},
		"bad os in pack":   {[]string{"bundle", "init", filepath.Join(dir, "j"), "--disk", disk(), "--os", "plan9", "--arch", "amd64", "--template", "t", "--template-ref", "r", "--image-version", "1", "--revision", "r", "--source-url", "u"}, cli.ExitOK},
		"second data disk": {append([]string{"bundle", "init", filepath.Join(dir, "k"), "--disk", disk(), "--disk", disk(), "--memory", "1073741824"}, base...), cli.ExitOK},
	} {
		t.Run(name, func(t *testing.T) {
			if code, o, e := run(t, c.args...); code != c.code {
				t.Fatalf("got %d, want %d: %s %s", code, c.code, o, e)
			}
		})
	}
	if b, err := pack.LoadBundle(
		filepath.Join(dir, "k"),
	); err != nil || len(b.File.Disks) != 2 ||
		b.File.Disks[1].Role != "data" {
		t.Fatalf("%+v %v", b, err)
	}
}
