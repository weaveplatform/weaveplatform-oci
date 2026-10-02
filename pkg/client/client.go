// Package client is the registry transport for weave guest artifacts, built
// on oras-go v2. It resolves references against a deployment profile, reads
// through the profile's mirrors before the canonical registry, pushes every
// blob before the manifests that reference them, refuses to overwrite an
// existing build tag, and discovers referrers through the OCI referrers API
// with the sha256-<hex> tag-schema fallback.
package client

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
	"oras.land/oras-go/v2/registry/remote/retry"

	"github.com/weaveplatform/weaveplatform-oci/pkg/profile"
)

// Environment variables read by DefaultCredentials. WEAVE_REGISTRY_HOSTNAME,
// when set, limits the username and password to that host; hostweave passes
// per-assignment credentials to child processes this way.
const (
	EnvUsername = "WEAVE_REGISTRY_USERNAME"
	EnvPassword = "WEAVE_REGISTRY_PASSWORD"
	EnvHostname = "WEAVE_REGISTRY_HOSTNAME"
)

var (
	// ErrReference reports an unusable image reference.
	ErrReference = errors.New("invalid reference")
	// ErrTagExists reports a push that would move an existing tag.
	ErrTagExists = errors.New("tag already exists")
	// ErrUnavailable reports that no source (mirror or canonical) could serve a read.
	ErrUnavailable = errors.New("no registry source could serve the request")
)

// Options configure a Client.
type Options struct {
	// Credentials supplies registry credentials; nil uses DefaultCredentials.
	Credentials auth.CredentialFunc
	// Transport is the base HTTP transport; nil uses http.DefaultTransport.
	Transport http.RoundTripper
	// Concurrency is the number of blobs copied in parallel; default 4.
	Concurrency int
	// UserAgent is sent with every request.
	UserAgent string
}

// Client reads and writes artifacts for one deployment profile.
type Client struct {
	prof  profile.Profile
	creds auth.CredentialFunc
	base  http.RoundTripper
	conc  int
	ua    string
}

// New returns a client for p.
func New(p profile.Profile, o Options) *Client {
	c := &Client{
		prof:  p,
		creds: o.Credentials,
		base:  o.Transport,
		conc:  o.Concurrency,
		ua:    o.UserAgent,
	}
	if c.creds == nil {
		c.creds = DefaultCredentials()
	}
	if c.base == nil {
		c.base = http.DefaultTransport
	}
	if c.conc <= 0 {
		c.conc = 4
	}
	if c.ua == "" {
		c.ua = "weaveoci"
	}
	return c
}

// Profile returns the client's profile.
func (c *Client) Profile() profile.Profile { return c.prof }

// DefaultCredentials reads WEAVE_REGISTRY_* first, then the Docker config
// file and its credential helpers (osxkeychain, wincred, pass).
func DefaultCredentials() auth.CredentialFunc {
	store, storeErr := credentials.NewStoreFromDocker(credentials.StoreOptions{})
	return func(ctx context.Context, host string) (auth.Credential, error) {
		user, pass, scope := os.Getenv(EnvUsername), os.Getenv(EnvPassword), os.Getenv(EnvHostname)
		if user != "" && (scope == "" || scope == host) {
			return auth.Credential{Username: user, Password: pass}, nil
		}
		if storeErr != nil {
			return auth.EmptyCredential, nil //nolint:nilerr // no docker config means anonymous access
		}
		cred, err := credentials.Credential(store)(ctx, host)
		if err != nil {
			return auth.EmptyCredential, fmt.Errorf("credentials for %s: %w", host, err)
		}
		return cred, nil
	}
}

// Reference is a parsed artifact reference.
type Reference struct {
	Registry   profile.Registry // the canonical registry the reference names
	Repository string           // full path, namespace included
	Tag        string
	Digest     digest.Digest
}

// Ref is the tag or digest part, digest preferred.
func (r Reference) Ref() string {
	if r.Digest != "" {
		return r.Digest.String()
	}
	return r.Tag
}

