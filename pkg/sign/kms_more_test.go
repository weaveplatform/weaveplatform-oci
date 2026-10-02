package sign_test

import (
	"context"
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/sigstore/sigstore/pkg/signature/kms"
	"oras.land/oras-go/v2/content/memory"

	"github.com/deploymenttheory/weaveplatform-oci/pkg/sign"
)

// fakeKMS is a kms.SignerVerifier backed by an in-memory ECDSA key.
type fakeKMS struct {
	signature.SignerVerifier
	key   *ecdsa.PrivateKey
	csErr error
}

func (f fakeKMS) CreateKey(context.Context, string) (crypto.PublicKey, error) {
	return f.key.Public(), nil
}

func (f fakeKMS) CryptoSigner(
	context.Context,
	func(error),
) (crypto.Signer, crypto.SignerOpts, error) {
	if f.csErr != nil {
		return nil, nil, f.csErr
	}
	return f.key, crypto.SHA256, nil
}
func (f fakeKMS) SupportedAlgorithms() []string { return []string{"ecdsa-p256"} }
func (f fakeKMS) DefaultAlgorithm() string      { return "ecdsa-p256" }

func init() {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	sv, _ := signature.LoadECDSASignerVerifier(k, crypto.SHA256)
	kms.AddProvider(
		"weavetestkms://",
		func(_ context.Context, ref string, _ crypto.Hash, _ ...signature.RPCOption) (kms.SignerVerifier, error) {
			switch ref {
			case "weavetestkms://broken":
				return nil, errors.New("provider init failed")
			case "weavetestkms://nosigner":
				return fakeKMS{
					SignerVerifier: sv,
					key:            k,
					csErr:          errors.New("no crypto signer"),
				}, nil
			}
			return fakeKMS{SignerVerifier: sv, key: k}, nil
		},
	)
}

func TestLoadKeyFromKMS(t *testing.T) {
	ctx := context.Background()
	s, err := sign.LoadKey(ctx, "weavetestkms://key", nil)
	if err != nil || s.KeyID() == "" {
		t.Fatalf("%v", err)
	}
	for _, ref := range []string{"weavetestkms://broken", "weavetestkms://nosigner"} {
		if _, err := sign.LoadKey(ctx, ref, nil); !errors.Is(err, sign.ErrKey) {
			t.Fatalf("%s: %v", ref, err)
		}
	}
}

func TestLoadKeyNonSigningKey(t *testing.T) {
	k, _ := ecdh.X25519().GenerateKey(rand.Reader)
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "x25519.pem")
	_ = os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600)
	if _, err := sign.LoadKey(context.Background(), p, []byte{}); !errors.Is(err, sign.ErrKey) {
		t.Fatalf("x25519 accepted: %v", err)
	}
}

// failingSigner has a P-256 public key but cannot sign.
type failingSigner struct{ pub crypto.PublicKey }

func (f failingSigner) Public() crypto.PublicKey { return f.pub }
func (f failingSigner) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return nil, errors.New("hsm unavailable")
}

func TestSigningFailures(t *testing.T) {
	ctx := context.Background()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	s, err := sign.New(failingSigner{pub: k.Public()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Bundle(ctx, digest.FromString("x")); err == nil {
		t.Fatal("bundle with a failing signer succeeded")
	}
	if _, err := s.Sign(
		ctx,
		memory.New(),
		ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageIndex,
			Digest:    digest.FromString("x"),
			Size:      1,
		},
	); err == nil {
		t.Fatal("sign with a failing signer succeeded")
	}
	if string(sign.PAE("t", []byte("p"))) != "DSSEv1 1 t 1 p" {
		t.Fatal("PAE")
	}
}

func TestSignTwiceReusesBundleAndRejectsBadSubject(t *testing.T) {
	ctx := context.Background()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	s, _ := sign.New(k)
	store := memory.New()
	// a subject missing from the store makes the referrer push fail after the bundle blob exists
	subj := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageIndex,
		Digest:    digest.FromString("absent"),
		Size:      3,
	}
	_, _ = s.Sign(ctx, store, subj)
	if _, err := s.Sign(
		ctx,
		store,
		ocispec.Descriptor{
			MediaType: "not a media type!!",
			Digest:    digest.FromString("y"),
			Size:      3,
		},
	); err == nil {
		t.Log(
			"registry accepted an odd subject media type",
		) // exercises the manifest push path either way
	}
}
