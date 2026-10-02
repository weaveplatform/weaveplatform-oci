# 0004: Shared Go module in weaveplatform-oci

Status: Proposed

## Context

Three OCI implementations exist in the weave projects. guestweave-cli-macos carries a
hand-ported registry client of 627 lines plus four pull codecs; guestweave-cli-windows
has its own client, layer format and cache on go-containerregistry; hostweave has a
resolve-and-pin layer on go-containerregistry and no agent-side fetch for QEMU. Each
has its own cache layout, credential chain and notion of a valid artifact. The format
knowledge is duplicated between `hostweave/pkg/images/vm.go` and what each CLI reports
through `weave capabilities`. The project owner has decided that `weaveplatform-oci`
owns the artifact specification, a shared Go module and the publication workflows.

## Decision

Module path `github.com/deploymenttheory/weaveplatform-oci`, Go 1.27, `CGO_ENABLED=0`,
`oras.land/oras-go/v2` pinned at v2.6.2 as the registry library, `github.com/klauspost/compress/zstd`
for compression, `github.com/sigstore/sigstore-go` for bundle verification and
`github.com/sigstore/sigstore/pkg/signature` for key-based signing. The template
`workload/` directory is replaced by the packages below.

| Package | Responsibility |
|---|---|
| `spec` | media-type and annotation constants, config types, validator, embedded JSON Schema, `Inspect(manifest) (Description, error)`, platform helpers, canonical zero-chunk digest |
| `chunk` | chunker: raw reader to chunk descriptors (zero detection, zstd, digests); reassembler with sparse writes behind a `HolePuncher` interface |
| `pack` | bundle directory to manifest and index via `oras.PackManifest(PackManifestVersion1_1)`; index assembly from children |
| `client` | profiles (host, organisation, insecure, ordered mirrors); credential chain (Docker config, `WEAVE_REGISTRY_*` env, keychain hook interface); resolve; fetch with Range resume and retry; push with mount and HEAD skip; referrers discovery with fallback tag; tag list |
| `cache` | content-addressed store `blobs/sha256/`, `refs/` index, in-use pins, LRU GC, quota, `DiskSpaceGuard` interface, `oci-layout` import and export |
| `verify` | Sigstore bundle verification against a trusted root; channel-manifest verification through an extracted `weaveplatform-manifest` verifier package; a policy combinator |
| `disk/vhd` | raw to fixed VHD (footer) and, pending a spike, dynamic VHDX; used only by the Windows consumer |
| `sign` | key-based signing that produces a cosign-compatible Sigstore bundle (v0.3) stored as an OCI 1.1 referrer with the fallback tag where the registry lacks the referrers API; keys from a file, `env://` or a KMS URI; no transparency log ([0006](0006-trust-attestations-and-channel-manifest.md)) |
| `profile` | deployment-profile configuration (`github`, `private`, `hybrid`): canonical registry, mirrors, signature provider and verification material, channel URL and trust anchors ([0011](0011-deployment-profiles-and-reference-registry.md)) |
| `publish` | orchestration of every publication stage: refuse an existing build tag, pack, validate, push, sign (key) or hand off to GitHub attestation, self-verify by re-pull on a clean cache, emit the promotion request ([0007](0007-publication-pipeline.md)) |
| `cmd/weaveoci` | `pack`, `push`, `pull`, `inspect`, `verify`, `sign`, `publish`, `profile`, `export-layout`, `import-layout`, `republish`, `gc`, `healthcheck` |
| `.github/workflows/` | reusable `build-macos.yml`, `build-windows.yml`, `build-linux.yml`, `publish.yml` (thin wrappers over `weaveoci publish`); `ci.yml` quality gates ([0013](0013-quality-gates.md)); `release-images.yml` ([0012](0012-container-images.md)) |
| `deploy/zot/` | `Dockerfile`, `config/store.json`, `config/mirror.json`, `compose.yaml` for `weave-zot` ([0011](0011-deployment-profiles-and-reference-registry.md), [0012](0012-container-images.md)) |
| `test/acceptance/` | godog features and steps run against the real `weaveoci` binary and real registries ([0013](0013-quality-gates.md)) |

**Consumers and what they delete.**

- hostweave server: `pkg/images/vm.go` `inspectVM` is replaced by `spec.Inspect`;
  `pkg/images/registry.go` keeps resolve-and-pin and gains index acceptance plus
  attestation and channel evidence; `pkg/types/image.go` gains `ArtifactType`,
  `OSVersion`, `TotalSize`, `Attestation` and `ChannelDigestPinned` on `ImageVersion`
  and `Mirrors` on `RegistryConnection`; the OpenAPI schemas and paths are extended.
- hostweave agent, QEMU: `agent/runtime/qemu/qemu.go` pulls through `cache` and
  `client` into a raw base and creates the qcow2 overlay per attempt;
  `HOSTWEAVE_QEMU_IMAGES` is removed.
- hostweave agent, guestweave: unchanged shell-out; `capabilities.go` expects
  `image_formats: ["weave-guest-v1"]`.
- guestweave-cli-macos deletes `internal/oci/{codec_tart,codec_lume_chunked,codec_lume_sharded,codec_lume_lz4,format,lume_manifest,oci_registry,oci_manifest,oci_layerizer_disk,oci_layerizer_diskv2,locallayercache,oci_authentication,oci_authenticationkeeper,oci_wwwauthenticate,oci_remotename,oci_digest}.go`
  and `internal/vm/storage/{oci,lume,registry}.go`; profile resolution moves from
  `internal/registry/resolver.go` into `client`; `internal/ipsw`, the install path in
  `internal/vm/vm.go`, `pkg/iso` and `internal/fsutil/clone.go` stay. Push writes the raw
  `disk.img` (ASIF converted to raw) and `nvram.bin` as an auxiliary-storage state layer.
  The macOS purgeable-space implementation of `DiskSpaceGuard` stays in guestweave-macos.