// String renders host/repository[:tag][@digest].
func (r Reference) String() string {
	s := r.Registry.Host + "/" + r.Repository
	if r.Tag != "" {
		s += ":" + r.Tag
	}
	if r.Digest != "" {
		s += "@" + r.Digest.String()
	}
	return s
}

// Parse resolves ref against the profile. A reference whose first path
// element is the profile's registry host (or a mirror host) is fully
// qualified; anything else is a repository name relative to the profile
// namespace, e.g. "ubuntu-24.04:24.04-20260915-r1".
func (c *Client) Parse(ref string) (Reference, error) {
	if ref == "" {
		return Reference{}, fmt.Errorf("%w: empty", ErrReference)
	}
	reg := c.prof.Registry
	path := ref
	if host, rest, ok := strings.Cut(ref, "/"); ok && c.knownHost(host) {
		if host != reg.Host {
			return Reference{}, fmt.Errorf(
				"%w: %s names a mirror; use the canonical host %s",
				ErrReference,
				ref,
				reg.Host,
			)
		}
		path = rest
	} else if reg.Namespace != "" {
		path = reg.Namespace + "/" + ref
	}
	name, dg, _ := strings.Cut(path, "@")
	tag := ""
	if k := strings.LastIndex(name, ":"); k > strings.LastIndex(name, "/") {
		name, tag = name[:k], name[k+1:]
	}
	rr := registry.Reference{Registry: reg.Host, Repository: name, Reference: tag}
	if err := rr.ValidateRepository(); err != nil {
		return Reference{}, fmt.Errorf("%w: %s: %w", ErrReference, ref, err)
	}
	if tag != "" {
		if err := rr.ValidateReferenceAsTag(); err != nil {
			return Reference{}, fmt.Errorf("%w: %s: %w", ErrReference, ref, err)
		}
	}
	r := Reference{Registry: reg, Repository: name, Tag: tag}
	if dg != "" {
		d, err := digest.Parse(dg)
		if err != nil {
			return Reference{}, fmt.Errorf("%w: %s: %w", ErrReference, ref, err)
		}
		r.Digest = d
	}
	if r.Tag == "" && r.Digest == "" {
		return Reference{}, fmt.Errorf("%w: %s has no tag or digest", ErrReference, ref)
	}
	return r, nil
}

func (c *Client) knownHost(h string) bool {
	if h == c.prof.Registry.Host {
		return true
	}
	for _, m := range c.prof.Mirrors {
		if h == m.Host {
			return true
		}
	}
	return false
}

// Repository returns an oras-go repository on reg with the client's
// credentials, retry policy and TLS settings.
func (c *Client) Repository(reg profile.Registry, repo string) (*remote.Repository, error) {
	r, err := remote.NewRepository(reg.Host + "/" + repo)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrReference, err)
	}
	r.PlainHTTP = reg.PlainHTTP
	base := c.base
	if reg.InsecureSkipTLSVerify {
		if t, ok := base.(*http.Transport); ok {
			t = t.Clone()
			t.TLSClientConfig = &tls.Config{
				InsecureSkipVerify: true,
			} //nolint:gosec // explicitly configured per mirror
			base = t
		}
	}
	h := http.Header{}
	h.Set("User-Agent", c.ua)
	r.Client = &auth.Client{
		Client:     &http.Client{Transport: retry.NewTransport(base)},
		Header:     h,
		Cache:      auth.NewCache(),
		Credential: c.creds,
	}
	return r, nil
}

// sources returns the read sources: mirrors in order, then the canonical registry.
func (c *Client) sources() []profile.Registry {
	return append(append([]profile.Registry{}, c.prof.Mirrors...), c.prof.Registry)
}

// Source names where a read was served from.
type Source struct {
	Registry profile.Registry
	Mirror   bool
}

