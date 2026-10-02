# weave VM artifact contract v1 (draft)

Status: Draft. Proposed by [decision 0001](decisions/0001-vm-artifact-contract.md).
Research baseline 2026-10-02.

This document specifies how a virtual-machine image is encoded as an OCI artifact so
that hostweave, guestweave-cli-macos and guestweave-cli-windows read and write one
format. It is written against OCI image-spec v1.1.1 and distribution-spec v1.1.1 and
uses only conformant constructs: a custom `artifactType`, a custom config media type,
custom layer media types and standard annotations. See the
[OCI primer](01-oci-primer.md) for the vocabulary and the
[artifacts guidance](https://github.com/opencontainers/image-spec/blob/v1.1.1/artifacts-guidance.md)
for the conformance shapes this contract follows.

The key words MUST, MUST NOT, SHOULD and MAY are used as in RFC 2119.

## 1. Scope

- **In scope.** A guest disk image (any guest OS), the firmware state a guest needs to
  boot, and the metadata a consumer needs to instantiate and clone a VM. One artifact
  per (guest OS, architecture, OS build); one OCI image index per published image tag.
- **Out of scope.** Container images for the hostweave moby runtime (ordinary OCI
  images in separate repositories), source media such as IPSW, ISO or ESD files (fetched
  by the guestweave CLIs from vendor endpoints; see
  [build pipelines](07-build-pipelines.md)), provisioning payloads (cloud-init seeds,
  unattend side volumes — a media type is reserved but unused in v1), and per-instance
  identity (never shipped; see §7).
- **Producers.** The reusable publication workflows in this repository and
  `weave push` in the guestweave CLIs, all through the shared Go module
  ([10-shared-go-module.md](10-shared-go-module.md)).
- **Consumers.** hostweave server (inspect and pin), hostweave agent (QEMU runtime,
  guestweave shell-out), guestweave-cli-macos (Virtualization.framework and the
  Hypervisor.framework Windows VMM), guestweave-cli-windows (HCS).

## 2. Media types

All media types live under `application/vnd.weave.guest.*`. The org already publishes
`application/vnd.weave.module` for agent modules
(`deploymenttheory/weaveplatform-agent-modules@main`,
`.github/workflows/module-release.yml:116`), the vendor tree needs no IANA
registration, and the name survives a GitHub organisation rename. Annotation keys use
`run.weaveplatform.guest.*`, because OCI requires reverse-DNS annotation keys
([annotations.md](https://github.com/opencontainers/image-spec/blob/v1.1.1/annotations.md))
and the project owns the domain `weaveplatform.run`. The keys were set before any
artifact was published, when the repositories moved from the `deploymenttheory` to the
`weaveplatform` GitHub organisation, so no published image carries the earlier
`com.deploymenttheory.weave.guest.*` keys. The split between media types (`vnd.weave`)
and annotation keys (`run.weaveplatform`) is deliberate and permanent.

The noun is `guest`, not `vm`, for one reason: guestweave-cli-windows still recognises
two legacy strings, `application/vnd.weave.vm.config.v1+json` and
`application/vnd.weave.vm.disk.v1.vhdx+gzip`
(`weaveplatform/guestweave-cli-windows@main internal/oci/oci.go:59-60`, see
[03-current-state.md](03-current-state.md)). A `vnd.weave.vm.*.v1` contract would be
byte-identical to a format with different semantics. `guest` also matches the project
naming (guestweave) and reads correctly for Linux guests on QEMU.

| Role | Media type | Where |
|---|---|---|
| Artifact type **and** config media type | `application/vnd.weave.guest.config.v1+json` | `artifactType` and `config.mediaType` of every VM manifest |
| Disk chunk | `application/vnd.weave.guest.disk.v1.raw+zstd` | layers |
| macOS Virtualization.framework auxiliary storage (`nvram.bin`) | `application/vnd.weave.guest.state.auxstorage.v1` | layer |
| UEFI variable store (`NVRAM.dat`, `OVMF_VARS.fd`) | `application/vnd.weave.guest.state.uefivars.v1` | layer |
| Firmware and vTPM policy | `application/vnd.weave.guest.state.firmware-policy.v1+json` | layer |
| Provisioning seed (reserved, not used in v1) | `application/vnd.weave.guest.state.seed.v1+tar` | — |
| Image manifest | `application/vnd.oci.image.manifest.v1+json` | standard |
| Image index | `application/vnd.oci.image.index.v1+json` | standard |

Using the same string for `artifactType` and `config.mediaType` is the second
conformant shape in the artifacts guidance ("the config is the artifact-specific
metadata"), and it means `oras discover --artifact-type` and registry referrer filters
select VM artifacts without reading the config. The guidance's third shape (empty
config plus `artifactType`) is not used because the config carries real data.

The `+zstd` suffix on disk chunks and `+json` on the config follow the structured
syntax suffix convention used by the OCI layer and config types
([layer.md](https://github.com/opencontainers/image-spec/blob/v1.1.1/layer.md)).

## 3. Manifest shape

A VM artifact is one OCI image manifest
([manifest.md](https://github.com/opencontainers/image-spec/blob/v1.1.1/manifest.md))
with:

- `schemaVersion: 2`, `mediaType: application/vnd.oci.image.manifest.v1+json`.
- `artifactType: application/vnd.weave.guest.config.v1+json`.
- `config`: a descriptor of the VM config document (§4), media type as above.
- `layers`: in this order — all disk chunks of the first disk in ascending chunk index,
  then all chunks of any further disks, then state blobs. Consumers MUST NOT depend on
  layer order for correctness (chunks are addressed by annotation), but producers MUST
  emit this order so that manifests are deterministic.
- `annotations`: §8.

Registries accept unknown config media types: lume publishes
`application/vnd.trycua.lume.config.v1+json` on ghcr.io today, and image-spec requires
clients to treat an unknown config as opaque bytes.

### 3.1 macOS example

A macOS 26.0 (build 25A354) arm64 image with a 64 GiB system disk (128 chunks of
512 MiB, 41 of them all-zero) and carried auxiliary storage.

```json
{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.manifest.v1+json",
  "artifactType": "application/vnd.weave.guest.config.v1+json",
  "config": {
    "mediaType": "application/vnd.weave.guest.config.v1+json",
    "digest": "sha256:3f0c…",
    "size": 1412
  },
  "layers": [
    {
      "mediaType": "application/vnd.weave.guest.disk.v1.raw+zstd",
      "digest": "sha256:9a1b…",
      "size": 201326592,
      "annotations": {
        "org.opencontainers.image.title": "disk0.chunk.000000",
        "run.weaveplatform.guest.disk.name": "disk0",
        "run.weaveplatform.guest.disk.chunk.index": "0",
        "run.weaveplatform.guest.disk.chunk.offset": "0",
        "run.weaveplatform.guest.disk.chunk.size": "536870912",
        "run.weaveplatform.guest.disk.chunk.digest": "sha256:77e4…"
      }
    },
    {
      "mediaType": "application/vnd.weave.guest.disk.v1.raw+zstd",
      "digest": "sha256:<canonical-zero-chunk-digest>",
      "size": 1234,
      "annotations": {
        "org.opencontainers.image.title": "disk0.chunk.000001",
        "run.weaveplatform.guest.disk.name": "disk0",
        "run.weaveplatform.guest.disk.chunk.index": "1",
        "run.weaveplatform.guest.disk.chunk.offset": "536870912",
        "run.weaveplatform.guest.disk.chunk.size": "536870912",
        "run.weaveplatform.guest.disk.chunk.digest": "sha256:<zero-512mib-digest>",
        "run.weaveplatform.guest.disk.chunk.zero": "true"
      }
    },
    { "...": "chunks 2 … 127 omitted" },
    {
      "mediaType": "application/vnd.weave.guest.state.auxstorage.v1",
      "digest": "sha256:b2c3…",
      "size": 33554432,
      "annotations": {
        "org.opencontainers.image.title": "nvram.bin",
        "run.weaveplatform.guest.state.name": "auxstorage",
        "run.weaveplatform.guest.state.semantics": "carry"
      }
    }
  ],
  "annotations": {
    "org.opencontainers.image.created": "2026-10-02T09:14:00Z",
    "org.opencontainers.image.version": "26.0-25A354-r1",
    "org.opencontainers.image.revision": "4c1e9d7f",
    "org.opencontainers.image.source": "https://github.com/weaveplatform/weaveplatform-oci",
    "org.opencontainers.image.title": "macos-26-vanilla",
    "org.opencontainers.image.description": "macOS 26.0 (25A354) vanilla, weave agent 0.2.0 baked",
    "org.opencontainers.image.vendor": "weaveplatform",
    "org.opencontainers.image.licenses": "LicenseRef-Apple-macOS-SLA",
    "run.weaveplatform.guest.os": "darwin",
    "run.weaveplatform.guest.arch": "arm64",
    "run.weaveplatform.guest.osVersion": "26.0",
    "run.weaveplatform.guest.osBuild": "25A354",
    "run.weaveplatform.guest.disk.totalSize": "68719476736",
    "run.weaveplatform.guest.hypervisors": "vz"
  }
}
```

The config document for this manifest:

```json
{
  "schemaVersion": 1,
  "guest": {
    "os": "darwin",
    "arch": "arm64",
    "osVersion": "26.0",
    "osBuild": "25A354",
    "edition": "",
    "variant": "vanilla"
  },
  "firmware": {
    "type": "apple",
    "secureBoot": true,
    "tpm": "none",
    "hardwareModel": "YnBsaXN0MDDTAQIDBAUGXxAZRGF0YVJlcHJlc2VudGF0aW9uVmVyc2lvbl8QD1BsYXRmb3JtVmVyc2lvbl8QEk1pbmltdW1TdXBwb3J0ZWRPUxMAAAAAAAAAARACowcICRANEAAQAAgPKz1SVVdaXF4AAAAAAAABAQAAAAAAAAAKAAAAAAAAAAAAAAAAAAAAYA==",
    "minHostOS": "26.0"
  },
  "disks": [
    {
      "name": "disk0",
      "role": "system",
      "logicalSize": 68719476736,
      "chunkSize": 536870912,
      "chunkCount": 128,
      "compression": "zstd",
      "zeroChunks": 41
    }
  ],
  "state": [
    {
      "name": "auxstorage",
      "mediaType": "application/vnd.weave.guest.state.auxstorage.v1",
      "semantics": "carry",
      "required": true
    }
  ],
  "resources": {
    "cpu": { "min": 2, "default": 4 },
    "memory": { "min": 4294967296, "default": 8589934592 }
  },
  "provisioning": {
    "defaultUser": "admin",
    "credentialHint": "set-at-first-boot",
    "agent": { "name": "guestweave", "version": "0.2.0" }
  },
  "build": {
    "template": "macos-vanilla",
    "templateRef": "github.com/weaveplatform/weaveplatform-oci@4c1e9d7f",
    "sourceMedia": [
      {
        "kind": "ipsw",
        "uri": "https://updates.cdn-apple.com/…/UniversalMac_26.0_25A354_Restore.ipsw",
        "digest": "sha256:5d7e…"
      }
    ],
    "created": "2026-10-02T09:14:00Z"
  }
}
```

The `hardwareModel` value above is illustrative; the real value is the base64 of
`VZMacHardwareModel.dataRepresentation` captured at install time
(`weaveplatform/guestweave-cli-macos@main`, `internal/vm/config/platformdarwin.go:69-70`).

### 3.2 Windows example

A Windows 11 25H2 (10.0.26200.6584) amd64 image with one 64 GiB disk, an optional
UEFI variable store and a firmware policy that tells the HCS consumer to create a fresh
`guest.vmgs` with vTPM and Secure Boot.

```json
{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.manifest.v1+json",
  "artifactType": "application/vnd.weave.guest.config.v1+json",
  "config": {
    "mediaType": "application/vnd.weave.guest.config.v1+json",
    "digest": "sha256:a0b1…",
    "size": 1380
  },
  "layers": [
    { "...": "128 disk0 chunks as in §3.1" },
    {
      "mediaType": "application/vnd.weave.guest.state.uefivars.v1",
      "digest": "sha256:c4d5…",
      "size": 385024,
      "annotations": {
        "org.opencontainers.image.title": "NVRAM.dat",
        "run.weaveplatform.guest.state.name": "uefivars",
        "run.weaveplatform.guest.state.semantics": "carry"
      }
    },
    {
      "mediaType": "application/vnd.weave.guest.state.firmware-policy.v1+json",
      "digest": "sha256:e6f7…",
      "size": 96,
      "annotations": {
        "org.opencontainers.image.title": "firmware-policy.json",
        "run.weaveplatform.guest.state.name": "firmware-policy",
        "run.weaveplatform.guest.state.semantics": "regenerate"
      }
    }
  ],
  "annotations": {
    "org.opencontainers.image.created": "2026-10-02T10:02:00Z",
    "org.opencontainers.image.version": "11-25H2-26200.6584-r1",
    "org.opencontainers.image.revision": "4c1e9d7f",
    "org.opencontainers.image.source": "https://github.com/weaveplatform/weaveplatform-oci",
    "org.opencontainers.image.title": "windows-11-base",
    "org.opencontainers.image.description": "Windows 11 Pro 25H2 base, weave agent 0.2.0 baked",
    "org.opencontainers.image.vendor": "weaveplatform",
    "org.opencontainers.image.licenses": "LicenseRef-Microsoft-Windows-11",
    "run.weaveplatform.guest.os": "windows",
    "run.weaveplatform.guest.arch": "amd64",
    "run.weaveplatform.guest.osVersion": "10.0.26200.6584",
    "run.weaveplatform.guest.osBuild": "26200.6584",
    "run.weaveplatform.guest.disk.totalSize": "68719476736",
    "run.weaveplatform.guest.hypervisors": "hcs,kvm,hvf"
  }
}
```

Firmware-policy blob:

```json
{ "schemaVersion": 1, "secureBoot": true, "tpm": "required", "generation": 2 }
```

Config excerpt (fields that differ from §3.1):

```json
{
  "guest": { "os": "windows", "arch": "amd64", "osVersion": "10.0.26200.6584",
             "osBuild": "26200.6584", "edition": "Pro", "variant": "base" },
  "firmware": { "type": "uefi", "secureBoot": true, "tpm": "required", "minHostOS": "" },
  "state": [
    { "name": "uefivars", "mediaType": "application/vnd.weave.guest.state.uefivars.v1",
      "semantics": "carry", "required": false },
    { "name": "firmware-policy",
      "mediaType": "application/vnd.weave.guest.state.firmware-policy.v1+json",
      "semantics": "regenerate", "required": true }
  ],
  "provisioning": { "defaultUser": "weave", "credentialHint": "baked",
                    "agent": { "name": "guestweave", "version": "0.2.0" } },
  "build": { "template": "windows-11-base", "templateRef": "github.com/weaveplatform/weaveplatform-oci@4c1e9d7f",
             "sourceMedia": [ { "kind": "iso", "uri": "softwaredownload:Win11_25H2_English_x64", "digest": "sha256:1a2b…" } ],
             "created": "2026-10-02T10:02:00Z" }
}
```

### 3.3 Linux example

An Ubuntu 24.04 arm64 image derived from the upstream cloud image, 20 GiB disk, no
state blobs (a QEMU or VZ consumer creates fresh UEFI variables).

```json
{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.manifest.v1+json",
  "artifactType": "application/vnd.weave.guest.config.v1+json",
  "config": {
    "mediaType": "application/vnd.weave.guest.config.v1+json",
    "digest": "sha256:0c1d…",
    "size": 1190
  },
  "layers": [
    { "...": "40 disk0 chunks" }
  ],
  "annotations": {
    "org.opencontainers.image.created": "2026-10-02T06:30:00Z",
    "org.opencontainers.image.version": "24.04-20260915-r1",
    "org.opencontainers.image.revision": "4c1e9d7f",
    "org.opencontainers.image.source": "https://github.com/weaveplatform/weaveplatform-oci",
    "org.opencontainers.image.title": "ubuntu-24.04",
    "org.opencontainers.image.description": "Ubuntu 24.04 cloud image 20260915, weave agent 0.2.0 baked",
    "org.opencontainers.image.vendor": "weaveplatform",
    "org.opencontainers.image.licenses": "Ubuntu-IPRights",
    "run.weaveplatform.guest.os": "linux",
    "run.weaveplatform.guest.arch": "arm64",
    "run.weaveplatform.guest.distro": "ubuntu",
    "run.weaveplatform.guest.osVersion": "24.04",
    "run.weaveplatform.guest.osBuild": "20260915",
    "run.weaveplatform.guest.disk.totalSize": "21474836480",
    "run.weaveplatform.guest.hypervisors": "kvm,vz,hvf,hcs"
  }
}
```

Config excerpt:

```json
{
  "guest": { "os": "linux", "arch": "arm64", "osVersion": "24.04", "osBuild": "20260915",
             "edition": "", "variant": "", "distro": "ubuntu" },
  "firmware": { "type": "uefi", "secureBoot": false, "tpm": "none", "minHostOS": "" },
  "disks": [ { "name": "disk0", "role": "system", "logicalSize": 21474836480,
               "chunkSize": 536870912, "chunkCount": 40, "compression": "zstd", "zeroChunks": 33 } ],
  "state": [],
  "provisioning": { "defaultUser": "ubuntu", "credentialHint": "cloud-init",
                    "agent": { "name": "guestweave", "version": "0.2.0" } },
  "build": { "template": "linux-cloud-image", "templateRef": "github.com/weaveplatform/weaveplatform-oci@4c1e9d7f",
             "sourceMedia": [ { "kind": "cloud-image",
               "uri": "https://cloud-images.ubuntu.com/noble/20260915/noble-server-cloudimg-arm64.img",
               "digest": "sha256:9e8f…" } ],
             "created": "2026-10-02T06:30:00Z" }
}
```

### 3.4 Index example

One index per repository tag
([image-index.md](https://github.com/opencontainers/image-spec/blob/v1.1.1/image-index.md)).
Each child is one VM manifest; children carry the `platform` object and the same
`run.weaveplatform.guest.*` annotations as their manifest so that listings
never need to fetch children.

```json
{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.index.v1+json",
  "artifactType": "application/vnd.weave.guest.config.v1+json",
  "manifests": [
    {
      "mediaType": "application/vnd.oci.image.manifest.v1+json",
      "artifactType": "application/vnd.weave.guest.config.v1+json",
      "digest": "sha256:aa11…",
      "size": 61920,
      "platform": { "os": "linux", "architecture": "amd64" },
      "annotations": {
        "run.weaveplatform.guest.os": "linux",
        "run.weaveplatform.guest.arch": "amd64",
        "run.weaveplatform.guest.distro": "ubuntu",
        "run.weaveplatform.guest.osVersion": "24.04",
        "run.weaveplatform.guest.osBuild": "20260915"
      }
    },
    {
      "mediaType": "application/vnd.oci.image.manifest.v1+json",
      "artifactType": "application/vnd.weave.guest.config.v1+json",
      "digest": "sha256:bb22…",
      "size": 61904,
      "platform": { "os": "linux", "architecture": "arm64" },
      "annotations": { "...": "as above with arch arm64" }
    }
  ],
  "annotations": {
    "org.opencontainers.image.created": "2026-10-02T06:30:00Z",
    "org.opencontainers.image.version": "24.04-20260915-r1",
    "org.opencontainers.image.source": "https://github.com/weaveplatform/weaveplatform-oci",
    "org.opencontainers.image.description": "Ubuntu 24.04 cloud image 20260915, weave agent 0.2.0 baked"
  }
}
```

A single-platform image (every macOS image) is still published as an index with one
child, so consumers have one code path. `artifactType` on the index and on child
descriptors is permitted by image-spec 1.1 and lets a consumer reject a container
image index before fetching anything else.

## 4. Config document

`config.mediaType` is `application/vnd.weave.guest.config.v1+json`. The document is UTF-8
JSON with keys in the order shown, so that a given set of values always produces the
same digest. Sizes are bytes. Versions are strings.

### 4.1 Field table

| Field | Type | Required | Meaning |
|---|---|---|---|
| `schemaVersion` | integer | yes | `1` |
| `guest.os` | `darwin` \| `windows` \| `linux` | yes | GOOS value; equals index `platform.os` |
| `guest.arch` | `arm64` \| `amd64` | yes | GOARCH value; equals index `platform.architecture` |
| `guest.osVersion` | string | yes | darwin: ProductVersion (`26.0`); windows: kernel version (`10.0.26200.6584`); linux: distro release (`24.04`, `42`) |
| `guest.osBuild` | string | yes | darwin build (`25A354`); windows UBR build (`26200.6584`); linux image serial (`20260915`) |
| `guest.edition` | string | no | windows edition (`Pro`, `Enterprise`); empty otherwise |
| `guest.variant` | string | no | image variant (`vanilla`, `base`, `xcode-26`) |
| `guest.distro` | string | linux only | `ubuntu`, `fedora`, `debian`, `fedora-bootc`, … |
| `firmware.type` | `apple` \| `uefi` \| `bios` | yes | darwin MUST be `apple` |
| `firmware.secureBoot` | boolean | yes | whether the guest was installed with Secure Boot on |
| `firmware.tpm` | `none` \| `required` | yes | whether the guest requires a vTPM at boot |
| `firmware.hardwareModel` | base64 string | darwin only | `VZMacHardwareModel.dataRepresentation`; MUST be absent for other OSes |
| `firmware.minHostOS` | string | no | minimum host OS version known to boot it (darwin: VZ requirement) |
| `disks[]` | array, ≥1 | yes | one entry per disk; `disks[0]` is the boot disk |
| `disks[].name` | string | yes | `disk0`, `disk1`, …; matches chunk annotations |
| `disks[].role` | `system` \| `data` | yes | |
| `disks[].logicalSize` | integer | yes | guest-visible size in bytes |
| `disks[].chunkSize` | integer | yes | `536870912` in v1 |
| `disks[].chunkCount` | integer | yes | `ceil(logicalSize / chunkSize)` |
| `disks[].compression` | `zstd` | yes | |
| `disks[].zeroChunks` | integer | yes | number of chunks flagged `zero=true` |
| `state[]` | array | yes (may be empty) | one entry per state layer |
| `state[].name` | string | yes | `auxstorage`, `uefivars`, `firmware-policy` |
| `state[].mediaType` | string | yes | one of the `state.*` media types in §2 |
| `state[].semantics` | `carry` \| `regenerate` | yes | §7 |
| `state[].required` | boolean | yes | consumer MUST fail if a required state blob is missing |
| `resources.cpu.min` / `.default` | integer | yes | vCPUs |
| `resources.memory.min` / `.default` | integer | yes | bytes |
| `provisioning.defaultUser` | string | no | account present in the image |
| `provisioning.credentialHint` | `baked` \| `set-at-first-boot` \| `cloud-init` \| `none` | yes | how the consumer obtains access |
| `provisioning.agent.name` / `.version` | string | no | in-guest agent baked into the image |
| `build.template` | string | yes | pipeline template name |
| `build.templateRef` | string | yes | `<repo>@<commit>` of the template |
| `build.sourceMedia[]` | array | yes | each `{kind: ipsw|iso|esd|cloud-image|bootc, uri, digest}` |
| `build.created` | RFC 3339 | yes | equals `org.opencontainers.image.created` |

### 4.2 Forbidden content

The config MUST NOT contain, and a validator MUST reject a document containing, any of:

- `ecid` or `machineIdentifier` (Virtualization.framework machine identity);
- `macAddress` or any NIC configuration;
- vTPM state, `guest.vmgs` bytes, or references to them;
- display, clipboard, sharing or network-profile settings (run-time configuration);
- credentials, tokens or key material of any kind.

Per-instance identity is regenerated by the consumer at instantiation (§7). This is the
single largest deliberate difference from the formats surveyed in
[prior art](02-prior-art.md), which ship a machine identifier inside the image.

### 4.3 JSON Schema

Embedded in the `spec` package and published as `pkg/spec/schema/vm-config-v1.schema.json`.

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://github.com/weaveplatform/weaveplatform-oci/spec/schema/vm-config-v1.schema.json",
  "title": "weave VM artifact config v1",
  "type": "object",
  "additionalProperties": false,
  "required": ["schemaVersion", "guest", "firmware", "disks", "state", "resources", "provisioning", "build"],
  "properties": {
    "schemaVersion": { "const": 1 },
    "guest": {
      "type": "object",
      "additionalProperties": false,
      "required": ["os", "arch", "osVersion", "osBuild"],
      "properties": {
        "os": { "enum": ["darwin", "windows", "linux"] },
        "arch": { "enum": ["arm64", "amd64"] },
        "osVersion": { "type": "string", "minLength": 1 },
        "osBuild": { "type": "string", "minLength": 1 },
        "edition": { "type": "string" },
        "variant": { "type": "string" },
        "distro": { "type": "string" }
      },
      "if": { "properties": { "os": { "const": "linux" } } },
      "then": { "required": ["distro"] }
    },
    "firmware": {
      "type": "object",
      "additionalProperties": false,
      "required": ["type", "secureBoot", "tpm"],
      "properties": {
        "type": { "enum": ["apple", "uefi", "bios"] },
        "secureBoot": { "type": "boolean" },
        "tpm": { "enum": ["none", "required"] },
        "hardwareModel": { "type": "string", "contentEncoding": "base64" },
        "minHostOS": { "type": "string" }
      }
    },
    "disks": {
      "type": "array",
      "minItems": 1,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["name", "role", "logicalSize", "chunkSize", "chunkCount", "compression", "zeroChunks"],
        "properties": {
          "name": { "type": "string", "pattern": "^disk[0-9]+$" },
          "role": { "enum": ["system", "data"] },
          "logicalSize": { "type": "integer", "minimum": 1 },
          "chunkSize": { "const": 536870912 },
          "chunkCount": { "type": "integer", "minimum": 1 },
          "compression": { "const": "zstd" },
          "zeroChunks": { "type": "integer", "minimum": 0 }
        }
      }
    },
    "state": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["name", "mediaType", "semantics", "required"],
        "properties": {
          "name": { "enum": ["auxstorage", "uefivars", "firmware-policy"] },
          "mediaType": {
            "enum": [
              "application/vnd.weave.guest.state.auxstorage.v1",
              "application/vnd.weave.guest.state.uefivars.v1",
              "application/vnd.weave.guest.state.firmware-policy.v1+json"
            ]
          },
          "semantics": { "enum": ["carry", "regenerate"] },
          "required": { "type": "boolean" }
        }
      }
    },
    "resources": {
      "type": "object",
      "additionalProperties": false,
      "required": ["cpu", "memory"],
      "properties": {
        "cpu": { "$ref": "#/$defs/minDefault" },
        "memory": { "$ref": "#/$defs/minDefault" }
      }
    },
    "provisioning": {
      "type": "object",
      "additionalProperties": false,
      "required": ["credentialHint"],
      "properties": {
        "defaultUser": { "type": "string" },
        "credentialHint": { "enum": ["baked", "set-at-first-boot", "cloud-init", "none"] },
        "agent": {
          "type": "object",
          "additionalProperties": false,
          "required": ["name", "version"],
          "properties": { "name": { "type": "string" }, "version": { "type": "string" } }
        }
      }
    },
    "build": {
      "type": "object",
      "additionalProperties": false,
      "required": ["template", "templateRef", "sourceMedia", "created"],
      "properties": {
        "template": { "type": "string" },
        "templateRef": { "type": "string" },
        "sourceMedia": {
          "type": "array",
          "items": {
            "type": "object",
            "additionalProperties": false,
            "required": ["kind", "uri", "digest"],
            "properties": {
              "kind": { "enum": ["ipsw", "iso", "esd", "cloud-image", "bootc"] },
              "uri": { "type": "string" },
              "digest": { "type": "string", "pattern": "^sha256:[a-f0-9]{64}$" }
            }
          }
        },
        "created": { "type": "string", "format": "date-time" }
      }
    }
  },
  "$defs": {
    "minDefault": {
      "type": "object",
      "additionalProperties": false,
      "required": ["min", "default"],
      "properties": {
        "min": { "type": "integer", "minimum": 1 },
        "default": { "type": "integer", "minimum": 1 }
      }
    }
  }
}
```

`additionalProperties: false` at every level is what makes the forbidden-field rule
mechanical: `ecid`, `machineIdentifier`, `macAddress` and friends fail validation
because they are not in the schema. Semantic rules the schema cannot express (darwin
requires `firmware.type = apple` and `hardwareModel`; non-darwin MUST NOT carry
`hardwareModel`; `chunkCount = ceil(logicalSize/chunkSize)`; `zeroChunks ≤ chunkCount`;
every `state[]` entry has exactly one matching layer) are enforced by the `spec`
validator and listed in §11.

## 5. Disk chunk layers

A disk is published as a sequence of chunks of guest logical block address (LBA) space.
Chunks address the guest disk, never a container file format: a consumer on Windows
builds a VHD/VHDX around the raw bytes, a consumer on Linux may use the raw file
directly or put a qcow2 overlay on it, and Virtualization.framework reads raw (or ASIF
converted from it). This is why one artifact serves every hypervisor.

1. Producers MUST split each disk into chunks of exactly `536870912` bytes (512 MiB) of
   uncompressed guest LBA space, except the last chunk, which MUST be
   `logicalSize mod 536870912` bytes when that is non-zero.
2. Chunk indexes MUST be `0 … chunkCount-1` with no gaps and no duplicates. Chunk
   `i` covers bytes `[i × 536870912, i × 536870912 + size)`. The `offset` annotation
   MUST equal `i × 536870912`.
3. Each chunk MUST be compressed as exactly one zstd frame
   ([RFC 8878](https://www.rfc-editor.org/rfc/rfc8878)) with the frame content size
   field present, so that a consumer can preallocate without reading the frame.
   Producers SHOULD use zstd level 3 until the benchmark in
   [open questions](12-open-questions.md) settles the level; consumers MUST accept any
   level.
4. Each chunk layer descriptor MUST carry the annotations in the table below. `size`
   and `digest` in the annotations describe the **uncompressed** bytes; the
   descriptor's own `size` and `digest` describe the compressed blob, as the
   descriptor specification requires
   ([descriptor.md](https://github.com/opencontainers/image-spec/blob/v1.1.1/descriptor.md)).
5. A chunk whose uncompressed bytes are all zero MUST be published as the **canonical
   zero chunk** for its length, with `run.weaveplatform.guest.disk.chunk.zero`
   set to `"true"`. The canonical zero chunk of `n` bytes is defined byte for byte, so
   its digest never depends on an encoder version:

   | Bytes | Value |
   |---|---|
   | magic | `28 b5 2f fd` |
   | frame header descriptor | `c0` (8-byte content size; no single segment, checksum or dictionary) |
   | window descriptor | `38` (window log 17, 128 KiB) |
   | frame content size | `n`, 8 bytes little endian |
   | blocks | `ceil(n / 131072)` RLE blocks of byte `00`: a 3-byte little-endian header `size << 3 \| 1 << 1 \| last` followed by `00`; every block holds 131072 bytes except the last |

   For a full 512 MiB chunk the frame is 16,398 bytes and its digests are published
   as constants in the reference implementation (`spec.ZeroChunkCompressedDigest`,
   `spec.ZeroChunkUncompressedDigest`):

   ```
   compressed   sha256:bc5ab29610eed538b180edfbdf41019a8f3d47c96a5f366fc737ecb461ecbc75
   uncompressed sha256:9acca8e8c22201155389f65abbf6bc9723edc7384ead80503839f49dcc56d767
   ```

   A short final chunk that is all zero uses the canonical zero chunk of its own
   length; consumers compute its digests on demand.
6. Consumers MAY skip fetching a `zero=true` chunk and instead punch a hole (or leave
   the sparse file untouched) over its range. Consumers MUST still verify the
   descriptor digest if they do fetch it.
7. A disk whose every chunk is zero is still a valid disk (an empty data disk).
8. Verification order on pull: (a) the compressed blob's sha256 MUST equal the
   descriptor `digest` and its length MUST equal descriptor `size`; (b) after
   decompression the uncompressed length MUST equal the `chunk.size` annotation and
   its sha256 MUST equal the `chunk.digest` annotation; (c) the frame content size MUST
   equal `chunk.size`. A consumer MUST discard a chunk failing any check and MAY retry
   it independently of other chunks.
9. Consumers SHOULD fetch chunks concurrently and SHOULD resume an interrupted pull by
   re-checking each already-written chunk range against `chunk.digest` rather than
   re-fetching it. Registries SHOULD honour HTTP Range on blob GET
   ([distribution-spec](https://github.com/opencontainers/distribution-spec/blob/v1.1.1/spec.md));
   a consumer MAY use Range to resume inside a chunk.
10. Producers SHOULD issue `HEAD /v2/<name>/blobs/<digest>` before uploading each chunk
    and skip blobs the registry already has; across image versions, unchanged guest
    LBA ranges therefore cost nothing to upload. Producers MAY use cross-repository
    blob mount when the same chunk exists in another repository they can read.
11. A single blob MUST NOT exceed the smallest registry layer limit the project
    targets (10 GB on GHCR); with 512 MiB chunks this cannot happen.

| Annotation key | Value | Required |
|---|---|---|
| `org.opencontainers.image.title` | `<disk>.chunk.<index padded to 6 digits>` | SHOULD (for ORAS/zot tooling) |
| `run.weaveplatform.guest.disk.name` | `disk0` … | MUST |
| `run.weaveplatform.guest.disk.chunk.index` | decimal integer | MUST |
| `run.weaveplatform.guest.disk.chunk.offset` | decimal bytes | MUST |
| `run.weaveplatform.guest.disk.chunk.size` | decimal bytes, uncompressed | MUST |
| `run.weaveplatform.guest.disk.chunk.digest` | `sha256:<hex>` of uncompressed bytes | MUST |
| `run.weaveplatform.guest.disk.chunk.zero` | `"true"` | MUST when all-zero; MUST be absent otherwise |

Why 512 MiB and not content-defined chunking: fixed guest-LBA chunks are stable across
rebuilds of block-level images (an installed OS does not shift its partitions), every
surveyed VM-in-OCI tool converged on the same size, and no production tool applies
content-defined chunking to VM disks today. See
[large artifacts](06-large-artifacts.md) for the analysis.

## 6. State blob layers

State blobs are the non-disk files a hypervisor needs. Each is one layer with one of
the `state.*` media types and these annotations:

| Annotation key | Value | Required |
|---|---|---|
| `org.opencontainers.image.title` | file name on disk (`nvram.bin`, `NVRAM.dat`, `firmware-policy.json`) | SHOULD |
| `run.weaveplatform.guest.state.name` | `auxstorage` \| `uefivars` \| `firmware-policy` | MUST |
| `run.weaveplatform.guest.state.semantics` | `carry` \| `regenerate` | MUST; equals the config `state[]` entry |

- `auxstorage` is the Virtualization.framework macOS auxiliary storage file, created
  by `VZMacAuxiliaryStorage` at install time. It is bound to the installed OS and the
  hardware model and MUST be carried verbatim.
- `uefivars` is a UEFI variable store (`NVRAM.dat` for the Hypervisor.framework
  Windows VMM, `OVMF_VARS.fd` for QEMU). It holds boot entries and Secure Boot keys
  but no per-instance secrets. Carrying it is optional; a consumer that cannot use the
  carried format MUST regenerate.
- `firmware-policy` is a small JSON document `{schemaVersion, secureBoot, tpm,
  generation}` from which consumers that keep firmware and TPM state in an opaque
  container (HCS `guest.vmgs`) create a **fresh** one at instantiation. It is always
  `regenerate`.

State blobs are small (KiB to tens of MiB) and are never chunked.

## 7. Carry versus regenerate

The rule: an image carries only what is bound to the installed operating system;
everything that identifies one running instance is regenerated by the consumer.

| Guest | Item | Semantics | Reason |
|---|---|---|---|
| macOS | `firmware.hardwareModel` (config) | carry, required | Virtualization.framework refuses a disk installed for a different hardware model |
| macOS | auxiliary storage (`nvram.bin`) | carry, required | EFI variables and sealed state belong to the installed OS |
| macOS | ECID / `VZMacMachineIdentifier` | regenerate, never shipped | unique per VM; VZ generates one; sharing it breaks activation and iCloud assumptions |
| macOS | MAC address | regenerate | collisions on clone |
| Windows (HCS, guestweave-cli-windows) | `guest.vmgs` (UEFI vars + vTPM) | regenerate from `firmware-policy`, never shipped | contains TPM secrets; the first elevated run creates it (hostweave decision 0010) |
| Windows (Hypervisor.framework VMM, guestweave-cli-macos) | `NVRAM.dat` | carry, optional | boot-entry convenience only; regenerable |
| Windows (QEMU) | `OVMF_VARS.fd` | regenerate, or carry `uefivars` if the consumer accepts it | |
| Windows | `tpm/` directory (swtpm state) | regenerate, never shipped | per-instance secrets |
| Linux (VZ) | `machineidentifier.bin` (`VZGenericMachineIdentifier`) | regenerate | per-instance |
| Linux (QEMU) | `OVMF_VARS.fd` | regenerate | |
| all | MAC addresses | regenerate | |
| all | SSH host keys | regenerate; build MUST delete them before packing | otherwise every clone shares host keys |
| all | cloud-init `instance-id`, machine-id (`/etc/machine-id`) | regenerate; build MUST clear them | repeated instance-id makes cloud-init skip first boot |
| all | guest-agent channel keys | regenerate; provisioned at instantiation | per-VM authentication (agent-modules handoff) |

The guestweave CLIs already regenerate the MAC on clone (`--regenerate-random-mac`,
`--keep-mac`); the contract makes that the only behaviour.

## 8. Annotations

### 8.1 Manifest annotations

| Key | Value | Required |
|---|---|---|
| `org.opencontainers.image.created` | RFC 3339; equals `build.created` | MUST |
| `org.opencontainers.image.version` | the immutable tag (§10) | MUST |
| `org.opencontainers.image.revision` | template commit | MUST |
| `org.opencontainers.image.source` | template repository URL (GHCR links the package to the repository through this key) | MUST |
| `org.opencontainers.image.title` | repository short name | SHOULD |
| `org.opencontainers.image.description` | ≤512 characters (GHCR limit) | SHOULD |
| `org.opencontainers.image.vendor` | `weaveplatform` | SHOULD |
| `org.opencontainers.image.licenses` | SPDX expression or `LicenseRef-…` for proprietary OS images; ≤256 characters | SHOULD |
| `run.weaveplatform.guest.os` | equals `guest.os` | MUST |
| `run.weaveplatform.guest.arch` | equals `guest.arch` | MUST |
| `run.weaveplatform.guest.osVersion` | equals `guest.osVersion` | MUST |
| `run.weaveplatform.guest.osBuild` | equals `guest.osBuild` | MUST |
| `run.weaveplatform.guest.distro` | equals `guest.distro` | MUST for linux |
| `run.weaveplatform.guest.disk.totalSize` | sum of `disks[].logicalSize` | MUST |
| `run.weaveplatform.guest.hypervisors` | comma-separated advisory list from `vz,hvf,hcs,kvm,tcg` | SHOULD |

Duplicating the guest fields from the config into manifest and index annotations is
deliberate: `weave images`, hostweave's registry browser and `oras discover` can list and
filter without fetching config blobs. The config remains authoritative; a validator
MUST reject a manifest whose annotations disagree with its config.

### 8.2 Index annotations

`org.opencontainers.image.{created,version,source,description}` as on the manifest.
Child descriptors carry the `run.weaveplatform.guest.*` set.

### 8.3 Layer annotations

§5 (disk chunks) and §6 (state blobs). Every layer SHOULD carry
`org.opencontainers.image.title` so that generic tools (`oras pull`, zot's UI) show a
file name.

Annotation values are strings; integers are decimal with no separators; booleans are
the literal `"true"`.

## 9. Index and platform conventions

- `platform.os` and `platform.architecture` MUST be GOOS and GOARCH values
  (`darwin`, `windows`, `linux`; `arm64`, `amd64`), as image-index.md recommends.
- `platform.os.version`:
  - darwin: the ProductVersion, e.g. `26.0.1` (the build is in annotations).
  - windows: the kernel version string Windows containers use, e.g. `10.0.26200.6584`.
  - linux: omitted; distro and version live in annotations because `os.version` has no
    cross-distro meaning.
- `platform.variant` is not used. `platform.os.features` is not used.
- One index per repository tag. One child per (os, architecture) actually built. A
  repository MUST hold images of one guest OS family (§10), so an index never mixes
  darwin and linux children; it may hold both `amd64` and `arm64` for linux.
- The hypervisor is **not** a platform axis. Because the disk is raw guest LBA space,
  the same child serves VZ, HCS, KVM and the Hypervisor.framework VMM; the
  `hypervisors` annotation is advisory (it records what the pipeline verified).
- A consumer selecting a child MUST match `os` and `architecture` exactly and MAY
  additionally filter on `os.version` or annotations. If several children match, the
  first wins, as image-index.md specifies.

The surveyed alternative — Podman's `machine-os`, which puts non-GOARCH values
(`aarch64`, `x86_64`) and a `disktype` annotation in one index — is avoided because it
makes standard platform matching fail and encodes the hypervisor into the artifact.

## 10. Repositories and tags

### 10.1 Repository naming

```
ghcr.io/weaveplatform/weave-images/<family>-<major>[-<variant>]
```

| Repository | Example contents |
|---|---|
| `weave-images/macos-26-vanilla` | macOS 26.x, fresh install, agent baked |
| `weave-images/macos-26-base` | macOS 26.x with the base tool set |
| `weave-images/windows-11-base` | Windows 11 client, base |
| `weave-images/windows-server-2025-base` | Windows Server 2025, base |
| `weave-images/ubuntu-24.04` | Ubuntu 24.04 cloud image, amd64 + arm64 |
| `weave-images/fedora-bootc-46` | derived from `quay.io/fedora/fedora-bootc:46` |

Rules: one guest OS family and major version per repository; variants are separate
repositories, not tags, so that retention and visibility can differ per variant;
container images for the moby runtime MUST NOT share a repository with VM artifacts.
GHCR accepts nested package names (the org already publishes
`weaveplatform-modules/<id>`). Visibility: `macos-*` and `windows-*` repositories MUST
be private (see the licensing section of
[registries and GitHub](04-registries-and-github.md)); `ubuntu-*`, `fedora-*`,
`debian-*` MAY be public.

### 10.2 Tags

| Tag form | Example | Mutability |
|---|---|---|
| `<osver>-<build>-r<rev>` | `26.0-25A354-r1`, `11-25H2-26200.6584-r1`, `24.04-20260915-r1` | immutable by policy |
| `<osver>-<build>` | `26.0-25A354` | moves to the latest `-r<rev>` of that build |
| `stable`, `edge`, `latest` | | channel tags; move on promotion |

`<rev>` increments when the same OS build is rebuilt (new agent, new template). GHCR
cannot enforce tag immutability, so immutability is a project policy enforced by the
publication workflow (refuse to push a tag that already resolves) and made irrelevant
to consumers by digest pinning: hostweave records `repo@sha256:…` and the channel
manifest lists digests, never tags. Registries that support immutability rules
(Harbor, Quay, ECR, ACR, GAR) SHOULD have a rule for `*-r*` tags when used as mirrors.

### 10.3 Digest pinning

Consumers MUST resolve a tag to an index digest once and record that digest. Every
subsequent fetch, verification and cache lookup uses the digest. A tag is a human
convenience and a promotion handle, nothing more. This matches hostweave's existing
`ImageVersion.Reference = repo@sha256:…` rule
(`weaveplatform/hostweave@main`, `pkg/types/image.go`).

## 11. Referrers

Referrers attach signed metadata to an artifact by its digest without changing it
([manifest.md `subject`](https://github.com/opencontainers/image-spec/blob/v1.1.1/manifest.md),
[referrers API](https://github.com/opencontainers/distribution-spec/blob/v1.1.1/spec.md)).

| Referrer | `artifactType` | Produced by | Subject |
|---|---|---|---|
| Build provenance (Sigstore bundle, SLSA v1 predicate) | `application/vnd.dev.sigstore.bundle.v0.3+json` | `actions/attest` with `push-to-registry` | each VM manifest digest and the index digest |
| SBOM (SPDX or CycloneDX) | `application/spdx+json` or `application/vnd.cyclonedx+json` | `actions/attest` SBOM mode or `oras attach` | the VM manifest digest |
| cosign signature (optional) | per cosign v3 bundle | `cosign sign` | manifest or index digest |

Discovery order a consumer MUST implement, in this sequence, stopping at the first
success:

1. `GET /v2/<name>/referrers/<digest>[?artifactType=…]` (registries with the API).
2. On HTTP 404, fetch the fallback tag `sha256-<hex of the subject digest>`, which is an
   image index whose manifests are the referrers (the tag schema in
   distribution-spec 1.1). GHCR has no referrers API as of the research baseline, so
   this path is the normal one there.
3. For provenance only: the GitHub attestations API (`gh attestation verify
   oci://…`), which holds every attestation regardless of registry.

Verification policy lives in [supply chain](05-supply-chain.md) and
[decision 0006](decisions/0006-trust-attestations-and-channel-manifest.md). The channel
manifest (promotion) lists the index digest, not the referrers, so a mirror that drops
referrers still yields a verifiable image through the channel path.

## 12. Conformance checklist

A validator (`spec.Validate`, `weaveoci inspect --strict`) checks, in this order, and
reports every failure rather than stopping at the first:

**Index**

1. `mediaType` is the OCI index type; `artifactType` equals the VM config media type.
2. Every child `mediaType` is the OCI manifest type, `artifactType` equals the VM
   config media type, and `platform.os`/`platform.architecture` are in the allowed sets.
3. No two children share the same (`os`, `architecture`, `os.version`).
4. Child annotations `guest.os` and `guest.arch` equal the child platform.
5. All children's `guest.os` values are the same (one OS family per repository).

**Manifest**

6. `schemaVersion` is 2; `mediaType` is the OCI manifest type; `artifactType` and
   `config.mediaType` both equal `application/vnd.weave.guest.config.v1+json`.
7. `config.size` is ≤ 4 MiB (hostweave's existing metadata cap) and the blob's digest
   and size match the descriptor.
8. The config validates against the JSON Schema (§4.3).
9. Semantic rules: darwin ⇒ `firmware.type = apple` and `hardwareModel` present;
   non-darwin ⇒ `hardwareModel` absent; `chunkCount = ceil(logicalSize / 536870912)`;
   `zeroChunks ≤ chunkCount`; `resources.*.min ≤ default`; `build.created` equals the
   `created` annotation.
10. Manifest annotations marked MUST in §8.1 are present and agree with the config.
11. Every layer media type is one of §2; no layer has an unknown type.
12. For each disk in `disks[]`: exactly `chunkCount` chunk layers with that
    `disk.name`; indexes form `0 … chunkCount-1`; `offset = index × 536870912`;
    `size` is 536870912 for all but the last and `logicalSize mod 536870912` (or
    536870912) for the last; the number of `zero=true` layers equals `zeroChunks`;
    every `zero=true` layer's descriptor digest equals the canonical zero-chunk digest
    for its length.
13. For each `state[]` entry: exactly one layer with that `state.name`, whose media type
    and `semantics` annotation match; no state layer lacks a config entry; a `required`
    entry's layer is present.
14. A blob may appear more than once (repeated zero chunks, identical data chunks
    at different offsets), but every occurrence MUST carry the same media type and
    the same content annotations (`chunk.size`, `chunk.digest`, `chunk.zero`); only
    position annotations may differ.

**Content (optional, `--deep`)**

15. Each fetched chunk passes the verification order in §5 rule 8.
16. Each state blob's digest and size match its descriptor.

**Referrers (optional, `--verify`)**

17. At least one provenance referrer is discoverable through the order in §11 and
    verifies against the configured policy; or the index digest is listed in a
    verified channel manifest.

## 13. Worked size example

A macOS 26 vanilla image on a 64 GiB disk (the layout observed in a local golden VM:
~36 GB allocated of 64 GB logical; see [current state](03-current-state.md)).

| Quantity | Value |
|---|---|
| Logical disk | 68,719,476,736 bytes (64 GiB) |
| Chunks | 128 × 512 MiB |
| All-zero chunks (skipped on pull, one shared blob on push) | ~41 |
| Data chunks | ~87 |
| Compressed size at zstd level 3 (assuming the ~0.55 ratio Tart's LZ4 achieves on macOS disks is matched or beaten) | ≈ 24–26 GiB |
| Largest single blob | ≤ 512 MiB uncompressed, typically 150–400 MiB compressed |
| Auxiliary storage | 33.6 MB (one layer) |
| Config | ~1.4 KB |
| Manifest | 130 descriptors × ~450 bytes ≈ 60 KB |
| Index | one child, ~1 KB |
| Push with HEAD-skip, second revision of the same build | only changed chunks; a typical agent-only rebuild changes a few GiB of LBA space |
| Pull on a fresh host | ~87 blobs, concurrency 4–8, resumable per chunk |
| Pull on a host that has the previous revision cached | only chunks whose digest is not already in the cache |

Comparable numbers from the surveyed public image: 96 layers and 27.3 GB compressed for
a 50 GB disk ([prior art](02-prior-art.md)).

## 14. Contract versioning

- The `v1` in every media type (`…disk.v1…`, `…config.v1…`, `…state.*.v1`) is the
  contract version. All v1 media types evolve together.
- Within v1, changes are **additive only**: new optional config fields (the JSON Schema
  is relaxed, never tightened for existing documents), new optional annotations, new
  `state.*` media types with a corresponding `state[].name` enum value. A v1 consumer
  MUST ignore unknown annotations and unknown optional config fields; it MUST reject
  unknown layer media types, because an unknown layer could be required state.
- A change that alters chunk size, compression, the meaning of an existing field, or
  removes a field is a **v2**: new media types `…v2…`, a new `schemaVersion`, and a
  new config media type. A v2 producer MAY publish v1 and v2 children in one index
  only if they are distinguishable by `artifactType`; consumers select the highest
  version they support.
- The reference implementation in `spec` exports the version constants, the JSON
  Schema and the canonical zero-chunk digests, and ships fixtures under
  `pkg/spec/testdata` for every example in this document. A change to this document that
  changes any byte of an example MUST change the fixture in the same commit.
- This document's status moves from Draft to Accepted when
  [decision 0001](decisions/0001-vm-artifact-contract.md) is accepted and the
  conformance suite passes against the fixtures.

## References

- OCI image manifest: <https://github.com/opencontainers/image-spec/blob/v1.1.1/manifest.md>
- OCI image index: <https://github.com/opencontainers/image-spec/blob/v1.1.1/image-index.md>
- OCI descriptor: <https://github.com/opencontainers/image-spec/blob/v1.1.1/descriptor.md>
- OCI artifacts guidance: <https://github.com/opencontainers/image-spec/blob/v1.1.1/artifacts-guidance.md>
- OCI annotations: <https://github.com/opencontainers/image-spec/blob/v1.1.1/annotations.md>
- OCI layers (media type suffixes): <https://github.com/opencontainers/image-spec/blob/v1.1.1/layer.md>
- OCI distribution spec (blobs, Range, mount, referrers, fallback tag): <https://github.com/opencontainers/distribution-spec/blob/v1.1.1/spec.md>
- zstd frame format: <https://www.rfc-editor.org/rfc/rfc8878>
- JSON Schema 2020-12: <https://json-schema.org/draft/2020-12/schema>
- GHCR limits and annotation rendering: <https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry>
- In-house artifactType precedent: `deploymenttheory/weaveplatform-agent-modules@main`, `.github/workflows/module-release.yml:116`
- hostweave digest-pinning rule: `weaveplatform/hostweave@main`, `pkg/types/image.go`; metadata cap `pkg/images/vm.go:18`
- Hardware model capture: `weaveplatform/guestweave-cli-macos@main`, `internal/vm/config/platformdarwin.go:69-70`
- Related: [01-oci-primer.md](01-oci-primer.md), [06-large-artifacts.md](06-large-artifacts.md), [08-target-architecture.md](08-target-architecture.md), [10-shared-go-module.md](10-shared-go-module.md), [11-migration.md](11-migration.md), decisions [0001](decisions/0001-vm-artifact-contract.md), [0002](decisions/0002-registry-repositories-tags-visibility.md), [0009](decisions/0009-guest-state-carry-vs-regenerate.md)
