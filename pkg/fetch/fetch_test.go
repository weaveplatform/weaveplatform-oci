package fetch_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/weaveplatform/weaveplatform-oci/internal/testbundle"
	"github.com/weaveplatform/weaveplatform-oci/internal/testregistry"
	"github.com/weaveplatform/weaveplatform-oci/pkg/cache"
	"github.com/weaveplatform/weaveplatform-oci/pkg/chunk"
	"github.com/weaveplatform/weaveplatform-oci/pkg/client"
	"github.com/weaveplatform/weaveplatform-oci/pkg/fetch"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
	"github.com/weaveplatform/weaveplatform-oci/pkg/profile"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

type env struct {
	reg   *testregistry.Server
	c     *client.Client
	store *cache.Store
}

func newEnv(t *testing.T, mode profile.VerifyMode) env {
	t.Helper()
	reg := testregistry.New(testregistry.Options{})
	t.Cleanup(reg.Close)
	p := profile.Profile{
		Registry: profile.Registry{Host: reg.Host, Namespace: "weave-images", PlainHTTP: true},
		Verify:   profile.Verify{Mode: mode},
	}
	if mode == profile.VerifySignature {
		p.Signing = profile.Signing{Provider: profile.SigningCosignKey}
	}
	c := client.New(p, client.Options{
		Credentials: func(context.Context, string) (auth.Credential, error) { return auth.EmptyCredential, nil },
	})
	s, err := cache.Open(context.Background(), t.TempDir(), cache.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return env{reg, c, s}
}

// publish pushes an index of one bundle per platform, or a bare manifest
// when asManifest is set, as repo:tag.
func (e env) publish(
	t *testing.T,
	ref string,
	asManifest bool,
	platforms ...[2]string,
) ocispec.Descriptor {
	t.Helper()
	ctx := context.Background()
	s := memory.New()
	var kids []ocispec.Descriptor
	for i, p := range platforms {
		dir := filepath.Join(t.TempDir(), p[0]+p[1])
		if err := testbundle.Write(
			dir,
			testbundle.Options{OS: p[0], Arch: p[1], Seed: uint64(i + 1)},
		); err != nil {
			t.Fatal(err)
		}
		b, _ := pack.LoadBundle(dir)
		m, err := pack.Manifest(ctx, b, s, chunk.Options{})
		if err != nil {
			t.Fatal(err)
		}
		kids = append(kids, m)
	}
	root := kids[0]
	if !asManifest {
		var err error
		if root, err = pack.Index(ctx, s, kids, nil); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.Tag(ctx, root, "src")
	r, _ := e.c.Parse(ref)
	if _, err := e.c.Push(ctx, s, "src", r, client.PushOptions{}); err != nil {
		t.Fatal(err)
	}
	return root
}

var (
	linuxAMD = [2]string{spec.OSLinux, spec.ArchAMD64}
	linuxARM = [2]string{spec.OSLinux, spec.ArchARM64}
)

func TestPullVerifyPinUnpack(t *testing.T) {
	e := newEnv(t, profile.VerifyNone)
	root := e.publish(t, "ubuntu:24.04-r1", false, linuxAMD, linuxARM)
	ctx := context.Background()

	// cache only: no platform, nothing unpacked
	res, err := fetch.Pull(ctx, fetch.Request{Client: e.c, Cache: e.store, Ref: "ubuntu:24.04-r1"})
	if err != nil || res.Root.Digest != root.Digest || res.Unpacked != nil ||
		res.Manifest.Digest != "" {
		t.Fatalf("%+v %v", res, err)
	}
	if res.Policy.Mode != profile.VerifyNone || res.Reference.Repository != "weave-images/ubuntu" {
		t.Fatalf("policy %+v ref %+v", res.Policy, res.Reference)
	}

	// select, pin and unpack one platform
	to := filepath.Join(t.TempDir(), "vm")
	res, err = fetch.Pull(ctx, fetch.Request{
		Client: e.c, Cache: e.store, Ref: "ubuntu:24.04-r1",
		Platform: "linux/arm64", To: to, Pin: "vm-1",
	})
	if err != nil || res.Unpacked == nil || res.Manifest.Platform.Architecture != spec.ArchARM64 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(to, pack.BundleFileName)); err != nil {
		t.Fatal("bundle not written")
	}
	es := e.store.Entries()
	if len(es) != 1 || len(es[0].PinnedBy) != 1 || es[0].PinnedBy[0] != "vm-1" {
		t.Fatalf("pin: %+v", es)
	}
	// platform without To selects but does not unpack
	res, err = fetch.Pull(
		ctx,
		fetch.Request{Client: e.c, Cache: e.store, Ref: "ubuntu:24.04-r1", Platform: "linux/amd64"},
	)
	if err != nil || res.Unpacked != nil || res.Manifest.Platform.Architecture != spec.ArchAMD64 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestSinglePlatformAndBareManifest(t *testing.T) {
	e := newEnv(t, profile.VerifyNone)
	ctx := context.Background()
	e.publish(t, "one:1", false, linuxAMD)
	res, err := fetch.Pull(
		ctx,
		fetch.Request{
			Client: e.c,
			Cache:  e.store,
			Ref:    "one:1",
			To:     filepath.Join(t.TempDir(), "a"),
		},
	)
	if err != nil || res.Unpacked == nil {
		t.Fatalf("single-platform index without a platform: %v", err)
	}
	m := e.publish(t, "bare:1", true, linuxARM)
	res, err = fetch.Pull(
		ctx,
		fetch.Request{
			Client: e.c,
			Cache:  e.store,
			Ref:    "bare:1",
			To:     filepath.Join(t.TempDir(), "b"),
		},
	)
	if err != nil || res.Manifest.Digest != m.Digest {
		t.Fatalf("bare manifest: %+v %v", res, err)
	}
}

func TestPlatformErrors(t *testing.T) {
	e := newEnv(t, profile.VerifyNone)
	ctx := context.Background()
	e.publish(t, "multi:1", false, linuxAMD, linuxARM)
	for name, c := range map[string]struct {
		platform string
		usage    bool
	}{
		"ambiguous": {"", true},
		"malformed": {"linux", true},
		"missing":   {"windows/amd64", false},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := fetch.Pull(
				ctx,
				fetch.Request{
					Client:   e.c,
					Cache:    e.store,
					Ref:      "multi:1",
					Platform: c.platform,
					To:       t.TempDir(),
				},
			)
			if err == nil || errors.Is(err, fetch.ErrPlatform) != c.usage {
				t.Fatalf("%v (usage=%v)", err, c.usage)
			}
		})
	}
}

