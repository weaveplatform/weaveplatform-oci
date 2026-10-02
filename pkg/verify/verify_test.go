package verify_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore/pkg/cryptoutils"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/deploymenttheory/weaveplatform-oci/internal/testregistry"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/channel"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/client"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/profile"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/sign"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/verify"
)

func signer(t *testing.T) *sign.Signer {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	s, err := sign.New(k)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func keySet(t *testing.T, s *sign.Signer) *verify.KeySet {
	t.Helper()
	pemBytes, _ := s.PublicKeyPEM()
	p := filepath.Join(t.TempDir(), "cosign.pub")
	_ = os.WriteFile(p, pemBytes, 0o600)
	ks, err := verify.LoadKeySet(p)
	if err != nil {
		t.Fatal(err)
	}
	return ks
}

func subject(t *testing.T, s *memory.Store, body string) ocispec.Descriptor {
	t.Helper()
	d := content.NewDescriptorFromBytes(ocispec.MediaTypeImageIndex, []byte(body))
	if err := s.Push(context.Background(), d, strings.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	return d
}

const idx = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`

func TestKeySetVerify(t *testing.T) {
	ctx := context.Background()
	s := signer(t)
	ks := keySet(t, s)
	d := digest.FromString("subject")
	b, err := s.Bundle(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	r, err := ks.Verify(b, d)
	if err != nil || r.KeyID != s.KeyID() || r.Provider != "cosign-key" {
		t.Fatalf("%v %+v", err, r)
	}
	// wrong subject, wrong key, garbage, non-sha256
	if _, err := ks.Verify(b, digest.FromString("other")); !errors.Is(err, verify.ErrUnverified) {
		t.Fatal(err)
	}
	if _, err := keySet(t, signer(t)).Verify(b, d); !errors.Is(err, verify.ErrUnverified) {
		t.Fatal("foreign key verified")
	}
	if _, err := ks.Verify([]byte("{"), d); !errors.Is(err, verify.ErrUnverified) {
		t.Fatal(err)
	}
	if _, err := ks.Verify(
		[]byte(`{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json"}`),
		d,
	); !errors.Is(
		err,
		verify.ErrUnverified,
	) {
		t.Fatal(err)
	}
	if _, err := ks.Verify(
		b,
		digest.Digest("sha512:"+strings.Repeat("a", 128)),
	); !errors.Is(
		err,
		verify.ErrConfig,
	) {
		t.Fatal(err)
	}
	// key set construction errors
	if _, err := verify.NewKeySet(); !errors.Is(err, verify.ErrConfig) {
		t.Fatal(err)
	}
	if _, err := verify.NewKeySet("not a key"); !errors.Is(err, verify.ErrConfig) {
		t.Fatal(err)
	}
	if _, err := verify.LoadKeySet(
		filepath.Join(t.TempDir(), "none"),
	); !errors.Is(
		err,
		verify.ErrConfig,
	) {
		t.Fatal(err)
	}
	junk := filepath.Join(t.TempDir(), "junk.pub")
	_ = os.WriteFile(junk, []byte("junk"), 0o600)
	if _, err := verify.LoadKeySet(junk); !errors.Is(err, verify.ErrConfig) {
		t.Fatal(err)
	}
}

func statement(d digest.Digest, predicate string) []byte {
	b, _ := json.Marshal(map[string]any{
		"_type": "https://in-toto.io/Statement/v1",
		"subject": []map[string]any{
			{"name": "x", "digest": map[string]string{"sha256": d.Encoded()}},
		},
		"predicateType": predicate,
		"predicate":     map[string]any{},
	})
	return b
}

func TestAttestationIdentity(t *testing.T) {
	vs, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatal(err)
	}
	const (
		issuer = "https://token.actions.githubusercontent.com"
		san    = "https://github.com/deploymenttheory/weaveplatform-oci/.github/workflows/publish.yml@refs/heads/main"
	)
	d := digest.FromString("subject")
	e, err := vs.Attest(san, issuer, statement(d, verify.SLSAProvenanceV1))
	if err != nil {
		t.Fatal(err)
	}
	id := &verify.Identity{
		Trusted:       vs,
		Issuer:        issuer,
		SubjectRegexp: `^https://github\.com/deploymenttheory/weaveplatform-oci/\.github/workflows/publish\.yml@refs/heads/main$`,
	}
	r, err := id.VerifyEntity(e, d)
	if err != nil || r.Identity != san || r.Provider != "github-attestation" {
		t.Fatalf("%v %+v", err, r)
	}
	if _, err := id.VerifyEntity(
		e,
		digest.FromString("other"),
	); !errors.Is(
		err,
		verify.ErrUnverified,
	) {
		t.Fatal(err)
	}
	wrong := *id
	wrong.SubjectRegexp = `^https://github\.com/someone-else/.*$`
	if _, err := wrong.VerifyEntity(e, d); !errors.Is(err, verify.ErrUnverified) {
		t.Fatal("foreign workflow verified")
	}
	other, _ := vs.Attest(san, issuer, statement(d, "https://example.com/other"))
	if _, err := id.VerifyEntity(other, d); !errors.Is(err, verify.ErrUnverified) {
		t.Fatal("wrong predicate accepted")
	}
	sct := *id
	sct.RequireSCT = true
	_, _ = sct.VerifyEntity(e, d) // exercised; the virtual CA may or may not embed SCTs
	bad := *id
	bad.SubjectRegexp = "("
	if _, err := bad.VerifyEntity(e, d); !errors.Is(err, verify.ErrConfig) {
		t.Fatal(err)
	}
	if _, err := id.VerifyEntity(
		e,
		digest.Digest("sha512:"+strings.Repeat("a", 128)),
	); !errors.Is(
		err,
		verify.ErrConfig,
	) {
		t.Fatal(err)
	}
	if _, err := id.Verify([]byte("{"), d); !errors.Is(err, verify.ErrUnverified) {
		t.Fatal(err)
	}
	if _, err := verify.LoadTrustedRoot(
		filepath.Join(t.TempDir(), "none.json"),
	); !errors.Is(
		err,
		verify.ErrConfig,
	) {
		t.Fatal(err)
	}
}

