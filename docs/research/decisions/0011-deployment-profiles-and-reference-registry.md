# 0011: Deployment profiles and the reference private registry

Status: Proposed

## Context

The first design ([0002](0002-registry-repositories-tags-visibility.md)) made GHCR the
only canonical registry and treated a self-hosted registry as a future mirror. The
project owner has since asked for the same platform to run in two ways:

- built and distributed through GitHub; and
- hosted privately, for example as a registry container in Docker, with no dependency
  on GitHub at all.

Several findings support this:

- The research already showed that GHCR has no referrers API and no tag immutability.
- GHCR accepts only classic personal access tokens for device pulls and documents no
  egress limits.
- macOS and Windows images may only be distributed inside the licensee's organisation.
- Air-gapped sites cannot reach GHCR.

The project owner chose three things:

- **zot** as the reference private registry, with Harbor and CNCF distribution v3 as
  supported targets.
- **cosign key-based signing plus the channel manifest** as the trust model for the
  private profile.
- **weaveplatform-oci publishes its own container images**, including a preconfigured
  zot ([0012](0012-container-images.md)).

## Decision

**One codebase, three profiles.** A deployment profile is configuration, not a fork. It
is read by the publisher (`weaveoci publish`) and by every consumer: hostweave server,
hostweave agent, both guestweave CLIs and `weaveoci pull`.

| Profile | Canonical registry | Build-time signature | Promotion | Typical user |
|---|---|---|---|---|
| `github` | `ghcr.io/deploymenttheory` | GitHub artifact attestation (Sigstore bundle, keyless) | channel manifest | open-source baseline, public Linux images |
| `private` | an organisation's `weave-zot` (or Harbor, or distribution v3) | cosign-compatible key-based bundle (file or KMS key) | channel manifest under the organisation's own root, or the deploymenttheory root | enterprises, air-gapped sites, macOS and Windows images |
| `hybrid` | GHCR upstream, `weave-zot` per site in the mirror role | as published upstream | channel manifest from upstream | offices with limited bandwidth that consume deploymenttheory images |

The profile names the canonical registry, the ordered mirror list, the signature
provider and its verification material, the channel URL, and the channel trust anchors.
The default anchor is the deploymenttheory root key embedded in core. An organisation
running the private profile may mint its own root with `weavemanifest keygen` and add it
as an anchor. Consumers accept a channel signed by any configured anchor and nothing
else.

**Reference private registry.** zot v2.1.21, the full image `ghcr.io/project-zot/zot`
(not `zot-minimal`, which lacks the sync and trust extensions), is shipped as
`ghcr.io/deploymenttheory/weave-zot` with two configuration roles under
`deploy/zot/config/`:

- **store:** canonical private registry. It has local filesystem or S3 storage, auth,
  access control, retention, the trust extension for cosign keys, and metrics. It has
  no sync.
- **mirror:** pull-through cache of an upstream (GHCR by default). It runs `sync` with
  `onDemand: true` and `preserveDigest: true`, which requires `http.compat:
  ["docker2s2"]`. Credentials for the upstream go in `credentialsFile`.

Settings that both roles must carry, because the artifacts are 30 to 60 GiB in 512 MiB
blobs:

- `http.readTimeout` and `http.writeTimeout` set to `"0s"`. The default since zot
  v2.1.17 is 60 s, which breaks large blob uploads.
- `storage.gcDelay` set longer than the slowest expected push. The reference value is
  `24h`, because GC removes unreferenced blobs older than `gcDelay` and a push uploads
  every blob before its manifest.
- Every artifact is pushed with a tag. Retention deletes untagged manifests by default,
  and the retention policies end with a catch-all `**` rule so unmatched tags survive.
- `extensions.search.cve` is omitted. Otherwise zot downloads the Trivy database from
  GHCR, which defeats an air-gapped deployment.
- An authenticating scheme (htpasswd or OpenID) is always enabled. zot only restricts
  the `/v2/_zot/ext/cosign` public-key upload endpoint to admins when htpasswd, LDAP,
  OpenID or API keys are on.
- Tag immutability is enforced by access control. Publishers get `read` and `create` but
  not `update`; only promotion credentials may move channel tags.

**Supported targets.**

- Harbor 2.15 is supported for organisations that want its UI, RBAC and replication.
- CNCF distribution v3 (`registry:3.1.2`) is supported. It has no referrers API, so it
  also serves as the test target for the `sha256-<hex>` fallback-tag path that GHCR
  needs.
- Any OCI 1.1 registry that accepts custom config media types should work. Only these
  three, plus GHCR, are tested.

**Credentials by profile.**

- `github`: as [0002](0002-registry-repositories-tags-visibility.md) describes.
- `private`: htpasswd users or OpenID for people; robot users or API keys for
  publishers and hostweave; device pulls through the OS keychain via the Docker
  credential-helper protocol.
