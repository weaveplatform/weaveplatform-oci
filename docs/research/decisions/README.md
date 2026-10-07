# Design decisions

These records set out the intended design of the weave image platform: how VM and
container images are packaged, published, verified, distributed and consumed across
hostweave, guestweave-cli-macos and guestweave-cli-windows. Each record gives the
context, contracts, rationale, constraints and the evidence that will verify it. Every
record carries its own status; the research reports in the parent directory
are the evidence they rest on, and acceptance follows the first implementation phase
that exercises the record.

Use the [template](TEMPLATE.md) for new decisions, and leave out sections that would be
empty. Integrate amendments into the relevant record, keeping its number and filename so
existing links still resolve. Records never contain implementation milestones. Sequencing
belongs in the [migration plan](../11-migration.md).

The [target architecture](../08-target-architecture.md) summarises how the decisions fit
together, the [artifact contract](../09-artifact-contract-v1.md) is the normative wire
format that 0001 adopts, and [prior art](../02-prior-art.md) records the projects these
decisions borrow from or deliberately avoid.

| Decision | Design |
|---|---|
| 0001 | [One weave-native VM artifact contract](0001-vm-artifact-contract.md) |
| 0002 | [Registry, repositories, tags and visibility](0002-registry-repositories-tags-visibility.md) |
| 0003 | [Consumer modes: hostweave OCI-only, guestweave from-source or OCI](0003-consumer-modes.md) |
| 0004 | [Shared Go module in weaveplatform-oci](0004-shared-go-module.md) |
| 0005 | [Linux images: bootc source of truth, cloud images fallback](0005-linux-images-bootc-and-cloud-images.md) |
| 0006 | [Trust: attestations at build, channel manifest at promotion](0006-trust-attestations-and-channel-manifest.md) |
| 0007 | [Publication pipeline](0007-publication-pipeline.md) |
| 0008 | [Device cache, garbage collection and mirrors](0008-device-cache-gc-and-mirrors.md) |
| 0009 | [Guest state: carry versus regenerate](0009-guest-state-carry-vs-regenerate.md) |
| 0010 | [Remove Tart and Lume compatibility from guestweave-macos](0010-remove-tart-and-lume-compatibility.md) |
| 0011 | [Deployment profiles and the reference private registry](0011-deployment-profiles-and-reference-registry.md) |
| 0012 | [Container images published by weaveplatform-oci](0012-container-images.md) |
| 0013 | [Quality gates: coverage and acceptance per phase](0013-quality-gates.md) |
| 0014 | [Image tiers: base images and derived images](0014-image-tiers.md) |
| 0015 | [One image release with target-specific delivery](0015-target-image-delivery.md) |

Fixed decisions taken by the project owner on 2026-10-02, which these records elaborate
and do not reopen:

1. One weave-native OCI artifact contract for VM images, with no dependency on Tart.
2. Trust is both GitHub artifact attestations at build time and the minisign channel
   manifest at promotion time.
3. `weaveplatform-oci` holds the artifact specification, a shared Go module and the
   reusable publication workflows.
4. GHCR is the canonical registry; any OCI registry can mirror it. Linux images may be
   public; macOS and Windows images are organisation-private.
5. hostweave consumes images only through OCI. The guestweave CLIs keep their
   from-source modes and must remain usable without any registry.
6. Linux images derive from bootc images where practical, otherwise from upstream cloud
   images with verified checksums.

Further decisions taken by the project owner on 2026-10-02 when the scope grew to
include private hosting and quality gates:

7. Three deployment profiles from one codebase: `github`, `private` and `hybrid`. zot is
   the reference private registry; Harbor and CNCF distribution v3 are supported targets.
8. The private profile signs with cosign-compatible key-based signing (file or KMS key)
   plus the minisign channel manifest.
9. `weaveplatform-oci` publishes its own container images to GHCR:
   `ghcr.io/weaveplatform/weaveoci` and `ghcr.io/weaveplatform/weave-zot`.
10. The implementation is Go, gated at ≥95% merged test coverage, and every
    implementation phase delivers its own acceptance tests.