// Resolve returns the descriptor ref points at, trying mirrors first.
func (c *Client) Resolve(ctx context.Context, ref Reference) (ocispec.Descriptor, Source, error) {
	var errs []error
	for i, reg := range c.sources() {
		repo, err := c.Repository(reg, ref.Repository)
		if err != nil {
			return ocispec.Descriptor{}, Source{}, err
		}
		d, err := repo.Resolve(ctx, ref.Ref())
		if err == nil {
			return d, Source{Registry: reg, Mirror: i < len(c.prof.Mirrors)}, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", reg.Host, err))
		if ctx.Err() != nil {
			break
		}
	}
	return ocispec.Descriptor{}, Source{}, fmt.Errorf(
		"%w: resolve %s: %w",
		ErrUnavailable,
		ref,
		errors.Join(errs...),
	)
}

// PullOptions tune Pull.
type PullOptions struct {
	// Tag names the root in dst; default ref.Ref().
	Tag string
	// Referrers also copies manifests that refer to the root (signatures,
	// attestations, SBOMs), so they travel into caches and air-gapped layouts.
	Referrers bool
}

// Pull copies the graph ref points at (index, manifests, configs, chunks and
// state blobs) into dst. Blobs already in dst are not fetched again, so an
// interrupted pull resumes at blob granularity. Mirrors are tried first;
// content is verified against its digest whichever source served it.
func (c *Client) Pull(
	ctx context.Context,
	ref Reference,
	dst oras.Target,
	o PullOptions,
) (ocispec.Descriptor, Source, error) {
	tag := o.Tag
	if tag == "" {
		tag = ref.Ref()
	}
	var errs []error
	for i, reg := range c.sources() {
		repo, err := c.Repository(reg, ref.Repository)
		if err != nil {
			return ocispec.Descriptor{}, Source{}, err
		}
		var root ocispec.Descriptor
		if o.Referrers {
			opts := oras.DefaultExtendedCopyOptions
			opts.Concurrency = c.conc
			root, err = oras.ExtendedCopy(ctx, repo, ref.Ref(), dst, tag, opts)
		} else {
			opts := oras.DefaultCopyOptions
			opts.Concurrency = c.conc
			root, err = oras.Copy(ctx, repo, ref.Ref(), dst, tag, opts)
		}
		if err == nil {
			return root, Source{Registry: reg, Mirror: i < len(c.prof.Mirrors)}, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", reg.Host, err))
		if ctx.Err() != nil {
			break
		}
	}
	return ocispec.Descriptor{}, Source{}, fmt.Errorf(
		"%w: pull %s: %w",
		ErrUnavailable,
		ref,
		errors.Join(errs...),
	)
}

// Size returns the bytes a pull of ref would download into dst: the sum of
// every blob and manifest in the graph that dst does not already hold. It
// reads only manifests and indexes, never chunks.
func (c *Client) Size(
	ctx context.Context,
	ref Reference,
	dst content.ReadOnlyStorage,
) (int64, error) {
	root, src, err := c.Resolve(ctx, ref)
	if err != nil {
		return 0, err
	}
	repo, err := c.Repository(src.Registry, ref.Repository)
	if err != nil {
		return 0, err
	}
	var total int64
	seen := map[digest.Digest]bool{}
	queue := []ocispec.Descriptor{root}
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		if seen[d.Digest] {
			continue
		}
		seen[d.Digest] = true
		if ok, err := dst.Exists(ctx, d); err == nil && ok {
			continue
		}
		total += d.Size
		kids, err := content.Successors(ctx, repo, d)
		if err != nil {
			return 0, fmt.Errorf("walk %s: %w", d.Digest, err)
		}
		queue = append(queue, kids...)
	}
	return total, nil
}

// PushOptions tune Push.
type PushOptions struct {
	// AllowExisting permits moving a tag that already resolves (promotion of
	// channel tags). Build tags are pushed with AllowExisting false.
	AllowExisting bool
}

// Push copies the graph rooted at srcRef in src to ref on the canonical
// registry. oras copies successors before predecessors, so every chunk and
// state blob is present before the manifest that references it (zot does not
// check this for custom config media types).
func (c *Client) Push(
	ctx context.Context,
	src oras.ReadOnlyTarget,
	srcRef string,
	ref Reference,
	o PushOptions,
) (ocispec.Descriptor, error) {
	if ref.Tag == "" {
		return ocispec.Descriptor{}, fmt.Errorf("%w: push needs a tag: %s", ErrReference, ref)
	}
	repo, err := c.Repository(ref.Registry, ref.Repository)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	if !o.AllowExisting {
		existing, err := repo.Resolve(ctx, ref.Tag)
		switch {
		case err == nil:
			return existing, fmt.Errorf("%w: %s resolves to %s", ErrTagExists, ref, existing.Digest)
		case !errors.Is(err, errdef.ErrNotFound):
			return ocispec.Descriptor{}, fmt.Errorf("check %s: %w", ref, err)
		}
	}
	opts := oras.DefaultCopyOptions
	opts.Concurrency = c.conc
	root, err := oras.Copy(ctx, src, srcRef, repo, ref.Tag, opts)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("push %s: %w", ref, err)
	}
	return root, nil
}