func TestRefusedImageIsNeitherPinnedNorUnpacked(t *testing.T) {
	// a profile that requires a cosign-key signature from a key nobody used
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(key.Public())
	pub := filepath.Join(t.TempDir(), "cosign.pub")
	_ = os.WriteFile(pub, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600)
	e := newEnv(t, profile.VerifyNone)
	p := e.c.Profile()
	p.Signing = profile.Signing{Provider: profile.SigningCosignKey}
	p.Verify = profile.Verify{Mode: profile.VerifySignature, PublicKeys: []string{pub}}
	c := client.New(p, client.Options{
		Credentials: func(context.Context, string) (auth.Credential, error) { return auth.EmptyCredential, nil },
	})
	e.publish(t, "unsigned:1", false, linuxAMD)
	to := filepath.Join(t.TempDir(), "vm")
	_, err := fetch.Pull(
		context.Background(),
		fetch.Request{Client: c, Cache: e.store, Ref: "unsigned:1", To: to, Pin: "vm"},
	)
	if err == nil {
		t.Fatal("an unsigned image was accepted")
	}
	if _, serr := os.Stat(to); serr == nil {
		t.Fatal("a refused image was unpacked")
	}
	es := e.store.Entries()
	if len(es) != 1 || len(es[0].PinnedBy) != 0 {
		t.Fatalf("a refused image was pinned or not cached: %+v", es)
	}
	// a policy the profile cannot build (no keys) is refused before pulling
	p.Verify.PublicKeys = nil
	c = client.New(p, client.Options{})
	if _, err := fetch.Pull(
		context.Background(),
		fetch.Request{Client: c, Cache: e.store, Ref: "unsigned:1"},
	); err == nil {
		t.Fatal("an unbuildable policy was accepted")
	}
}

func TestUnpackFailure(t *testing.T) {
	e := newEnv(t, profile.VerifyNone)
	e.publish(t, "one:1", false, linuxAMD)
	file := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(file, nil, 0o600)
	if _, err := fetch.Pull(context.Background(), fetch.Request{
		Client: e.c, Cache: e.store, Ref: "one:1", To: filepath.Join(file, "vm"),
	}); err == nil {
		t.Fatal("unpack into a path under a file succeeded")
	}
}

func TestRequestErrors(t *testing.T) {
	e := newEnv(t, profile.VerifyNone)
	ctx := context.Background()
	for name, r := range map[string]fetch.Request{
		"no client":     {Cache: e.store, Ref: "x:1"},
		"no cache":      {Client: e.c, Ref: "x:1"},
		"no reference":  {Client: e.c, Cache: e.store},
		"bad reference": {Client: e.c, Cache: e.store, Ref: "UPPER CASE::"},
	} {
		if _, err := fetch.Pull(ctx, r); !errors.Is(err, fetch.ErrRequest) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := fetch.Pull(
		ctx,
		fetch.Request{Client: e.c, Cache: e.store, Ref: "absent:1"},
	); err == nil {
		t.Fatal("pull of an absent image succeeded")
	}
}