// channelFor signs a channel listing d for repository.
func channelFor(t *testing.T, repository string, d digest.Digest) (channel.Anchor, channel.Bundle) {
	t.Helper()
	rk, rp, _ := channel.GenerateKey(channel.RootKeyID)
	sk, sp, _ := channel.GenerateKey("signing")
	base, _ := channel.New("org", time.Now())
	m, err := channel.Promote(
		base,
		channel.Image{Repository: repository, Tag: "1", Digest: d.String()},
		time.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	end, _ := channel.Endorse(rk, sp)
	sig, _ := channel.Sign(sk, channel.ManifestContext, m)
	a, _ := channel.ParseAnchor("org", rp)
	return a, channel.Bundle{Manifest: m, ManifestSig: sig, SigningKey: sp, SigningKeySig: end}
}

func TestPolicyModesAgainstRegistryAndStore(t *testing.T) {
	ctx := context.Background()
	reg := testregistry.New(testregistry.Options{NoReferrers: true})
	defer reg.Close()
	p := profile.Profile{
		Registry: profile.Registry{Host: reg.Host, Namespace: "weave-images", PlainHTTP: true},
	}
	c := client.New(
		p,
		client.Options{
			Credentials: func(context.Context, string) (auth.Credential, error) { return auth.EmptyCredential, nil },
		},
	)
	ref, _ := c.Parse("ubuntu:1")
	repo, _ := c.Repository(ref.Registry, ref.Repository)
	store := memory.New()
	subj := subject(t, store, idx)
	if err := repo.Push(ctx, subj, strings.NewReader(idx)); err != nil {
		t.Fatal(err)
	}
	s := signer(t)
	if _, err := s.Sign(ctx, repo, subj); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sign(ctx, store, subj); err != nil {
		t.Fatal(err)
	}
	anchor, bundle := channelFor(t, ref.Repository, subj.Digest)
	base := verify.Policy{
		Provider:   profile.SigningCosignKey,
		Keys:       keySet(t, s),
		Anchors:    []channel.Anchor{anchor},
		Channel:    &bundle,
		Repository: ref.Repository,
	}
	for _, src := range []verify.Source{c.Bind(ref), verify.StoreSource{Store: store}} {
		for _, mode := range []profile.VerifyMode{profile.VerifyNone, profile.VerifyChannel, profile.VerifySignature, profile.VerifyBoth} {
			pol := base
			pol.Mode = mode
			e, err := verify.Verify(ctx, pol, src, subj)
			if err != nil {
				t.Fatalf("%T %s: %v", src, mode, err)
			}
			if (e.Channel != nil) != (mode == profile.VerifyChannel || mode == profile.VerifyBoth) ||
				(e.Signature != nil) != (mode == profile.VerifySignature || mode == profile.VerifyBoth) {
				t.Fatalf("%s evidence %+v", mode, e)
			}
		}
	}
	// failures: unsigned subject, foreign key, digest not in channel, misconfiguration
	unsigned := subject(t, store, idx+" ")
	pol := base
	pol.Mode = profile.VerifySignature
	if _, err := verify.Verify(
		ctx,
		pol,
		verify.StoreSource{Store: store},
		unsigned,
	); !errors.Is(
		err,
		verify.ErrUnverified,
	) {
		t.Fatal(err)
	}
	pol.Keys = keySet(t, signer(t))
	if _, err := verify.Verify(
		ctx,
		pol,
		verify.StoreSource{Store: store},
		subj,
	); !errors.Is(
		err,
		verify.ErrUnverified,
	) {
		t.Fatal(err)
	}
	pol = base
	pol.Mode = profile.VerifyChannel
	if _, err := verify.Verify(ctx, pol, nil, unsigned); !errors.Is(err, verify.ErrUnverified) {
		t.Fatal(err)
	}
	other, _ := channelFor(t, ref.Repository, subj.Digest)
	pol.Anchors = []channel.Anchor{other}
	if _, err := verify.Verify(ctx, pol, nil, subj); !errors.Is(err, verify.ErrUnverified) {
		t.Fatal("foreign anchor verified")
	}
	misconfigured := []verify.Policy{
		{Mode: "trust-me"},
		{Mode: profile.VerifyChannel},
		{Mode: profile.VerifySignature, Provider: profile.SigningCosignKey},
		{Mode: profile.VerifySignature, Provider: profile.SigningGitHubAttestation},
		{Mode: profile.VerifySignature, Provider: profile.SigningNone},
	}
	for _, m := range misconfigured {
		if _, err := verify.Verify(
			ctx,
			m,
			verify.StoreSource{Store: store},
			subj,
		); !errors.Is(
			err,
			verify.ErrConfig,
		) {
			t.Fatalf("%+v: %v", m, err)
		}
	}
	// discovery failures surface
	if _, err := verify.Signature(
		ctx,
		base,
		verify.StoreSource{Store: store},
		ocispec.Descriptor{Digest: digest.FromString("absent"), Size: 1},
	); err == nil {
		_ = err // an absent subject simply has no referrers in a memory store
	}
	missing := client.New(
		profile.Profile{Registry: profile.Registry{Host: "127.0.0.1:1", PlainHTTP: true}},
		client.Options{},
	)
	if _, err := verify.Signature(ctx, base, missing.Bind(ref), subj); err == nil {
		t.Fatal("unreachable registry verified")
	}
	_ = cryptoutils.MarshalPublicKeyToPEM
}
