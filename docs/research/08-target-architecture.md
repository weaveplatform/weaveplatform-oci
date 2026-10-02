# Target architecture

Research baseline: 2026-10-02. This document describes the system the weave projects move
towards once the decisions in [decisions/](decisions/README.md) are accepted. It draws on the
reports [01](01-oci-primer.md)–[07](07-build-pipelines.md), the artifact contract in
[09](09-artifact-contract-v1.md) and the shared module in [10](10-shared-go-module.md). Sequencing
is in [11-migration.md](11-migration.md); unresolved points are in
[12-open-questions.md](12-open-questions.md).

The four fixed decisions that shape everything below: one weave-native VM artifact contract with no
Tart dependency ([0001](decisions/0001-vm-artifact-contract.md)); trust from build-time attestations
plus promotion-time channel manifests ([0006](decisions/0006-trust-attestations-and-channel-manifest.md));
`weaveplatform-oci` as spec, shared Go module and publication workflows
([0004](decisions/0004-shared-go-module.md)); GHCR canonical with any OCI registry as mirror
([0002](decisions/0002-registry-repositories-tags-visibility.md)).

```mermaid
flowchart TB
    subgraph build["Build (GitHub Actions)"]
        wf["reusable workflows<br/>build-macos / build-windows / build-linux / publish"]
        media["source media<br/>IPSW · ISO/ESD · cloud image · bootc image"]
        media --> wf
    end

    subgraph oci["weaveplatform-oci"]
        spec["spec<br/>media types · config schema · validator"]
        mod["Go module<br/>chunk · pack · client · cache · verify · disk/vhd"]
        cli["cmd/weaveoci"]
        spec --- mod --- cli
    end

    subgraph reg["Registries"]
        ghcr["ghcr.io/deploymenttheory/weave-images/*<br/>(canonical)"]
        mirror["mirror / pull-through<br/>(zot · Harbor · oci-layout)"]
        ghcr -. sync .-> mirror
    end

    subgraph trust["Trust"]
        attest["GitHub attestations<br/>Sigstore bundle as referrer"]
        chan["weaveplatform-manifest<br/>channels/stable.json (minisign chain)"]
    end

    subgraph consumers["Consumers"]
        hw["hostweave server<br/>resolve · pin · verify channel"]
        agent["hostweave agent<br/>runtimes: guestweave · qemu · moby"]
        gwm["guestweave-cli-macos<br/>VZ · HV.framework"]
        gww["guestweave-cli-windows<br/>HCS"]
    end

    wf -- "weaveoci pack/push" --> ghcr
    wf -- "actions/attest push-to-registry" --> attest
    attest -. "sha256-<digest> fallback tag" .-> ghcr
    wf -- "repository_dispatch image-published" --> chan
    chan -- "promotion PR re-signs stable.json" --> chan

    mod --> hw
    mod --> agent
    mod --> gwm
    mod --> gww

    hw -- "Assign{job_json, registry_auth_json}" --> agent
    hw -- "digest pinned in channel?" --> chan
    agent -- "weave pull/clone (shell-out)" --> gwm
    agent -- "weave pull (shell-out)" --> gww
    agent -- "pull via cache" --> ghcr
    gwm -- "pull via cache" --> ghcr
    gww -- "pull via cache" --> ghcr
    gwm -. "pull" .-> mirror
    gww -. "pull" .-> mirror
    media -. "from-source (no registry)" .-> gwm
    media -. "from-source (no registry)" .-> gww
```

## Components

