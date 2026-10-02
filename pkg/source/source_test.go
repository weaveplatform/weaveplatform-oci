package source_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/weaveplatform-oci/pkg/source"
)

var medium = bytes.Repeat([]byte("weave cloud image "), 4096)

type key struct {
	entity  *openpgp.Entity
	armored []byte
	binary  []byte
	fpr     string
}

func newKey(t *testing.T, name string) key {
	t.Helper()
	cfg := &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA}
	e, err := openpgp.NewEntity(name, "", name+"@example.invalid", cfg)
	if err != nil {
		t.Fatal(err)
	}
	var bin, arm bytes.Buffer
	if err := e.Serialize(&bin); err != nil {
		t.Fatal(err)
	}
	w, _ := armor.Encode(&arm, openpgp.PublicKeyType, nil)
	_ = e.Serialize(w)
	_ = w.Close()
	return key{
		e,
		arm.Bytes(),
		bin.Bytes(),
		strings.ToUpper(hex.EncodeToString(e.PrimaryKey.Fingerprint)),
	}
}

func detached(t *testing.T, k key, msg []byte, armored bool) []byte {
	t.Helper()
	var b bytes.Buffer
	var err error
	if armored {
		err = openpgp.ArmoredDetachSign(&b, k.entity, bytes.NewReader(msg), nil)
	} else {
		err = openpgp.DetachSign(&b, k.entity, bytes.NewReader(msg), nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func clearSigned(t *testing.T, k key, msg []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w, err := clearsign.Encode(&b, k.entity.PrivateKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(msg)
	_ = w.Close()
	return b.Bytes()
}

func serve(t *testing.T, files map[string][]byte) string {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/*", func(w http.ResponseWriter, req *http.Request) {
		b, ok := files[strings.TrimPrefix(req.URL.Path, "/")]
		if !ok {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write(b)
	})
	s := httptest.NewServer(r)
	t.Cleanup(s.Close)
	return s.URL + "/"
}

func sha256sums(name string, b []byte) []byte {
	h := sha256.Sum256(b)
	return []byte(
		fmt.Sprintf(
			"0000000000000000000000000000000000000000000000000000000000000000 *other.img\n%s *%s\n",
			hex.EncodeToString(h[:]),
			name,
		),
	)
}

func TestDetachedBinaryAndArmored(t *testing.T) {
	k := newKey(t, "ubuntu")
	sums := sha256sums("disk.img", medium)
	for _, armored := range []bool{false, true} {
		base := serve(t, map[string][]byte{
			"disk.img": medium, "SHA256SUMS": sums, "SHA256SUMS.gpg": detached(t, k, sums, armored),
		})
		out := filepath.Join(t.TempDir(), "sub", "disk.img")
		for _, ring := range [][]byte{k.armored, k.binary} {
			r, err := source.Fetch(context.Background(), source.Options{
				URL:          base + "disk.img",
				Checksums:    base + "SHA256SUMS",
				Signature:    base + "SHA256SUMS.gpg",
				Keyring:      ring,
				Fingerprints: []string{"0x" + strings.ToLower(k.fpr)},
				Out:          out,
			})
			if err != nil {
				t.Fatal(err)
			}
			h := sha256.Sum256(medium)
			if r.Digest != "sha256:"+hex.EncodeToString(h[:]) || r.Size != int64(len(medium)) ||
				r.Verification != source.SignedDetached || r.Signer != k.fpr {
				t.Fatalf("result %+v", r)
			}
			if got, _ := os.ReadFile(out); !bytes.Equal(got, medium) {
				t.Fatal("medium not written")
			}
		}
	}
}

func TestClearsignedBSDSha512(t *testing.T) {
	k := newKey(t, "fedora")
	h := sha512.Sum512(medium)
	sums := []byte(
		"# Fedora-Cloud-Base\nSHA512 (Fedora.raw.xz) = " + hex.EncodeToString(h[:]) + "\n",
	)
	base := serve(
		t,
		map[string][]byte{"Fedora.raw.xz": medium, "CHECKSUM": clearSigned(t, k, sums)},
	)
	r, err := source.Fetch(context.Background(), source.Options{
		URL: base + "Fedora.raw.xz", Checksums: base + "CHECKSUM", Clearsigned: true,
		Keyring: k.armored, Fingerprints: []string{k.fpr}, Out: filepath.Join(t.TempDir(), "f"),
	})
	if err != nil || r.Verification != source.SignedClear ||
		!strings.HasPrefix(r.Digest, "sha256:") {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestUnsignedNeedsPermission(t *testing.T) {
	h := sha512.Sum512(medium)
	sums := []byte(hex.EncodeToString(h[:]) + "  ./debian.qcow2\n")
	base := serve(t, map[string][]byte{"debian.qcow2": medium, "SHA512SUMS": sums})
	o := source.Options{
		URL:       base + "debian.qcow2",
		Checksums: base + "SHA512SUMS",
		Out:       filepath.Join(t.TempDir(), "d"),
	}
	if _, err := source.Fetch(context.Background(), o); !errors.Is(err, source.ErrOptions) {
		t.Fatalf("unsigned accepted silently: %v", err)
	}
	o.AllowUnsigned = true
	r, err := source.Fetch(context.Background(), o)
	if err != nil || r.Verification != source.ChecksumOnly || r.Signer != "" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestRefusals(t *testing.T) {
	k, other := newKey(t, "ubuntu"), newKey(t, "mallory")
	sums := sha256sums("disk.img", medium)
	tampered := append([]byte{}, medium...)
	tampered[0] ^= 1
	base := serve(t, map[string][]byte{
		"disk.img":       medium,
		"bad.img":        tampered,
		"SHA256SUMS":     sums,
		"SHA256SUMS.gpg": detached(t, k, sums, false),
		"OTHER.gpg":      detached(t, other, sums, false),
		"BADSUMS": sha256sums(
			"bad.img",
			medium,
		),
		"BADSUMS.gpg": detached(t, k, sha256sums("bad.img", medium), false),
		"garbage":     []byte("not a signature"),
		"CLEAR":       clearSigned(t, other, sums),
	})
	good := source.Options{
		URL: base + "disk.img", Checksums: base + "SHA256SUMS", Signature: base + "SHA256SUMS.gpg",
		Keyring: k.armored, Fingerprints: []string{k.fpr},
	}
	cases := map[string]struct {
		mod  func(*source.Options)
		want error
	}{
		"signed by an unpinned key": {
			func(o *source.Options) { o.Signature = base + "OTHER.gpg" },
			source.ErrSignature,
		},
		"key in ring but not pinned": {func(o *source.Options) {
			o.Keyring = append(append([]byte{}, k.binary...), other.binary...)
			o.Signature, o.Fingerprints = base+"OTHER.gpg", []string{k.fpr}
		}, source.ErrSignature},
		"pinned key did not sign": {
			func(o *source.Options) { o.Fingerprints = []string{other.fpr} },
			source.ErrSignature,
		},
		"garbage signature": {
			func(o *source.Options) { o.Signature = base + "garbage" },
			source.ErrSignature,
		},
		"not clearsigned": {
			func(o *source.Options) { o.Signature, o.Clearsigned = "", true },
			source.ErrSignature,
		},
		"clearsigned by another key": {
			func(o *source.Options) { o.Signature, o.Clearsigned, o.Checksums = "", true, base+"CLEAR" },
			source.ErrSignature,
		},
		"tampered medium": {func(o *source.Options) {
			o.URL, o.Checksums, o.Signature = base+"bad.img", base+"BADSUMS", base+"BADSUMS.gpg"
		}, source.ErrChecksum},
		"medium not listed": {
			func(o *source.Options) { o.Name = "missing.img" },
			source.ErrNotListed,
		},
		"medium missing": {
			func(o *source.Options) { o.URL = base + "nope/disk.img" },
			source.ErrFetch,
		},
		"checksums missing": {
			func(o *source.Options) { o.Checksums = base + "nope" },
			source.ErrFetch,
		},
		"signature missing": {
			func(o *source.Options) { o.Signature = base + "nope" },
			source.ErrFetch,
		},
		"unreachable": {
			func(o *source.Options) { o.Checksums = "http://127.0.0.1:1/x" },
			source.ErrFetch,
		},
		"bad url": {
			func(o *source.Options) { o.Checksums = "http://[::1" },
			source.ErrOptions,
		},
		"bad keyring": {
			func(o *source.Options) { o.Keyring = []byte("nope") },
			source.ErrOptions,
		},
		"no out": {func(o *source.Options) { o.Out = "" }, source.ErrOptions},
		"both signature kinds": {
			func(o *source.Options) { o.Clearsigned = true },
			source.ErrOptions,
		},
		"no pinned keys": {
			func(o *source.Options) { o.Fingerprints = nil },
			source.ErrOptions,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			o := good
			o.Out = filepath.Join(t.TempDir(), "disk.img")
			c.mod(&o)
			if _, err := source.Fetch(context.Background(), o); !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
			if o.Out != "" {
				if _, err := os.Stat(o.Out); err == nil {
					t.Fatal("a refused medium was left at out")
				}
			}
		})
	}
}

func TestOutputDirectoryProblems(t *testing.T) {
	k := newKey(t, "ubuntu")
	sums := sha256sums("disk.img", medium)
	base := serve(
		t,
		map[string][]byte{
			"disk.img":       medium,
			"SHA256SUMS":     sums,
			"SHA256SUMS.gpg": detached(t, k, sums, false),
		},
	)
	file := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(file, nil, 0o600)
	dirAsOut := t.TempDir()
	for name, out := range map[string]string{
		"parent is a file":   filepath.Join(file, "disk.img"),
		"out is a directory": dirAsOut,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := source.Fetch(context.Background(), source.Options{
				URL:          base + "disk.img",
				Checksums:    base + "SHA256SUMS",
				Signature:    base + "SHA256SUMS.gpg",
				Keyring:      k.armored,
				Fingerprints: []string{k.fpr},
				Out:          out,
			})
			if !errors.Is(err, source.ErrFetch) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

// A body that fails part-way must not leave a medium behind.
func TestTruncatedDownload(t *testing.T) {
	k := newKey(t, "ubuntu")
	sums := sha256sums("disk.img", medium)
	sig := detached(t, k, sums, false)
	r := chi.NewRouter()
	r.Get("/SHA256SUMS", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(sums) })
	r.Get("/SHA256SUMS.gpg", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(sig) })
	r.Get("/disk.img", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(medium)))
		_, _ = w.Write(medium[:100])
	})
	s := httptest.NewServer(r)
	defer s.Close()
	out := filepath.Join(t.TempDir(), "disk.img")
	_, err := source.Fetch(context.Background(), source.Options{
		URL:          s.URL + "/disk.img",
		Checksums:    s.URL + "/SHA256SUMS",
		Signature:    s.URL + "/SHA256SUMS.gpg",
		Keyring:      k.armored,
		Fingerprints: []string{k.fpr},
		Out:          out,
		Client:       s.Client(),
	})
	if !errors.Is(err, source.ErrFetch) {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("truncated medium left behind")
	}
}
