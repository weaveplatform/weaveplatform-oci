package verify_test

import (
	"context"
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	protodsse "github.com/sigstore/protobuf-specs/gen/pb-go/dsse"
	"github.com/sigstore/sigstore-go/pkg/testing/data"
	"google.golang.org/protobuf/encoding/protojson"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"

	"github.com/weaveplatform/weaveplatform-oci/pkg/channel"
	"github.com/weaveplatform/weaveplatform-oci/pkg/profile"
	"github.com/weaveplatform/weaveplatform-oci/pkg/sign"
	"github.com/weaveplatform/weaveplatform-oci/pkg/verify"
)

var errBoom = errors.New("boom")

// craftBundle builds a key-signed DSSE bundle over an arbitrary statement.
func craftBundle(
	t *testing.T,
	k *ecdsa.PrivateKey,
	hint string,
	stmt []byte,
	withMaterial bool,
) []byte {
	t.Helper()
	d := sha256.Sum256(sign.PAE(sign.InTotoPayloadType, stmt))
	sig, err := k.Sign(rand.Reader, d[:], crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	b := &protobundle.Bundle{
		MediaType: sign.BundleMediaType,
		Content: &protobundle.Bundle_DsseEnvelope{DsseEnvelope: &protodsse.Envelope{
			Payload:     stmt,
			PayloadType: sign.InTotoPayloadType,
			Signatures:  []*protodsse.Signature{{Sig: sig}},
		}},
	}
	if withMaterial {
		b.VerificationMaterial = &protobundle.VerificationMaterial{
			Content: &protobundle.VerificationMaterial_PublicKey{
				PublicKey: &protocommon.PublicKeyIdentifier{Hint: hint},
			},
		}
	}
	out, err := protojson.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestKeySetEdgeCases(t *testing.T) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	s, _ := sign.New(k)
	ks, err := verify.NewKeySet(k.Public())
	if err != nil {
		t.Fatal(err)
	}
	d := digest.FromString("subject")
	// valid signature, wrong predicate type
	other := craftBundle(t, k, s.KeyID(), statement(d, "https://example.com/not-cosign"), true)
	if _, err := ks.Verify(other, d); !errors.Is(err, verify.ErrUnverified) {
		t.Fatalf("foreign predicate accepted: %v", err)
	}
	// no verification material at all
	if _, err := ks.Verify(
		craftBundle(t, k, s.KeyID(), statement(d, sign.PredicateType), false),
		d,
	); !errors.Is(
		err,
		verify.ErrUnverified,
	) {
		t.Fatal(err)
	}
	// a keyless (certificate) bundle has no key hint
	keyless := data.Bundle(t, "dsse.sigstore.json")
	raw, err := keyless.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ks.Verify(raw, d); !errors.Is(err, verify.ErrUnverified) {
		t.Fatal(err)
	}
	// a key type the verifier libraries cannot use
	x, _ := ecdh.X25519().GenerateKey(rand.Reader)
	if _, err := verify.NewKeySet(x.PublicKey()); !errors.Is(err, verify.ErrConfig) {
		t.Fatal(err)
	}
}

func TestAttestationBundleBytesAndTrustedRoot(t *testing.T) {
	tr := data.TrustedRoot(t, "public-good.json")
	raw, err := tr.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "trusted_root.json")
	_ = os.WriteFile(p, raw, 0o600)
	loaded, err := verify.LoadTrustedRoot(p)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := data.Bundle(t, "dsse.sigstore.json").MarshalJSON()
	id := &verify.Identity{
		Trusted:       loaded,
		Issuer:        "https://accounts.google.com",
		SubjectRegexp: ".*",
		RequireSCT:    true,
	}
	if _, err := id.Verify(b, digest.FromString("x")); err == nil {
		t.Fatal("unrelated subject verified")
	}
}

// fakeSource scripts referrer discovery.
type fakeSource struct {
	refs     []ocispec.Descriptor
	refsErr  error
	blobs    map[digest.Digest][]byte
	fetchErr map[digest.Digest]error
}

func (f fakeSource) Referrers(
	context.Context,
	ocispec.Descriptor,
	string,
) ([]ocispec.Descriptor, error) {
	return f.refs, f.refsErr
}

func (f fakeSource) FetchAll(_ context.Context, d ocispec.Descriptor) ([]byte, error) {
	if err := f.fetchErr[d.Digest]; err != nil {
		return nil, err
	}
	return f.blobs[d.Digest], nil
}

