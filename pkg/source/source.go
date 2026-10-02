// Package source downloads the upstream media an image is built from (a
// distribution cloud image, an ISO) and refuses it unless the distribution's
// own checksum file vouches for it.
//
// The chain is: an OpenPGP key the image definition pins by fingerprint
// signs a checksum file (detached, as Ubuntu and AlmaLinux publish it, or
// clearsigned, as Fedora does), the checksum file names the medium's hash,
// and the downloaded bytes must hash to it. Where a distribution signs
// nothing (Debian's cloud images ship SHA512SUMS alone), Fetch accepts the
// checksum only when the caller explicitly allows it, and says so in the
// result so the build provenance records how much was verified.
package source

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
)

// Verification says how much of the chain held.
type Verification string

const (
	// SignedDetached: a pinned key's detached signature over the checksum file.
	SignedDetached Verification = "openpgp-detached"
	// SignedClear: a pinned key's clearsigned checksum file.
	SignedClear Verification = "openpgp-clearsigned"
	// ChecksumOnly: the checksum file was unsigned; only transport integrity.
	ChecksumOnly Verification = "checksum-only"
)

var (
	// ErrSignature reports a checksum file the pinned key did not sign.
	ErrSignature = errors.New("source: checksum file signature does not verify")
	// ErrChecksum reports a medium whose bytes do not match the checksum file.
	ErrChecksum = errors.New("source: medium does not match its checksum")
	// ErrNotListed reports a medium the checksum file does not name.
	ErrNotListed = errors.New("source: medium is not listed in the checksum file")
	// ErrOptions reports unusable options.
	ErrOptions = errors.New("source: invalid options")
	// ErrFetch reports a download failure.
	ErrFetch = errors.New("source: download failed")
)

// Options describe one medium and the chain that vouches for it.
type Options struct {
	// URL of the medium. Its base name is looked up in the checksum file
	// unless Name is set.
	URL  string
	Name string
	// Checksums is the URL of the checksum file.
	Checksums string
	// Signature is the URL of a detached signature over the checksum file.
	// Empty with Clearsigned false means the checksum file is unsigned.
	Signature string
	// Clearsigned marks a checksum file that carries its own signature.
	Clearsigned bool
	// Keyring holds the distribution's public keys (armored or binary), and
	// Fingerprints the ones allowed to sign, as hex, case and spaces ignored.
	Keyring      []byte
	Fingerprints []string
	// AllowUnsigned accepts an unsigned checksum file.
	AllowUnsigned bool
	// Out is where the medium is written.
	Out string
	// Client is the HTTP client (http.DefaultClient when nil).
	Client *http.Client
}

// Result describes a verified medium.
type Result struct {
	URI          string       `json:"uri"`
	Digest       string       `json:"digest"` // sha256 of the medium, whatever the checksum file used
	Size         int64        `json:"size"`
	Verification Verification `json:"verification"`
	Signer       string       `json:"signer,omitempty"` // fingerprint of the key that signed the checksums
}

// Fetch downloads, verifies and writes one medium. Nothing is left at Out
// unless every check passed.
func Fetch(ctx context.Context, o Options) (Result, error) {
	if err := o.validate(); err != nil {
		return Result{}, err
	}
	c := o.Client
	if c == nil {
		c = http.DefaultClient
	}
	sums, err := get(ctx, c, o.Checksums)
	if err != nil {
		return Result{}, err
	}
	res := Result{URI: o.URL, Verification: ChecksumOnly}
	switch {
	case o.Clearsigned:
		block, _ := clearsign.Decode(sums)
		if block == nil {
			return Result{}, fmt.Errorf("%w: %s is not clearsigned", ErrSignature, o.Checksums)
		}
		res.Signer, err = checkSigner(o, func(ring openpgp.KeyRing) (*openpgp.Entity, error) {
			return block.VerifySignature(ring, nil)
		})
		if err != nil {
			return Result{}, err
		}
		sums, res.Verification = block.Plaintext, SignedClear
	case o.Signature != "":
		sig, err := get(ctx, c, o.Signature)
		if err != nil {
			return Result{}, err
		}
		signed := sums
		res.Signer, err = checkSigner(o, func(ring openpgp.KeyRing) (*openpgp.Entity, error) {
			// Ubuntu publishes binary signatures (.gpg), others armored (.asc).
			e, err := openpgp.CheckDetachedSignature(
				ring,
				bytes.NewReader(signed),
				bytes.NewReader(sig),
				nil,
			)
			if err != nil {
				e, err = openpgp.CheckArmoredDetachedSignature(
					ring,
					bytes.NewReader(signed),
					bytes.NewReader(sig),
					nil,
				)
			}
			return e, err
		})
		if err != nil {
			return Result{}, err
		}
		res.Verification = SignedDetached
	}
	name := o.Name
	if name == "" {
		name = path.Base(o.URL)
	}
	want, err := lookup(sums, name)
	if err != nil {
		return Result{}, err
	}
	res.Digest, res.Size, err = download(ctx, c, o.URL, o.Out, want)
	return res, err
}