| Component | Repository | Role in the target |
|---|---|---|
| Artifact spec | `weaveplatform-oci` (`spec/`, [09](09-artifact-contract-v1.md)) | The only VM artifact encoding any weave project publishes or consumes: `application/vnd.weave.guest.config.v1+json` config and artifactType, `application/vnd.weave.guest.disk.v1.raw+zstd` chunks, typed state blobs, one index per OS build |
| Shared Go module | `weaveplatform-oci` ([10](10-shared-go-module.md)) | `chunk`, `pack`, `client` (oras-go v2), `cache`, `verify`, `disk/vhd`, `cmd/weaveoci`. Replaces the three OCI clients that exist today |
| Publication workflows | `weaveplatform-oci/.github/workflows/` ([07](07-build-pipelines.md)) | Reusable `build-macos.yml`, `build-windows.yml`, `build-linux.yml`, `publish.yml`. Mirror the shape of `weaveplatform-agent-modules/.github/workflows/module-release.yml` |
| Canonical registry | `ghcr.io/deploymenttheory/weave-images/<family>-<major>[-<variant>]` ([04](04-registries-and-github.md)) | Linux repositories public; macOS and Windows repositories org-private. Tags are immutable `<osver>-<build>-r<rev>` plus moving `stable`, `edge`, `latest` |
| Mirrors | operator-run zot or Harbor; `oci-layout` directories for air gap | Registry profiles name a mirror list; digests are preserved so attestations and channel pins stay valid |
| Build-time trust | GitHub artifact attestations (`actions/attest`), Sigstore bundle pushed as a referrer ([05](05-supply-chain.md)) | SLSA provenance per image; on GHCR stored under the `sha256-<hex>` fallback tag because GHCR has no referrers API |
| Promotion-time trust | `weaveplatform-manifest` channel manifest, minisign root → signing key → `channels/stable.json` | The channel lists promoted image digests. hostweave and guestweave trust the channel; an air-gapped host verifies with the root key already embedded in core |
| hostweave server | `hostweave` (`pkg/images`, `internal/server`, `internal/agentgw`) | OCI-only. Resolves a selector to a digest with `pkg/images/registry.go`, inspects it with `spec.Inspect`, records a channel pin, binds the version to jobs and supplies, and sends per-task registry credentials in `Assign.registry_auth_json` (`internal/agentgw/gateway.go`) |
| hostweave agent | `hostweave` (`agent/runtime/{guestweave,qemu,moby}`, `pkg/scheduler/filter.go`) | Fingerprints `attr.driver.<name>.formats` and `.platforms`; pulls VM images through the shared `cache`; QEMU becomes an OCI consumer; guestweave remains a shell-out |
| guestweave-cli-macos | `guestweave-macos` | From-source (IPSW, Windows ARM64 ISO, Linux ISO) and OCI through the shared module. Tart and Lume codecs removed ([0010](decisions/0010-remove-tart-and-lume-compatibility.md)) |
| guestweave-cli-windows | `guestweave-windows` | From-source (retail ISO plus unattend side volume, Linux native or ISO) and OCI through the shared module. VHDX-v2 format removed; raw chunks are converted to a cached parent VHD/VHDX and cloned as differencing children |

## Data flows

Five flows cover the system. Each is drawn as a sequence below.

| Flow | Trigger | Produces |
|---|---|---|
| Publish | A workflow run in `weaveplatform-oci` (scheduled rebuild, source-media change, or manual) | A pushed index plus manifests, a Sigstore bundle referrer, a `repository_dispatch` to `weaveplatform-manifest` |
| Promote | The `image-published` dispatch | A promotion PR that adds the image digest to `channels/stable.json` and re-signs it; merge is the promotion act |
| hostweave dispatch | A requester submits a job that selects an image version | A pinned `ResolvedImage` on the workload, a placement on a host whose driver reports the format, a pull through the agent cache, a clone, a run |
| guestweave pull and clone | `weave pull <ref>` or `weave clone <ref> <name>` | A verified, cached image and a copy-on-write (macOS) or differencing (Windows) VM |
| guestweave from-source | `weave create --from-ipsw`, `--from-windows`, `--linux --iso` | A local VM bundle with no registry involved; optionally `weave push` afterwards |

### Publish

```mermaid
sequenceDiagram
    autonumber
    participant Src as Source media<br/>(Apple CDN · go-sdk-winmediafoundry · distro mirror · bootc image)
    participant Runner as Runner<br/>(self-hosted Apple silicon · ubuntu KVM)
    participant WO as weaveoci
    participant GHCR as ghcr.io/deploymenttheory/weave-images
    participant Att as actions/attest
    participant Man as weaveplatform-manifest

    Runner->>Src: fetch media, verify checksum / signature
    Runner->>Runner: install guest, bake agent, strip identity (host keys, MAC, ECID)
    Runner->>WO: weaveoci pack bundle/ --config build.json
    WO->>WO: chunk raw disk (512 MiB, zstd, zero detect), write config, state blobs
    WO->>GHCR: HEAD blobs (skip existing), PUT missing chunks, PUT manifest, PUT index
    GHCR-->>WO: index digest
    Runner->>Att: subject-name repo, subject-digest, push-to-registry
    Att->>GHCR: PUT bundle manifest with subject (fallback tag sha256-<hex>)
    Runner->>WO: weaveoci verify repo@digest --attestation
    WO->>GHCR: GET referrers → 404 → GET tag sha256-<hex> → fetch bundle
    WO-->>Runner: provenance verified against workflow identity
    Runner->>Man: repository_dispatch image-published {repository, tag, digest}
```

