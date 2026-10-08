package imagecheck

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/memory"

	"github.com/weaveplatform/weaveplatform-oci/internal/testregistry"
	"github.com/weaveplatform/weaveplatform-oci/pkg/channel"
	"github.com/weaveplatform/weaveplatform-oci/pkg/client"
	"github.com/weaveplatform/weaveplatform-oci/pkg/profile"
	"github.com/weaveplatform/weaveplatform-oci/pkg/sign"
	"github.com/weaveplatform/weaveplatform-oci/pkg/verify"
)

func attachEvidence(t *testing.T, target oras.Target, subject ocispec.Descriptor, data []byte) {
	t.Helper()
	blob, err := oras.PushBytes(t.Context(), target, sign.BundleMediaType, data)
	require(t, err)
	_, err = oras.PackManifest(
		t.Context(),
		target,
		oras.PackManifestVersion1_1,
		sign.BundleMediaType,
		oras.PackManifestOptions{Subject: &subject, Layers: []ocispec.Descriptor{blob}},
	)
	require(t, err)
}

func TestAttachedAcceptanceAuthenticatesBeforeSelectingPredicate(t *testing.T) {
	authority, err := ca.NewVirtualSigstore()
	require(t, err)
	p := admissionPolicy(t, authority)
	report, inspection := currentFixture("linux", false, false)
	build := signedPredicate(
		t,
		authority,
		inspection.Root.Digest,
		verify.SLSAProvenanceV1,
		map[string]any{},
	)
	accepted := signedPredicate(t, authority, inspection.Root.Digest, AcceptancePredicate, report)
	for _, mode := range []string{"valid", "no policy", "no build", "no acceptance", "unrelated", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			policy := p
			store := memory.New()
			ctx := t.Context()
			// The referrer store does not need payload blobs to discover predecessors.
			attachEvidence(t, store, inspection.Root, []byte("bad-signature"))
			if mode != "no build" {
				attachEvidence(t, store, inspection.Root, build)
			}
			if mode != "no acceptance" && mode != "unrelated" {
				attachEvidence(t, store, inspection.Root, accepted)
			}
			if mode == "unrelated" {
				attachEvidence(
					t,
					store,
					inspection.Root,
					signedPredicate(
						t,
						authority,
						digest.FromString("other"),
						AcceptancePredicate,
						report,
					),
				)
			}
			if mode == "no policy" {
				policy.Acceptance = nil
			}
			if mode == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			_, err := AdmitAttached(ctx, policy, inspection, "r1", verify.StoreSource{Store: store})
			if (err == nil) != (mode == "valid") {
				t.Fatal(mode, err)
			}
		})
	}
}

func TestPublishedAdmissionCopiesAndAuthenticatesExactIndex(t *testing.T) {
	authority, err := ca.NewVirtualSigstore()
	require(t, err)
	candidate := candidate(t, "linux/arm64", false)
	report, err := Validate(
		t.Context(),
		candidate,
		func(_ context.Context, c Clone) (Boot, error) { return observed(c), nil },
	)
	require(t, err)
	for _, fallback := range []bool{false, true} {
		server := testregistry.New(testregistry.Options{NoReferrers: fallback})
		defer server.Close()
		prof := profile.Profile{Registry: profile.Registry{Host: server.Host, PlainHTTP: true}}
		c := client.New(prof, client.Options{})
		p := admissionPolicy(t, authority)
		p.Registry = server.Host
		ref, err := c.Parse(server.Host + "/images/base:candidate")
		require(t, err)
		_, err = c.Push(t.Context(), candidate.Store, "candidate", ref, client.PushOptions{})
		require(t, err)
		repo, err := c.Repository(ref.Registry, ref.Repository)
		require(t, err)
		attachEvidence(
			t,
			repo,
			candidate.Root,
			signedPredicate(
				t,
				authority,
				candidate.Root.Digest,
				verify.SLSAProvenanceV1,
				map[string]any{},
			),
		)
		attachEvidence(
			t,
			repo,
			candidate.Root,
			signedPredicate(t, authority, candidate.Root.Digest, AcceptancePredicate, report),
		)
		entry := channel.Image{
			Repository: "images/base",
			Tag:        "candidate",
			Digest:     candidate.Root.Digest.String(),
			Signature: &channel.Signer{
				Provider:      string(profile.SigningGitHubAttestation),
				Issuer:        p.Build.Issuer,
				SubjectRegexp: p.Build.SubjectRegexp,
			},
			Platforms: []channel.Platform{
				{
					OS:        "linux",
					Arch:      "arm64",
					OSVersion: report.Platforms["linux/arm64"][0].OSVersion,
					Digest:    report.PlatformDigests["linux/arm64"],
				},
			},
		}
		for _, mode := range []string{"valid", "nil-client", "registry", "digest", "signer", "reference", "tag", "moved", "layout", "missing-platform", "wrong-platform", "bad-acceptance-policy"} {
			t.Run(mode, func(t *testing.T) {
				policy := p
				cc := c
				e := entry
				e.Platforms = append([]channel.Platform(nil), entry.Platforms...)
				out := filepath.Join(t.TempDir(), "layout")
				switch mode {
				case "nil-client":
					cc = nil
				case "registry":
					policy.Registry = "elsewhere.invalid"
				case "digest":
					e.Digest = "latest"
				case "signer":
					e.Signature = nil
				case "reference":
					e.Tag = "invalid tag"
				case "tag":
					e.Tag = "absent"
				case "moved":
					e.Digest = digest.FromString("different").String()
				case "layout":
					require(t, os.WriteFile(out, []byte("not a layout"), 0o600))
				case "missing-platform":
					e.Platforms = nil
				case "wrong-platform":
					e.Platforms[0].OSVersion = "unknown"
				case "bad-acceptance-policy":
					bad := *policy.Acceptance
					bad.SubjectRegexp = "^wrong$"
					policy.Acceptance = &bad
				}
				got, err := AdmitPublished(t.Context(), policy, cc, e, out)
				if (err == nil) != (mode == "valid") {
					t.Fatal(mode, got, err)
				}
				if err == nil && got.IndexDigest != entry.Digest {
					t.Fatal(got)
				}
			})
		}
	}
}
