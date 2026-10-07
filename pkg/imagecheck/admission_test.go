package imagecheck

import (
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/weaveplatform/weaveplatform-oci/pkg/channel"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
	"github.com/weaveplatform/weaveplatform-oci/pkg/verify"
)

func signedPredicate(
	t *testing.T,
	authority *ca.VirtualSigstore,
	subject digest.Digest,
	kind string,
	predicate any,
) []byte {
	t.Helper()
	statement, err := json.Marshal(
		map[string]any{
			"_type": "https://in-toto.io/Statement/v1",
			"subject": []any{
				map[string]any{
					"name":   "candidate",
					"digest": map[string]string{"sha256": subject.Encoded()},
				},
			},
			"predicateType": kind,
			"predicate":     predicate,
		},
	)
	require(t, err)
	entity, err := authority.AttestAtTimeHashedRekordV2(
		"https://github.com/weaveplatform/weaveplatform-oci/.github/workflows/build.yml@refs/heads/main",
		"https://token.actions.githubusercontent.com",
		statement,
		time.Now(),
	)
	require(t, err)
	material, err := entity.VerificationContent()
	require(t, err)
	signature, err := entity.SignatureContent()
	require(t, err)
	entries, err := entity.TlogEntries()
	require(t, err)
	logs := []json.RawMessage{}
	for _, entry := range entries {
		data, e := protojson.Marshal(entry.TransparencyLogEntry())
		require(t, e)
		logs = append(logs, data)
	}
	timestamps, err := entity.Timestamps()
	require(t, err)
	stamps := []map[string]any{}
	for _, stamp := range timestamps {
		stamps = append(stamps, map[string]any{"signedTimestamp": stamp})
	}
	data, err := json.Marshal(map[string]any{
		"mediaType": "application/vnd.dev.sigstore.bundle.v0.3+json",
		"verificationMaterial": map[string]any{
			"certificate":               map[string]any{"rawBytes": material.Certificate().Raw},
			"tlogEntries":               logs,
			"timestampVerificationData": map[string]any{"rfc3161Timestamps": stamps},
		},
		"dsseEnvelope": signature.EnvelopeContent().RawEnvelope(),
	})
	require(t, err)
	return data
}

func admissionPolicy(t *testing.T, authority *ca.VirtualSigstore) AdmissionPolicy {
	t.Helper()
	root, pub, err := channel.GenerateKey(channel.RootKeyID)
	require(t, err)
	key, signing, err := channel.GenerateKey("signing")
	require(t, err)
	endorsement, err := channel.Endorse(root, signing)
	require(t, err)
	manifest, err := channel.New("stable", time.Now())
	require(t, err)
	sig, err := channel.Sign(key, channel.ManifestContext, manifest)
	require(t, err)
	anchor, err := channel.ParseAnchor("test", pub)
	require(t, err)
	id := &verify.Identity{
		Trusted: authority,
		Issuer:  "https://token.actions.githubusercontent.com",
		SubjectRegexp: "^" + regexp.QuoteMeta(
			"https://github.com/weaveplatform/weaveplatform-oci/.github/workflows/build.yml@refs/heads/main",
		) + "$",
	}
	return AdmissionPolicy{
		Build:      id,
		Acceptance: id,
		Registry:   "ghcr.io",
		Anchors:    []channel.Anchor{anchor},
		Channel: channel.Bundle{
			Manifest:      manifest,
			ManifestSig:   sig,
			SigningKey:    signing,
			SigningKeySig: endorsement,
		},
	}
}

func TestAdmissionAuthenticatesExactPredicateAndChannel(t *testing.T) {
	authority, err := ca.NewVirtualSigstore()
	require(t, err)
	r, image := currentFixture("linux", false, false)
	build := signedPredicate(
		t,
		authority,
		image.Root.Digest,
		verify.SLSAProvenanceV1,
		map[string]any{},
	)
	accepted := signedPredicate(t, authority, image.Root.Digest, AcceptancePredicate, r)
	p := admissionPolicy(t, authority)
	got, err := Admit(p, image, "r1", build, accepted)
	require(t, err)
	if got.Report.SchemaVersion != 3 || got.IndexDigest != image.Root.Digest.String() ||
		got.BuildSigner == "" ||
		got.AcceptanceSigner == "" {
		t.Fatal(got)
	}
	for _, mode := range []string{"no build policy", "no acceptance policy", "no registry", "bad image", "build signature", "acceptance signature", "predicate", "subject", "legacy", "failed", "channel", "parent"} {
		t.Run(mode, func(t *testing.T) {
			policy := p
			report, candidate := currentFixture("linux", false, false)
			b, a := build, accepted
			switch mode {
			case "no build policy":
				policy.Build = nil
			case "no acceptance policy":
				policy.Acceptance = nil
			case "no registry":
				policy.Registry = ""
			case "bad image":
				candidate.Children = nil
			case "build signature":
				b = []byte("bad")
			case "acceptance signature":
				a = []byte("bad")
			case "predicate":
				a = build
			case "subject":
				a = signedPredicate(
					t,
					authority,
					digest.FromString("other"),
					AcceptancePredicate,
					report,
				)
			case "legacy":
				report.SchemaVersion = 2
				a = signedPredicate(t, authority, image.Root.Digest, AcceptancePredicate, report)
			case "failed":
				report.Passed = false
				a = signedPredicate(t, authority, image.Root.Digest, AcceptancePredicate, report)
			case "channel":
				policy.Channel.ManifestSig = []byte("bad")
			case "parent":
				candidate.Children[0].Description.Config.Build.Base = &spec.BaseImage{
					Name:   "parent",
					Digest: digest.FromString("p").String(),
				}
			}
			if _, e := Admit(policy, candidate, "r1", b, a); e == nil {
				t.Fatal("admitted invalid evidence")
			}
		})
	}
}

func TestCurrentParentSelectionRejectsHistoricalAndAmbiguousEntries(t *testing.T) {
	_, image := fixture("linux", true)
	old, current := digest.FromString("old").String(), digest.FromString("current").String()
	image.Children[0].Description.Config.Build.Base = &spec.BaseImage{
		Name:   "ghcr.io/weave/base",
		Digest: old,
	}
	m := channel.Manifest{Images: []channel.Image{
		{
			Repository: "weave/base",
			Tag:        "old",
			Platforms:  []channel.Platform{{OS: "linux", Arch: "arm64", Digest: old}},
		},
		{
			Repository: "weave/base",
			Tag:        "current",
			Platforms:  []channel.Platform{{OS: "linux", Arch: "arm64", Digest: current}},
		},
	}}
	for _, tag := range []string{"", "absent", "current"} {
		if err := CheckCurrentParents(
			image,
			m,
			"ghcr.io",
			map[string]string{"weave/base": tag},
		); err == nil {
			t.Fatal("admitted stale/missing parent", tag)
		}
	}
	image.Children[0].Description.Config.Build.Base.Digest = current
	require(t, CheckCurrentParents(image, m, "ghcr.io", map[string]string{"weave/base": "current"}))
	image.Children = append(image.Children, image.Children[0])
	require(t, CheckCurrentParents(image, m, "ghcr.io", map[string]string{"weave/base": "current"}))
	m.Images = append(m.Images, m.Images[1])
	if err := CheckCurrentParents(
		image,
		m,
		"ghcr.io",
		map[string]string{"weave/base": "current"},
	); err == nil {
		t.Fatal("ambiguous parent accepted")
	}
}
