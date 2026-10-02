// Package sign produces cosign-compatible key signatures for weave guest
// artifacts (decision 0006, private profile). A signature is a Sigstore
// bundle (v0.3) holding a DSSE envelope over an in-toto statement whose
// subject is the signed manifest or index digest, pushed as an OCI 1.1
// referrer exactly as `cosign sign --key` v3 does, so that
// `cosign verify --key cosign.pub --insecure-ignore-tlog <ref@digest>` and
// zot's trust extension both accept it. No transparency log or timestamp
// authority is used, so signing and verification work offline.
package sign

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	intotov1 "github.com/in-toto/attestation/go/v1"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/secure-systems-lab/go-securesystemslib/encrypted"
	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	protodsse "github.com/sigstore/protobuf-specs/gen/pb-go/dsse"
	"github.com/sigstore/sigstore/pkg/cryptoutils"
	"github.com/sigstore/sigstore/pkg/signature/kms"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
)

// Wire constants shared with cosign v3.
const (
	BundleMediaType     = "application/vnd.dev.sigstore.bundle.v0.3+json"
	PredicateType       = "https://sigstore.dev/cosign/sign/v1"
	InTotoPayloadType   = "application/vnd.in-toto+json"
	AnnotationContent   = "dev.sigstore.bundle.content"
	AnnotationPredicate = "dev.sigstore.bundle.predicateType"
	EnvPassword         = "COSIGN_PASSWORD"
	privateKeyPEMType   = "ENCRYPTED SIGSTORE PRIVATE KEY"
	envKeyPrefix        = "env://"
)

// ErrKey reports an unusable signing key.
var ErrKey = errors.New("unusable signing key")

// Signer signs subjects and pushes the signature referrer.
type Signer struct {
	key  crypto.Signer
	hint []byte
	now  func() time.Time
}

// New wraps an ECDSA P-256 signer (a file key or a KMS key).
func New(s crypto.Signer) (*Signer, error) {
	pub, ok := s.Public().(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, fmt.Errorf("%w: cosign-compatible signatures need an ECDSA P-256 key", ErrKey)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrKey, err)
	}
	return &Signer{key: s, hint: []byte(KeyHint(der)), now: time.Now}, nil
}

// KeyHint is cosign's public-key hint: base64(sha256(PKIX DER)).
func KeyHint(pkixDER []byte) string {
	h := sha256.Sum256(pkixDER)
	return base64.StdEncoding.EncodeToString(h[:])
}

// KeyID returns the hint identifying this signer's key.
func (s *Signer) KeyID() string { return string(s.hint) }

// PublicKeyPEM returns the public key as cosign writes cosign.pub.
func (s *Signer) PublicKeyPEM() ([]byte, error) {
	b, err := cryptoutils.MarshalPublicKeyToPEM(s.key.Public())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrKey, err)
	}
	return b, nil
}

// Bundle returns the Sigstore bundle JSON signing subject.
func (s *Signer) Bundle(_ context.Context, subject digest.Digest) ([]byte, error) {
	if err := subject.Validate(); err != nil || subject.Algorithm() != digest.SHA256 {
		return nil, fmt.Errorf("%w: subject %q must be a sha256 digest", ErrKey, subject)
	}
	ann, err := structpb.NewStruct(nil)
	if err != nil {
		return nil, fmt.Errorf("statement: %w", err)
	}
	stmt := &intotov1.Statement{
		Type: intotov1.StatementTypeUri,
		Subject: []*intotov1.ResourceDescriptor{
			{Digest: map[string]string{"sha256": subject.Encoded()}, Annotations: ann},
		},
		PredicateType: PredicateType,
		Predicate:     &structpb.Struct{},
	}
	payload, err := protojson.Marshal(stmt)
	if err != nil {
		return nil, fmt.Errorf("statement: %w", err)
	}
	sig, err := s.signPAE(InTotoPayloadType, payload)
	if err != nil {
		return nil, err
	}
	pb := &protobundle.Bundle{
		MediaType: BundleMediaType,
		VerificationMaterial: &protobundle.VerificationMaterial{
			Content: &protobundle.VerificationMaterial_PublicKey{
				PublicKey: &protocommon.PublicKeyIdentifier{Hint: string(s.hint)},
			},
		},
		Content: &protobundle.Bundle_DsseEnvelope{DsseEnvelope: &protodsse.Envelope{
			Payload:     payload,
			PayloadType: InTotoPayloadType,
			Signatures:  []*protodsse.Signature{{Sig: sig}},
		}},
	}
	out, err := protojson.Marshal(pb)
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	return out, nil
}

