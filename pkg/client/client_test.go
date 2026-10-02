package client_test

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/weaveplatform/weaveplatform-oci/internal/testbundle"
	"github.com/weaveplatform/weaveplatform-oci/internal/testregistry"
	"github.com/weaveplatform/weaveplatform-oci/pkg/chunk"
	"github.com/weaveplatform/weaveplatform-oci/pkg/client"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
	"github.com/weaveplatform/weaveplatform-oci/pkg/profile"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

func prof(host string, mirrors ...string) profile.Profile {
	p := profile.Profile{
		Name:     "t",
		Kind:     profile.KindPrivate,
		Registry: profile.Registry{Host: host, Namespace: "weave-images", PlainHTTP: true},
		Signing: profile.Signing{
			Provider: profile.SigningCosignKey,
		},
		Verify: profile.Verify{Mode: profile.VerifyNone},
	}
	for _, m := range mirrors {
		p.Mirrors = append(p.Mirrors, profile.Registry{Host: m, PlainHTTP: true})
	}
	return p
}

func static(user, pass string) auth.CredentialFunc {
	return func(context.Context, string) (auth.Credential, error) {
		return auth.Credential{Username: user, Password: pass}, nil
	}
}

// packed returns a memory store holding a contract index tagged "src".
func packed(t *testing.T) (*memory.Store, ocispec.Descriptor) {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "b")
	if err := testbundle.Write(
		dir,
		testbundle.Options{OS: spec.OSLinux, Arch: spec.ArchAMD64, ExtraDisk: true},
	); err != nil {
		t.Fatal(err)
	}
	b, err := pack.LoadBundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := memory.New()
	m, err := pack.Manifest(ctx, b, s, chunk.Options{})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := pack.Index(ctx, s, []ocispec.Descriptor{m}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Tag(ctx, idx, "src"); err != nil {
		t.Fatal(err)
	}
	return s, idx
}