- `hybrid`: devices authenticate to the site mirror only. The mirror holds the single
  upstream credential.

## Rationale

zot is a single binary in a single container. It implements the OCI 1.1 referrers API,
which GHCR lacks. It can be both a private store and a digest-preserving pull-through
cache. It stores on a filesystem or S3, and its access control can express tag
immutability. It also verifies cosign v3 bundles with uploaded public keys, although
that result is informational. These properties make it the smallest complete answer for
both the private and hybrid profiles. Shipping it with weave's configuration baked in
removes the settings that are easy to get wrong for multi-GiB artifacts.

Keeping profiles as configuration means a single code path is tested against every
registry, and the GitHub path does not become special.

Alternatives considered:

- **Harbor as the reference.** It has the richest feature set: UI, RBAC, immutability
  rules, proxy cache and replication. It needs several containers and a database, so it
  is too heavy as the default; it remains a supported target.
- **CNCF distribution v3 as the reference.** It is the simplest option, but it has no
  referrers API and no access control. Rejected as the reference; it is kept as the test
  target for the fallback path.
- **Writing a weave registry server.** It would duplicate a mature project with nothing
  gained. Rejected.
- **GitHub only.** Rejected by the project owner. It cannot serve air-gapped sites and
  keeps fleet credentials on classic PATs.

## Constraints

- The upstream zot image runs as root, is distroless (no shell, curl or wget) and has
  no healthcheck subcommand. The weave image adds a static `weaveoci healthcheck` probe
  ([0012](0012-container-images.md)).
- The binary path inside the upstream image is architecture-specific, so the config
  can only be validated by running `zot verify`, not with a Dockerfile `RUN`.
- zot's trust extension is informational. It records signature status in search
  metadata but does not block a pull. Enforcement is the consumer's job
  ([0006](0006-trust-attestations-and-channel-manifest.md)).
- `sync.onlySigned` checks that a signature exists, not that it is valid.
- For a manifest with a custom config media type, zot does not check that the layers
  exist. The `client` must push every blob before the manifest and must verify after
  push.
- v2.1.21 stops hydrating deduplicated blobs from other repositories on HEAD and Range
  GET. Cross-repository reuse must use `POST ?mount=`.
- Known open issues: stale on-demand sync staging can hang
  ([#4357](https://github.com/project-zot/zot/issues/4357)), and S3 or R2 multipart
  behaviour is reported problematic
  ([#2142](https://github.com/project-zot/zot/issues/2142)). The mirror role defaults to
  filesystem storage.
- Licensing does not change with the profile. macOS and Windows images remain
  organisation-private wherever they are hosted
  ([04-registries-and-github.md](../04-registries-and-github.md)).

## Verification

- Acceptance features run `weaveoci publish`, `pull` and `verify` against a
  `weave-zot` container in the store role, the mirror role, and `registry:3.1.2`. Each
  starts through testcontainers-go and waits on `/readyz` ([0013](0013-quality-gates.md)).
- CI validates each config role with `docker run --rm <weave-zot> verify
  /etc/zot/config/<role>.json`.
- A mirror test pulls an artifact through the mirror role and checks that the index
  digest and the referrer digests match the upstream.
- An immutability test: a publisher credential's second push of an existing build tag
  is refused by the store role.
- A profile test: the same feature file runs with the `github` profile against an
  in-process registry configured to return 404 from the referrers API.

## References

- [13-deployment-profiles.md](../13-deployment-profiles.md), [04-registries-and-github.md](../04-registries-and-github.md), [05-supply-chain.md](../05-supply-chain.md)
- zot v2.1.21 release: <https://github.com/project-zot/zot/releases/tag/v2.1.21>
- zot configuration structs: <https://github.com/project-zot/zot/blob/v2.1.21/pkg/api/config/config.go>, <https://github.com/project-zot/zot/blob/v2.1.21/pkg/extensions/config/sync/config.go>
- zot mirroring: <https://zotregistry.dev/v2.1.21/articles/mirroring/>
- zot retention: <https://zotregistry.dev/v2.1.21/articles/retention/>
- zot immutable tags: <https://zotregistry.dev/v2.1.21/articles/immutable-tags/>
- zot signature verification: <https://zotregistry.dev/v2.1.21/articles/verifying-signatures/>
- zot cosign v3 bundle verification: <https://github.com/project-zot/zot/pull/4307>
- zot timeout issue: <https://github.com/project-zot/zot/issues/4079>
- zot dedupe hydration change: <https://github.com/project-zot/zot/pull/4363>
- Harbor: <https://goharbor.io/blog/harbor-2.11>; distribution v3.1.2: <https://github.com/distribution/distribution>
