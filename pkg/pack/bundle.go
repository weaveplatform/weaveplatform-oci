package pack

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

// BundleFileName is the descriptor file at the root of a bundle directory.
const BundleFileName = "bundle.json"

// ErrBundle reports an unusable bundle directory.
var ErrBundle = errors.New("invalid bundle")

// BundleFile is the on-disk bundle.json. It carries the parts of the config
// that a producer chooses; disk sizes, chunk counts and state entries are
// computed by Manifest.
type BundleFile struct {
	SchemaVersion int               `json:"schemaVersion"`
	Guest         spec.Guest        `json:"guest"`
	Firmware      spec.Firmware     `json:"firmware"`
	Resources     spec.Resources    `json:"resources"`
	Provisioning  spec.Provisioning `json:"provisioning"`
	Build         spec.Build        `json:"build"`
	Disks         []BundleDisk      `json:"disks"`
	State         []BundleState     `json:"state,omitempty"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

// BundleDisk names one raw disk file, relative to the bundle directory.
type BundleDisk struct {
	Name string `json:"name"`
	Role string `json:"role"`
	Path string `json:"path"`
	// ChunkDigests optionally pins raw chunks to a producer handoff snapshot.
	ChunkDigests []string `json:"chunkDigests,omitempty"`
}

// BundleState names one state file, relative to the bundle directory.
type BundleState struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Semantics string `json:"semantics"`
	Required  bool   `json:"required"`
	Digest    string `json:"digest,omitempty"`
}

// Bundle is a loaded bundle with absolute file paths.
type Bundle struct {
	Dir  string
	File BundleFile
}

// LoadBundle reads dir/bundle.json and checks that every referenced file is
// inside dir and exists.
func LoadBundle(dir string) (Bundle, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Bundle{}, fmt.Errorf("%w: %w", ErrBundle, err)
	}
	raw, err := os.ReadFile(
		filepath.Join(abs, BundleFileName),
	) //nolint:gosec // reading the caller's bundle is the purpose
	if err != nil {
		return Bundle{}, fmt.Errorf("%w: %w", ErrBundle, err)
	}
	var f BundleFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return Bundle{}, fmt.Errorf("%w: %s: %w", ErrBundle, BundleFileName, err)
	}
	b := Bundle{Dir: abs, File: f}
	if f.SchemaVersion != 1 {
		return Bundle{}, fmt.Errorf("%w: schemaVersion %d, want 1", ErrBundle, f.SchemaVersion)
	}
	if len(f.Disks) == 0 {
		return Bundle{}, fmt.Errorf("%w: no disks", ErrBundle)
	}
	for _, d := range f.Disks {
		if _, err := b.path(d.Path); err != nil {
			return Bundle{}, err
		}
	}
	for _, s := range f.State {
		if _, err := b.path(s.Path); err != nil {
			return Bundle{}, err
		}
	}
	return b, nil
}

// path resolves a bundle-relative path and refuses anything outside the bundle.
func (b Bundle) path(rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: path %q must be relative to the bundle", ErrBundle, rel)
	}
	p := filepath.Join(b.Dir, filepath.FromSlash(rel))
	if r, err := filepath.Rel(
		b.Dir,
		p,
	); err != nil || r == ".." ||
		strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: path %q escapes the bundle", ErrBundle, rel)
	}
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("%w: %w", ErrBundle, err)
	}
	return p, nil
}

// WriteBundleFile writes bundle.json into dir.
func WriteBundleFile(dir string, f BundleFile) error {
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("encode bundle: %w", err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, BundleFileName),
		append(raw, '\n'),
		0o600,
	); err != nil {
		return fmt.Errorf("write bundle: %w", err)
	}
	return nil
}
