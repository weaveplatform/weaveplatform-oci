# 0001: One weave-native VM artifact contract

Status: Proposed

## Context

Three weave projects move VM images through OCI registries and none of them agree on
the bytes. guestweave-cli-macos pushes a third-party macOS wire format (LZ4 slices of a raw disk
under an OCI image config stub) and pulls three further third-party encodings. guestweave-cli-windows pushes its own
`application/vnd.guestweave.vm.disk.v2.vhdx+zstd` format: 1 GiB windows of a flattened
VHDX *file*, so chunk offsets address file bytes rather than guest sectors. hostweave
recognises four formats in `pkg/images/vm.go` and rejects image indexes outright. There
is no Linux VM artifact at all; the QEMU runtime reads a local path map. Neither CLI can
pull the other's images, firmware and identity state is not modelled, and nothing
declares an `artifactType`.

The project owner has decided that one contract replaces all of these and that it must
have no dependency on third-party VM image tooling. The requirement is a format that every weave consumer can
read, that survives GHCR's 10 GB per-layer and ten-minute upload limits, that supports
parallel, resumable and sparse-aware transfer, and that carries enough metadata for a
registry listing to be useful without downloading blobs.

## Decision

The normative text is [09-artifact-contract-v1.md](../09-artifact-contract-v1.md). This
record fixes the shape; the contract fixes every field.

**Media types** (byte-exact; these strings appear identically in the contract, the shared
module and this record):

| Role | Media type |
|---|---|
| `artifactType` and `config.mediaType` | `application/vnd.weave.guest.config.v1+json` |
| disk chunk layer | `application/vnd.weave.guest.disk.v1.raw+zstd` |
| macOS Virtualization.framework auxiliary storage | `application/vnd.weave.guest.state.auxstorage.v1` |
| UEFI variable store | `application/vnd.weave.guest.state.uefivars.v1` |
| firmware and vTPM policy | `application/vnd.weave.guest.state.firmware-policy.v1+json` |
| reserved, unused in v1 | `application/vnd.weave.guest.state.seed.v1+tar` |
| index | `application/vnd.oci.image.index.v1+json` (standard) |

The manifest is an ordinary OCI image manifest. Its `config` descriptor and its
`artifactType` both carry `application/vnd.weave.guest.config.v1+json`, which is the second
conformant shape in the OCI artifacts guidance (artifact-specific config media type).
The OCI image config media type is never used for a VM artifact.

The media-type noun is `guest` rather than `vm` because guestweave-cli-windows already
recognises legacy `application/vnd.weave.vm.config.v1+json` and
`application/vnd.weave.vm.disk.v1.vhdx+gzip` strings
(`weaveplatform/guestweave-cli-windows@main internal/oci/oci.go:59-60`); a new
contract must not be byte-identical to an old format with different semantics.

**Disk encoding.** A disk is the raw guest block device, cut into fixed 512 MiB chunks of
guest LBA space (the last chunk may be shorter). Each chunk is one zstd frame with the
content size recorded in the frame header, so every chunk decompresses independently.
Every chunk index from 0 to N-1 is present as a layer, in ascending order. A chunk that
is entirely zero is encoded as the canonical zstd zeros blob, which gives it one shared
digest across all images, and is annotated `run.weaveplatform.guest.disk.chunk.zero: "true"`
so a consumer may skip the fetch and punch a hole. Consumers verify the compressed
digest from the descriptor and the uncompressed digest from the annotation.

**Config.** `schemaVersion: 1` with `guest`, `firmware`, `disks[]`, `state[]`,
`resources`, `provisioning` and `build` objects. The validator rejects any config that
carries `ecid`, `machineIdentifier`, `macAddress`, TPM or `vmgs` bytes, or display
settings; those are per-instance values ([0009](0009-guest-state-carry-vs-regenerate.md)).
Sizes are bytes.

**State blobs** are typed layers with `run.weaveplatform.guest.state.name` and
`.state.semantics` (`carry` or `regenerate`) annotations. macOS ships its auxiliary
storage and its hardware model (in the config); nothing ships an identity.

**Index.** One index per repository tag, one child manifest per `(os, architecture)`
actually built. `platform.os` and `platform.architecture` use GOOS/GOARCH values
(`darwin|windows|linux`, `arm64|amd64`). `os.version` is `"26.0.1"` style for darwin,
`"10.0.26200.6584"` style for windows, and omitted for linux. The hypervisor is not a
platform axis because the disk is raw; `run.weaveplatform.guest.hypervisors` is an
advisory annotation.

**Annotations.** Layer: `run.weaveplatform.guest.disk.{name,chunk.index,chunk.offset,chunk.size,chunk.digest,chunk.zero}`.
Manifest and index child: the `org.opencontainers.image.*` set plus
`run.weaveplatform.guest.{os,arch,osVersion,osBuild,disk.totalSize,hypervisors}`
so that `weave images` and the hostweave catalogue never need the config blob. Every
descriptor carries `org.opencontainers.image.title`.

**Policy, not protocol.** The chunk size, the zstd choice and the annotation namespace
are weave policy. The manifest shape, the empty-descriptor rules, the referrers fallback
and the platform value rules are OCI 1.1 protocol requirements and are not negotiable.

