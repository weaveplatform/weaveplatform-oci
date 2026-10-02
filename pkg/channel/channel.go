// Package channel reads, writes, signs and verifies weave channel manifests
// (decision 0006): the promotion record whose signature chain is an offline
// root key, an endorsed signing key, and the manifest. The formats are
// byte-compatible with weavemanifest and weaveplatform-agent-core's
// manifestverify (key, signature and manifest JSON; Ed25519 over a
// domain-separated message), so existing tooling signs and verifies channels
// that carry images.
//
// This package adds an "images" section. Agents that predate it ignore the
// field (they decode non-strictly), and the signature covers the exact bytes,
// so adding images changes nothing for them.
package channel

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/opencontainers/go-digest"
)

// Domain-separation contexts (weaveplatform-agent-core sdk/manifest/sig.go).
const (
	EndorseContext  = "weave-endorse-v1"
	ManifestContext = "weave-manifest-v1"
	// RootKeyID is the key_id every root public key file carries.
	RootKeyID = "root"
)

var (
	// ErrFormat reports a malformed key, signature or manifest file.
	ErrFormat = errors.New("channel: malformed file")
	// ErrSignature reports a broken signature chain.
	ErrSignature = errors.New("channel: signature chain does not verify")
	// ErrExpired reports a manifest past its expiry.
	ErrExpired = errors.New("channel: manifest expired")
	// ErrRollback reports a manifest older than one already accepted.
	ErrRollback = errors.New("channel: manifest sequence went backwards")
	// ErrFetch reports a channel file that could not be fetched.
	ErrFetch = errors.New("channel: fetch failed")
	// ErrNotListed reports an image digest absent from the channel.
	ErrNotListed = errors.New("channel: image is not in the channel")
)

// SigningMessage is the exact message signed: context, a NUL byte, the file bytes.
func SigningMessage(context string, data []byte) []byte {
	m := make([]byte, 0, len(context)+1+len(data))
	m = append(m, context...)
	m = append(m, 0)
	return append(m, data...)
}

// PublicKeyFile is a <name>.pub file.
type PublicKeyFile struct {
	Schema    int    `json:"schema"`
	KeyID     string `json:"key_id"`     //nolint:tagliatelle // existing wire format
	PublicKey string `json:"public_key"` //nolint:tagliatelle // existing wire format
}

// PrivateKeyFile is a <name>.key file.
type PrivateKeyFile struct {
	Schema     int    `json:"schema"`
	KeyID      string `json:"key_id"`      //nolint:tagliatelle // existing wire format
	PrivateKey string `json:"private_key"` //nolint:tagliatelle // existing wire format
}

// SignatureFile is a <file>.sig file.
type SignatureFile struct {
	Schema    int    `json:"schema"`
	KeyID     string `json:"key_id"` //nolint:tagliatelle // existing wire format
	Signature string `json:"signature"`
}

func marshalFile(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}
	return append(b, '\n'), nil
}

// GenerateKey returns a new key pair as .key and .pub file bytes.
func GenerateKey(keyID string) (keyFile, pubFile []byte, err error) {
	if keyID == "" {
		return nil, nil, fmt.Errorf("%w: key id is required", ErrFormat)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}
	if keyFile, err = marshalFile(PrivateKeyFile{Schema: 1, KeyID: keyID, PrivateKey: base64.StdEncoding.EncodeToString(priv)}); err != nil {
		return nil, nil, err
	}
	if pubFile, err = marshalFile(PublicKeyFile{Schema: 1, KeyID: keyID, PublicKey: base64.StdEncoding.EncodeToString(pub)}); err != nil {
		return nil, nil, err
	}
	return keyFile, pubFile, nil
}

// ParsePublicKey decodes a .pub file.
func ParsePublicKey(data []byte) (string, ed25519.PublicKey, error) {
	var f PublicKeyFile
	if err := json.Unmarshal(data, &f); err != nil {
		return "", nil, fmt.Errorf("%w: public key: %w", ErrFormat, err)
	}
	raw, err := base64.StdEncoding.DecodeString(f.PublicKey)
	if err != nil || f.Schema != 1 || f.KeyID == "" || len(raw) != ed25519.PublicKeySize {
		return "", nil, fmt.Errorf("%w: public key needs schema 1, a key_id and %d bytes", ErrFormat, ed25519.PublicKeySize)
	}
	return f.KeyID, ed25519.PublicKey(raw), nil
}

