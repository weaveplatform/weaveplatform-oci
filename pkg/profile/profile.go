// Package profile loads the deployment profile file shared by weaveoci,
// hostweave and the guestweave CLIs (decision 0011): which registry is
// canonical, which mirrors to read from, how images are signed when
// published, what consumers verify, and which channel trust anchors they
// accept.
//
// The file is strict YAML: unknown keys are errors. Its location is chosen
// by flag, then the WEAVEOCI_PROFILES environment variable, then
// $XDG_CONFIG_HOME/weave/oci-profiles.yaml (os.UserConfigDir on macOS and
// Windows). Relative paths inside the file resolve against its directory.
package profile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// EnvProfiles names the environment variable that overrides the file location.
const EnvProfiles = "WEAVEOCI_PROFILES"

// ErrInvalid wraps every validation failure.
var ErrInvalid = errors.New("invalid profile")

// Kind is the deployment profile kind.
type Kind string

// Profile kinds.
const (
	KindGitHub  Kind = "github"
	KindPrivate Kind = "private"
	KindHybrid  Kind = "hybrid"
)

// SigningProvider selects how publish signs an artifact.
type SigningProvider string

// Signing providers.
const (
	SigningGitHubAttestation SigningProvider = "github-attestation"
	SigningCosignKey         SigningProvider = "cosign-key"
	SigningNone              SigningProvider = "none"
)

// VerifyMode selects what consumers require on pull.
type VerifyMode string

// Verification modes (the --verify flag uses the same values).
const (
	VerifyChannel   VerifyMode = "channel"
	VerifySignature VerifyMode = "signature"
	VerifyBoth      VerifyMode = "both"
	VerifyNone      VerifyMode = "none"
)

// File is the profile file.
type File struct {
	SchemaVersion int       `yaml:"schemaVersion"`
	Default       string    `yaml:"default"`
	Profiles      []Profile `yaml:"profiles"`
}

// Profile is one deployment profile.
type Profile struct {
	Name     string     `yaml:"name"`
	Kind     Kind       `yaml:"kind"`
	Registry Registry   `yaml:"registry"`
	Mirrors  []Registry `yaml:"mirrors,omitempty"`
	Signing  Signing    `yaml:"signing"`
	Verify   Verify     `yaml:"verify"`
	Channel  Channel    `yaml:"channel,omitempty"`
}

// Registry is a registry endpoint.
type Registry struct {
	Host string `yaml:"host"`
	// Namespace prefixes bare repository names, e.g. deploymenttheory/weave-images.
	Namespace string `yaml:"namespace,omitempty"`
	// PlainHTTP talks HTTP instead of HTTPS (local and test registries only).
	PlainHTTP bool `yaml:"plainHTTP,omitempty"`
	// InsecureSkipTLSVerify accepts any certificate (self-signed site mirrors).
	InsecureSkipTLSVerify bool `yaml:"insecureSkipTLSVerify,omitempty"`
}

// Signing configures publish.
type Signing struct {
	Provider SigningProvider `yaml:"provider"`
	// Key is a cosign private key reference for cosign-key: a file path,
	// env://NAME, or a KMS URI (awskms://, gcpkms://, azurekms://, hashivault://).
	Key string `yaml:"key,omitempty"`
}

// Verify configures what consumers require.
type Verify struct {
	Mode VerifyMode `yaml:"mode"`
	// PublicKeys verify cosign-key signatures (PEM files).
	PublicKeys []string `yaml:"publicKeys,omitempty"`
	// Identity verifies GitHub artifact attestations.
	Identity *Identity `yaml:"identity,omitempty"`
}

// Identity pins a keyless signing identity.
type Identity struct {
	Issuer        string `yaml:"issuer"`
	SubjectRegexp string `yaml:"subjectRegexp"`
	// TrustedRoot is a trusted_root.json (`gh attestation trusted-root`)
	// used to verify attestations offline.
	TrustedRoot string `yaml:"trustedRoot,omitempty"`
}

// Channel locates the channel manifest and its trust anchors.
type Channel struct {
	// Manifest is an https:// URL or a file path to the channel manifest.
	Manifest string   `yaml:"manifest,omitempty"`
	Anchors  []Anchor `yaml:"anchors,omitempty"`
}

// Anchor is a channel root public key.
type Anchor struct {
	Name      string `yaml:"name"`
	PublicKey string `yaml:"publicKey"`
}

// Path returns the profile file location: flag, then environment, then the
// per-user default.
func Path(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if v := os.Getenv(EnvProfiles); v != "" {
		return v, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate profile file: %w", err)
	}
	return filepath.Join(dir, "weave", "oci-profiles.yaml"), nil
}

// Load reads and validates a profile file. Relative key, public-key, anchor
// and channel paths are made absolute against the file's directory.
func Load(path string) (File, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the caller chooses the profile file
	if err != nil {
		return File{}, fmt.Errorf("read profiles: %w", err)
	}
	f, err := Parse(raw)
	if err != nil {
		return File{}, fmt.Errorf("%s: %w", path, err)
	}
	base := filepath.Dir(path)
	for i := range f.Profiles {
		p := &f.Profiles[i]
		p.Signing.Key = resolve(base, p.Signing.Key)
		for j := range p.Verify.PublicKeys {
			p.Verify.PublicKeys[j] = resolve(base, p.Verify.PublicKeys[j])
		}
		if p.Verify.Identity != nil {
			p.Verify.Identity.TrustedRoot = resolve(base, p.Verify.Identity.TrustedRoot)
		}
		for j := range p.Channel.Anchors {
			p.Channel.Anchors[j].PublicKey = resolve(base, p.Channel.Anchors[j].PublicKey)
		}
		p.Channel.Manifest = resolve(base, p.Channel.Manifest)
	}
	return f, nil
}