func TestParse(t *testing.T) {
	c := client.New(prof("reg.example:5000", "mirror.example"), client.Options{})
	cases := map[string]client.Reference{
		"ubuntu-24.04:24.04-r1":         {Repository: "weave-images/ubuntu-24.04", Tag: "24.04-r1"},
		"reg.example:5000/other/repo:t": {Repository: "other/repo", Tag: "t"},
		"ubuntu:t@sha256:" + hex64: {
			Repository: "weave-images/ubuntu",
			Tag:        "t",
			Digest:     "sha256:" + hex64,
		},
		"ubuntu@sha256:" + hex64: {
			Repository: "weave-images/ubuntu",
			Digest:     "sha256:" + hex64,
		},
		"nested/path/name:1": {Repository: "weave-images/nested/path/name", Tag: "1"},
	}
	for in, want := range cases {
		got, err := c.Parse(in)
		if err != nil || got.Repository != want.Repository || got.Tag != want.Tag ||
			got.Digest != want.Digest {
			t.Errorf("%s: %+v %v", in, got, err)
		}
	}
	r, _ := c.Parse("ubuntu:t@sha256:" + hex64)
	if r.Ref() != "sha256:"+hex64 ||
		r.String() != "reg.example:5000/weave-images/ubuntu:t@sha256:"+hex64 {
		t.Fatalf("%s %s", r.Ref(), r)
	}
	for _, bad := range []string{"", "UPPER:t", "ubuntu", "ubuntu:bad tag", "ubuntu@sha256:short", "mirror.example/x:t"} {
		if _, err := c.Parse(bad); !errors.Is(err, client.ErrReference) {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
	if c.Profile().Name != "t" {
		t.Fatal("profile")
	}
}

const hex64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestPushPullTagsReferrers(t *testing.T) {
	ctx := context.Background()
	for _, noRef := range []bool{false, true} {
		reg := testregistry.New(
			testregistry.Options{Username: "u", Password: "p", NoReferrers: noRef, FailFirst: 2},
		)
		defer reg.Close()
		c := client.New(
			prof(reg.Host),
			client.Options{Credentials: static("u", "p"), Concurrency: 2, UserAgent: "test"},
		)
		src, idx := packed(t)
		ref, err := c.Parse("ubuntu-24.04:24.04-r1")
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.Push(ctx, src, "src", ref, client.PushOptions{})
		if err != nil || got.Digest != idx.Digest {
			t.Fatalf("push: %v", err)
		}
		// the build tag is immutable by policy
		if _, err := c.Push(
			ctx,
			src,
			"src",
			ref,
			client.PushOptions{},
		); !errors.Is(
			err,
			client.ErrTagExists,
		) {
			t.Fatalf("second push: %v", err)
		}
		if _, err := c.Push(
			ctx,
			src,
			"src",
			ref,
			client.PushOptions{AllowExisting: true},
		); err != nil {
			t.Fatal(err)
		}
		if err := c.Tag(ctx, ref, idx, "stable"); err != nil {
			t.Fatal(err)
		}
		tags, err := c.Tags(ctx, ref)
		if err != nil || len(tags) != 2 {
			t.Fatalf("tags %v %v", tags, err)
		}
		d, srcInfo, err := c.Resolve(ctx, ref)
		if err != nil || d.Digest != idx.Digest || srcInfo.Mirror {
			t.Fatalf("resolve %v", err)
		}
		dst := memory.New()
		root, _, err := c.Pull(ctx, ref, dst, client.PullOptions{Referrers: true})
		if err != nil || root.Digest != idx.Digest {
			t.Fatalf("pull %v", err)
		}
		if n, err := c.Size(ctx, ref, dst); err != nil || n != 0 {
			t.Fatalf("size after pull %d %v", n, err)
		}
		if n, err := c.Size(ctx, ref, memory.New()); err != nil || n <= idx.Size {
			t.Fatalf("size into an empty store %d %v", n, err)
		}
		if raw, err := c.FetchAll(ctx, ref, idx); err != nil || len(raw) != int(idx.Size) {
			t.Fatalf("fetch %v", err)
		}
		// attach a referrer and discover it through the API or the fallback tag
		repo, _ := c.Repository(ref.Registry, ref.Repository)
		sig, err := oras.PackManifest(
			ctx,
			repo,
			oras.PackManifestVersion1_1,
			"application/vnd.test.sig",
			oras.PackManifestOptions{Subject: &idx},
		)
		if err != nil {
			t.Fatal(err)
		}
		refs, err := c.Referrers(ctx, ref, idx, "application/vnd.test.sig")
		if err != nil || len(refs) != 1 || refs[0].Digest != sig.Digest {
			t.Fatalf("noReferrers=%v referrers %v %v", noRef, refs, err)
		}
	}
}

func TestMirrorsAreReadFirstAndFallBack(t *testing.T) {
	ctx := context.Background()
	canon := testregistry.New(testregistry.Options{})
	defer canon.Close()
	mirror := testregistry.New(testregistry.Options{})
	defer mirror.Close()
	down := "127.0.0.1:1" // nothing listens here
	src, idx := packed(t)
	// populate both: canonical via the client, mirror directly
	cc := client.New(prof(canon.Host), client.Options{Credentials: static("", "")})
	ref, _ := cc.Parse("ubuntu:1")
	if _, err := cc.Push(ctx, src, "src", ref, client.PushOptions{}); err != nil {
		t.Fatal(err)
	}
	mc := client.New(prof(mirror.Host), client.Options{Credentials: static("", "")})
	mref, _ := mc.Parse("ubuntu:1")
	if _, err := mc.Push(ctx, src, "src", mref, client.PushOptions{}); err != nil {
		t.Fatal(err)
	}
	c := client.New(
		prof(canon.Host, down, mirror.Host),
		client.Options{Credentials: static("", "")},
	)
	before := canon.Requests()
	root, s, err := c.Pull(ctx, ref, memory.New(), client.PullOptions{Tag: "x"})
	if err != nil || root.Digest != idx.Digest || !s.Mirror || s.Registry.Host != mirror.Host {
		t.Fatalf("pull %+v %v", s, err)
	}
	if canon.Requests() != before {
		t.Fatal("canonical registry contacted although a mirror served the pull")
	}
	if _, s, err := c.Resolve(ctx, ref); err != nil || !s.Mirror {
		t.Fatal(err)
	}
	// the mirror lacks a tag the canonical has: fall through
	if err := cc.Tag(ctx, ref, idx, "only-canonical"); err != nil {
		t.Fatal(err)
	}
	r2, _ := c.Parse("ubuntu:only-canonical")
	if _, s, err := c.Resolve(ctx, r2); err != nil || s.Mirror {
		t.Fatalf("fallback %+v %v", s, err)
	}
	if _, err := c.Tags(ctx, r2); err != nil {
		t.Fatal(err)
	}
	// nothing anywhere
	r3, _ := c.Parse("missing:1")
	if _, _, err := c.Resolve(ctx, r3); !errors.Is(err, client.ErrUnavailable) {
		t.Fatal(err)
	}
	if _, _, err := c.Pull(
		ctx,
		r3,
		memory.New(),
		client.PullOptions{},
	); !errors.Is(
		err,
		client.ErrUnavailable,
	) {
		t.Fatal(err)
	}
	if _, err := c.Size(ctx, r3, memory.New()); !errors.Is(err, client.ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := c.FetchAll(
		ctx,
		r3,
		ocispec.Descriptor{Digest: "sha256:" + hex64, Size: 1},
	); !errors.Is(
		err,
		client.ErrUnavailable,
	) {
		t.Fatal(err)
	}
	if _, err := c.Referrers(
		ctx,
		r3,
		ocispec.Descriptor{MediaType: spec.MediaTypeIndex, Digest: "sha256:" + hex64, Size: 1},
		"",
	); !errors.Is(
		err,
		client.ErrUnavailable,
	) {
		t.Fatal(err)
	}
	dead := client.New(prof(down), client.Options{Credentials: static("", "")})
	if _, err := dead.Tags(ctx, r3); !errors.Is(err, client.ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestPushErrors(t *testing.T) {
	ctx := context.Background()
	reg := testregistry.New(testregistry.Options{Username: "u", Password: "p"})
	defer reg.Close()
	src, _ := packed(t)
	c := client.New(prof(reg.Host), client.Options{Credentials: static("u", "wrong")})
	ref, _ := c.Parse("ubuntu:1")
	if _, err := c.Push(ctx, src, "src", ref, client.PushOptions{}); err == nil {
		t.Fatal("bad credentials accepted")
	}
	ok := client.New(prof(reg.Host), client.Options{Credentials: static("u", "p")})
	if _, err := ok.Push(ctx, src, "missing-src-tag", ref, client.PushOptions{}); err == nil {
		t.Fatal("missing source tag accepted")
	}
	digestOnly, _ := ok.Parse("ubuntu@sha256:" + hex64)
	if _, err := ok.Push(
		ctx,
		src,
		"src",
		digestOnly,
		client.PushOptions{},
	); !errors.Is(
		err,
		client.ErrReference,
	) {
		t.Fatal(err)
	}
	if err := ok.Tag(
		ctx,
		ref,
		ocispec.Descriptor{MediaType: spec.MediaTypeIndex, Digest: "sha256:" + hex64, Size: 3},
		"x",
	); err == nil {
		t.Fatal("tagging an absent manifest succeeded")
	}
	bad := client.Reference{
		Registry:   profile.Registry{Host: reg.Host, PlainHTTP: true},
		Repository: "UPPER",
		Tag:        "t",
	}
	if _, err := ok.Push(
		ctx,
		src,
		"src",
		bad,
		client.PushOptions{},
	); !errors.Is(
		err,
		client.ErrReference,
	) {
		t.Fatal(err)
	}
	if err := ok.Tag(ctx, bad, ocispec.Descriptor{}, "x"); !errors.Is(err, client.ErrReference) {
		t.Fatal(err)
	}
	for _, f := range []func() error{
		func() error { _, _, e := ok.Resolve(ctx, bad); return e },
		func() error { _, _, e := ok.Pull(ctx, bad, memory.New(), client.PullOptions{}); return e },
		func() error { _, e := ok.Tags(ctx, bad); return e },
		func() error { _, e := ok.Referrers(ctx, bad, ocispec.Descriptor{}, ""); return e },
		func() error { _, e := ok.FetchAll(ctx, bad, ocispec.Descriptor{}); return e },
	} {
		if err := f(); !errors.Is(err, client.ErrReference) {
			t.Fatal(err)
		}
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := ok.Resolve(cctx, ref); err == nil {
		t.Fatal("cancelled resolve succeeded")
	}
	if _, _, err := ok.Pull(
		cctx,
		ref,
		memory.New(),
		client.PullOptions{Referrers: true},
	); err == nil {
		t.Fatal("cancelled pull succeeded")
	}
}

func TestCredentialsAndTLS(t *testing.T) {
	ctx := context.Background()
	t.Setenv(client.EnvUsername, "envuser")
	t.Setenv(client.EnvPassword, "envpass")
	t.Setenv(client.EnvHostname, "")
	cred, err := client.DefaultCredentials()(ctx, "any.example")
	if err != nil || cred.Username != "envuser" || cred.Password != "envpass" {
		t.Fatalf("%+v %v", cred, err)
	}
	t.Setenv(client.EnvHostname, "only.example")
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	cred, err = client.DefaultCredentials()(ctx, "other.example")
	if err != nil || cred.Username != "" {
		t.Fatalf("scoped env leaked: %+v %v", cred, err)
	}
	// an insecure mirror clones the transport with TLS verification off
	p := prof("reg.example")
	p.Registry.InsecureSkipTLSVerify = true
	p.Registry.PlainHTTP = false
	c := client.New(p, client.Options{Transport: http.DefaultTransport.(*http.Transport).Clone()})
	if _, err := c.Repository(p.Registry, "x"); err != nil {
		t.Fatal(err)
	}
}
