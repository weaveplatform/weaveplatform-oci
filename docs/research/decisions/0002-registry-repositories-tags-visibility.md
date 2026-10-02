# 0002: Registry, repositories, tags and visibility

Status: Proposed

## Context

Images must be published from one place and consumed by hostweave agents and
guestweave CLIs on macOS, Windows and Linux devices, some of them in offices with limited
bandwidth and some air-gapped. The organisation already publishes agent modules to
`ghcr.io/weaveplatform/weaveplatform-modules/<id>` with ORAS from GitHub Actions.
Apple's and Microsoft's licence terms forbid redistributing macOS and Windows outside the
licensee, while community Linux distributions may be redistributed unmodified. GHCR has no
referrers API, no immutable tags and accepts only classic personal access tokens or
`GITHUB_TOKEN`, so the registry choice shapes verification and credential handling.

## Decision

**Canonical registry by profile.** The canonical registry depends on the deployment
profile ([0011](0011-deployment-profiles-and-reference-registry.md)). In the `github`
profile, and as the upstream of the `hybrid` profile, it is `ghcr.io/weaveplatform`,
published from GitHub Actions in `weaveplatform-oci`. In the `private` profile it is an
organisation's `weave-zot` (or a supported Harbor or distribution v3), published by
`weaveoci publish` from any CI system or workstation
([0007](0007-publication-pipeline.md)). Any OCI 1.1 registry may hold a mirror; a mirror
is configured as a registry profile on the consumer, never as a fork of the publication
pipeline. The repository and tag grammar below is the same in every profile; only the
host and organisation prefix change.

**Repositories.** `ghcr.io/weaveplatform/weave-images/<family>-<major>[-<variant>]`, for
example `macos-26-vanilla`, `macos-26-base`, `windows-11-base`, `windows-server-2025-base`,
`ubuntu-24.04`, `fedora-bootc-46`. One repository holds one OS family and variant; the
image index inside a tag separates architectures. Container images for the hostweave
moby runtime live in separate repositories and are never mixed into a VM index.

**Tags.** Immutable build tags `<osver>-<build>-r<rev>`: `26.0-25A354-r1`,
`11-25H2-26200.6584-r1`, `24.04-20260915-r1`. The revision increments when the same OS
build is rebuilt. Moving channel tags `stable`, `edge` and `latest` are rewritten by
promotion only. GHCR cannot enforce tag immutability, so immutability is policy: the
publication workflow refuses to push a build tag that already resolves, the channel
manifest records digests not tags, and every weave consumer pins the digest it resolved
([0006](0006-trust-attestations-and-channel-manifest.md)).

**Visibility.** Linux repositories may be public. macOS and Windows repositories are
private to the organisation, and their images are built from Apple restore images and
Microsoft volume-licensing media on hardware the organisation owns. No workflow in this
repository publishes a macOS or Windows image to a public package.

**Mirrors and air gap.** Consumers carry an ordered list of mirrors per profile and fall
through on failure. A site mirror is a zot or Harbor pull-through cache configured to
preserve digests. Air-gapped sites import `oci-layout` directories exported by
`weaveoci export-layout`; digests and referrers survive the transfer unchanged.

**Credentials.** In the `private` profile, people authenticate to zot with htpasswd or
OpenID, publishers and hostweave with robot users or API keys, and devices through the
OS keychain; in the `hybrid` profile devices authenticate only to the site mirror, which
holds the single upstream credential ([0011](0011-deployment-profiles-and-reference-registry.md)).
In the `github` profile, devices pulling private packages from GHCR use a service account's
classic PAT with `read:packages` only, stored in the OS keychain through the Docker
credential-helper protocol. Workflows push with `GITHUB_TOKEN` and the package's
"Manage Actions access" set to Write for the publishing repository. hostweave delivers
pull credentials per assignment, as it does today.

## Rationale

