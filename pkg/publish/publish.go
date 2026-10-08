// Package publish is the publication pipeline as a library (decision 0007):
// pack bundles into a contract index, check conformance, push every blob
// then the manifests under a build tag that must not already exist, sign
// with the profile's provider, pull the index back into an empty store and
// verify it and its signature, and emit the channel promotion entry. Every
// CI system and a workstation run the same steps; GitHub workflows are thin
// wrappers around `weaveoci publish`.
package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/oci"

	"github.com/weaveplatform/weaveplatform-oci/pkg/channel"
	"github.com/weaveplatform/weaveplatform-oci/pkg/chunk"
	"github.com/weaveplatform/weaveplatform-oci/pkg/client"
	"github.com/weaveplatform/weaveplatform-oci/pkg/conformance"
	"github.com/weaveplatform/weaveplatform-oci/pkg/imagecheck"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
	"github.com/weaveplatform/weaveplatform-oci/pkg/profile"
	"github.com/weaveplatform/weaveplatform-oci/pkg/sign"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
	"github.com/weaveplatform/weaveplatform-oci/pkg/verify"
)

var (
	// ErrRequest reports an unusable publish request.
	ErrRequest = errors.New("invalid publish request")
	// ErrSelfCheck reports that the pushed artifact did not read back correctly.
	ErrSelfCheck = errors.New("published artifact failed its self-check")
)

// ChannelTags are moved by promotion only and are refused as build tags.
var ChannelTags = map[string]bool{"stable": true, "edge": true, "latest": true}

// Request is one publication.
type Request struct {
	Client  *client.Client
	Bundles []string // bundle directories, one per platform
	// Layout publishes an already validated OCI index without repacking it.
	// LayoutRef selects the local tag/digest; ExpectedDigest pins the accepted
	// bytes. These inputs are mutually exclusive with Bundles.
	Layout         string
	LayoutRef      string
	ExpectedDigest string
	// Acceptance is the locally trusted validator's JSON report. Required for
	// layout publication; the workflow separately signs this evidence.
	Acceptance []byte
	// Repository is relative to the profile namespace or fully qualified.
	Repository string
	Tag        string       // the immutable build tag, e.g. 24.04-20260915-r1
	Signer     *sign.Signer // required for the cosign-key provider
	WorkDir    string       // scratch space for the packed layout; default a temp dir
	Chunk      chunk.Options
	Now        func() time.Time
}

// Result describes what was published.
type Result struct {
	Reference client.Reference
	Index     ocispec.Descriptor
	Children  []spec.Description
	Signature *ocispec.Descriptor
	Promotion channel.Image
}

// Run publishes r.
func Run(ctx context.Context, r Request) (Result, error) {
	if r.Client == nil || (len(r.Bundles) == 0 && r.Layout == "") || r.Repository == "" ||
		r.Tag == "" {
		return Result{}, fmt.Errorf(
			"%w: client, bundles or layout, repository and tag are required",
			ErrRequest,
		)
	}
	if (r.Layout != "" && (len(r.Bundles) != 0 || r.LayoutRef == "" || r.ExpectedDigest == "" || len(r.Acceptance) == 0)) ||
		(r.Layout == "" && (r.LayoutRef != "" || r.ExpectedDigest != "" || len(r.Acceptance) != 0)) {
		return Result{}, fmt.Errorf(
			"%w: layout requires a reference and expected digest, without bundles",
			ErrRequest,
		)
	}
	if ChannelTags[r.Tag] {
		return Result{}, fmt.Errorf(
			"%w: %q is a channel tag, moved only by promotion",
			ErrRequest,
			r.Tag,
		)
	}
	prov := r.Client.Profile().Signing.Provider
	if prov == profile.SigningCosignKey && r.Signer == nil {
		return Result{}, fmt.Errorf(
			"%w: the profile signs with cosign-key but no key was given",
			ErrRequest,
		)
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	ref, err := r.Client.Parse(r.Repository + ":" + r.Tag)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrRequest, err)
	}
	work := r.WorkDir
	if work == "" {
		if work, err = os.MkdirTemp("", "weaveoci-publish-"); err != nil {
			return Result{}, fmt.Errorf("publish: %w", err)
		}
		defer func() { _ = os.RemoveAll(work) }()
	}
	res := Result{Reference: ref}
	store, index, children, err := prepare(ctx, r, work)
	if err != nil {
		return res, err
	}
	res.Index, res.Children = index, children
	rep, err := conformance.Check(
		ctx,
		store,
		res.Index,
		conformance.Options{Deep: r.Layout != ""},
	)
	if err != nil ||
		!rep.OK() {
		return res, fmt.Errorf("%w: conformance: %v %v", ErrSelfCheck, err, rep.Problems())
	}
	if r.Layout != "" {
		if err := imagecheck.Check(r.Acceptance, rep, r.Tag); err != nil {
			return res, fmt.Errorf("publish: %w", err)
		}
	}
	if err := checkParents(ctx, r, rep); err != nil {
		return res, err
	}
	if _, err := r.Client.Push(
		ctx,
		store,
		res.Index.Digest.String(),
		ref,
		client.PushOptions{},
	); err != nil {
		return res, err //nolint:wrapcheck // client errors name the reference
	}
	if prov == profile.SigningCosignKey {
		repo, err := r.Client.Repository(ref.Registry, ref.Repository)
		if err != nil {
			return res, err //nolint:wrapcheck // client errors name the reference
		}
		sig, err := r.Signer.Sign(ctx, repo, res.Index)
		if err != nil {
			return res, err //nolint:wrapcheck // sign errors name the subject
		}
		res.Signature = &sig
	}
	if err := selfCheck(ctx, r, ref, res); err != nil {
		return res, err
	}
	res.Promotion = promotion(ref, res, r.Signer, prov, r.Client.Profile().Verify.Identity, now())
	return res, nil
}