### Promote

```mermaid
sequenceDiagram
    autonumber
    participant Man as weaveplatform-manifest CI
    participant GHCR as ghcr.io
    participant PR as Promotion PR
    participant Human as Reviewer
    participant Chan as channels/stable.json

    Man->>GHCR: resolve repository:tag, confirm digest matches dispatch payload
    Man->>GHCR: fetch attestation (fallback tag), verify signer workflow and repo
    Man->>PR: add {repository, tag, digest, platforms, osBuild} under images[]
    Man->>PR: weavemanifest sign signing-2026.key channels/stable.json
    Human->>PR: review and merge (the promotion act)
    PR->>Chan: stable.json + stable.json.sig updated
    Note over Chan: channels/pinned/<service-version>.json are never touched by automation
```

### hostweave dispatch

```mermaid
sequenceDiagram
    autonumber
    participant Req as Requester (hwctl / UI)
    participant Srv as hostweave server
    participant Reg as Registry (pkg/images/registry.go)
    participant Chan as Channel manifest
    participant Sch as Scheduler (pkg/scheduler/filter.go)
    participant Ag as hostweave agent
    participant Cache as shared cache
    participant RT as Runtime (guestweave / qemu)

    Req->>Srv: POST /images/{id}/versions {selector, registry_connection_id}
    Srv->>Reg: resolve tag → digest, GET index, inspect children
    Reg->>Reg: spec.Inspect: artifactType, config schema, platforms, chunk rules
    Srv->>Chan: is digest listed in the trusted channel?
    Chan-->>Srv: pinned (or: not promoted → status metadata_validated only)
    Srv-->>Req: ImageVersion {Reference repo@sha256, Format weave-guest-v1, Platforms, ChannelDigestPinned}
    Req->>Srv: submit job {image_version_id, kind vm}
    Srv->>Srv: bindWorkloadImage: overwrite Workload.Image with repo@digest, snapshot ResolvedImage
    Srv->>Sch: evaluate
    Sch->>Sch: feasibility: attr.driver.<name>.formats ∋ weave-guest-v1, .platforms ∋ os/arch
    Sch-->>Srv: placement on host H
    Srv->>Ag: Assign{job_json, registry_auth_json} (internal/agentgw/gateway.go)
    Ag->>Cache: ensure(digest) with per-task credentials
    Cache->>Reg: fetch manifest, chunks (Range resume, mirror profile first)
    Cache->>Cache: verify compressed + uncompressed digests, sparse reassembly, pin in-use
    Ag->>RT: Prepare(workload, attempt)
    alt macOS or Windows host
        RT->>RT: weave clone <ref> hw-<attempt> (agent/runtime/guestweave)
    else Linux host
        RT->>RT: qemu-img create -f qcow2 -b <cache raw> hw-<attempt>.qcow2 (agent/runtime/qemu)
    end
    RT-->>Ag: Handle
    Ag->>RT: Start, Exec, CollectArtifacts, Destroy
    Ag->>Cache: unpin(digest)
```

