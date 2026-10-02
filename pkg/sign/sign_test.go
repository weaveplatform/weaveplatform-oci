package sign_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"

	"github.com/deploymenttheory/weaveplatform-oci/pkg/sign"
)

var errBoom = errors.New("boom")

func keyFile(t *testing.T, pw string) (string, []byte) {
	t.Helper()
	priv, pub, err := sign.GenerateKeyPair([]byte(pw))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "cosign.key")
	_ = os.WriteFile(p, priv, 0o600)
	return p, pub
}

func subjectIn(t *testing.T, s *memory.Store) ocispec.Descriptor {
	t.Helper()
	b := []byte(
		`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`,
	)
	d := content.NewDescriptorFromBytes(ocispec.MediaTypeImageIndex, b)
	if err := s.Push(context.Background(), d, strings.NewReader(string(b))); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSignProducesCosignShape(t *testing.T) {
	ctx := context.Background()
	path, pubPEM := keyFile(t, "pw")
	s, err := sign.LoadKey(ctx, path, []byte("pw"))
	if err != nil {
		t.Fatal(err)
	}
	gotPub, _ := s.PublicKeyPEM()
	if string(gotPub) != string(pubPEM) {
		t.Fatal("public key mismatch")
	}
	block, _ := pem.Decode(pubPEM)
	if s.KeyID() != sign.KeyHint(block.Bytes) {
		t.Fatal("hint")
	}
	store := memory.New()
	subj := subjectIn(t, store)
	ref, err := s.Sign(ctx, store, subj)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := content.FetchAll(ctx, store, ref)
	var m ocispec.Manifest
	_ = json.Unmarshal(raw, &m)
	if m.ArtifactType != sign.BundleMediaType || m.Config.MediaType != ocispec.MediaTypeEmptyJSON ||
		len(
			m.Layers,
		) != 1 || m.Layers[0].MediaType != sign.BundleMediaType || m.Subject == nil || m.Subject.Digest != subj.Digest ||
		m.Annotations[sign.AnnotationContent] != "dsse-envelope" || m.Annotations[sign.AnnotationPredicate] != sign.PredicateType {
		t.Fatalf("referrer manifest %s", raw)
	}
	bundleJSON, _ := content.FetchAll(ctx, store, m.Layers[0])
	var b struct {
		MediaType            string `json:"mediaType"`
		VerificationMaterial struct {
			PublicKey struct{ Hint string } `json:"publicKey"`
		} `json:"verificationMaterial"`
		DSSEEnvelope struct {
			Payload     string `json:"payload"`
			PayloadType string `json:"payloadType"`
		} `json:"dsseEnvelope"`
	}
	if err := json.Unmarshal(bundleJSON, &b); err != nil {
		t.Fatal(err)
	}
	payload, _ := base64.StdEncoding.DecodeString(b.DSSEEnvelope.Payload)
	var stmt struct {
		Type          string `json:"_type"`
		PredicateType string `json:"predicateType"`
		Subject       []struct{ Digest map[string]string }
	}
	_ = json.Unmarshal(payload, &stmt)
	if b.MediaType != sign.BundleMediaType || b.VerificationMaterial.PublicKey.Hint != s.KeyID() ||
		b.DSSEEnvelope.PayloadType != sign.InTotoPayloadType || stmt.PredicateType != sign.PredicateType ||
		stmt.Subject[0].Digest["sha256"] != subj.Digest.Encoded() || stmt.Type != "https://in-toto.io/Statement/v1" {
		t.Fatalf("bundle %s / %s", bundleJSON, payload)
	}
	// signing again reuses the identical-content blobs
	if _, err := s.Sign(ctx, store, subj); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Bundle(ctx, "sha512:abc"); !errors.Is(err, sign.ErrKey) {
		t.Fatal("non-sha256 subject accepted")
	}
}

func TestLoadKeyVariants(t *testing.T) {
	ctx := context.Background()
	path, _ := keyFile(t, "secret")
	raw, _ := os.ReadFile(path)
	t.Setenv("WEAVE_TEST_KEY", string(raw))
	t.Setenv(sign.EnvPassword, "secret")
	if _, err := sign.LoadKey(ctx, "env://WEAVE_TEST_KEY", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := sign.LoadKey(ctx, path, nil); err != nil {
		t.Fatal("COSIGN_PASSWORD not used")
	}
	cases := map[string]string{
		"wrong password": path,
		"empty env":      "env://WEAVE_TEST_MISSING",
		"missing file":   filepath.Join(t.TempDir(), "none"),
		"unknown kms":    "nokms://key",
	}
	for name, ref := range cases {
		t.Run(name, func(t *testing.T) {
			pw := []byte("secret")
			if name == "wrong password" {
				pw = []byte("nope")
			}
			if _, err := sign.LoadKey(ctx, ref, pw); !errors.Is(err, sign.ErrKey) {
				t.Fatalf("want ErrKey, got %v", err)
			}
		})
	}
	// an unencrypted Ed25519 key is refused (cosign verify uses ECDSA P-256)
	_, edKey, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(edKey)
	edPath := filepath.Join(t.TempDir(), "ed.pem")
	_ = os.WriteFile(edPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600)
	if _, err := sign.LoadKey(ctx, edPath, []byte{}); !errors.Is(err, sign.ErrKey) {
		t.Fatal("ed25519 accepted")
	}
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if _, err := sign.New(p384); !errors.Is(err, sign.ErrKey) {
		t.Fatal("P-384 accepted")
	}
	// an unencrypted P-256 PKCS#8 key works with an empty password
	p256, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ = x509.MarshalPKCS8PrivateKey(p256)
	plain := filepath.Join(t.TempDir(), "plain.pem")
	_ = os.WriteFile(plain, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600)
	if _, err := sign.LoadKey(ctx, plain, []byte{}); err != nil {
		t.Fatal(err)
	}
}

type failingTarget struct {
	*memory.Store
	existsErr, pushErr error
}

func (f failingTarget) Exists(ctx context.Context, d ocispec.Descriptor) (bool, error) {
	if f.existsErr != nil {
		return false, f.existsErr
	}
	return f.Store.Exists(ctx, d) //nolint:wrapcheck // test double
}

func (f failingTarget) Push(ctx context.Context, d ocispec.Descriptor, r io.Reader) error {
	if f.pushErr != nil {
		return f.pushErr
	}
	return f.Store.Push(ctx, d, r) //nolint:wrapcheck // test double
}

func TestSignErrors(t *testing.T) {
	ctx := context.Background()
	path, _ := keyFile(t, "")
	s, err := sign.LoadKey(ctx, path, []byte{})
	if err != nil {
		t.Fatal(err)
	}
	store := memory.New()
	subj := subjectIn(t, store)
	if _, err := s.Sign(
		ctx,
		failingTarget{Store: store, existsErr: errBoom},
		subj,
	); !errors.Is(
		err,
		errBoom,
	) {
		t.Fatal(err)
	}
	if _, err := s.Sign(
		ctx,
		failingTarget{Store: memory.New(), pushErr: errBoom},
		subj,
	); !errors.Is(
		err,
		errBoom,
	) {
		t.Fatal(err)
	}
	// the bundle blob exists but the manifest push fails
	fresh := memory.New()
	if _, err := s.Sign(ctx, fresh, subj); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sign(
		ctx,
		failingTarget{Store: fresh, pushErr: errBoom},
		ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageIndex,
			Digest:    digest.FromString("other"),
			Size:      5,
		},
	); err == nil {
		t.Fatal("manifest push failure swallowed")
	}
	if _, err := s.Sign(
		ctx,
		store,
		ocispec.Descriptor{Digest: "sha512:x"},
	); !errors.Is(
		err,
		sign.ErrKey,
	) {
		t.Fatal(err)
	}
}
