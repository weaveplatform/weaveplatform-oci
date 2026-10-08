package imagecheck

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/opencontainers/go-digest"

	"github.com/weaveplatform/weaveplatform-oci/pkg/channel"
	"github.com/weaveplatform/weaveplatform-oci/pkg/client"
	"github.com/weaveplatform/weaveplatform-oci/pkg/conformance"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
	"github.com/weaveplatform/weaveplatform-oci/pkg/profile"
	"github.com/weaveplatform/weaveplatform-oci/pkg/verify"
)

// AdmitPublished resolves the dispatched tag once, copies its exact immutable
// graph and referrers, and authenticates both evidence types. The dispatch can
// never supply trust identities or silently omit an index platform. The layout
// is retained for audit and subsequent operations on the same tested bytes.
func AdmitPublished(
	ctx context.Context,
	p AdmissionPolicy,
	c *client.Client,
	entry channel.Image,
	layout string,
) (Admission, error) {
	var result Admission
	if c == nil || p.Build == nil || p.Acceptance == nil || layout == "" ||
		c.Profile().Registry.Host != p.Registry {
		return result, fmt.Errorf(
			"%w: matching registry, identities and audit layout required",
			ErrEvidence,
		)
	}
	want, err := digest.Parse(entry.Digest)
	if err != nil || want.Algorithm() != digest.SHA256 {
		return result, fmt.Errorf("%w: promotion needs an exact SHA-256 index", ErrEvidence)
	}
	if entry.Signature == nil ||
		entry.Signature.Provider != string(profile.SigningGitHubAttestation) ||
		entry.Signature.Issuer != p.Build.Issuer ||
		entry.Signature.SubjectRegexp != p.Build.SubjectRegexp ||
		entry.Signature.KeyID != "" {
		return result, fmt.Errorf("%w: dispatched signer differs from reviewed policy", ErrEvidence)
	}
	ref, err := c.Parse(p.Registry + "/" + entry.Repository + ":" + entry.Tag)
	if err != nil {
		return result, fmt.Errorf("promotion reference: %w", err)
	}
	if ref.Registry.Host != p.Registry || ref.Repository != entry.Repository ||
		ref.Tag != entry.Tag ||
		ref.Digest != "" {
		return result, fmt.Errorf("%w: malformed promotion reference", ErrEvidence)
	}
	root, _, err := c.Resolve(ctx, ref)
	if err != nil {
		return result, fmt.Errorf("resolve promotion: %w", err)
	}
	if root.Digest != want {
		return result, fmt.Errorf("%w: promotion tag moved from dispatched index", ErrEvidence)
	}
	store, err := pack.OpenLayout(ctx, layout)
	if err != nil {
		return result, fmt.Errorf("promotion layout: %w", err)
	}
	ref.Digest = want
	copied, _, err := c.Pull(ctx, ref, store, client.PullOptions{Tag: entry.Tag, Referrers: true})
	if err != nil {
		return result, fmt.Errorf("copy promotion: %w", err)
	}
	if copied.Digest != want {
		return result, fmt.Errorf("%w: copied index differs", ErrEvidence)
	}
	inspection, err := conformance.Check(ctx, store, copied, conformance.Options{Deep: true})
	if err != nil {
		return result, fmt.Errorf("inspect promotion: %w", err)
	}
	if !inspection.OK() || len(entry.Platforms) != len(inspection.Children) {
		return result, fmt.Errorf(
			"%w: promotion platform inventory differs from index",
			ErrEvidence,
		)
	}
	remaining := slices.Clone(entry.Platforms)
	for _, child := range inspection.Children {
		g := child.Description.Config.Guest
		expected := channel.Platform{
			OS:        g.OS,
			Arch:      g.Arch,
			OSVersion: g.OSVersion,
			Digest:    child.Descriptor.Digest.String(),
		}
		n := slices.Index(remaining, expected)
		if n < 0 {
			return result, fmt.Errorf(
				"%w: promotion platform metadata differs from index",
				ErrEvidence,
			)
		}
		remaining = slices.Delete(remaining, n, n+1)
	}
	return AdmitAttached(ctx, p, inspection, entry.Tag, verify.StoreSource{Store: store})
}

// AdmitAttached searches actual registry referrers. Invalid or unrelated
// attestations cannot authorize promotion; a valid acceptance must bind this
// exact tag/index and satisfy the current report schema and parent policy.
func AdmitAttached(
	ctx context.Context,
	p AdmissionPolicy,
	inspection conformance.Report,
	tag string,
	src verify.Source,
) (Admission, error) {
	var result Admission
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("admission cancelled: %w", err)
	}
	if p.Build == nil || p.Acceptance == nil {
		return result, fmt.Errorf("%w: reviewed identities required", ErrEvidence)
	}
	bundles, err := verify.Bundles(ctx, src, inspection.Root)
	if err != nil {
		return result, fmt.Errorf("promotion evidence: %w", err)
	}
	buildID := *p.Build
	buildID.PredicateType = verify.SLSAProvenanceV1
	var build []byte
	for _, candidate := range bundles {
		if _, _, err := buildID.VerifyStatement(candidate, inspection.Root.Digest); err == nil {
			build = candidate
			break
		}
	}
	if len(build) == 0 {
		return result, fmt.Errorf("%w: no authenticated build provenance", ErrEvidence)
	}
	failures := []error{ErrEvidence}
	for _, candidate := range bundles {
		admitted, err := Admit(p, inspection, tag, build, candidate)
		if err == nil {
			return admitted, nil
		}
		failures = append(failures, err)
	}
	return result, fmt.Errorf("no admissible acceptance attestation: %w", errors.Join(failures...))
}
