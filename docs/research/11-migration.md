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
   ([hostweave ADR 0010](https://github.com/weaveplatform/hostweave/blob/main/docs/research/decisions/0010-vm-runtimes-guestweave-and-qemu.md)).
   A phase that changes `weave capabilities` output also bumps the pin and the contract tests.
8. **Quality gates on every phase.** A phase is done only when its godog acceptance features pass
   against real registries and the merged coverage gate is green: at least 95 % in total and at
   least 90 % per package, with `golangci-lint` and `govulncheck` clean
   ([0013](decisions/0013-quality-gates.md)). The implementation is Go; helper scripts may use
   other languages.
9. **Every profile is tested.** From phase 2b onwards each acceptance suite runs against
   `weave-zot` (referrers API) and `registry:3.1.2` (fallback tag). GHCR is covered by an opt-in
   smoke job, so the private and hybrid profiles never lag the github profile
   ([0011](decisions/0011-deployment-profiles-and-reference-registry.md)).

## Phases

| Phase | Repository | Delivers | Depends on | Done when |
|---|---|---|---|---|
| 0 | weaveplatform-oci | This research set; decision records 0001–0010 as Proposed | — | Records reviewed; open questions assigned owners ([12](12-open-questions.md)) |
| 1 | weaveplatform-oci | `spec`, `chunk`, `pack`, `conformance`; fixtures under `pkg/spec/testdata`; `weaveoci pack`, `inspect`, `unpack`, `healthcheck`. **Pulled forward from 2b at the owner's request so the correctness loop runs from the start:** the `weave-zot` image (`deploy/zot`: Dockerfile, `private` and `mirror` config roles, `compose.yaml`) and Docker-backed acceptance against `weave-zot` and `registry:3.1.2`. Quality-gate workflow `.github/workflows/quality-gate.yml` (unit matrix on `ubuntu-latest`, `macos-latest`, `windows-latest`; acceptance; blocking lint; govulncheck; cross-compile; merged coverage gate). Acceptance: `phase1_pack_inspect.feature`, `phase1_registry_roundtrip.feature` | 0 | **Implemented 2026-10-02.** Acceptance (16 scenarios) passes locally and the merged gate is green at 96.1%. A bundle packs to an index whose manifests pass `spec.Inspect`; repacking an unpacked bundle reproduces the digest; resume and sparse output verified; artifacts round-trip through weave-zot and registry:3 with digests unchanged; weave-zot refuses tag moves for the publisher and serves the referrers API |
| 2 | weaveplatform-oci; weaveplatform-api; weaveplatform-manifest; weaveplatform-agent | `client` (oras-go v2, profiles, mirrors, Range resume, HEAD skip, all blobs before manifests, referrers with tag fallback), `cache` (CAS, pins, LRU, quota, `oci-layout`), `verify` (sigstore-go bundle verification for attestations and cosign keys; channel verification via an extracted `manifestverify` package), `profile` (github, private, hybrid), `sign` (cosign key-based, file or KMS key, no transparency log), `publish` (the pipeline as a library); `weaveoci push/pull/verify/sign/publish/profile/export-layout/import-layout`; channel-manifest schema gains an `images[]` section. Acceptance: `phase2_push_pull.feature`, `phase2_sign_verify.feature`, `phase2_layout_airgap.feature` | 1 | Acceptance passes and the coverage gate is green. Round trip against an in-process registry (oras-go memory store) and against GHCR; `verify` accepts a bundle under the `sha256-<hex>` fallback tag; `channels/stable.json` with an `images[]` entry validates against the updated schema **Implemented in this repository 2026-10-02:** `client`, `cache`, `profile`, `sign`, `verify`, `publish`, `channel` and the CLI verbs; acceptance features `phase2_push_pull_sign`, `phase2_channel` and `phase2_layout_airgap` pass against weave-zot and registry:3, including `cosign verify` interop. **In the repositories that own them:** the channel-manifest schema change ([weaveplatform-agent-core#56](https://github.com/weaveplatform/weaveplatform-agent-core/pull/56)) and the promote workflow ([weaveplatform-channels#14](https://github.com/weaveplatform/weaveplatform-channels/pull/14)); `pkg/channel` output is validated against that schema. **Still deferred:** a real root key (Q29). |
| 2b | weaveplatform-oci | (the image, configs, compose and container acceptance landed in phase 1) Remaining: `deploy/zot/`: the `weave-zot` image (`FROM ghcr.io/project-zot/zot:v2.1.21` pinned by digest, config roles `private` and `mirror`, `weaveoci healthcheck` as `HEALTHCHECK`), `compose.yaml`; `release-images.yml` publishing multi-arch `ghcr.io/weaveplatform/weaveoci` and `ghcr.io/weaveplatform/weave-zot`, signed and attested ([0012](decisions/0012-container-images.md)); acceptance suites from phases 1–2 rerun against `weave-zot` and `registry:3.1.2` containers. Acceptance: `phase2b_weave_zot.feature` (startup, readiness, auth, create-without-update tag immutability, referrers API, mirror sync with digests preserved) | 2 | Acceptance passes and the coverage gate is green; both images are published, signed and pullable; `docker compose up` in `deploy/zot/` yields a registry that `weaveoci publish --profile private` can publish to and `weaveoci pull --verify=signature` can read from **Implemented in this repository 2026-10-02:** `release-images.yml` (ko for `weaveoci`, buildx for `weave-zot`, keyless cosign signatures, SPDX SBOM attestations, GitHub provenance when public, release archives with `SHA256SUMS`, a Compose smoke test on pull requests) and `phase2b_weave_zot.feature`. The images publish on the first release tag. |
| 3 | weaveplatform-oci; weaveplatform-manifest | `build-linux.yml` and `publish.yml`: bootc image or verified cloud image → raw → pack → push → `actions/attest push-to-registry` → self-verify → `repository_dispatch image-published` → promotion PR | 2b | Acceptance (`phase3_linux_publish.feature`) passes and the coverage gate is green. github profile: a public `ubuntu-24.04` (or `fedora-bootc-46`) image is promoted into `channels/stable.json` and `weaveoci pull --verify=channel` succeeds on a clean machine. private profile: the same build published by `weaveoci publish` from a non-GitHub runner to `weave-zot`, cosign key-signed, and pulled with `--verify=both` against a test channel root **Implemented in this repository 2026-10-02 (cloud-image path):** `pkg/source`, `weaveoci source fetch` and `bundle init`, `images/linux/ubuntu-24.04`, `build-linux.yml` and `image-ubuntu-24.04.yml` (boot test on every pull request; publish by hand), and `phase3_linux_publish.feature`. **Open:** the first publish and promotion of the real image, a real channel root (Q29), the bootc path and the baked-in agent. |
| 4 | guestweave-macos | Adopt the module for `pull`, `clone <ref>`, `push`, `images`, `login`; delete Tart and Lume codecs and the hand-ported client; `weave capabilities` reports `image_formats: ["weave-guest-v1"]`; push carries `hardwareModel` and auxiliary storage, strips ECID and MAC; Windows ARM64 VMs become pushable | 2 | Existing macOS acceptance suite passes with the module; a macOS image pushed from a Mac pulls and boots on another Mac; `grep -r cirruslabs internal/` is empty **Design 2026-10-02:** [14-guestweave-adoption.md](14-guestweave-adoption.md). guestweave stays usable standalone (`-tags standalone` links no OCI code); the cache, bases and pins come from `pkg/cache`; existing caches are discarded, not migrated; `capabilities` and `GUESTWEAVE_REGISTRY_ENV_ONLY` are added. |
| 5 | guestweave-windows | Adopt the module; raw chunks → cached parent VHD/VHDX via `disk/vhd` → differencing child; drop VHDX-v2 reader and writer; `weave capabilities` reports `weave-guest-v1`; `guest.vmgs` regenerated per clone | 2, outcome of the VHD-vs-VHDX spike ([12](12-open-questions.md)) | A Windows image published from the Windows pipeline (phase 7) or from a host pulls and boots; prune refuses to delete a parent with children **Design 2026-10-02:** [14-guestweave-adoption.md](14-guestweave-adoption.md). Fixed VHD as the intermediate, dynamic VHDX at rest, conversion by virtdisk (Q14 resolved); `guest.vmgs` regenerated; native Linux VMs are not pushable until the contract carries a kernel and rootfs. |
| 6 | hostweave | Server: `spec.Inspect` replaces the `inspectVM` classifier; `ImageVersion` gains `ArtifactType`, `OSVersion`, `TotalSize`, `Attestation`, `ChannelDigestPinned`; `RegistryConnection` gains mirrors; OpenAPI updated. Agent: QEMU pulls through `cache` and overlays with qcow2; `HOSTWEAVE_QEMU_IMAGES` removed; `attr.driver.qemu.formats` fingerprinted; scheduler filter consults every VM driver's formats; guestweave pin bumped to the phase 4/5 revisions | 4, 5 | `pkg/images` tests use the contract fixtures; a VM job lands on a Linux QEMU host from a channel-pinned image; macOS and Windows contract tests pass against the pinned CLIs |
| 7 | weaveplatform-oci | `build-macos.yml` (self-hosted Apple silicon, IPSW via restore-image lookup, unattended setup, agent baked) and `build-windows.yml` (go-sdk-winmediafoundry ISO, autounattend, QEMU/KVM on ubuntu runners or self-hosted); org-private repositories | 3, 4, 5 | A `macos-26-vanilla` and a `windows-11-base` image are promoted and pulled through hostweave and both guestweave CLIs |
| 8 | all | Hybrid profile validated with a site `weave-zot` in the `mirror` role (`preserveDigest`, `http.compat: ["docker2s2"]`); `oci-layout` export on one machine, import and verify on an air-gapped one; `delete-package-versions` retention tested against chunked and attested images | 3–7 | Acceptance (`phase8_mirror_airgap.feature`) passes; the drill runbook in [04](04-registries-and-github.md) and [13](13-deployment-profiles.md) passes end to end without touching GHCR from the air-gapped side |

Phases 4–7 each add their own acceptance features in the repository they change
(`phase4_guestweave_macos.feature`, `phase5_guestweave_windows.feature`,
`phase6_hostweave_oci.feature`, `phase7_macos_windows_publish.feature`) and must meet the same
coverage gate as weaveplatform-oci. Phase 2b was inserted after the original numbering was
published; later phase numbers are unchanged.

## Per-repository change lists

### weaveplatform-oci

- Replace the template `workload/` directory with the Go module: `spec`, `chunk`, `pack`, `client`,
  `cache`, `verify`, `profile`, `sign`, `publish`, `disk/vhd`, `cmd/weaveoci`
  ([10](10-shared-go-module.md)).
- Add `deploy/zot/` (the `weave-zot` image and Compose file), `test/acceptance/` (godog) and
  `scripts/` (coverage gate) ([13](13-deployment-profiles.md)).
- Add the quality-gate workflow and `release-images.yml`, which publishes `weaveoci` and
  `weave-zot` to GHCR ([0012](decisions/0012-container-images.md),
  [0013](decisions/0013-quality-gates.md)).
- Add reusable workflows `build-linux.yml`, `build-macos.yml`, `build-windows.yml`, `publish.yml`
  ([07](07-build-pipelines.md)).
- Add release-please configuration for the module tag (`v0.x`), mirroring
  `weaveplatform-agent-modules`.
- Keep `docs/research/` as the design record; decision records move from Proposed to Accepted as
  phases complete.

### weaveplatform-api and weaveplatform-manifest

- `weaveplatform-agent-core` (absorbed weaveplatform-api): `schema/channel-manifest.schema.json`
  gains `images[]` (`repository`, `tag`, `digest`, `platforms[]`, `signature`, `build_date`) and
  `sdk/manifest` parses it
  ([#56](https://github.com/weaveplatform/weaveplatform-agent-core/pull/56)). Modules and images
  share one channel document so a pinned channel is still one file. Still open: `weavemanifest pin`
  covering images.
- `weaveplatform-channels` (was weaveplatform-manifest): `promote.yml` handles the
  `image-published` dispatch and re-resolves the payload's digests from the registry before
  writing them ([#14](https://github.com/weaveplatform/weaveplatform-channels/pull/14)). Still
  open: checking the build-time signature in the promotion job.
- Channel verification for hostweave and guestweave: `pkg/channel` in this repository
  re-implements the chain check byte-compatibly, so core's `internal/manifestverify` does not
  need extracting.

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

Images already on `ghcr.io/weaveplatform` in the Tart encoding (pushed by guestweave-macos) or
the VHDX-v2 encoding (pushed by guestweave-windows) are converted once:

```sh
weaveoci republish ghcr.io/weaveplatform/<old-repo>:<tag> \
  ghcr.io/weaveplatform/weave-images/<family>-<major>-<variant>:<osver>-<build>-r1
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
  `--verify=signature` and `--verify=none` are explicit choices. `none` prints a warning.
- The channel manifest itself. A CLI with no channel configured pulls by digest or tag and
  verifies blob digests only.
- hostweave. guestweave is a standalone tool; hostweave drives it through the CLI boundary and
  never becomes a dependency.

## Rollback

| Phase | Rollback |
|---|---|
| 1–2 | Module versions are tagged; consumers pin a version. Nothing in production depends on them yet |
| 2b | Container images are tagged by version and pinned by digest in `compose.yaml`; roll back by pinning the previous digest. Registry data lives in a volume and survives an image downgrade |
| 3 | Un-promote by reverting the channel PR; the published image stays in GHCR but is not trusted |
| 4 | guestweave-macos release before the module adoption is tagged; hostweave keeps its earlier pin. Images pushed under the contract remain readable by later versions **Design 2026-10-02:** [14-guestweave-adoption.md](14-guestweave-adoption.md). guestweave stays usable standalone (`-tags standalone` links no OCI code); the cache, bases and pins come from `pkg/cache`; existing caches are discarded, not migrated; `capabilities` and `GUESTWEAVE_REGISTRY_ENV_ONLY` are added. |
| 5 | Same as 4 for guestweave-windows; cached VHDX-v2 parents remain usable by the previous release **Design 2026-10-02:** [14-guestweave-adoption.md](14-guestweave-adoption.md). Fixed VHD as the intermediate, dynamic VHDX at rest, conversion by virtdisk (Q14 resolved); `guest.vmgs` regenerated; native Linux VMs are not pushable until the contract carries a kernel and rootfs. |
| 6 | hostweave migration `0017` is additive (new JSON fields); downgrading the server leaves the extra fields ignored. QEMU rollback restores `HOSTWEAVE_QEMU_IMAGES` |
| 7 | Disable the workflow; the last promoted digest stays pinned |
| 8 | Operational only; remove the mirror profile |
