# 0007: Publication pipeline

Status: Proposed

## Context

Images are produced today by hand: a developer installs macOS from an IPSW or Windows
from media on a laptop, prepares it, and pushes it from the CLI. There is no recipe,
no provenance and no promotion. The agent-modules repository shows the shape the
organisation already trusts: release-please tags, a build matrix from a manifest, a
sidecar digest manifest, an ORAS push to GHCR and a `repository_dispatch` that opens a
promotion pull request in `weaveplatform-manifest`. Building VM images adds hard
runner constraints: GitHub-hosted macOS runners cannot run macOS guests, hosted Windows
runners have no nested virtualisation, and hosted Ubuntu runners have KVM inside about
14 GB of disk.

## Decision

**Location.** The reusable workflows live in `weaveplatform-oci/.github/workflows/`:
`build-macos.yml`, `build-windows.yml`, `build-linux.yml` and `publish.yml`. Image
definitions (templates, unattend files, provisioning scripts, `Containerfile`s) live
under `images/<repository-name>/` in the same repository, one directory per published
repository, with an `image.yaml` naming the family, variant, source media selector,
runner labels and channels.

**Runner placement.**

| Guest | Runner | Build method |
|---|---|---|
| macOS | self-hosted, Apple silicon, bare metal, `max-parallel: 1` | Apple restore image resolved through Virtualization.framework, installed by guestweave-cli-macos, unattended setup, agent baked, shut down, packed from the raw disk and `nvram.bin` |
| Windows | hosted `ubuntu-*` with KVM, or self-hosted Linux or Windows | ISO acquired with go-sdk-winmediafoundry, `autounattend.xml` plus virtio drivers, installed under QEMU, agent baked, sysprep-equivalent stripping, packed from the raw disk |
| Linux | hosted `ubuntu-*` with KVM | bootc image built and pushed, raw disk derived with `image-builder --type raw`; or cloud image verified, converted to raw, booted once with a NoCloud seed, packed |

**Stages of `publish.yml`.** Resolve the build tag and refuse if it already exists;
pack with `weaveoci pack` and run the conformance validator; push with `weaveoci push`
(HEAD skip, mount, retry); run `actions/attest` with `push-to-registry: true`; verify the
attestation with `gh attestation verify --bundle-from-oci`; pull the index back on a
clean cache and compare digests; dispatch `image-published` to `weaveplatform-manifest`
with repository, tag, index digest and per-platform digests. Channel tags are never
written by the build workflow; promotion writes them.

**Identity and secrets.** Push uses `GITHUB_TOKEN` with `packages: write`,
`id-token: write`, `attestations: write` and `artifact-metadata: write`; each package's
"Manage Actions access" grants the repository Write. The dispatch uses a fine-scoped PAT
as the modules pipeline does. Source media for macOS and Windows is fetched from Apple
and Microsoft endpoints at build time and cached on the self-hosted runner; it is never
stored in a public package.

**Cadence.** A scheduled monthly rebuild of every `stable` image, plus on-demand builds
when a source media version or a template changes. Every build produces a new `-r<rev>`
tag; nothing is overwritten.

## Rationale

Putting definitions and workflows next to the contract and the tooling keeps one
repository responsible for "what a published image is", and reusing the modules pipeline
shape means hosts, reviewers and the promotion tooling already know the flow. Runner
placement follows the hard facts: Apple does not permit nested macOS guests on any
hardware, GitHub does not expose nested virtualisation on hosted Windows runners, and
KVM on hosted Ubuntu is sufficient for Windows and Linux installs at the sizes involved.
Self-verification after push catches a broken fallback-tag referrer or a corrupted
chunk before a human is asked to promote.

Alternatives considered:

- **Build in each consumer repository.** Spreads three pipelines over three repositories
  and three sets of secrets. Rejected.
- **Packer with a QEMU or Hyper-V builder as the orchestrator.** Mature, but it adds a
  tool between the CLIs and the artifact, and the Windows plugin needs Hyper-V, which
  hosted runners lack. Rejected; the CLIs and `weaveoci` are the builders, Packer is
  recorded as prior art.
- **Hosted macOS runners.** Not possible for macOS guests; usable only for packing an
  already-built disk, which gains nothing.
- **Build Windows on a self-hosted Windows host under HCS.** Possible and closest to
  production, but it needs elevation for the first run and a dedicated Windows machine.
  Kept as an alternative runner label, not the default.

## Constraints

- Self-hosted Apple-silicon capacity is required for every macOS build; two macOS
  guests per host is the Apple limit, and builds run serially.
- Hosted Ubuntu runners have about 14 GB of free disk; a Windows install plus a raw
  disk and a packed artifact may exceed it, in which case a larger or self-hosted runner
  is needed.
- arm64 Linux and Windows derivation needs arm64 runners or emulation; emulated installs
  are slow.
- GHCR's ten-minute upload timeout applies per blob; the 512 MiB chunk keeps each PUT
  well inside it on any reasonable uplink, but a runner with a slow uplink can still
  fail and must rely on retry and HEAD skip.
- Attestations for private repositories need GitHub Enterprise Cloud.
- Promotion remains a human merge in `weaveplatform-manifest`; the pipeline cannot
  bypass it.

## Verification

- A dry-run mode of each build workflow that stops after `weaveoci pack` and the
  conformance validator, runnable on a pull request without pushing.
- `publish.yml` integration test against a scratch package: push, attest, verify,
  re-pull, dispatch to a test repository, and a second run that fails on the existing
  tag.
- Linux is the first pipeline exercised end to end (migration phase 3); macOS and
  Windows follow (phase 7).
- Existing evidence: `deploymenttheory/weaveplatform-agent-modules@main .github/workflows/module-release.yml:85-135`
  (sidecar manifest, ORAS push, dispatch); runner facts recorded in
  [07-build-pipelines.md](../07-build-pipelines.md) with sources dated 2026-10-02.

## References

- [07-build-pipelines.md](../07-build-pipelines.md), [06-large-artifacts.md](../06-large-artifacts.md)
- GitHub-hosted runners: <https://docs.github.com/en/actions/reference/runners/github-hosted-runners>, <https://docs.github.com/en/actions/reference/runners/larger-runners>; KVM on Linux runners: <https://github.blog/changelog/2024-04-02-github-actions-hardware-accelerated-android-virtualization-now-available/>; no nested virtualisation on Windows runners: <https://github.com/actions/runner-images/issues/183>; Apple on nested macOS guests: <https://developer.apple.com/forums/thread/827663>
- `actions/attest`: <https://github.com/actions/attest>; `oras-project/setup-oras`: <https://github.com/oras-project/setup-oras>
- `deploymenttheory/weaveplatform-manifest@main docs/trust-chain.md` (promotion)
- `deploymenttheory/go-sdk-winmediafoundry@main` (Windows media acquisition)
