# Migration

Research baseline: 2026-10-02. This document sequences the move from today's three incompatible
image encodings ([03-current-state.md](03-current-state.md)) to the target in
[08-target-architecture.md](08-target-architecture.md). Phases are ordered by dependency, not by
date. Each phase names the repository it changes and what "done" means.

## Principles

1. **Digest compatibility.** A digest minted in phase 1 must still verify in phase 8. The contract
   is versioned in its media types (`…v1…`); additive changes only
   ([09](09-artifact-contract-v1.md)).
2. **No flag day.** Each consumer adopts the shared module behind its existing verbs. `weave pull`,
   `weave clone`, `weave push`, `hwctl images resolve` keep their names and output shapes.
3. **guestweave works with no registry.** `--from-ipsw`, `--from-windows`, `--linux --iso`, local
   `clone`, `export` and `import` never require network access to a registry
   ([0003](decisions/0003-consumer-modes.md)).
4. **hostweave is OCI-only.** No phase adds a non-OCI VM image path to hostweave. The QEMU runtime's
   `HOSTWEAVE_QEMU_IMAGES` name→path map is removed when its OCI consumer lands.
5. **Linux first.** The Linux pipeline carries no licensing risk and exercises every component, so it
   proves the chain before macOS and Windows.
6. **Old encodings are re-published, not read.** Images already pushed in the Tart or VHDX-v2
   encodings are converted once with `weaveoci republish`; no consumer keeps a reader for them
   ([0010](decisions/0010-remove-tart-and-lume-compatibility.md)).