func packAll(
	ctx context.Context,
	store content.Storage,
	dirs []string,
	o chunk.Options,
) (ocispec.Descriptor, []spec.Description, error) {
	kids := make([]ocispec.Descriptor, 0, len(dirs))
	descs := make([]spec.Description, 0, len(dirs))
	for _, dir := range dirs {
		b, err := pack.LoadBundle(dir)
		if err != nil {
			return ocispec.Descriptor{}, nil, err //nolint:wrapcheck // pack names the bundle
		}
		m, err := pack.Manifest(ctx, b, store, o)
		if err != nil {
			return ocispec.Descriptor{}, nil, err //nolint:wrapcheck // pack names the bundle
		}
		d, err := pack.Describe(ctx, store, m)
		if err != nil {
			return ocispec.Descriptor{}, nil, err //nolint:wrapcheck // pack names the digest
		}
		kids = append(kids, m)
		descs = append(descs, d)
	}
	idx, err := pack.Index(ctx, store, kids, nil)
	if err != nil {
		return ocispec.Descriptor{}, nil, err //nolint:wrapcheck // spec errors carry the rule list
	}
	return idx, descs, nil
}

// selfCheck pulls the index into an empty store, re-runs conformance, and
// verifies the signature the way consumers will.
func selfCheck(ctx context.Context, r Request, ref client.Reference, res Result) error {
	dir, err := os.MkdirTemp("", "weaveoci-selfcheck-")
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSelfCheck, err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	fresh, err := oci.NewWithContext(ctx, dir)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSelfCheck, err)
	}
	byDigest := ref
	byDigest.Digest = res.Index.Digest
	root, _, err := r.Client.Pull(
		ctx,
		byDigest,
		fresh,
		client.PullOptions{Tag: "check", Referrers: true},
	)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSelfCheck, err)
	}
	if root.Digest != res.Index.Digest {
		return fmt.Errorf("%w: pulled %s, pushed %s", ErrSelfCheck, root.Digest, res.Index.Digest)
	}
	rep, err := conformance.Check(ctx, fresh, root, conformance.Options{Deep: true})
	if err != nil || !rep.OK() {
		return fmt.Errorf("%w: deep conformance: %v %v", ErrSelfCheck, err, rep.Problems())
	}
	if res.Signature == nil {
		return nil
	}
	pub, err := r.Signer.PublicKeyPEM()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSelfCheck, err)
	}
	keyFile := filepath.Join(dir, "cosign.pub")
	if err := os.WriteFile(keyFile, pub, 0o600); err != nil {
		return fmt.Errorf("%w: %w", ErrSelfCheck, err)
	}
	ks, err := verify.LoadKeySet(keyFile)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSelfCheck, err)
	}
	pol := verify.Policy{
		Mode:     profile.VerifySignature,
		Provider: profile.SigningCosignKey,
		Keys:     ks,
	}
	if _, err := verify.Verify(ctx, pol, r.Client.Bind(ref), res.Index); err != nil {
		return fmt.Errorf("%w: %w", ErrSelfCheck, err)
	}
	return nil
}

func promotion(
	ref client.Reference,
	res Result,
	s *sign.Signer,
	prov profile.SigningProvider,
	identity *profile.Identity,
	now time.Time,
) channel.Image {
	img := channel.Image{
		Repository: ref.Repository,
		Tag:        ref.Tag,
		Digest:     res.Index.Digest.String(),
		BuildDate:  now.UTC().Format(time.RFC3339),
	}
	for _, d := range res.Children {
		img.Platforms = append(img.Platforms, channel.Platform{
			OS:        d.Config.Guest.OS,
			Arch:      d.Config.Guest.Arch,
			OSVersion: d.Config.Guest.OSVersion,
			Digest:    d.Digest.String(),
		})
	}
	switch prov {
	case profile.SigningCosignKey:
		img.Signature = &channel.Signer{Provider: string(prov), KeyID: s.KeyID()}
	case profile.SigningGitHubAttestation:
		img.Signature = &channel.Signer{Provider: string(prov)}
		if identity != nil {
			// These are expected signer claims from reviewed configuration.
			// Candidate/admission verification must authenticate the statements.
			img.Signature.Issuer = identity.Issuer
			img.Signature.SubjectRegexp = identity.SubjectRegexp
		}
	}
	return img
}

// WritePromotion writes the promotion entry as JSON for the promotion step
// (a repository_dispatch payload or `weaveoci channel promote --entry`).
func WritePromotion(path string, img channel.Image) error {
	b, err := json.MarshalIndent(img, "", "  ")
	if err != nil {
		return fmt.Errorf("encode promotion: %w", err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("write promotion: %w", err)
	}
	return nil
}