## Rationale

The contract meets the requirement because a raw guest disk is the one encoding every
hypervisor in the fleet can derive its native format from: Virtualization.framework
boots raw directly (or converts to ASIF), QEMU uses raw as a qcow2 backing file, and HCS
consumes a VHD or VHDX produced from raw on the consumer side. Chunking by guest LBA
means two images that share most of their disk also share most of their blobs, which the
Windows file-offset encoding could never guarantee. Fixed chunks under 1 GiB keep every
blob inside GHCR's limits, allow a HEAD-before-upload skip, and make a pull resumable at
chunk granularity using Range requests, which GHCR's CDN honours.

Alternatives considered:

- **Per-platform native formats under one index** (the legacy macOS wire format, KubeVirt containerDisk
  for Linux and Windows, VHDX sibling for Hyper-V). Maximum ecosystem compatibility, but
  three encodings, three code paths in every consumer, and no single contract to
  validate. Rejected because the stated goal is consistency.
- **The legacy macOS wire format everywhere.** Simplest for guestweave-macos today, but
  the OCI image config stub is historical non-conformance, LZ4 is the only codec, the
  ECID ships inside the artifact, and the format is controlled by a third party whose
  ownership changed in 2026. Rejected by the project owner; see
  [0010](0010-remove-tart-and-lume-compatibility.md).
- **containerDisk for Linux only.** A single gzip tar layer with the disk at `/disk/`
  cannot exceed 10 GB compressed on GHCR, cannot resume, and cannot dedupe across
  versions. Kubernetes interoperability is not a weave requirement. Rejected; recorded as
  prior art only.
- **Content-defined chunking.** No production tool applies CDC to VM disk layers, and the
  rolling-hash formats that exist (`zstd:chunked`, PuzzleFS, Nydus) are tar-level
  mechanisms for container rootfs. Dedupe across versions comes from stable guest-LBA
  chunks and from lineage (qcow2 backing, differencing VHDX, APFS clones) instead.
- **Empty config plus `artifactType` only.** The guidance prefers this when there is no
  structured metadata. A VM has structured metadata (hardware model, firmware, disk
  geometry), so a custom config media type is the correct shape, and third-party images on
  GHCR with custom config media types prove the registry accepts them.

## Constraints

- The chunk size (256 versus 512 MiB) and the zstd level have not been benchmarked on
  real macOS and Windows disks. The contract fixes 512 MiB for v1; a benchmark may move
  it in v2, never within v1.
- ASIF disks on macOS 26 and later must be converted to raw at push. Whether to allow an
  ASIF-native variant later is open.
- Windows consumers need a raw-to-VHD or VHDX step. A fixed VHD is a raw file plus a
  512-byte footer; a dynamic VHDX needs a writer. The choice is a spike
  ([12-open-questions.md](../12-open-questions.md)).
- `os.version` values are implementation-defined in the OCI spec; a registry or tool
  "MAY refuse" unknown versions. No registry probed on 2026-10-02 did so, but the
  behaviour is not guaranteed.
- No registry enforces the manifest shape; a conformance validator in the shared module
  is the only gate.

## Verification

- Fixtures to exist under `pkg/spec/testdata/`: `macos-26.0-25A354.manifest.json`,
  `windows-11-25H2-26200.6584.manifest.json`, `ubuntu-24.04-20260915.manifest.json`, an
  index for each, and a config blob for each.
- Conformance tests to exist in the shared module: `TestConformance_MediaTypes`,
  `TestConformance_ChunkOrderAndCoverage`, `TestConformance_ZeroChunkDigest`,
  `TestConformance_ConfigRejectsIdentityFields`, `TestConformance_IndexPlatforms`.
- Round-trip test: pack a sparse 8 GiB raw disk, push to an in-process registry
  (oras-go `memory` store), pull, reassemble, compare sha256 and allocated size.
- Evidence already recorded: live probes on 2026-10-02 showing GHCR returns 206 on
  ranged blob GETs, 404 on the referrers endpoint, and accepts a custom config media
  type (a public third-party macOS image; see [04-registries-and-github.md](../04-registries-and-github.md)).

## References

- [09-artifact-contract-v1.md](../09-artifact-contract-v1.md), [01-oci-primer.md](../01-oci-primer.md), [06-large-artifacts.md](../06-large-artifacts.md), [03-current-state.md](../03-current-state.md)
- OCI image-spec v1.1.1: <https://github.com/opencontainers/image-spec/blob/v1.1.1/manifest.md>, <https://github.com/opencontainers/image-spec/blob/v1.1.1/image-index.md>, <https://github.com/opencontainers/image-spec/blob/v1.1.1/artifacts-guidance.md>
- GHCR limits: <https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry>
- CNCF ModelPack as a template for typed layers: <https://github.com/modelpack/model-spec/blob/main/docs/spec.md>
- `weaveplatform/hostweave@main pkg/images/vm.go:22-150` (formats being replaced)
- `weaveplatform/guestweave-cli-windows@main internal/oci/layer.go` (file-offset VHDX chunks being replaced)