func TestSignatureDiscoveryFailures(t *testing.T) {
	ctx := context.Background()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ks, _ := verify.NewKeySet(k.Public())
	subj := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageIndex,
		Digest:    digest.FromString("s"),
		Size:      1,
	}
	layer := content.NewDescriptorFromBytes(sign.BundleMediaType, []byte("{}"))
	good, _ := json.Marshal(ocispec.Manifest{Layers: []ocispec.Descriptor{layer}})
	twoLayers, _ := json.Marshal(ocispec.Manifest{Layers: []ocispec.Descriptor{layer, layer}})
	gd, td, notJSONDesc := digest.FromBytes(good), digest.FromBytes(twoLayers), digest.FromString("notjson")
	src := fakeSource{
		refs: []ocispec.Descriptor{{Digest: notJSONDesc}, {Digest: td}, {Digest: gd}},
		blobs: map[digest.Digest][]byte{
			notJSONDesc:           []byte("not json"),
			td:           twoLayers,
			gd:           good,
			layer.Digest: []byte("{}"),
		},
	}
	base := verify.Policy{
		Mode:     profile.VerifySignature,
		Provider: profile.SigningCosignKey,
		Keys:     ks,
	}
	if _, err := verify.Signature(ctx, base, src, subj); !errors.Is(err, verify.ErrUnverified) {
		t.Fatalf("garbage bundle verified: %v", err)
	}
	failRef := src
	failRef.fetchErr = map[digest.Digest]error{notJSONDesc: errBoom}
	if _, err := verify.Signature(ctx, base, failRef, subj); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	failBlob := src
	failBlob.fetchErr = map[digest.Digest]error{layer.Digest: errBoom}
	if _, err := verify.Signature(ctx, base, failBlob, subj); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
	if _, err := verify.Signature(
		ctx,
		base,
		fakeSource{refsErr: errBoom},
		subj,
	); !errors.Is(
		err,
		errBoom,
	) {
		t.Fatal(err)
	}
	// attestation provider with an identity: bundle fails, error is ErrUnverified
	vs := base
	vs.Provider = profile.SigningGitHubAttestation
	vs.Identity = &verify.Identity{
		Trusted:       data.TrustedRoot(t, "public-good.json"),
		Issuer:        "i",
		SubjectRegexp: ".*",
	}
	if _, err := verify.Signature(ctx, vs, src, subj); !errors.Is(err, verify.ErrUnverified) {
		t.Fatal(err)
	}
}

// failingGraph is a store whose Predecessors or Fetch fail.
type failingGraph struct {
	*memory.Store
	predErr, fetchErr error
}

func (f failingGraph) Predecessors(
	ctx context.Context,
	d ocispec.Descriptor,
) ([]ocispec.Descriptor, error) {
	if f.predErr != nil {
		return nil, f.predErr
	}
	return f.Store.Predecessors(ctx, d) //nolint:wrapcheck // test double
}

func (f failingGraph) Fetch(ctx context.Context, d ocispec.Descriptor) (io.ReadCloser, error) {
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	return f.Store.Fetch(ctx, d) //nolint:wrapcheck // test double
}

func TestStoreSourceFailuresAndFiltering(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	subj := subject(t, store, idx)
	s := signer(t)
	if _, err := s.Sign(ctx, store, subj); err != nil {
		t.Fatal(err)
	}
	// a non-referrer predecessor: an index listing the subject
	parent, _ := json.Marshal(
		ocispec.Index{
			Versioned: subjVersioned(),
			MediaType: ocispec.MediaTypeImageIndex,
			Manifests: []ocispec.Descriptor{subj},
		},
	)
	pd := content.NewDescriptorFromBytes(ocispec.MediaTypeImageIndex, parent)
	_ = store.Push(ctx, pd, bytesReader(parent))
	refs, err := verify.StoreSource{Store: store}.Referrers(ctx, subj, "")
	if err != nil || len(refs) != 1 {
		t.Fatalf("%v %d", err, len(refs))
	}
	if refs, _ := (verify.StoreSource{Store: store}).Referrers(
		ctx,
		subj,
		"application/other",
	); len(
		refs,
	) != 0 {
		t.Fatal("artifactType filter ignored")
	}
	if _, err := (verify.StoreSource{Store: failingGraph{Store: store, predErr: errBoom}}).Referrers(
		ctx,
		subj,
		"",
	); !errors.Is(
		err,
		errBoom,
	) {
		t.Fatal(err)
	}
	if _, err := (verify.StoreSource{Store: failingGraph{Store: store, fetchErr: errBoom}}).Referrers(
		ctx,
		subj,
		"",
	); !errors.Is(
		err,
		errBoom,
	) {
		t.Fatal(err)
	}
	if _, err := (verify.StoreSource{Store: store}).FetchAll(
		ctx,
		ocispec.Descriptor{Digest: digest.FromString("absent"), Size: 1},
	); err == nil {
		t.Fatal("absent blob fetched")
	}
}

