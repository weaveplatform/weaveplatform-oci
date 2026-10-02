# Research: a consistent OCI image approach for the weave projects

Research baseline: **2026-10-02**. This set of documents records what was researched, what
exists today in hostweave, guestweave-cli-macos and guestweave-cli-windows, and the design
that follows from both. It is the foundation for `weaveplatform-oci`: the artifact
specification, the shared Go module and the publication workflows that all three projects
will adopt. Nothing here is implemented yet; the decision records are Proposed.

## Purpose

hostweave brokers ephemeral compute and runs jobs as containers or full VMs. The two
guestweave CLIs run those VMs on macOS and Windows hosts. Each project obtains images its own
way, and each pushes VM images to registries in a format the others cannot read. The goal is
one OCI-centred model: centralised publication through GitHub, hostweave consuming OCI only,
guestweave usable with or without a registry, and one artifact contract shared by every
consumer.

## How to read

1. Reports `01`–`07` are the research. Start with the [OCI primer](01-oci-primer.md) if OCI is new, then
   [prior art](02-prior-art.md) and the [current state](03-current-state.md) of the weave projects.
2. The [target architecture](08-target-architecture.md) shows how the pieces fit.
3. The [artifact contract](09-artifact-contract-v1.md) and the [shared Go module](10-shared-go-module.md) are the
   normative drafts.
4. [Migration](11-migration.md) sequences the work; [open questions](12-open-questions.md) lists what is unresolved.
5. The [decision records](decisions/README.md) state the contracts and rationale in the same form hostweave uses.

## Document map

| Document | Purpose |
|---|---|
| [01-oci-primer.md](01-oci-primer.md) | OCI image, distribution and artifact concepts for someone new to them; Go library landscape |
| [02-prior-art.md](02-prior-art.md) | How Tart, Lume, Podman, KubeVirt, bootc and others store VM and OS images in registries; what is adopted and avoided |
| [03-current-state.md](03-current-state.md) | As-is image handling in hostweave, guestweave-cli-macos, guestweave-cli-windows, go-sdk-winmediafoundry and the agent-modules pipeline; gap list |
| [04-registries-and-github.md](04-registries-and-github.md) | GHCR capabilities and limits, registry comparison, mirroring and air-gap, credentials, licensing constraints |
| [05-supply-chain.md](05-supply-chain.md) | Attestations and cosign at build time, the channel-manifest chain at promotion, provenance, SBOM, verification in Go |
| [06-large-artifacts.md](06-large-artifacts.md) | Chunking, compression, sparse disks, resumable transfer, deduplication, device cache and GC |
| [07-build-pipelines.md](07-build-pipelines.md) | Building macOS, Windows and Linux images on GitHub Actions; runner constraints; the shared publish stage |
| [08-target-architecture.md](08-target-architecture.md) | Components, data flows and sequence diagrams for publish, promote, dispatch, pull and from-source |
| [09-artifact-contract-v1.md](09-artifact-contract-v1.md) | Draft specification of the weave VM artifact: media types, config schema, chunk rules, state blobs, index, tags |
| [10-shared-go-module.md](10-shared-go-module.md) | Package map, API sketches, consumer mapping, CLI verbs, reusable workflows, testing |
| [11-migration.md](11-migration.md) | Phases across repositories, what each delivers, what stays optional in guestweave |
| [12-open-questions.md](12-open-questions.md) | Unresolved questions, who resolves them and what they block |
| [glossary.md](glossary.md) | Terms used across the set |
| [decisions/](decisions/README.md) | Decision records 0001–0010 |

## Versions at the research baseline

| Component | Version | Note |
|---|---|---|
| OCI image-spec | 1.1.1 | no 1.2 exists |
| OCI distribution-spec | 1.1.1 | referrers API, tag-schema fallback |
| oras-go | v2.6.2 | v3 on main is not production-ready |
| ORAS CLI | 1.3.4 | |
| cosign | 3.1.3 | bundles and referrers by default |
| sigstore-go | 1.3.0 | |
| actions/attest | 4.2.2 | attest-build-provenance v4 wraps it |
| bootc | 1.16.13 | |
| osbuild/image-builder | v85.0.0 | home of bootc-image-builder |
| zot | 2.1.21 | |
| Harbor | 2.15.2 | |
| CNCF distribution | 3.1.2 | no referrers API yet |
| go-containerregistry | v0.22.1 | used by hostweave today |

## Conventions

- Every external claim cites a URL. Claims about weave code cite `deploymenttheory/<repo>@<branch>` and `path:line`.
- Items that could not be confirmed against a primary source are marked *(unverified)*.
- Sizes use GiB and MiB. "GB" appears only when a vendor limit is quoted verbatim.
- Diagrams are fenced `mermaid` blocks.
- Status values for decisions: Proposed, Accepted, Superseded.
- Tart and Lume appear only as prior art, in the current-state description and in decision 0010. They are not recommended anywhere.
- Super Linter in this repository currently excludes Markdown; link and lint checks for these documents are run manually.

## Decisions fixed before writing

1. One weave-native OCI artifact contract for VM images, with no dependency on Tart. Tart, Lume and the current VHDX-v2 encoding are replaced.
2. Trust is layered: GitHub artifact attestations or cosign bundles as referrers at build time, and the existing minisign channel-manifest chain at promotion time.
3. `weaveplatform-oci` becomes the artifact specification, the shared Go module and the reusable publication workflows.
4. GHCR is the canonical registry, published from GitHub Actions; any OCI registry can mirror it. Linux images may be public; macOS and Windows images are organisation-private.
5. hostweave consumes OCI only. The guestweave CLIs keep their from-source modes and must work without a registry.
6. Linux images use a bootc OCI image as the source of truth where practical, with upstream cloud images as the fallback; hostweave's QEMU runtime becomes an OCI consumer.

## Relationship to other repositories

- hostweave: [decision 0029, image identities and builds](https://github.com/deploymenttheory/hostweave/blob/main/docs/research/decisions/0029-image-identities-and-builds.md) and the [image lifecycle phase record](https://github.com/deploymenttheory/hostweave/blob/main/docs/implementation/image-lifecycle.md) describe the image catalogue this work plugs into.
- weaveplatform-manifest: the [trust chain](https://github.com/deploymenttheory/weaveplatform-manifest/blob/main/docs/trust-chain.md) that promotion extends to images.
- weaveplatform-agent-modules: the [release pipeline](https://github.com/deploymenttheory/weaveplatform-agent-modules/blob/main/docs/release-pipeline.md) whose ORAS push and dispatch the image pipeline mirrors.
- guestweave-cli-macos: `internal/docs/registries-and-image-formats.md` documents the registry profiles and codecs being replaced.
