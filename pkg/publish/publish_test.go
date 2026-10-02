package publish_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/weaveplatform/weaveplatform-oci/internal/testbundle"
	"github.com/weaveplatform/weaveplatform-oci/internal/testregistry"
	"github.com/weaveplatform/weaveplatform-oci/pkg/channel"
	"github.com/weaveplatform/weaveplatform-oci/pkg/client"
	"github.com/weaveplatform/weaveplatform-oci/pkg/profile"
	"github.com/weaveplatform/weaveplatform-oci/pkg/publish"
	"github.com/weaveplatform/weaveplatform-oci/pkg/sign"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

func setup(
	t *testing.T,
	provider profile.SigningProvider,
	o testregistry.Options,
) (*client.Client, []string, *sign.Signer) {
	t.Helper()
	reg := testregistry.New(o)
	t.Cleanup(reg.Close)
	kind := profile.KindPrivate
	if provider != profile.SigningCosignKey {
		kind = profile.KindGitHub
	}
	p := profile.Profile{
		Name:     "t",
		Kind:     kind,
		Registry: profile.Registry{Host: reg.Host, Namespace: "weave-images", PlainHTTP: true},
		Signing: profile.Signing{
			Provider: provider,
		},
		Verify: profile.Verify{Mode: profile.VerifyNone},
	}
	c := client.New(
		p,
		client.Options{
			Credentials: func(context.Context, string) (auth.Credential, error) { return auth.EmptyCredential, nil },
		},
	)
	var dirs []string
	for _, arch := range []string{spec.ArchARM64, spec.ArchAMD64} {
		d := filepath.Join(t.TempDir(), arch)
		if err := testbundle.Write(
			d,
			testbundle.Options{OS: spec.OSLinux, Arch: arch, Seed: uint64(len(arch))},
		); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, d)
	}
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	s, _ := sign.New(k)
	return c, dirs, s
}

func TestPublishPrivateProfile(t *testing.T) {
	ctx := context.Background()
	c, dirs, s := setup(t, profile.SigningCosignKey, testregistry.Options{NoReferrers: true})
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	res, err := publish.Run(ctx, publish.Request{
		Client: c, Bundles: dirs, Repository: "ubuntu-24.04", Tag: "24.04-20260915-r1", Signer: s,
		WorkDir: t.TempDir(), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	p := res.Promotion
	if res.Signature == nil || p.Repository != "weave-images/ubuntu-24.04" || p.Tag != "24.04-20260915-r1" || p.Digest != res.Index.Digest.String() ||
		len(p.Platforms) != 2 ||
		p.Signature.Provider != "cosign-key" ||
		p.Signature.KeyID != s.KeyID() ||
		p.BuildDate != "2026-10-02T00:00:00Z" {
		t.Fatalf("promotion %+v", p)
	}
	// the promotion entry is accepted by channel.Promote
	base, _ := channel.New("org", now)
	if _, err := channel.Promote(base, p, now); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "promotion.json")
	if err := publish.WritePromotion(out, p); err != nil {
		t.Fatal(err)
	}
	var back channel.Image
	raw, _ := os.ReadFile(out)
	if json.Unmarshal(raw, &back) != nil || back.Digest != p.Digest {
		t.Fatal("promotion file")
	}
	if err := publish.WritePromotion(filepath.Join(out, "x"), p); err == nil {
		t.Fatal("write under a file succeeded")
	}
	// the build tag is immutable
	if _, err := publish.Run(
		ctx,
		publish.Request{
			Client:     c,
			Bundles:    dirs,
			Repository: "ubuntu-24.04",
			Tag:        "24.04-20260915-r1",
			Signer:     s,
		},
	); !errors.Is(
		err,
		client.ErrTagExists,
	) {
		t.Fatalf("republish: %v", err)
	}
}

func TestPublishGitHubProfileLeavesSigningToTheWorkflow(t *testing.T) {
	c, dirs, _ := setup(t, profile.SigningGitHubAttestation, testregistry.Options{})
	res, err := publish.Run(
		context.Background(),
		publish.Request{Client: c, Bundles: dirs[:1], Repository: "ubuntu-24.04", Tag: "r1"},
	)
	if err != nil || res.Signature != nil ||
		res.Promotion.Signature.Provider != "github-attestation" {
		t.Fatalf("%v %+v", err, res)
	}
	c2, dirs2, _ := setup(t, profile.SigningNone, testregistry.Options{})
	res, err = publish.Run(
		context.Background(),
		publish.Request{Client: c2, Bundles: dirs2[:1], Repository: "x", Tag: "r1"},
	)
	if err != nil || res.Promotion.Signature != nil {
		t.Fatalf("%v %+v", err, res.Promotion)
	}
}

func TestPublishRefusesBadRequests(t *testing.T) {
	ctx := context.Background()
	c, dirs, s := setup(t, profile.SigningCosignKey, testregistry.Options{})
	cases := map[string]publish.Request{
		"no client":   {Bundles: dirs, Repository: "r", Tag: "t"},
		"no bundles":  {Client: c, Repository: "r", Tag: "t"},
		"channel tag": {Client: c, Bundles: dirs, Repository: "r", Tag: "stable", Signer: s},
		"no signer":   {Client: c, Bundles: dirs, Repository: "r", Tag: "t"},
		"bad repo":    {Client: c, Bundles: dirs, Repository: "UPPER", Tag: "t", Signer: s},
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := publish.Run(ctx, r); !errors.Is(err, publish.ErrRequest) {
				t.Fatalf("want ErrRequest, got %v", err)
			}
		})
	}
	bad := filepath.Join(t.TempDir(), "missing")
	if _, err := publish.Run(
		ctx,
		publish.Request{Client: c, Bundles: []string{bad}, Repository: "r", Tag: "t", Signer: s},
	); err == nil {
		t.Fatal("missing bundle published")
	}
	mixed := filepath.Join(t.TempDir(), "win")
	_ = testbundle.Write(mixed, testbundle.Options{OS: spec.OSWindows, Arch: spec.ArchAMD64})
	if _, err := publish.Run(
		ctx,
		publish.Request{
			Client:     c,
			Bundles:    []string{dirs[0], mixed},
			Repository: "r",
			Tag:        "t",
			Signer:     s,
		},
	); !errors.Is(
		err,
		spec.ErrInvalid,
	) {
		t.Fatalf("mixed index published: %v", err)
	}
	file := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(file, nil, 0o600)
	if _, err := publish.Run(
		ctx,
		publish.Request{
			Client:     c,
			Bundles:    dirs,
			Repository: "r",
			Tag:        "t",
			Signer:     s,
			WorkDir:    file,
		},
	); err == nil {
		t.Fatal("work dir under a file accepted")
	}
	// an unreachable registry fails at push
	dead := client.New(
		profile.Profile{
			Registry: profile.Registry{Host: "127.0.0.1:1", PlainHTTP: true},
			Signing:  profile.Signing{Provider: profile.SigningNone},
		},
		client.Options{},
	)
	if _, err := publish.Run(
		ctx,
		publish.Request{Client: dead, Bundles: dirs[:1], Repository: "r", Tag: "t"},
	); err == nil {
		t.Fatal("unreachable registry accepted")
	}
}
