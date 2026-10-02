package channel

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// MaxFileSize caps each fetched channel file, as core does.
const MaxFileSize = 8 << 20

// Sibling file names next to a manifest.
const (
	SigningKeyFile    = "signing.pub"
	SigningKeySigFile = "signing.pub.sig"
)

// Load fetches the bundle for the manifest at location: a file path or an
// https:// URL. The manifest's signature is <manifest>.sig, and signing.pub
// and signing.pub.sig sit in the same directory.
func Load(ctx context.Context, location string, hc *http.Client) (Bundle, error) {
	get := readFile
	join := func(name string) string { return filepath.Join(filepath.Dir(location), name) }
	if strings.HasPrefix(location, "https://") || strings.HasPrefix(location, "http://") {
		if hc == nil {
			hc = http.DefaultClient
		}
		get = func(loc string) ([]byte, error) { return fetch(ctx, hc, loc) }
		join = func(name string) string { return location[:strings.LastIndex(location, "/")+1] + path.Base(name) }
	}
	var b Bundle
	for _, f := range []struct {
		dst *[]byte
		loc string
	}{
		{&b.Manifest, location},
		{&b.ManifestSig, location + ".sig"},
		{&b.SigningKey, join(SigningKeyFile)},
		{&b.SigningKeySig, join(SigningKeySigFile)},
	} {
		data, err := get(f.loc)
		if err != nil {
			return Bundle{}, err
		}
		*f.dst = data
	}
	return b, nil
}

func readFile(loc string) ([]byte, error) {
	f, err := os.Open(loc) //nolint:gosec // the operator configures the channel location
	if err != nil {
		return nil, fmt.Errorf("channel: %w", err)
	}
	defer func() { _ = f.Close() }()
	return limited(f, loc)
}

func fetch(ctx context.Context, hc *http.Client, loc string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, loc, nil)
	if err != nil {
		return nil, fmt.Errorf("channel: %w", err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("channel: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: GET %s: %s", ErrFetch, loc, resp.Status)
	}
	return limited(resp.Body, loc)
}

func limited(r io.Reader, loc string) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("channel: read %s: %w", loc, err)
	}
	if len(b) > MaxFileSize {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes", ErrFormat, loc, MaxFileSize)
	}
	return b, nil
}
