// Package fetch is the consumer side of the contract in one call: resolve a
// reference, pull it into the device cache, verify it against the profile's
// policy, select a platform and optionally unpack it as a bundle.
//
// weaveoci pull, guestweave-cli-macos, guestweave-cli-windows and hostweave
// all consume images this way, so the order of the steps (nothing is
// unpacked before it verifies) is decided once, here.
package fetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"

	"github.com/weaveplatform/weaveplatform-oci/pkg/cache"
	"github.com/weaveplatform/weaveplatform-oci/pkg/chunk"
	"github.com/weaveplatform/weaveplatform-oci/pkg/client"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
	"github.com/weaveplatform/weaveplatform-oci/pkg/profile"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
	"github.com/weaveplatform/weaveplatform-oci/pkg/verify"
)

var (
	// ErrPlatform reports an ambiguous index with no platform chosen or a
	// malformed platform string: a mistake in the request, not the image.
	ErrPlatform = errors.New("fetch: platform")
	// ErrRequest reports an unusable request.
	ErrRequest = errors.New("fetch: invalid request")
)

// Request is one pull.
type Request struct {
	Client *client.Client
	Cache  *cache.Store
	// Ref is a reference in any form the client's profile resolves.
	Ref string
	// Mode overrides the profile's verification mode when set.
	Mode profile.VerifyMode
	// Platform is os/arch. Empty selects the only child of a single-platform
	// index and is an error for a multi-platform one.
	Platform string
	// To, when set, is the bundle directory the platform is unpacked into.
	To       string
	Assemble chunk.AssembleOptions
	// Pin, when set, pins the pulled image for this owner before anything is
	// unpacked, so a concurrent prune cannot evict it underneath the caller.
	Pin string
}

// Result describes what was pulled, why it was trusted and what was written.
type Result struct {
	Reference client.Reference
	Root      ocispec.Descriptor
	Policy    verify.Policy
	Evidence  verify.Evidence
	// Manifest is the selected platform's manifest; zero when no platform was
	// selected (no Platform and no To).
	Manifest ocispec.Descriptor
	Unpacked *pack.UnpackResult
}

// Pull runs the whole consumer path. An image that fails verification is
// left in the cache unpinned (prune reclaims it) and is never unpacked.
func Pull(ctx context.Context, r Request) (Result, error) {
	if r.Client == nil || r.Cache == nil || r.Ref == "" {
		return Result{}, fmt.Errorf("%w: client, cache and reference are required", ErrRequest)
	}
	ref, err := r.Client.Parse(r.Ref)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrRequest, err)
	}
	res := Result{Reference: ref}
	res.Policy, err = verify.FromProfile(ctx, r.Client.Profile(), ref.Repository, r.Mode, nil)
	if err != nil {
		return res, err //nolint:wrapcheck // verify names the configuration problem
	}
	if res.Root, err = r.Cache.Pull(ctx, r.Client, ref); err != nil {
		return res, err //nolint:wrapcheck // cache errors name the reference
	}
	res.Evidence, err = verify.Verify(
		ctx,
		res.Policy,
		verify.StoreSource{Store: r.Cache.Target()},
		res.Root,
	)
	if err != nil {
		return res, err //nolint:wrapcheck // verify errors name the digest
	}
	if r.Pin != "" {
		if err := r.Cache.Pin(res.Root.Digest, r.Pin); err != nil {
			return res, err //nolint:wrapcheck // cache errors name the digest
		}
	}
	if r.Platform == "" && r.To == "" {
		return res, nil
	}
	if res.Manifest, err = SelectPlatform(ctx, r.Cache.Target(), res.Root, r.Platform); err != nil {
		return res, err
	}
	if r.To == "" {
		return res, nil
	}
	u, err := pack.Unpack(ctx, r.Cache.Target(), res.Manifest, r.To, r.Assemble)
	if err != nil {
		return res, err //nolint:wrapcheck // pack errors name the disk
	}
	res.Unpacked = &u
	return res, nil
}

// SelectPlatform returns the manifest for platform ("os/arch") from root.
// A manifest root is returned as is. An empty platform selects the only
// child of a single-platform index.
func SelectPlatform(
	ctx context.Context,
	f content.Fetcher,
	root ocispec.Descriptor,
	platform string,
) (ocispec.Descriptor, error) {
	if root.MediaType != spec.MediaTypeIndex {
		return root, nil
	}
	raw, err := content.FetchAll(ctx, f, root)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("fetch index: %w", err)
	}
	var idx ocispec.Index
	if err := json.Unmarshal(raw, &idx); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("decode index: %w", err)
	}
	if platform == "" {
		if len(idx.Manifests) != 1 {
			return ocispec.Descriptor{}, fmt.Errorf(
				"%w: the index has %d platforms; choose one",
				ErrPlatform,
				len(idx.Manifests),
			)
		}
		return idx.Manifests[0], nil
	}
	osName, arch, ok := strings.Cut(platform, "/")
	if !ok || osName == "" || arch == "" {
		return ocispec.Descriptor{}, fmt.Errorf("%w: %q is not os/arch", ErrPlatform, platform)
	}
	// A platform the index does not hold is not a usage mistake: the
	// image simply was not built for it.
	d, err := spec.SelectChild(idx, ocispec.Platform{OS: osName, Architecture: arch})
	if err != nil {
		return ocispec.Descriptor{}, err //nolint:wrapcheck // spec names the platform
	}
	return d, nil
}