func (o Options) validate() error {
	switch {
	case o.URL == "" || o.Checksums == "" || o.Out == "":
		return fmt.Errorf("%w: url, checksums and out are required", ErrOptions)
	case o.Clearsigned && o.Signature != "":
		return fmt.Errorf(
			"%w: a checksum file is clearsigned or has a detached signature, not both",
			ErrOptions,
		)
	case !o.Clearsigned && o.Signature == "" && !o.AllowUnsigned:
		return fmt.Errorf("%w: the checksum file is unsigned; allow it explicitly", ErrOptions)
	case (o.Clearsigned || o.Signature != "") && (len(o.Keyring) == 0 || len(o.Fingerprints) == 0):
		return fmt.Errorf(
			"%w: a signed checksum file needs a keyring and pinned fingerprints",
			ErrOptions,
		)
	}
	return nil
}

// checkSigner runs a signature check and then requires the signing key's
// primary fingerprint to be pinned: a keyring file can hold more keys than
// the image definition means to trust.
func checkSigner(o Options, check func(openpgp.KeyRing) (*openpgp.Entity, error)) (string, error) {
	ring, err := readKeyRing(o.Keyring)
	if err != nil {
		return "", err
	}
	signer, err := check(ring)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrSignature, err)
	}
	fpr := strings.ToUpper(hex.EncodeToString(signer.PrimaryKey.Fingerprint))
	for _, f := range o.Fingerprints {
		if normalise(f) == fpr {
			return fpr, nil
		}
	}
	return "", fmt.Errorf("%w: signed by %s, which is not pinned", ErrSignature, fpr)
}

func normalise(f string) string {
	return strings.ToUpper(
		strings.ReplaceAll(strings.TrimPrefix(strings.TrimSpace(f), "0x"), " ", ""),
	)
}

func readKeyRing(b []byte) (openpgp.EntityList, error) {
	if ring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(b)); err == nil {
		return ring, nil
	}
	ring, err := openpgp.ReadKeyRing(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("%w: keyring: %w", ErrOptions, err)
	}
	return ring, nil
}

var (
	gnuLine = regexp.MustCompile(`^([0-9a-fA-F]{64}|[0-9a-fA-F]{128})\s+\*?(.+)$`)
	bsdLine = regexp.MustCompile(`^(SHA256|SHA512)\s*\((.+)\)\s*=\s*([0-9a-fA-F]+)$`)
)

// expected is a hash the checksum file names for the medium.
type expected struct {
	algo string // sha256 or sha512
	hex  string
}

// lookup finds the medium in a GNU (`<hash> [*]<name>`) or BSD
// (`SHA256 (<name>) = <hash>`) checksum file.
func lookup(sums []byte, name string) (expected, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if m := gnuLine.FindStringSubmatch(
			line,
		); m != nil &&
			strings.TrimPrefix(m[2], "./") == name {
			algo := "sha256"
			if len(m[1]) == 128 {
				algo = "sha512"
			}
			return expected{algo, strings.ToLower(m[1])}, nil
		}
		if m := bsdLine.FindStringSubmatch(line); m != nil && m[2] == name {
			return expected{strings.ToLower(m[1]), strings.ToLower(m[3])}, nil
		}
	}
	return expected{}, fmt.Errorf("%w: %s", ErrNotListed, name)
}

func get(ctx context.Context, c *http.Client, url string) ([]byte, error) {
	body, err := open(ctx, c, url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	b, err := io.ReadAll(io.LimitReader(body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrFetch, url, err)
	}
	return b, nil
}

func open(ctx context.Context, c *http.Client, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOptions, err)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrFetch, url, err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: %s: %s", ErrFetch, url, resp.Status)
	}
	return resp.Body, nil
}

// download streams the medium to a temporary file beside out, hashing it
// with the checksum file's algorithm and with sha256 for the record, and
// renames it into place only when the hash matches.
func download(
	ctx context.Context,
	c *http.Client,
	url, out string,
	want expected,
) (string, int64, error) {
	body, err := open(ctx, c, url)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = body.Close() }()
	if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
		return "", 0, fmt.Errorf("%w: %w", ErrFetch, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(out), ".fetch-*")
	if err != nil {
		return "", 0, fmt.Errorf("%w: %w", ErrFetch, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	var check hash.Hash = sha256.New()
	if want.algo == "sha512" {
		check = sha512.New()
	}
	record := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, check, record), body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, fmt.Errorf("%w: %s: %w", ErrFetch, url, err)
	}
	if got := hex.EncodeToString(check.Sum(nil)); got != want.hex {
		return "", 0, fmt.Errorf(
			"%w: %s: %s is %s, the checksum file says %s",
			ErrChecksum,
			url,
			want.algo,
			got,
			want.hex,
		)
	}
	if err := os.Rename(tmp.Name(), out); err != nil {
		return "", 0, fmt.Errorf("%w: %w", ErrFetch, err)
	}
	return "sha256:" + hex.EncodeToString(record.Sum(nil)), n, nil
}