func writeFile(t *testing.T, dir, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFromProfile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := signer(t)
	pub, _ := s.PublicKeyPEM()
	pubPath := writeFile(t, dir, "cosign.pub", pub)
	rk, rp, _ := channel.GenerateKey(channel.RootKeyID)
	sk, sp, _ := channel.GenerateKey("signing")
	m, _ := channel.New("org", time.Now())
	end, _ := channel.Endorse(rk, sp)
	sig, _ := channel.Sign(sk, channel.ManifestContext, m)
	manifest := writeFile(t, dir, "stable.json", m)
	writeFile(t, dir, "stable.json.sig", sig)
	writeFile(t, dir, channel.SigningKeyFile, sp)
	writeFile(t, dir, channel.SigningKeySigFile, end)
	rootPath := writeFile(t, dir, "root.pub", rp)
	trRaw, _ := data.TrustedRoot(t, "public-good.json").MarshalJSON()
	trPath := writeFile(t, dir, "trusted_root.json", trRaw)
	notRoot := writeFile(t, dir, "signing-as-root.pub", sp)

	cosign := profile.Profile{
		Signing: profile.Signing{Provider: profile.SigningCosignKey},
		Verify:  profile.Verify{Mode: profile.VerifyBoth, PublicKeys: []string{pubPath}},
		Channel: profile.Channel{
			Manifest: manifest,
			Anchors:  []profile.Anchor{{Name: "org", PublicKey: rootPath}},
		},
	}
	pol, err := verify.FromProfile(ctx, cosign, "r", "", nil)
	if err != nil || pol.Keys == nil || pol.Channel == nil || len(pol.Anchors) != 1 ||
		pol.Repository != "r" {
		t.Fatalf("%v %+v", err, pol)
	}
	if pol, err := verify.FromProfile(
		ctx,
		cosign,
		"r",
		profile.VerifyNone,
		nil,
	); err != nil ||
		pol.Keys != nil {
		t.Fatal("none override")
	}
	gh := profile.Profile{
		Signing: profile.Signing{Provider: profile.SigningGitHubAttestation},
		Verify: profile.Verify{
			Mode:     profile.VerifySignature,
			Identity: &profile.Identity{Issuer: "i", SubjectRegexp: ".*", TrustedRoot: trPath},
		},
	}
	if pol, err := verify.FromProfile(ctx, gh, "r", "", nil); err != nil || pol.Identity == nil {
		t.Fatalf("attestation: %v", err)
	}
	bad := []profile.Profile{
		{
			Signing: profile.Signing{Provider: profile.SigningCosignKey},
			Verify: profile.Verify{
				Mode:       profile.VerifySignature,
				PublicKeys: []string{filepath.Join(dir, "none.pub")},
			},
		},
		{
			Signing: profile.Signing{Provider: profile.SigningGitHubAttestation},
			Verify: profile.Verify{
				Mode:     profile.VerifySignature,
				Identity: &profile.Identity{Issuer: "i"},
			},
		},
		{
			Signing: profile.Signing{Provider: profile.SigningGitHubAttestation},
			Verify: profile.Verify{
				Mode:     profile.VerifySignature,
				Identity: &profile.Identity{Issuer: "i", TrustedRoot: pubPath},
			},
		},
		{
			Signing: profile.Signing{Provider: profile.SigningNone},
			Verify:  profile.Verify{Mode: profile.VerifySignature},
		},
		{
			Verify: profile.Verify{Mode: profile.VerifyChannel},
			Channel: profile.Channel{
				Manifest: manifest,
				Anchors: []profile.Anchor{
					{Name: "a", PublicKey: filepath.Join(dir, "missing.pub")},
				},
			},
		},
		{
			Verify: profile.Verify{Mode: profile.VerifyChannel},
			Channel: profile.Channel{
				Manifest: manifest,
				Anchors:  []profile.Anchor{{Name: "a", PublicKey: notRoot}},
			},
		},
		{
			Verify:  profile.Verify{Mode: profile.VerifyChannel},
			Channel: profile.Channel{Anchors: []profile.Anchor{{Name: "a", PublicKey: rootPath}}},
		},
		{
			Verify: profile.Verify{Mode: profile.VerifyChannel},
			Channel: profile.Channel{
				Manifest: filepath.Join(dir, "missing.json"),
				Anchors:  []profile.Anchor{{Name: "a", PublicKey: rootPath}},
			},
		},
	}
	for i, p := range bad {
		if _, err := verify.FromProfile(ctx, p, "r", "", nil); !errors.Is(err, verify.ErrConfig) {
			t.Fatalf("case %d: %v", i, err)
		}
	}
}