hostweave never accepts a free-form image string for VM jobs in the target: `Workload.Image` is
always `repo@sha256:…` set by the server. Container jobs keep the moby runtime's engine pull
([0009 in hostweave](https://github.com/deploymenttheory/hostweave/blob/main/docs/research/decisions/0009-container-runtime-moby.md))
and are outside the VM contract.

### guestweave pull and clone

```mermaid
sequenceDiagram
    autonumber
    participant User as Operator
    participant CLI as weave (guestweave CLI)
    participant Prof as Registry profiles<br/>($XDG_CONFIG_HOME/weave/config.yaml)
    participant Cli as client (oras-go v2)
    participant Reg as Registry / mirror
    participant Ver as verify
    participant Cache as cache (CAS)
    participant Lay as VM bundle (internal/vm/layout/layout.go)

    User->>CLI: weave pull macos-26-base:stable  (or weave clone <ref> vm1)
    CLI->>Prof: resolve bare name → host/org, mirrors, insecure, credentials order
    CLI->>Cli: resolve(ref)
    Cli->>Reg: GET manifests/stable (mirror first, canonical on miss)
    Reg-->>Cli: index digest + child manifests
    Cli->>Cli: select child by platform {darwin, arm64}
    CLI->>Ver: verify(digest) per --verify=channel|attestation|none
    Ver->>Ver: channel: root → signing key → stable.json → digest listed
    Ver->>Reg: attestation: referrers → fallback tag → bundle → sigstore-go
    Ver-->>CLI: ok
    CLI->>Cache: ensure(digest)
    Cache->>Cache: disk-space guard (purgeable-aware on macOS), LRU prune if needed
    Cache->>Reg: fetch missing chunks in parallel, Range resume
    Cache->>Cache: verify descriptor digest and uncompressed digest per chunk
    Cache->>Cache: write disk.img sparsely (zero chunks → hole), store state blobs
    CLI->>Lay: clone
    alt macOS host
        Lay->>Lay: clonefile(2) disk.img (internal/fsutil/clone.go), copy nvram.bin, new ECID + MAC
    else Windows host
        Lay->>Lay: raw → cached parent VHD/VHDX once, new differencing child, new vmgs/vTPM
    end
    Lay-->>User: vm1 ready, lineage.json records source digest
```

### guestweave from-source

```mermaid
sequenceDiagram
    autonumber
    participant User as Operator
    participant CLI as weave
    participant Src as Source
    participant VMM as Hypervisor
    participant Lay as VM bundle
    participant WO as weaveoci (optional)

    User->>CLI: weave create vm1 --from-ipsw latest | --from-windows pro-25h2 | --linux --iso x.iso
    alt macOS guest
        CLI->>Src: restore-image lookup (VZ fetchLatestSupported), download to cache/IPSWs/sha256:<hash>.ipsw
        CLI->>VMM: VZMacOSInstaller with hardware model + fresh ECID, nvram.bin aux storage
    else Windows guest
        CLI->>Src: go-sdk-winmediafoundry softwaredownload ISO, unattend side volume or remaster
        CLI->>VMM: boot installer (HCS or HV.framework VMM), vTPM state created locally
    else Linux guest
        CLI->>Src: distro ISO or cloud image, checksum verified
        CLI->>VMM: boot installer with kickstart / autoinstall / NoCloud seed
    end
    VMM-->>Lay: config.json, disk.img or disk.vhdx, firmware state
    User->>CLI: weave setup / provisioning (agent baked), weave snapshot create clean
    opt publish
        User->>CLI: weave push vm1 macos-26-base:26.0-25A354-r1
        CLI->>WO: pack (strip ECID, MAC, host keys, carry hardwareModel and aux storage) → push
    end
```

No step in the from-source flow contacts a registry. This is the mode guestweave must keep working
with no network beyond the vendor media endpoints
([0003](decisions/0003-consumer-modes.md)).

## Runtime consumption

The artifact carries raw guest-LBA chunks. Each hypervisor backend turns the reassembled raw disk
into what it can boot. Conversion happens once per cached digest, never per clone.

| Host / hypervisor | Cached form | Per-VM instantiation | Firmware and identity state |
|---|---|---|---|
| macOS, Virtualization.framework (macOS and Linux guests) | raw `disk.img` (sparse); ASIF conversion optional on macOS 26+ | APFS `clonefile(2)` copy (`internal/fsutil/clone.go`) | carry `hardwareModel` and auxiliary storage (`nvram.bin`); regenerate ECID, `machineidentifier.bin`, MAC |
| macOS, HV.framework VMM (Windows 11 ARM64 guests) | raw `disk.img` | APFS clone | carry `NVRAM.dat` if present (optional); regenerate `tpm/` |
| Linux, QEMU/KVM (hostweave agent) | raw base file in the agent cache | `qemu-img create -f qcow2 -b <base>` overlay per attempt | regenerate OVMF VARS from firmware policy; cloud-init NoCloud seed per attempt as today |
| Windows, HCS (guestweave-cli-windows) | raw chunks → parent `disk.vhdx` written by `disk/vhd` (fixed VHD footer or dynamic VHDX, see [12](12-open-questions.md)) | differencing child VHDX (about 4 MB) | regenerate `guest.vmgs` (UEFI vars plus vTPM) from firmware policy; first run elevated as hostweave ADR 0010 records |

Windows QEMU and Linux VZ follow the same rules as their hypervisor row. A format that a host
cannot consume is never pulled: the scheduler filter and `weave capabilities` both report
`image_formats: ["weave-guest-v1"]` and the guest platforms the host can boot.

## Trust placement

| Where | What is verified | Mechanism | Failure behaviour |
|---|---|---|---|
| CI, after push | The pushed digest has a valid provenance bundle from this workflow | `weaveoci verify --attestation` (sigstore-go, trusted root from TUF) | Workflow fails; no dispatch sent |
| Promotion PR | Digest matches the dispatch; attestation signer is the expected workflow | CI check on the PR plus human review | PR not merged; image stays unpromoted |
| hostweave server, at resolve time | Digest is listed in the configured channel; artifact conforms to the contract | `verify.Channel` (root key, signing key, `stable.json`) plus `spec.Inspect` | Version recorded as `metadata_validated` but not pinnable for jobs when policy requires a channel pin |
| hostweave agent, at pull time | Blob digests match descriptors; uncompressed chunk digests match annotations | `cache` | Attempt fails as a capacity-class error and is retried elsewhere |
| guestweave CLI, at pull time | As configured: `--verify=channel` (default when a channel is configured), `attestation`, or `none` | `verify` | Pull refused with the reason; `--verify=none` prints a warning |
| Air-gapped host | Channel manifest against the embedded root key; attestation bundles against a downloaded `trusted_root.jsonl` | `verify` with local inputs; images imported from `oci-layout` | Same as online |

Attestation verification is optional for private mirrors that cannot reach Sigstore; channel
verification is mandatory for hostweave ([0006](decisions/0006-trust-attestations-and-channel-manifest.md)).

## Shared-module boundary

| In the shared module (`weaveplatform-oci`) | In each consumer |
|---|---|
| Media types, config schema and validator (`spec`) | Translating the config into the consumer's own VM config (`vmconfig.VMConfig` on macOS, `internal/vm/config` on Windows, `types.ImageVersion` in hostweave) |
| Chunking, zero detection, zstd, sparse reassembly (`chunk`) | Hole punching implementation per filesystem is behind an interface; the macOS purgeable-space disk guard stays in guestweave-macos |
| Manifest and index packing with `oras.PackManifest` (`pack`) | Which bundle files are state blobs and their carry/regenerate semantics come from the spec, but the consumer decides what to regenerate at clone |
| Registry profiles, credential chain, resolve, fetch with Range resume, push with HEAD skip, referrers discovery (`client`) | Keychain and Windows Credential Manager hooks; hostweave's provider-backed token exchange (ECR, ACR, GAR) feeds credentials in |
| Content-addressed cache, pins, LRU, quota, `oci-layout` import and export (`cache`) | Cache location and quota policy; hostweave's agent health check reads free space from it |
| Sigstore bundle and channel-manifest verification (`verify`) | Policy: which channel, which workflow identity, whether `none` is allowed |
| Raw to VHD/VHDX conversion (`disk/vhd`) | Hypervisor-specific attach, differencing children, overlays |
| `cmd/weaveoci` for pipelines and operators | `weave` and `hwctl` keep their own verbs and call the module |

The module has no hypervisor code, no SSH, no exec, and no knowledge of hostweave's job model.

## Migration summary

Sequencing is in [11-migration.md](11-migration.md). In short: document first (this set), then the
spec and chunker with fixtures, then the client, cache and verifier, then the Linux pipeline end to
end because it carries no licensing risk, then guestweave-macos, guestweave-windows and hostweave
adopt the module, then the macOS and Windows pipelines, then a mirror and air-gap drill. Images
already published in the Tart or VHDX-v2 encodings are re-published once with `weaveoci republish`.

## Non-goals

- **Splitting provisioning from the image.** Per-instance configuration (credentials, agent channel
  keys, cloud-init or unattend seeds) stays a consumer concern. The contract reserves a seed media
  type but v1 does not use it.
- **Content-defined chunking.** Fixed 512 MiB guest-LBA chunks with digest-level dedupe and
  lineage-based sharing (APFS clones, qcow2 overlays, differencing VHDX) are the v1 answer. CDC is
  revisited only if rebuild frequency makes bandwidth the dominant cost
  ([06](06-large-artifacts.md)).
- **Running a registry.** GHCR is canonical. Mirrors are documented and supported by profiles, but
  no self-hosted registry is stood up in this phase.
- **Container images.** The moby runtime keeps pulling ordinary images through the engine. The
  contract covers VM images only.
- **Wire compatibility with Tart or Lume.** Removed deliberately
  ([0010](decisions/0010-remove-tart-and-lume-compatibility.md)).
