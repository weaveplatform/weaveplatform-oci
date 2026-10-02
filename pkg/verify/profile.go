package verify

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/weaveplatform/weaveplatform-oci/pkg/channel"
	"github.com/weaveplatform/weaveplatform-oci/pkg/profile"
)

// FromProfile builds the consumer policy a profile describes, loading
// public keys, the trusted root, channel anchors and the channel bundle.
// mode overrides the profile's verify mode when not empty (the --verify flag).
func FromProfile(
	ctx context.Context,
	p profile.Profile,
	repository string,
	mode profile.VerifyMode,
	hc *http.Client,
) (Policy, error) {
	pol := Policy{Mode: p.Verify.Mode, Provider: p.Signing.Provider, Repository: repository}
	if mode != "" {
		pol.Mode = mode
	}
	if pol.Mode == profile.VerifyNone {
		return pol, nil
	}
	if pol.Mode == profile.VerifySignature || pol.Mode == profile.VerifyBoth {
		switch p.Signing.Provider {
		case profile.SigningCosignKey:
			ks, err := LoadKeySet(p.Verify.PublicKeys...)
			if err != nil {
				return pol, err
			}
			pol.Keys = ks
		case profile.SigningGitHubAttestation:
			if p.Verify.Identity == nil || p.Verify.Identity.TrustedRoot == "" {
				return pol, fmt.Errorf(
					"%w: attestation verification needs verify.identity.trustedRoot",
					ErrConfig,
				)
			}
			tr, err := LoadTrustedRoot(p.Verify.Identity.TrustedRoot)
			if err != nil {
				return pol, err
			}
			pol.Identity = &Identity{
				Trusted:       tr,
				Issuer:        p.Verify.Identity.Issuer,
				SubjectRegexp: p.Verify.Identity.SubjectRegexp,
				RequireSCT:    true,
			}
		default:
			return pol, fmt.Errorf(
				"%w: signing provider %q cannot be verified",
				ErrConfig,
				p.Signing.Provider,
			)
		}
	}
	if pol.Mode == profile.VerifyChannel || pol.Mode == profile.VerifyBoth {
		for _, a := range p.Channel.Anchors {
			raw, err := os.ReadFile(a.PublicKey) //nolint:gosec // operator-configured anchor path
			if err != nil {
				return pol, fmt.Errorf("%w: anchor %s: %w", ErrConfig, a.Name, err)
			}
			anchor, err := channel.ParseAnchor(a.Name, raw)
			if err != nil {
				return pol, fmt.Errorf("%w: %w", ErrConfig, err)
			}
			pol.Anchors = append(pol.Anchors, anchor)
		}
		if p.Channel.Manifest == "" || len(pol.Anchors) == 0 {
			return pol, fmt.Errorf(
				"%w: channel verification needs channel.manifest and anchors",
				ErrConfig,
			)
		}
		b, err := channel.Load(ctx, p.Channel.Manifest, hc)
		if err != nil {
			return pol, fmt.Errorf("%w: %w", ErrConfig, err)
		}
		pol.Channel = &b
	}
	return pol, nil
}