// ParsePrivateKey decodes a .key file.
func ParsePrivateKey(data []byte) (string, ed25519.PrivateKey, error) {
	var f PrivateKeyFile
	if err := json.Unmarshal(data, &f); err != nil {
		return "", nil, fmt.Errorf("%w: private key: %w", ErrFormat, err)
	}
	raw, err := base64.StdEncoding.DecodeString(f.PrivateKey)
	if err != nil || f.Schema != 1 || f.KeyID == "" || len(raw) != ed25519.PrivateKeySize {
		return "", nil, fmt.Errorf("%w: private key needs schema 1, a key_id and %d bytes", ErrFormat, ed25519.PrivateKeySize)
	}
	return f.KeyID, ed25519.PrivateKey(raw), nil
}

// ParseSignature decodes a .sig file.
func ParseSignature(data []byte) (string, []byte, error) {
	var f SignatureFile
	if err := json.Unmarshal(data, &f); err != nil {
		return "", nil, fmt.Errorf("%w: signature: %w", ErrFormat, err)
	}
	raw, err := base64.StdEncoding.DecodeString(f.Signature)
	if err != nil || f.Schema != 1 || f.KeyID == "" || len(raw) != ed25519.SignatureSize {
		return "", nil, fmt.Errorf("%w: signature needs schema 1, a key_id and %d bytes", ErrFormat, ed25519.SignatureSize)
	}
	return f.KeyID, raw, nil
}

// Sign signs data under context with a .key file and returns the .sig file.
func Sign(keyFile []byte, context string, data []byte) ([]byte, error) {
	id, priv, err := ParsePrivateKey(keyFile)
	if err != nil {
		return nil, err
	}
	sig := ed25519.Sign(priv, SigningMessage(context, data))
	return marshalFile(SignatureFile{Schema: 1, KeyID: id, Signature: base64.StdEncoding.EncodeToString(sig)})
}

// Endorse signs a signing key's .pub file with the root .key file.
func Endorse(rootKeyFile, signingPubFile []byte) ([]byte, error) {
	if id, _, err := ParsePrivateKey(rootKeyFile); err != nil {
		return nil, err
	} else if id != RootKeyID {
		return nil, fmt.Errorf("%w: endorsing key must have key_id %q, has %q", ErrFormat, RootKeyID, id)
	}
	return Sign(rootKeyFile, EndorseContext, signingPubFile)
}

// Manifest is the channel manifest. Fields the module and core sections
// carry are preserved in Raw for callers that need them; this package reads
// only the envelope fields and images.
type Manifest struct {
	Schema      int            `json:"schema"`
	Channel     string         `json:"channel"`
	GeneratedAt string         `json:"generated_at"` //nolint:tagliatelle // existing wire format
	Sequence    uint64         `json:"sequence"`
	Expires     string         `json:"expires,omitempty"`
	Protocol    ProtocolWindow `json:"protocol"`
	Images      []Image        `json:"images,omitempty"`
}

// ProtocolWindow is the core protocol range.
type ProtocolWindow struct {
	Min uint32 `json:"min"`
	Max uint32 `json:"max"`
}

// Image is one promoted guest image.
type Image struct {
	Repository string     `json:"repository"`
	Tag        string     `json:"tag"`
	Digest     string     `json:"digest"`
	Platforms  []Platform `json:"platforms,omitempty"`
	Signature  *Signer    `json:"signature,omitempty"`
	BuildDate  string     `json:"build_date,omitempty"` //nolint:tagliatelle // existing wire format
}

// Platform is one child manifest of an image index.
type Platform struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	OSVersion string `json:"os_version,omitempty"` //nolint:tagliatelle // existing wire format
	Digest    string `json:"digest"`
}

// Signer names the build-time signature consumers should also expect.
type Signer struct {
	Provider      string `json:"provider"`                 // cosign-key or github-attestation
	KeyID         string `json:"key_id,omitempty"`         //nolint:tagliatelle // existing wire format
	Issuer        string `json:"issuer,omitempty"`
	SubjectRegexp string `json:"subject_regexp,omitempty"` //nolint:tagliatelle // existing wire format
}

