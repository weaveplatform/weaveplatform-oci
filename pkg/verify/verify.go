// Package verify decides whether an artifact may be used (decision 0006):
// cosign-key signatures and GitHub artifact attestations attached as OCI
// referrers, and membership in a verified channel manifest, combined by a
// verification mode (channel, signature, both or none). It never fetches
// over the network itself: callers hand it a Source (pkg/client provides
// one) and channel bundles (pkg/channel loads them).
package verify

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/deploymenttheory/weaveplatform-oci/pkg/channel"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/profile"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/sign"
)

var (
	// ErrUnverified reports that the required evidence was not found or did not verify.
	ErrUnverified = errors.New("artifact is not verified")
	// ErrConfig reports an unusable verification configuration.
	ErrConfig = errors.New("verification is misconfigured")
)

// Source lists and fetches referrers of a subject in one repository.
type Source interface {
	Referrers(
		ctx context.Context,
		subject ocispec.Descriptor,
		artifactType string,
	) ([]ocispec.Descriptor, error)
	FetchAll(ctx context.Context, d ocispec.Descriptor) ([]byte, error)
}

// bundles returns the Sigstore bundle bytes attached to subject: each
// referrer with the bundle artifactType and exactly one bundle layer, as
// cosign requires.
func bundles(ctx context.Context, src Source, subject ocispec.Descriptor) ([][]byte, error) {
	refs, err := src.Referrers(ctx, subject, sign.BundleMediaType)
	if err != nil {
		return nil, fmt.Errorf("referrers of %s: %w", subject.Digest, err)
	}
	var out [][]byte
	for _, r := range refs {
		raw, err := src.FetchAll(ctx, r)
		if err != nil {
			return nil, fmt.Errorf("referrer %s: %w", r.Digest, err)
		}
		m, err := decodeManifest(raw)
		if err != nil || len(m.Layers) != 1 ||
			!strings.HasPrefix(m.Layers[0].MediaType, "application/vnd.dev.sigstore.bundle") {
			continue
		}
		b, err := src.FetchAll(ctx, m.Layers[0])
		if err != nil {
			return nil, fmt.Errorf("bundle %s: %w", m.Layers[0].Digest, err)
		}
		out = append(out, b)
	}
	return out, nil
}

// Evidence records what verified.
type Evidence struct {
	Subject   digest.Digest
	Signature *SignatureResult
	Channel   *ChannelResult
}

// SignatureResult names the build-time signature that verified.
type SignatureResult struct {
	Provider string // cosign-key or github-attestation
	KeyID    string // cosign key hint
	Identity string // certificate subject for attestations
	Issuer   string
}

// ChannelResult names the channel entry that admitted the artifact.
type ChannelResult struct {
	Anchor   string
	Channel  string
	Sequence uint64
	Image    channel.Image
}

// Policy is what a consumer requires, derived from a deployment profile.
type Policy struct {
	Mode       profile.VerifyMode
	Provider   profile.SigningProvider
	Keys       *KeySet   // cosign-key
	Identity   *Identity // github-attestation
	Anchors    []channel.Anchor
	Channel    *channel.Bundle
	Repository string // repository path the channel entry must name
	Options    channel.Options
}

// Verify gathers the evidence the policy's mode requires for subject and
// fails closed when any of it is missing or invalid.
func Verify(
	ctx context.Context,
	p Policy,
	src Source,
	subject ocispec.Descriptor,
) (Evidence, error) {
	e := Evidence{Subject: subject.Digest}
	wantChannel := p.Mode == profile.VerifyChannel || p.Mode == profile.VerifyBoth
	wantSig := p.Mode == profile.VerifySignature || p.Mode == profile.VerifyBoth
	switch p.Mode {
	case profile.VerifyNone:
		return e, nil
	case profile.VerifyChannel, profile.VerifySignature, profile.VerifyBoth:
	default:
		return e, fmt.Errorf("%w: mode %q", ErrConfig, p.Mode)
	}
	if wantChannel {
		r, err := InChannel(p, subject.Digest)
		if err != nil {
			return e, err
		}
		e.Channel = r
	}
	if wantSig {
		r, err := Signature(ctx, p, src, subject)
		if err != nil {
			return e, err
		}
		e.Signature = r
	}
	return e, nil
}

// InChannel verifies the channel chain and finds subject in it.
func InChannel(p Policy, subject digest.Digest) (*ChannelResult, error) {
	if p.Channel == nil || len(p.Anchors) == 0 {
		return nil, fmt.Errorf(
			"%w: channel verification needs a channel bundle and anchors",
			ErrConfig,
		)
	}
	m, anchor, err := channel.Verify(p.Anchors, *p.Channel, p.Options)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnverified, err)
	}
	img, err := m.Image(p.Repository, subject)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnverified, err)
	}
	return &ChannelResult{
		Anchor:   anchor.Name,
		Channel:  m.Channel,
		Sequence: m.Sequence,
		Image:    *img,
	}, nil
}

// Signature finds a referrer bundle on subject that verifies under the
// policy's signing provider.
func Signature(
	ctx context.Context,
	p Policy,
	src Source,
	subject ocispec.Descriptor,
) (*SignatureResult, error) {
	bs, err := bundles(ctx, src, subject)
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, b := range bs {
		var r *SignatureResult
		switch p.Provider {
		case profile.SigningCosignKey:
			if p.Keys == nil {
				return nil, fmt.Errorf("%w: cosign-key verification needs public keys", ErrConfig)
			}
			r, err = p.Keys.Verify(b, subject.Digest)
		case profile.SigningGitHubAttestation:
			if p.Identity == nil {
				return nil, fmt.Errorf(
					"%w: attestation verification needs an identity and trusted root",
					ErrConfig,
				)
			}
			r, err = p.Identity.Verify(b, subject.Digest)
		default:
			return nil, fmt.Errorf(
				"%w: signing provider %q cannot be verified",
				ErrConfig,
				p.Provider,
			)
		}
		if err == nil {
			return r, nil
		}
		errs = append(errs, err)
	}
	return nil, fmt.Errorf("%w: no valid %s signature on %s among %d bundle(s): %w",
		ErrUnverified, p.Provider, subject.Digest, len(bs), errors.Join(errs...))
}