// Tag points an additional tag at desc on the canonical registry.
func (c *Client) Tag(
	ctx context.Context,
	ref Reference,
	desc ocispec.Descriptor,
	tag string,
) error {
	repo, err := c.Repository(ref.Registry, ref.Repository)
	if err != nil {
		return err
	}
	if err := repo.Tag(ctx, desc, tag); err != nil {
		return fmt.Errorf("tag %s:%s: %w", ref.Repository, tag, err)
	}
	return nil
}

// Tags lists the tags of ref's repository on the first source that answers.
func (c *Client) Tags(ctx context.Context, ref Reference) ([]string, error) {
	var errs []error
	for _, reg := range c.sources() {
		repo, err := c.Repository(reg, ref.Repository)
		if err != nil {
			return nil, err
		}
		var tags []string
		err = repo.Tags(ctx, "", func(t []string) error {
			tags = append(tags, t...)
			return nil
		})
		if err == nil {
			return tags, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", reg.Host, err))
	}
	return nil, fmt.Errorf("%w: tags %s: %w", ErrUnavailable, ref.Repository, errors.Join(errs...))
}

// Referrers lists referrers of subject with the given artifactType (empty
// for all), through the referrers API or the sha256-<hex> fallback tag, on
// the first source that answers.
func (c *Client) Referrers(
	ctx context.Context,
	ref Reference,
	subject ocispec.Descriptor,
	artifactType string,
) ([]ocispec.Descriptor, error) {
	var errs []error
	for _, reg := range c.sources() {
		repo, err := c.Repository(reg, ref.Repository)
		if err != nil {
			return nil, err
		}
		var out []ocispec.Descriptor
		err = repo.Referrers(ctx, subject, artifactType, func(r []ocispec.Descriptor) error {
			out = append(out, r...)
			return nil
		})
		if err == nil {
			return out, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", reg.Host, err))
	}
	return nil, fmt.Errorf(
		"%w: referrers of %s: %w",
		ErrUnavailable,
		subject.Digest,
		errors.Join(errs...),
	)
}

// FetchAll reads a small blob or manifest (configs, bundles, indexes) from
// the first source that has it, verified against its descriptor.
func (c *Client) FetchAll(
	ctx context.Context,
	ref Reference,
	d ocispec.Descriptor,
) ([]byte, error) {
	var errs []error
	for _, reg := range c.sources() {
		repo, err := c.Repository(reg, ref.Repository)
		if err != nil {
			return nil, err
		}
		b, err := content.FetchAll(ctx, repo, d)
		if err == nil {
			return b, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", reg.Host, err))
	}
	return nil, fmt.Errorf("%w: fetch %s: %w", ErrUnavailable, d.Digest, errors.Join(errs...))
}

// RepoSource binds a client to one repository; it satisfies verify.Source.
type RepoSource struct {
	c   *Client
	ref Reference
}

// Bind returns a RepoSource for ref's repository.
func (c *Client) Bind(ref Reference) RepoSource { return RepoSource{c: c, ref: ref} }

// Referrers lists referrers of subject (API or fallback tag).
func (s RepoSource) Referrers(
	ctx context.Context,
	subject ocispec.Descriptor,
	artifactType string,
) ([]ocispec.Descriptor, error) {
	return s.c.Referrers(ctx, s.ref, subject, artifactType)
}

// FetchAll reads a manifest or blob.
func (s RepoSource) FetchAll(ctx context.Context, d ocispec.Descriptor) ([]byte, error) {
	return s.c.FetchAll(ctx, s.ref, d)
}