// Sign signs subject and pushes the referrer to dst (a registry repository or
// any oras target). Registries without the referrers API receive the
// sha256-<hex> fallback tag through oras. It returns the referrer descriptor.
func (s *Signer) Sign(
	ctx context.Context,
	dst oras.Target,
	subject ocispec.Descriptor,
) (ocispec.Descriptor, error) {
	b, err := s.Bundle(ctx, subject.Digest)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	layer := content.NewDescriptorFromBytes(BundleMediaType, b)
	if err := pushIfMissing(ctx, dst, layer, b); err != nil {
		return ocispec.Descriptor{}, err
	}
	subj := ocispec.Descriptor{
		MediaType: subject.MediaType,
		Digest:    subject.Digest,
		Size:      subject.Size,
	}
	d, err := oras.PackManifest(
		ctx,
		dst,
		oras.PackManifestVersion1_1,
		BundleMediaType,
		oras.PackManifestOptions{
			Subject: &subj,
			Layers:  []ocispec.Descriptor{layer},
			ManifestAnnotations: map[string]string{
				ocispec.AnnotationCreated: s.now().UTC().Format(time.RFC3339),
				AnnotationContent:         "dsse-envelope",
				AnnotationPredicate:       PredicateType,
			},
		},
	)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("push signature for %s: %w", subject.Digest, err)
	}
	return d, nil
}

func pushIfMissing(ctx context.Context, dst oras.Target, d ocispec.Descriptor, b []byte) error {
	ok, err := dst.Exists(ctx, d)
	if err != nil {
		return fmt.Errorf("push bundle: %w", err)
	}
	if ok {
		return nil
	}
	if err := dst.Push(ctx, d, strings.NewReader(string(b))); err != nil {
		return fmt.Errorf("push bundle: %w", err)
	}
	return nil
}

// PAE is the DSSE pre-authentication encoding that is actually signed.
func PAE(payloadType string, payload []byte) []byte {
	return []byte(
		fmt.Sprintf("DSSEv1 %d %s %d %s", len(payloadType), payloadType, len(payload), payload),
	)
}

// signPAE signs sha256(PAE) with ECDSA, as cosign and sigstore-go do.
func (s *Signer) signPAE(payloadType string, payload []byte) ([]byte, error) {
	d := sha256.Sum256(PAE(payloadType, payload))
	sig, err := s.key.Sign(rand.Reader, d[:], crypto.SHA256)
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	return sig, nil
}

// LoadKey resolves a key reference: a PEM file path, env://NAME holding the
// PEM, or a KMS URI (awskms://, gcpkms://, azurekms://, hashivault://) whose
// provider the binary has registered. Encrypted cosign keys are decrypted
// with password, which defaults to $COSIGN_PASSWORD.
func LoadKey(ctx context.Context, ref string, password []byte) (*Signer, error) {
	if password == nil {
		password = []byte(os.Getenv(EnvPassword))
	}
	var raw []byte
	switch {
	case strings.HasPrefix(ref, envKeyPrefix):
		raw = []byte(os.Getenv(strings.TrimPrefix(ref, envKeyPrefix)))
		if len(raw) == 0 {
			return nil, fmt.Errorf("%w: %s is empty", ErrKey, ref)
		}
	case strings.Contains(ref, "://"):
		sv, err := kms.Get(ctx, ref, crypto.SHA256)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrKey, ref, err)
		}
		cs, _, err := sv.CryptoSigner(ctx, func(error) {})
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrKey, ref, err)
		}
		return New(cs)
	default:
		b, err := os.ReadFile(ref) //nolint:gosec // the operator chooses the key file
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrKey, err)
		}
		raw = b
	}
	pk, err := cryptoutils.UnmarshalPEMToPrivateKey(raw, cryptoutils.StaticPasswordFunc(password))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrKey, err)
	}
	cs, ok := pk.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("%w: not a signing key", ErrKey)
	}
	return New(cs)
}

// GenerateKeyPair creates an ECDSA P-256 key pair in cosign's format: the
// private key encrypted with password (scrypt + secretbox) under the PEM type
// "ENCRYPTED SIGSTORE PRIVATE KEY", the public key as PKIX PEM.
func GenerateKeyPair(password []byte) (privatePEM, publicPEM []byte, err error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}
	enc, err := encrypted.Encrypt(der, password)
	if err != nil {
		return nil, nil, fmt.Errorf("encrypt key: %w", err)
	}
	pub, err := cryptoutils.MarshalPublicKeyToPEM(k.Public())
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: privateKeyPEMType, Bytes: enc}), pub, nil
}