// Parse decodes a manifest non-strictly (unknown fields are kept out of the
// struct, as core does) and checks the envelope rules core enforces.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%w: manifest: %w", ErrFormat, err)
	}
	if m.Schema != 1 || m.Channel == "" || m.Protocol.Min == 0 || m.Protocol.Max < m.Protocol.Min {
		return nil, fmt.Errorf("%w: manifest needs schema 1, a channel and a valid protocol window", ErrFormat)
	}
	for _, img := range m.Images {
		if _, err := digest.Parse(img.Digest); err != nil || img.Repository == "" {
			return nil, fmt.Errorf("%w: image %q has an invalid repository or digest", ErrFormat, img.Repository)
		}
	}
	return &m, nil
}

// Expired reports whether the manifest is past its expiry; an unparseable
// expiry counts as expired (fail closed), an empty one never expires.
func (m *Manifest) Expired(now time.Time) bool {
	if m.Expires == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, m.Expires)
	return err != nil || !now.Before(t)
}

// Image returns the entry promoting index digest d in repository (the
// repository path, for example deploymenttheory/weave-images/ubuntu-24.04).
func (m *Manifest) Image(repository string, d digest.Digest) (*Image, error) {
	for i := range m.Images {
		if m.Images[i].Digest == d.String() && (repository == "" || m.Images[i].Repository == repository) {
			return &m.Images[i], nil
		}
	}
	return nil, fmt.Errorf("%w: %s@%s in channel %q", ErrNotListed, repository, d, m.Channel)
}

// Bundle is the four files a consumer fetches to verify a channel.
type Bundle struct {
	Manifest      []byte // the exact manifest bytes
	ManifestSig   []byte // <manifest>.sig
	SigningKey    []byte // signing .pub
	SigningKeySig []byte // the root's endorsement: signing .pub.sig
}

// Anchor is a trusted root key.
type Anchor struct {
	Name string
	Key  ed25519.PublicKey
}

// ParseAnchor decodes a root .pub file, which must carry key_id "root".
func ParseAnchor(name string, pubFile []byte) (Anchor, error) {
	id, k, err := ParsePublicKey(pubFile)
	if err != nil {
		return Anchor{}, err
	}
	if id != RootKeyID {
		return Anchor{}, fmt.Errorf("%w: anchor %q has key_id %q, want %q", ErrFormat, name, id, RootKeyID)
	}
	return Anchor{Name: name, Key: k}, nil
}

// Options tune Verify.
type Options struct {
	Now func() time.Time
	// MinSequence refuses manifests with a lower sequence (anti-rollback);
	// callers persist the highest sequence they have accepted.
	MinSequence uint64
}

// Verify checks the chain under any of anchors, in manifestverify's order
// (endorsement, then manifest signature, then parsing), then expiry and
// sequence. It returns the manifest and the anchor that verified it.
func Verify(anchors []Anchor, b Bundle, o Options) (*Manifest, Anchor, error) {
	endorserID, endorsement, err := ParseSignature(b.SigningKeySig)
	if err != nil {
		return nil, Anchor{}, err
	}
	if endorserID != RootKeyID {
		return nil, Anchor{}, fmt.Errorf("%w: endorsement signed by %q, not the root", ErrSignature, endorserID)
	}
	var anchor *Anchor
	for i := range anchors {
		if ed25519.Verify(anchors[i].Key, SigningMessage(EndorseContext, b.SigningKey), endorsement) {
			anchor = &anchors[i]
			break
		}
	}
	if anchor == nil {
		return nil, Anchor{}, fmt.Errorf("%w: signing key is not endorsed by any configured anchor", ErrSignature)
	}
	signingID, signingKey, err := ParsePublicKey(b.SigningKey)
	if err != nil {
		return nil, Anchor{}, err
	}
	sigID, sig, err := ParseSignature(b.ManifestSig)
	if err != nil {
		return nil, Anchor{}, err
	}
	if sigID != signingID {
		return nil, Anchor{}, fmt.Errorf("%w: manifest signed by %q, endorsed key is %q", ErrSignature, sigID, signingID)
	}
	if !ed25519.Verify(signingKey, SigningMessage(ManifestContext, b.Manifest), sig) {
		return nil, Anchor{}, fmt.Errorf("%w: manifest signature", ErrSignature)
	}
	m, err := Parse(b.Manifest)
	if err != nil {
		return nil, Anchor{}, err
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	if m.Expired(now()) {
		return nil, Anchor{}, fmt.Errorf("%w: %s", ErrExpired, m.Expires)
	}
	if m.Sequence < o.MinSequence {
		return nil, Anchor{}, fmt.Errorf("%w: %d < %d", ErrRollback, m.Sequence, o.MinSequence)
	}
	return m, *anchor, nil
}