- guestweave-cli-windows deletes `internal/oci/{oci,layer}.go` and
  `internal/oci/cache/cache.go`; pull fetches raw chunks, produces the cached parent
  VHD or VHDX through `disk/vhd`, and creates the differencing child as today;
  `internal/winmedia`, `internal/linuxmedia` and `internal/nativebuild` stay.

**Boundaries.** The module never opens a hypervisor, never installs an OS, never reads
a VM `config.json` of either CLI, and has no network dependency beyond the registry. OS
keychains, VZ, HCS and QEMU stay in the consumers behind the interfaces above.

## Rationale

oras-go v2 is the library written for the artifact shape OCI 1.1 defines: it packs
manifests with `artifactType`, discovers referrers with the tag fallback, copies to and
from `oci-layout` stores, and fetches with Range. Its one gap, chunked upload, is
avoided by the contract's 512 MiB blobs, which push as short monolithic PUTs with retry.
go-containerregistry is kept by hostweave for ordinary container images, where it is the
better fit. One module gives one validator, one cache layout, one credential chain and
one set of fixtures, which is the only way the "consistency" requirement can be tested
rather than asserted.

Alternatives considered:

- **Spec plus workflows only, every consumer implements its own client.** Less
  coupling, but it preserves the three implementations that caused the problem.
  Rejected.
- **regclient as the registry library.** It has chunked and resumable push and per-host
  limits. Rejected for now because oras-go's artifact-first API matches the contract more
  closely; regclient remains the fallback if a registry ever demands chunked upload.
- **go-containerregistry everywhere.** Mature for images, awkward for custom
  artifacts, and known to struggle with multi-GB blobs. Rejected for the VM path.
- **Importing the guestweave CLIs as libraries.** Their code is under `internal/` by
  design; the direction of dependency must be CLI to module, not the reverse.

## Constraints

- oras-go v2 pushes blobs monolithically; a registry that advertises
  `OCI-Chunk-Min-Length` larger than a chunk, or rejects PUTs of 512 MiB, is not
  supported in v1.
- The channel-manifest verifier currently lives in
  `weaveplatform-agent/internal/manifestverify` and is not importable; extracting it
  into an importable package, and extending the schema in `weaveplatform-api` to carry
  image digests, are prerequisites for `verify`.
- The private `weaveplatform-api` and `weaveplatform-sdk` modules need a read token in
  CI, as the agent-modules repository already documents.
- `go.work` at `~/GitHub/weave/go.work` is untracked; every consumer must validate with
  `GOWORK=off` before release.
- Coverage gate follows hostweave practice and [0013](0013-quality-gates.md): ≥95% merged
  total, ≥90% per package.

## Verification

- `spec`: conformance suite over `pkg/spec/testdata/` fixtures (see
  [0001](0001-vm-artifact-contract.md)); fuzz test on the config validator.
- `chunk`: property test that chunk, reassemble and compare is the identity for random
  sparse inputs; zero-chunk digest equals the constant.
- `client` and `cache`: tests against an in-process registry built on oras-go
  `memory` and `oci` stores; a Range-resume test that kills the transfer mid-blob.
- `verify`: a bundle produced by `actions/attest` in this repository's own CI is
  verified against a pinned trusted root; a channel manifest signed with a test key
  chain is verified and a tampered digest is rejected.
- `cmd/weaveoci`: golden tests for `inspect` output; a `republish` test that converts
  a legacy fixture into a conformant artifact.
- `sign`: a bundle signed by `sign` with a test key verifies with `cosign verify --key
  --insecure-ignore-tlog` (cosign v3.1.3) and with the module's own `verify`, on zot
  (referrers API) and on `registry:3.1.2` (fallback tag).
- `publish`: acceptance features run the whole stage sequence against `weave-zot`
  ([0013](0013-quality-gates.md)).
- Consumer contract tests in hostweave and both CLIs run against the module's fixtures.

## References

- [10-shared-go-module.md](../10-shared-go-module.md), [09-artifact-contract-v1.md](../09-artifact-contract-v1.md), [11-migration.md](../11-migration.md)
- oras-go v2: <https://github.com/oras-project/oras-go>, <https://github.com/oras-project/oras-go/blob/v2/pack.go>; chunked push in v3 only: <https://github.com/oras-project/oras-go/pull/1434>
- regclient: <https://github.com/regclient/regclient>; go-containerregistry: <https://github.com/google/go-containerregistry>
- sigstore-go: <https://github.com/sigstore/sigstore-go>
- `deploymenttheory/guestweave-cli-macos@main internal/oci/oci_registry.go`, `internal/oci/format.go`, `internal/vm/storage/registry.go:71-163`
- `deploymenttheory/guestweave-cli-windows@main internal/oci/oci.go`, `internal/oci/layer.go`, `internal/oci/cache/cache.go`
- `deploymenttheory/hostweave@main pkg/images/vm.go`, `pkg/images/registry.go:103-236`, `agent/runtime/qemu/qemu.go`
- `deploymenttheory/weaveplatform-agent-modules@main handoff/repositories.md` (dependency direction, `GOWORK=off`)