GHCR is where the organisation's code, workflows and module artifacts already live; it is
free for container storage and bandwidth at the time of writing, needs no infrastructure,
and the one gap that matters for verification (no referrers API) is handled by the
`sha256-<hex>` fallback tag that cosign and GitHub attestations already write. The
repository naming follows the `<family>-<major>-<variant>` convention that the public
VM-image publishers surveyed in [02-prior-art.md](../02-prior-art.md) converged on, which
keeps listings readable to people who know those projects. Digest pinning in consumers is required anyway by
[0006](0006-trust-attestations-and-channel-manifest.md), so the absence of immutable
tags costs nothing in correctness.

Alternatives considered:

- **GHCR as the only canonical registry.** Superseded by the project owner's request for
  a privately hosted option; the self-hosted path is now the `private` profile in
  [0011](0011-deployment-profiles-and-reference-registry.md), with zot as the reference.
- **Cloud registries (ECR, ACR, Artifact Registry).** All support referrers, 200 GB layers
  and short-lived federated tokens. Rejected as canonical because builds run in GitHub
  Actions and the organisation has no primary cloud; any of them can be a mirror.
- **Tag suffixes per architecture instead of an index.** Simpler for tooling that chokes
  on `os: darwin`, but it doubles the tag surface and moves platform selection into
  string parsing. Rejected; no registry probed rejected darwin or windows platform values.
- **Public macOS images, as some third parties publish.** The macOS SLA §2J forbids
  redistribution and §2K forbids redistributing firmware. Rejected on licensing grounds.

## Constraints

- GHCR does not accept GitHub App installation tokens, so in the `github` profile device credentials are
  long-lived PATs until GitHub changes that; the `private` and `hybrid` profiles avoid this. The rotation period is a policy decision.
- GHCR "currently" charges nothing for container storage and bandwidth and promises at
  least a month's notice. Private multi-GB packages carry billing risk.
- Pull rate and egress limits on GHCR are undocumented; users of third-party VM tooling have hit
  "Egress is over the account limit" on 50 to 60 GB images. A site mirror is the
  mitigation.
- Whether internal distribution of macOS images to the organisation's own Macs counts
  as redistribution under the SLA, and whether Windows volume-licensing terms allow a
  private registry of installed images rather than installation media, are questions
  for counsel ([12-open-questions.md](../12-open-questions.md)).
- `actions/delete-package-versions` can delete untagged child manifests and
  fallback-tag referrer indexes; retention automation must be tested against a chunked,
  attested image before it is enabled.

## Verification

- Conformance test `TestRepositoryNameAndTagGrammar` in the shared module rejects
  names and tags outside the grammar above.
- Workflow test: a second run of `publish.yml` with an existing build tag fails before
  any blob upload.
- Live evidence, 2026-10-02: GHCR `GET /v2/<repo>/referrers/<digest>` returns 404;
  ranged blob GET returns 206 from `pkg-containers.githubusercontent.com`;
  `ghcr.io/weaveplatform/weaveplatform-modules/<id>` demonstrates nested package
  names.
- Mirror drill (migration phase 8): pull through a zot `sync` instance with
  `preserveDigest: true` and confirm the digest and the fallback-tag referrers match.

## References

- [04-registries-and-github.md](../04-registries-and-github.md), [05-supply-chain.md](../05-supply-chain.md), [08-target-architecture.md](../08-target-architecture.md), [13-deployment-profiles.md](../13-deployment-profiles.md)
- GHCR: <https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry>, <https://docs.github.com/en/packages/learn-github-packages/about-permissions-for-github-packages>, <https://docs.github.com/en/billing/concepts/product-billing/github-packages>
- GitHub App tokens not accepted: <https://github.com/orgs/community/discussions/171423>
- zot mirroring: <https://zotregistry.dev/v2.1.21/articles/mirroring/>; Harbor proxy cache: <https://goharbor.io/docs/main/administration/configure-proxy-cache/>
- Apple macOS Tahoe SLA: <https://www.apple.com/legal/sla/docs/macOSTahoe.pdf>; Windows 11 licence terms: <https://microsoft.com/content/dam/microsoft/usetm/documents/windows/11/oem-(pre-installed)/UseTerms_OEM_Windows_11_English.pdf>
- `deploymenttheory/weaveplatform-agent-modules@main .github/workflows/module-release.yml:106-118` (existing ORAS push to GHCR)