7. **Companion pins move together.** hostweave pins guestweave commits and verifies binaries
   ([hostweave ADR 0010](https://github.com/deploymenttheory/hostweave/blob/main/docs/research/decisions/0010-vm-runtimes-guestweave-and-qemu.md)).
   A phase that changes `weave capabilities` output also bumps the pin and the contract tests.

## Phases

| Phase | Repository | Delivers | Depends on | Done when |
|---|---|---|---|---|
| 0 | weaveplatform-oci | This research set; decision records 0001–0010 as Proposed | — | Records reviewed; open questions assigned owners ([12](12-open-questions.md)) |
| 1 | weaveplatform-oci | `spec`, `chunk`, `pack`; fixtures under `spec/testdata`; `weaveoci pack` and `inspect`; conformance tests | 0 | A bundle packs to an index whose manifests pass `spec.Inspect`; zero-chunk and resume fixtures pass; media-type strings match [09](09-artifact-contract-v1.md) byte for byte |
| 2 | weaveplatform-oci; weaveplatform-api; weaveplatform-manifest; weaveplatform-agent | `client` (oras-go v2, profiles, mirrors, Range resume, HEAD skip, referrers with tag fallback), `cache` (CAS, pins, LRU, quota, `oci-layout`), `verify` (sigstore-go bundle verification; channel verification via an extracted `manifestverify` package); `weaveoci push/pull/verify/export-layout/import-layout`; channel-manifest schema gains an `images[]` section | 1 | Round trip against an in-process registry (oras-go memory store) and against GHCR; `verify` accepts a bundle under the `sha256-<hex>` fallback tag; `channels/stable.json` with an `images[]` entry validates against the updated schema |
| 3 | weaveplatform-oci; weaveplatform-manifest | `build-linux.yml` and `publish.yml`: bootc image or verified cloud image → raw → pack → push → `actions/attest push-to-registry` → self-verify → `repository_dispatch image-published` → promotion PR | 2 | A public `ubuntu-24.04` (or `fedora-bootc-46`) image is promoted into `channels/stable.json`; `weaveoci pull --verify=channel` succeeds on a clean machine |
| 4 | guestweave-macos | Adopt the module for `pull`, `clone <ref>`, `push`, `images`, `login`; delete Tart and Lume codecs and the hand-ported client; `weave capabilities` reports `image_formats: ["weave-guest-v1"]`; push carries `hardwareModel` and auxiliary storage, strips ECID and MAC; Windows ARM64 VMs become pushable | 2 | Existing macOS acceptance suite passes with the module; a macOS image pushed from a Mac pulls and boots on another Mac; `grep -r cirruslabs internal/` is empty |
| 5 | guestweave-windows | Adopt the module; raw chunks → cached parent VHD/VHDX via `disk/vhd` → differencing child; drop VHDX-v2 reader and writer; `weave capabilities` reports `weave-guest-v1`; `guest.vmgs` regenerated per clone | 2, outcome of the VHD-vs-VHDX spike ([12](12-open-questions.md)) | A Windows image published from the Windows pipeline (phase 7) or from a host pulls and boots; prune refuses to delete a parent with children |
| 6 | hostweave | Server: `spec.Inspect` replaces the `inspectVM` classifier; `ImageVersion` gains `ArtifactType`, `OSVersion`, `TotalSize`, `Attestation`, `ChannelDigestPinned`; `RegistryConnection` gains mirrors; OpenAPI updated. Agent: QEMU pulls through `cache` and overlays with qcow2; `HOSTWEAVE_QEMU_IMAGES` removed; `attr.driver.qemu.formats` fingerprinted; scheduler filter consults every VM driver's formats; guestweave pin bumped to the phase 4/5 revisions | 4, 5 | `pkg/images` tests use the contract fixtures; a VM job lands on a Linux QEMU host from a channel-pinned image; macOS and Windows contract tests pass against the pinned CLIs |
| 7 | weaveplatform-oci | `build-macos.yml` (self-hosted Apple silicon, IPSW via restore-image lookup, unattended setup, agent baked) and `build-windows.yml` (go-sdk-winmediafoundry ISO, autounattend, QEMU/KVM on ubuntu runners or self-hosted); org-private repositories | 3, 4, 5 | A `macos-26-vanilla` and a `windows-11-base` image are promoted and pulled through hostweave and both guestweave CLIs |
| 8 | all | Mirror profile validated against zot with `preserveDigest`; `oci-layout` export on one machine, import and verify on an air-gapped one; `delete-package-versions` retention tested against chunked and attested images | 3–7 | The drill runbook in [04](04-registries-and-github.md) passes end to end without touching GHCR from the air-gapped side |

## Per-repository change lists

### weaveplatform-oci

- Replace the template `workload/` directory with the Go module: `spec`, `chunk`, `pack`, `client`,
  `cache`, `verify`, `disk/vhd`, `cmd/weaveoci` ([10](10-shared-go-module.md)).
- Add reusable workflows `build-linux.yml`, `build-macos.yml`, `build-windows.yml`, `publish.yml`
  ([07](07-build-pipelines.md)).
- Add release-please configuration for the module tag (`v0.x`), mirroring
  `weaveplatform-agent-modules`.
- Keep `docs/research/` as the design record; decision records move from Proposed to Accepted as
  phases complete.

### weaveplatform-api and weaveplatform-manifest

- `weaveplatform-api/schema/channel-manifest.schema.json`: add `images[]` with `repository`,
  `tag`, `digest`, `platforms[]`, `osBuild`, `promotedAt`. Modules and images share one channel
  document so a pinned channel is still one file.
- `weaveplatform-manifest`: handle the `image-published` dispatch; add the attestation check to the
  promotion PR; extend `weavemanifest promote` and `pin` to cover images.
- `weaveplatform-agent/internal/manifestverify`: extract to an importable package (or publish a
  copy under `weaveplatform-oci/verify/channel`) so hostweave and guestweave can verify without
  importing core.

### guestweave-macos

- Delete `internal/oci/` (client, manifest, layerizers, codecs, authentication, remote-name,
  digest helpers) and `internal/vm/storage/{oci,lume,registry}.go`; keep `internal/registry`
  profile semantics by moving them into the module's `client` profiles.
- Keep `internal/ipsw`, the install path in `internal/vm/vm.go`, `pkg/iso`, `internal/winmedia`,
  `internal/fsutil/clone.go`, snapshots, export and import.
- `push`: pack raw `disk.img` (convert ASIF to raw when needed), carry `hardwareModel` and
  `nvram.bin`, strip `ecid` and `macAddress`; also pack `NVRAM.dat` for Windows ARM64 VMs as an
  optional UEFI-variables blob.
- `pull`/`clone`: select the darwin/arm64 (or linux/arm64, windows/arm64) child; regenerate ECID,
  `machineidentifier.bin` and MAC on clone; write `lineage.json` with the source digest.
- `weave capabilities`: `image_formats: ["weave-guest-v1"]`, `guest_platforms` unchanged.
- Fix the GB/GiB inconsistency for Windows guests while touching the create path.

### guestweave-windows

- Delete `internal/oci/{oci,layer}.go` and `internal/oci/cache/cache.go`; use the module's `cache`
  and `client`.
- Pull: reassemble raw → `disk/vhd` → parent in the cache keyed by digest → differencing child;
  `oci-source.json` keeps pinning the parent; prune still refuses in-use parents.
- Push: flatten the chain to raw guest LBAs, pack, strip `guest.vmgs` and identity.
- Keep `internal/winmedia`, `internal/linuxmedia`, `internal/nativebuild`, side volumes.
- `weave capabilities`: `image_formats: ["weave-guest-v1"]`.

### hostweave

- `pkg/images/vm.go`: replace `inspectVM` with `spec.Inspect`; accept indexes for VM purpose;
  remove the arm64-only and "VHDX cannot be macOS" rules (the contract's platform entries carry
  this).
- `pkg/types/image.go`: extend `ImageVersion` and `RegistryConnection` as listed in phase 6;
  migration `0017_images_contract.sql` for the JSON `doc` columns.
- `agent/runtime/qemu/qemu.go`: pull through the module; raw base plus qcow2 overlay; delete the
  `Options.Images` map and `HOSTWEAVE_QEMU_IMAGES`.
- `agent/runtime/guestweave/capabilities.go`: expect `weave-guest-v1`; bump pins and contract tests.
- `pkg/scheduler/filter.go`: generalise the VM format check from `attr.driver.guestweave.*` to any
  VM driver.
- `internal/agentgw/gateway.go`: no change to `Assign.registry_auth_json`; the module's `client`
  consumes the same Docker-style JSON.
- `docs/research/decisions/0029` and `docs/implementation/image-lifecycle.md`: amend to reference
  this contract instead of the four format names.

### weaveplatform-agent-modules

- No format change. The module release pipeline is the template for `publish.yml`; the handoff
  decision "the agent is baked in at image build time" is implemented by the build workflows
  (phases 3 and 7), which install the released module packages into the guest.

## Re-publishing existing images

Images already on `ghcr.io/deploymenttheory` in the Tart encoding (pushed by guestweave-macos) or
the VHDX-v2 encoding (pushed by guestweave-windows) are converted once:

```sh
weaveoci republish ghcr.io/deploymenttheory/<old-repo>:<tag> \
  ghcr.io/deploymenttheory/weave-images/<family>-<major>-<variant>:<osver>-<build>-r1
```

`republish` is a one-off command that lives in `cmd/weaveoci` only, reads the old manifest with a
throw-away decoder that is not part of the module's public API, reassembles the raw disk, strips
identity, packs and pushes under the contract. It is removed from `weaveoci` once phase 8 is done.
No consumer ever links the old decoders. Old repositories are left in place, made private if
they were public, and deleted after the channel manifest no longer references them.

## What stays optional in guestweave

- Any registry at all. A host with no network beyond vendor media endpoints can create, run,
  snapshot, export and import VMs.
- Registry profiles and mirrors. Fully qualified references keep working with no profile.
- Verification mode. `--verify=channel` is the default when a channel is configured;
  `--verify=attestation` and `--verify=none` are explicit choices. `none` prints a warning.
- The channel manifest itself. A CLI with no channel configured pulls by digest or tag and
  verifies blob digests only.
- hostweave. guestweave is a standalone tool; hostweave drives it through the CLI boundary and
  never becomes a dependency.

## Rollback

| Phase | Rollback |
|---|---|
| 1–2 | Module versions are tagged; consumers pin a version. Nothing in production depends on them yet |
| 3 | Un-promote by reverting the channel PR; the published image stays in GHCR but is not trusted |
| 4 | guestweave-macos release before the module adoption is tagged; hostweave keeps its earlier pin. Images pushed under the contract remain readable by later versions |
| 5 | Same as 4 for guestweave-windows; cached VHDX-v2 parents remain usable by the previous release |
| 6 | hostweave migration `0017` is additive (new JSON fields); downgrading the server leaves the extra fields ignored. QEMU rollback restores `HOSTWEAVE_QEMU_IMAGES` |
| 7 | Disable the workflow; the last promoted digest stays pinned |
| 8 | Operational only; remove the mirror profile |