// resolve makes a plain relative path absolute; URIs and absolute paths pass through.
func resolve(base, ref string) string {
	if ref == "" || filepath.IsAbs(ref) || strings.Contains(ref, "://") {
		return ref
	}
	return filepath.Join(base, ref)
}

// Parse decodes and validates a profile file.
func Parse(raw []byte) (File, error) {
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return File{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if err := f.Validate(); err != nil {
		return File{}, err
	}
	return f, nil
}

// Validate checks the whole file.
func (f File) Validate() error {
	var errs []error
	if f.SchemaVersion != 1 {
		errs = append(errs, fmt.Errorf("%w: schemaVersion %d, want 1", ErrInvalid, f.SchemaVersion))
	}
	if len(f.Profiles) == 0 {
		errs = append(errs, fmt.Errorf("%w: no profiles", ErrInvalid))
	}
	seen := map[string]bool{}
	for _, p := range f.Profiles {
		if seen[p.Name] {
			errs = append(errs, fmt.Errorf("%w: duplicate profile %q", ErrInvalid, p.Name))
		}
		seen[p.Name] = true
		if err := p.Validate(); err != nil {
			errs = append(errs, err)
		}
	}
	if f.Default != "" && !seen[f.Default] {
		errs = append(
			errs,
			fmt.Errorf("%w: default profile %q is not defined", ErrInvalid, f.Default),
		)
	}
	return errors.Join(errs...)
}

// Select returns the named profile, or the default (or only) profile when
// name is empty.
func (f File) Select(name string) (Profile, error) {
	if name == "" {
		name = f.Default
	}
	if name == "" && len(f.Profiles) == 1 {
		return f.Profiles[0], nil
	}
	for _, p := range f.Profiles {
		if p.Name == name {
			return p, nil
		}
	}
	return Profile{}, fmt.Errorf("%w: no profile %q (and no default)", ErrInvalid, name)
}

var hostPattern = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]+)?$`)

// Validate checks the rules for a profile's kind (decision 0011).
func (p Profile) Validate() error {
	var errs []error
	bad := func(format string, args ...any) {
		errs = append(
			errs,
			fmt.Errorf("%w: profile %q: %s", ErrInvalid, p.Name, fmt.Sprintf(format, args...)),
		)
	}
	if p.Name == "" {
		bad("name is required")
	}
	if !hostPattern.MatchString(p.Registry.Host) {
		bad("registry.host %q is not host[:port]", p.Registry.Host)
	}
	for _, m := range p.Mirrors {
		if !hostPattern.MatchString(m.Host) {
			bad("mirror host %q is not host[:port]", m.Host)
		}
	}
	switch p.Kind {
	case KindGitHub:
		if len(p.Mirrors) > 0 {
			bad("github profiles have no mirrors; use kind hybrid")
		}
		if p.Signing.Provider == SigningCosignKey {
			bad("github profiles sign with github-attestation (or none)")
		}
	case KindPrivate:
		if p.Signing.Provider != SigningCosignKey {
			bad("private profiles sign with cosign-key")
		}
	case KindHybrid:
		if len(p.Mirrors) == 0 {
			bad("hybrid profiles need at least one mirror")
		}
		if p.Signing.Provider == SigningCosignKey {
			bad("hybrid profiles publish upstream with github-attestation (or none)")
		}
	default:
		bad("kind %q is not github, private or hybrid", p.Kind)
	}
	switch p.Signing.Provider {
	case SigningGitHubAttestation, SigningNone:
		if p.Signing.Key != "" {
			bad("signing.key applies only to cosign-key")
		}
	case SigningCosignKey:
	default:
		bad("signing.provider %q is not github-attestation, cosign-key or none", p.Signing.Provider)
	}
	p.validateVerify(bad)
	return errors.Join(errs...)
}

func (p Profile) validateVerify(bad func(string, ...any)) {
	mode := p.Verify.Mode
	switch mode {
	case VerifyChannel, VerifySignature, VerifyBoth, VerifyNone:
	default:
		bad("verify.mode %q is not channel, signature, both or none", mode)
		return
	}
	if mode == VerifyChannel || mode == VerifyBoth {
		if p.Channel.Manifest == "" || len(p.Channel.Anchors) == 0 {
			bad("verify.mode %s needs channel.manifest and at least one channel anchor", mode)
		}
	}
	if mode == VerifySignature || mode == VerifyBoth {
		switch p.Signing.Provider {
		case SigningCosignKey:
			if len(p.Verify.PublicKeys) == 0 {
				bad("verifying cosign-key signatures needs verify.publicKeys")
			}
		case SigningGitHubAttestation:
			if p.Verify.Identity == nil || p.Verify.Identity.Issuer == "" ||
				p.Verify.Identity.SubjectRegexp == "" {
				bad("verifying attestations needs verify.identity issuer and subjectRegexp")
			} else if _, err := regexp.Compile(
				p.Verify.Identity.SubjectRegexp,
			); err != nil {
				bad("verify.identity.subjectRegexp: %v", err)
			}
		default:
			bad("verify.mode %s needs a signing provider other than none", mode)
		}
	}
	for _, a := range p.Channel.Anchors {
		if a.Name == "" || a.PublicKey == "" {
			bad("channel anchors need name and publicKey")
		}
	}
}
