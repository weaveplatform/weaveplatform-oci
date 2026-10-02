# Prior art

This note records the projects studied before designing the weave image contract, the ideas
taken from each, and the ideas deliberately rejected. The [target architecture](08-target-architecture.md)
describes the resulting design, the [artifact contract](09-artifact-contract-v1.md) holds the wire
format, and the [design decisions](decisions/README.md) hold the contracts and rationale. Research
was carried out on 2026-10-02; versions and live registry probes are as of that date. Items marked
*(unverified)* could not be confirmed against a primary source during that session.

This is the one document in this set that discusses Tart and Lume in depth. They are prior art
only: [decision 0001](decisions/0001-vm-artifact-contract.md) adopts no Tart media type, and
[decision 0010](decisions/0010-remove-tart-and-lume-compatibility.md) removes the existing Tart and
Lume codecs from guestweave-macos.

## Summary

| Project | Category | Adopted | Avoided |
|---|---|---|---|
| Tart (openai/tart) | macOS/Linux VM images in OCI | 512 MiB fixed chunks, per-chunk uncompressed size and digest annotations, sparse zero-skipping pulls, resumable per-chunk pulls, HEAD-before-upload dedupe, digest-keyed cache with tag links, LRU prune, disk-space guard | Its media types, the OCI image-config stub, LZ4, VM identity (ECID, MAC) shipped in the artifact, stacked ASIF overlays, any runtime dependency |
| Lume / cua (trycua) | macOS and Windows VM images in OCI | Custom config media type on GHCR (proves registry acceptance), immutable date-plus-SHA tags, index annotations for guest OS | Four incompatible encodings in one ecosystem, raw uncompressed 500 MiB shards, mislabelled layer media types |
| Podman machine-os | Linux VM disks for Podman machine | One index carrying several hypervisor variants; `.zst` single-blob disks are a counter-example for size | Empty config without `artifactType` (violates a MUST), non-GOARCH platform values, hypervisor as an annotation |
| KubeVirt containerDisk + containerdisks | Linux VM disks as container images | Push → boot-verify → promote pipeline, immutable `<ver>-<timestamp>` tags, upstream checksum verification, "never publish an image that doesn't boot" | Single gzip tar layer, disk interpreted by the container runtime, uid-107 convention |
| KubeVirt CDI | Importer for VM disks | Explicit `algo:hash` checksums on HTTP sources, qcow2→raw conversion at the consumer | Node-pull mode (no checksum verification) |
| Kubernetes Image Volumes | OCI artifacts as read-only volumes | Confirmation that non-tar artifact layers are first-class in the ecosystem | Not a VM disk mechanism; out of scope by its own KEP |
| bootc + image-builder | OS as an OCI image | bootc image as the Linux source of truth, raw disks derived in CI, day-2 updates from the registry | Treating a bootc image as a disk (bootc explicitly leaves disk-image packaging out of scope) |
| Apple containerization | Linux containers in VZ microVMs | Kernel and init filesystem distributed as OCI content | Not applicable to macOS or Windows guests |
| Flintlock (Liquid Metal) | Firecracker/Cloud Hypervisor microVM host | Kernel and initrd delivered as OCI image contents | Firecracker-only, Linux-only |
| Kata Containers | Sandboxed containers | ORAS artefact caches resolved with `--platform` | Kernel and rootfs shipped as release tarballs |
| Kairos | Immutable edge Linux | OS as a single OCI image, derived ISO/raw via AuroraBoot, A/B upgrades | Linux-only |
| Talos Image Factory | Immutable Kubernetes OS | Content-addressed build schematics, signed installer image | Disks served over HTTPS rather than OCI |
| CNCF ModelPack (model-spec) | Domain artifact spec on OCI 1.1 | The shape of a conformant artifact spec: one `artifactType`, one config type, typed layers, raw and compressed variants | — |
| weaveplatform-agent-modules release pipeline (internal) | In-house ORAS publication | ORAS push with an explicit `--artifact-type`, sidecar manifest with per-artifact digests, `repository_dispatch` into weaveplatform-manifest, promotion PR as the trust act | — |

## Tart

Sources: [github.com/openai/tart](https://github.com/openai/tart) (formerly cirruslabs/tart; old URLs
redirect), [`Sources/tart/OCI/Manifest.swift`](https://github.com/openai/tart/blob/main/Sources/tart/OCI/Manifest.swift),
[`Sources/tart/OCI/Layerizer/DiskV2.swift`](https://github.com/cirruslabs/tart/blob/main/Sources/tart/OCI/Layerizer/DiskV2.swift),
[`Sources/tart/VMStorageOCI.swift`](https://github.com/cirruslabs/tart/blob/main/Sources/tart/VMStorageOCI.swift),
[`Sources/tart/Commands/Pull.swift`](https://github.com/cirruslabs/tart/blob/main/Sources/tart/Commands/Pull.swift),
[`Sources/tart/Commands/Push.swift`](https://github.com/cirruslabs/tart/blob/main/Sources/tart/Commands/Push.swift),
[FAQ: stacked disk images](https://github.com/openai/tart/blob/main/docs/faq.md#stacked-disk-images),
[issue #569 (resumable pulls)](https://github.com/openai/tart/issues/569),
[Orchard VM spec](https://github.com/openai/orchard/blob/main/pkg/resource/v1/v1.go).
Licence: FSL-1.1-ALv2 (Functional Source License), which restricts competing use by large
organisations; version 2.40.1 on 2026-09-30. Cirrus Labs joined OpenAI on 2026-04-07 and Cirrus CI
shut down on 2026-06-01 ([report](https://aiidelist.com/fr/blog/openai-brings-tart-team-into-agent-infrastructure)).
Maintenance of the public image templates is unclear after the move
([macos-image-templates #361](https://github.com/cirruslabs/macos-image-templates/issues/361)).

**Wire format** (verbatim strings from `Manifest.swift`):

```text
manifest.config:  application/vnd.oci.image.config.v1+json   # stub, "for Docker Hub compatibility"
layers:           application/vnd.cirruslabs.tart.config.v1   # VM config.json, first layer
                  application/vnd.cirruslabs.tart.disk.v2     # disk chunks
                  application/vnd.cirruslabs.tart.disk.asif.overlay.v1   # stacked overlays (macOS 27+)
                  application/vnd.cirruslabs.tart.nvram.v1    # NVRAM, last layer
legacy (refused): application/vnd.cirruslabs.tart.disk.v1
manifest annotations: org.cirruslabs.tart.uncompressed-disk-size, org.cirruslabs.tart.upload-time,
                      org.cirruslabs.tart.disk.block-size
config label:         org.cirruslabs.tart.disk.format   (raw | asif)
layer annotations:    org.cirruslabs.tart.uncompressed-size, org.cirruslabs.tart.uncompressed-content-digest,
                      org.cirruslabs.tart.disk-file-content-digest, org.cirruslabs.tart.disk-file-chunk-count
```

A live probe of `ghcr.io/cirruslabs/macos-tahoe-base:latest` on 2026-10-02 showed 96 layers (one
config, 94 `disk.v2`, one nvram), 27.3 GB compressed for a 50 GB logical disk, largest layer 533 MB.
The stub config is `{"architecture":"arm64","os":"darwin","config":{"Labels":{"org.cirruslabs.tart.disk.format":"raw"}}}`.
The VM config layer carries `hardwareModel` (base64 `VZMacHardwareModel.dataRepresentation`),
`ecid` (`VZMacMachineIdentifier`), `macAddress`, CPU and memory sizes and minimums, display and
`diskFormat`.

**Why Tart has its own format.** Tart is not solving a problem that an OCI-native design misses;
it is solving five ordinary problems, and guestweave-macos already solves them because it is a port
of Tart's code ([03-current-state.md](03-current-state.md)):

1. A VM is not a filesystem changeset. Standard OCI layers are tar diffs applied to a rootfs
   ([layer spec](https://github.com/opencontainers/image-spec/blob/v1.1.1/layer.md)). A macOS VM is
   a raw block device plus NVRAM plus a hardware model and a machine identifier. Custom media types
   stop container tooling from interpreting the bytes and let them be stored verbatim.
2. Registry limits. GHCR caps a layer at 10 GB and an upload at 10 minutes
   ([GitHub docs](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry)).
   A 50 GB disk must be split. `DiskV2.swift` cuts the disk into 512 MiB slices
   (`layerLimitBytes`), compresses each independently, uploads four at a time with five retries, and
   skips blobs the registry already has via `HEAD` (`blobExists`).
3. Sparseness. A fresh macOS disk is mostly zeros. On pull, Tart compares 4 MiB windows against a
   zero buffer and leaves holes, writing into a pre-truncated sparse file.
4. Boot metadata that Docker's config has no fields for. The VZ hardware model and ECID must travel
   with the disk or the guest will not boot, so Tart stores them in its own config layer.
5. History. Tart predates OCI image-spec 1.1 (February 2024), so there was no `artifactType` and no
   empty-config convention. It used a stub Docker-style config with custom layers, which the OCI
   [artifacts guidance](https://github.com/opencontainers/image-spec/blob/v1.1.1/artifacts-guidance.md)
   now calls historical non-conformance.

**Other behaviour worth knowing.** Pulls are resumable: an existing partial `disk.img` is reused and
each chunk is checked against its `uncompressed-content-digest` before it is downloaded again, with
`Range` requests ([#569](https://github.com/openai/tart/issues/569), PR #589). Reported failures
on 50–60 GB images are connection drops and GHCR's "Egress is over the account limit" 503
([#783](https://github.com/openai/tart/issues/783), [#944](https://github.com/openai/tart/issues/944)).
`chooseLocalLayerCache` picks the cached image with the largest byte overlap, and the opt-in
`--deduplicate` flag APFS-clones that disk as the base (PR #924). The cache is
`~/.tart/cache/OCIs/<host>/<namespace>/<sha256:…>` with tag symlinks pointing at digest directories;
an LRU prune runs before pull and clone with a 100 GB default (`TART_NO_AUTO_PRUNE`). `tart login`
stores credentials in the macOS Keychain and Docker credential helpers and `TART_REGISTRY_*` are
honoured. Push is monolithic unless `--chunk-size` is given; the help text claims "GitHub Container
Registry supports only chunks smaller than 4MB" *(unverified)*. Since Tart 2.36 (August 2026)
`tart clone --stacked` keeps the base disk immutable and writes to an ASIF overlay, and a push of a
derived image publishes only the overlay group (`disk.asif.overlay.v1`); it needs macOS 27 and
DiskImageKit. Orchard, the scheduler, records the digest-resolved `ImageFQN` after a worker pulls
and honours Apple's two-macOS-VMs-per-host limit.

**Adopted.** Fixed 512 MiB chunks keyed by guest-disk offset; per-chunk uncompressed size and digest
annotations; sparse zero-skipping reassembly; chunk-granular resume; `HEAD` before upload to skip
blobs the registry has; a digest-keyed cache with tag links, LRU prune and a disk-space guard;
digest pinning after pull as Orchard does ([06-large-artifacts.md](06-large-artifacts.md),
[decision 0008](decisions/0008-device-cache-gc-and-mirrors.md)).

**Avoided.** Every `org.cirruslabs` and `vnd.cirruslabs` string. The OCI image-config stub (we use
a custom config media type and `artifactType`, [decision 0001](decisions/0001-vm-artifact-contract.md)).
LZ4 (we use zstd). Shipping the ECID and MAC address in the artifact (regenerated per clone,
[decision 0009](decisions/0009-guest-state-carry-vs-regenerate.md)). Stacked ASIF overlays, which
tie dedupe to macOS 27 and DiskImageKit. Any build-time or runtime dependency on Tart, its Packer
plugin or its images ([decision 0010](decisions/0010-remove-tart-and-lume-compatibility.md)).

## Lume / cua (trycua)

Sources: [trycua/cua `media_types.rs`](https://github.com/trycua/cua/blob/main/libs/cua/crates/cua-image/src/media_types.rs),
[Lume CLI reference](https://cua.ai/docs/lume/reference/cli-reference),
[Windows 2022 `push-disk.sh`](https://github.com/trycua/cua/blob/main/libs/images/windows-2022/push-disk.sh),
live probes of `ghcr.io/trycua/macos-sequoia-cua:latest` and `ghcr.io/trycua/macos-sequoia-vanilla:latest`.
Licence: MIT.

Lume publishes macOS images on GHCR in several encodings, which guestweave-macos has had to handle
with four codecs ([03-current-state.md](03-current-state.md)):

- *Chunked*: custom config `application/vnd.trycua.lume.config.v1+json`; layers
  `application/vnd.trycua.lume.disk.v1` × N (512 MiB `disk.img.part.N` with
  `org.trycua.lume.part.{number,offset,total}` and uncompressed digest and size) and
  `application/vnd.trycua.lume.nvram.v1`. `macos-sequoia-cua:latest` has 161 layers for an 80 GB disk.
- *Sharded* (what the public images actually contain): raw, uncompressed 500 MiB splits whose media
  type embeds the order as non-RFC parameters
  (`application/vnd.oci.image.layer.v1.tar;part.number=N;part.total=M`), plus `config.json` and
  `nvram.bin` layers. The `.tar` label is a misnomer; the content is raw disk bytes.
- *Legacy*: sequential `application/octet-stream+lz4` parts.
- *QEMU*: `application/vnd.trycua.qemu.disk.v1[+gzip]`.

`media_types.rs` also recognises Tart, agoda macosvz and KubeVirt containerDisk layouts. For Windows,
cua publishes Windows Server 2022 as a KubeVirt containerDisk at
`ghcr.io/trycua/windows:2022-disk-<YYYYMMDD-sha7>`: a qcow2 at `/disk/disk.img` owned 107:107 inside
an OCI index annotated `ai.cua.image.os=windows`, with immutable tags checked before push.

**Adopted.** The fact that GHCR accepts an unknown config media type (Lume proves it live), immutable
date-plus-commit tags, and guest-OS annotations on the index so listings never need the config blob.

**Avoided.** Multiple encodings under one brand; uncompressed shards (41–82 GB transfers); media
types that lie about their content; the sharded `part.number` parameter hack.

## Podman machine-os

Sources: [`quay.io/podman/machine-os`](https://quay.io/repository/podman/machine-os) (live probe of tag
`5.6`), [`pkg/machine/ocipull/ociartifact.go`](https://github.com/containers/podman/blob/main/pkg/machine/ocipull/ociartifact.go).
Licence: Apache-2.0.

One index mixes the bootc container image (`linux/arm64`, `linux/amd64`) with disk artifacts whose
platform uses non-GOARCH values (`aarch64`, `x86_64`) plus a custom `disktype` annotation
(`applehv`, `hyperv`, `qemu`, `wsl`). Each disk manifest has an empty config
(`application/vnd.oci.empty.v1+json`) and a single `application/octet-stream` (or `application/zstd`)
layer titled, for example, `podman-machine.aarch64.applehv.raw.zst` (about 0.94 GB) or
`…hyperv.vhdx.zst`. No `artifactType` is set, which violates the image-spec MUST for an empty
config. Registries tolerate the non-standard platform values.

**Adopted.** One index per logical image carrying several children; the precedent that a VHDX can
sit in an OCI registry and be consumed by a Windows hypervisor (libkrun and Hyper-V consume these).

**Avoided.** Empty config without `artifactType`; non-GOARCH architectures; one monolithic blob per
disk (fine at 1 GB, not at 50 GB); hypervisor as the variant axis (our disks are raw, so the
hypervisor is advisory, [09-artifact-contract-v1.md](09-artifact-contract-v1.md)).

## KubeVirt containerDisk and kubevirt/containerdisks

Sources: [disks and volumes guide](https://kubevirt.io/user-guide/storage/disks_and_volumes/),
[container-register-disks design](https://github.com/kubevirt/kubevirt/blob/main/docs/container-register-disks.md),
[CNCF blog on the VM ecosystem](https://www.cncf.io/blog/2024/01/08/optimizing-the-construction-of-the-vm-ecosystem-with-kubevirt/),
[kubevirt/containerdisks README](https://github.com/kubevirt/containerdisks),
[`pkg/build/build.go`](https://github.com/kubevirt/containerdisks/blob/main/pkg/build/build.go),
[`pkg/build/tar.go`](https://github.com/kubevirt/containerdisks/blob/main/pkg/build/tar.go),
[`cmd/medius/images/push.go`](https://github.com/kubevirt/containerdisks/blob/main/cmd/medius/images/push.go),
[`pipeline.sh`](https://github.com/kubevirt/containerdisks/blob/main/pipeline.sh),
[KubeVirt v1.7.0 changelog](https://kubevirt.io/2025/changelog-v1.7.0.html). Licence: Apache-2.0.

**Layout.** A `FROM scratch` image with `ADD --chown=107:107 disk.qcow2 /disk/`. The production
builder writes one tar layer containing `disk/` (mode 0555, uid/gid 107 "qemu") and `disk/disk.img`
(0444), a Docker schema 2 manifest with `os=linux`, a config label `shasum=<upstream checksum>` and
`Entrypoint: ["no-entrypoint"]` so crun-vm can run the image directly. A live probe of
`quay.io/containerdisks/fedora:latest` showed a manifest list for amd64, arm64 and s390x, each with a
single `application/vnd.docker.image.rootfs.diff.tar.gzip` layer of about 526 MB. The virt-launcher
pod runs the image as a sidecar; virt-handler bind-mounts the file and libvirt uses it as a qcow2
backing file with an ephemeral overlay. The docs say it is "not a good solution for any workload that
requires persistent root disks across VM restarts". KubeVirt 1.7 promoted the ImageVolume feature
gate to beta (PR #16005) as the path to reimplement containerDisk on Kubernetes image volumes; the
latest KubeVirt is v1.9.0 (2026-07-30).

**Pipeline.** The `medius` tool detects the latest upstream release, compares it with Quay, and only
when they differ runs `images push`, then `images verify` (boots the image in a kubevirtci cluster
and runs SSH and guest-osinfo checks), then `images promote`. Images that do not boot are not
published. Each artifact verifies the upstream checksum file (Ubuntu `SHA256SUM`, AlmaLinux and
CentOS `CHECKSUM`, Debian JSON). Tags are `<version>-<YYMMDDHHMM>` (immutable), `<version>` and
`latest`; the live list shows `44`, `44-1.7`, `44-2604290212`, `45-beta`, `45-beta-2609160212`,
`latest`. No cosign code and no `sha256-*.sig` tags were found, so the images appear unsigned.
Metadata travels as environment variables named `*_KUBEVIRT_IO_*` rather than annotations, because
the manifest is unavailable when the node's runtime pulls the image
([CDI image-from-registry](https://github.com/kubevirt/containerized-data-importer/blob/main/doc/image-from-registry.md)).

**Adopted.** Push → boot-verify → promote as the publication gate
([07-build-pipelines.md](07-build-pipelines.md)); immutable timestamped tags plus moving tags;
upstream checksum verification recorded in provenance.

**Avoided.** A single gzip tar layer (no dedupe, single-threaded decompression, a 10 GB GHCR layer
cap on compressed size); letting the container runtime own the disk; uid 107 and `/disk/`
conventions, which only mean something to KubeVirt and crun-vm.

## KubeVirt CDI

Sources: [datavolumes.md](https://github.com/kubevirt/containerized-data-importer/blob/main/doc/datavolumes.md),
[CDI user guide](https://kubevirt.io/user-guide/storage/containerized_data_importer/). Licence:
Apache-2.0; latest v1.66.1 (2026-09-06).

DataVolume sources are `http`, `s3`, `gcs`, `registry`, `upload`, `imageio`, `vddk`, `blank`, `pvc`
and `snapshot`; formats raw and qcow2 (gz or xz compressed). HTTP sources accept `md5`, `sha1`,
`sha256` or `sha512` checksums as `algo:hash`. The `registry` source accepts the containerDisk layout
with `pullMethod: pod` (default; the importer fetches) or `pullMethod: node` (the node's runtime
pulls through CRI, node caches and pull secrets apply, conversion runs through nbdkit and qemu-img,
and checksum validation is not supported).

**Adopted.** Explicit checksums on every source download and conversion at the consumer, not the
publisher ([07-build-pipelines.md](07-build-pipelines.md)).

**Avoided.** Node-pull style delegation to a runtime that cannot verify the content.

## Kubernetes Image Volumes (KEP-4639)

Sources: [image volumes task](https://kubernetes.io/docs/tasks/configure-pod-container/image-volumes/),
[KEP-4639](https://github.com/kubernetes/enhancements/tree/master/keps/sig-node/4639-oci-volume-source),
[KEP-5365](https://www.kubernetes.dev/resources/keps/5365/),
[CRI-O 1.31 release](https://cncf.io/blog/2024/09/12/whats-new-in-cri-o-1-31). Licence: Apache-2.0.

`volumes[].image` is GA since Kubernetes 1.36 and on by default (alpha 1.31, beta 1.33, enabled by
default 1.35; current release 1.37.1). The kubelet pulls the object and passes it through CRI
`Mount.image`; the runtime merges the layers into one directory and bind-mounts it read-only.
Runtimes must handle both tarball and plain-file layers (CRI-O ≥ 1.31, containerd ≥ 2.1). The KEP
explicitly does not want the runtime "responsible for interpreting … qcow files", so this is a file
delivery mechanism, not a VM disk mechanism.

**Adopted.** Confidence that plain-file (non-tar) artifact layers are a mainstream pattern.

**Avoided.** Nothing to avoid; it is simply not a VM disk transport.

## bootc and image-builder

Sources: [bootc.dev](https://bootc.dev/bootc/),
[bootc-compatible images](https://github.com/bootc-dev/bootc/blob/main/docs/src/bootc-compatible-images.7.md),
[bootc upgrades](https://github.com/bootc-dev/bootc/blob/main/docs/src/bootc-upgrades.7.md),
[bootc and OCI artifacts](https://github.com/bootc-dev/bootc/blob/main/docs/src/bootc-oci-artifacts.7.md),
[bootc PR #299](https://github.com/bootc-dev/bootc/pull/299),
[osbuild/image-builder](https://github.com/osbuild/image-builder),
[bootc-image-builder README](https://github.com/osbuild/image-builder/blob/main/bootc-image-builder/README.md),
[bootc-generic imagetypes.yaml](https://github.com/osbuild/image-builder/blob/main/data/distrodefs/bootc-generic/imagetypes.yaml).
Licences: Apache-2.0 (bootc), Apache-2.0 (image-builder). bootc v1.16.13 (2026-09-15);
image-builder v85.0.0 (2026-10-01).

A bootc image is an ordinary OCI image labelled `containers.bootc=1` (the legacy label was
`ostree.bootable=1`), with the kernel at `/usr/lib/modules/$kver/vmlinuz` and nothing in `/boot`;
`bootc container lint` checks this. Deployed machines run `bootc upgrade` (A/B, `--apply`,
`--download-only`, `--check`), `bootc switch` and `bootc rollback`. bootc states that packaging disk
images as OCI artifacts "is not a goal". bootc-image-builder, now inside osbuild/image-builder,
turns a bootc image into `ami`, `qcow2` (default), `vmdk`, `raw`, `vhd`, `gce`, `anaconda-iso`,
`bootc-installer`, `pxe-tar-xz` and (per the distro definitions) `ova`, with `--rootfs`,
`--target-arch` and repeatable `--type`.

**Adopted.** For Linux, the bootc image is the source of truth and the raw disk is derived in CI
([decision 0005](decisions/0005-linux-images-bootc-and-cloud-images.md)). The artifact records the bootc image digest it
was derived from.

**Avoided.** Treating a bootc image as a VM disk, or pulling a bootc image on a device and building
a disk there.

## Apple containerization

Sources: [apple/containerization](https://github.com/apple/containerization); the `vminit` image
detail (`ghcr.io/apple/containerization/vminit:<ver>`) comes from a secondary source
([instagit summary](https://instagit.com/apple/container/container-init-image-purpose/)) *(unverified)*.
Licence: Apache-2.0.

Container images are unpacked into ext4 block devices and run in lightweight VZ Linux VMs; the init
filesystem is itself an OCI image, while the kernel is fetched as a tar from a URL
(`container system kernel set --tar`). Linux arm64 only.

**Adopted.** Nothing beyond confirmation that Apple ships VM boot content through GHCR.

**Avoided.** Not applicable to macOS or Windows guests.

## Flintlock (Liquid Metal)

Sources: [liquidmetal-dev/flintlock](https://github.com/liquidmetal-dev/flintlock), example payload
`hack/scripts/payload/CreateMicroVM.json`. Licence: MPL-2.0.

Kernel and initrd are files inside ordinary container images (`kernel.image` with
`filename: vmlinux`); the rootfs comes from a `container_source`. hostweave already borrowed its
watch stream ([hostweave prior art](https://github.com/deploymenttheory/hostweave/blob/main/docs/research/prior-art.md)).

**Adopted.** Nothing new for images.

**Avoided.** Firecracker-only, Linux-only scope.

## Kata Containers

Sources: [`tools/packaging/scripts/populate-oras-tarball-cache.sh`](https://github.com/kata-containers/kata-containers/blob/main/tools/packaging/scripts/populate-oras-tarball-cache.sh).
Licence: Apache-2.0.

Build caches are ORAS artifacts at `ghcr.io/kata-containers/cached-artefacts/<target>:latest-<branch>-<arch>`;
the confidential-containers extension disk is pulled with `oras resolve --platform linux/<arch>`.
Kernel and rootfs releases otherwise ship as tarballs.

**Adopted.** `oras resolve --platform` on an index as the consumer-side selection step.

**Avoided.** Release tarballs outside the registry.

## Kairos

Sources: [container architecture](https://kairos.io/docs/architecture/container/),
[AuroraBoot](https://kairos.io/docs/reference/auroraboot/). Licence: Apache-2.0.

"The OS is a single container image which contains all the OS components, including Kernel and
Initrd." Bootable media (ISO, raw, cloud images) are derived from that image by AuroraBoot
(`build-iso oci:…`), and nodes upgrade atomically A/B from the registry.

**Adopted.** Reinforces the bootc pattern: OCI image first, disk derived.

**Avoided.** Linux-only.

## Talos Image Factory

Sources: [Image Factory docs](https://docs.siderolabs.com/talos/v1.13/learn-more/image-factory.md).
Licence: MPL-2.0.

Schematics are content-addressed; disks are served from
`https://factory.talos.dev/image/<schematic>/<version>/<asset>` and the installer is an OCI image at
`factory.talos.dev/installer/<schematic>:<version>` that the factory signs and verifies.

**Adopted.** A content-addressed build definition recorded with the artifact (our provenance
predicate, [05-supply-chain.md](05-supply-chain.md)).

**Avoided.** Serving disks over HTTPS beside the registry; one transport is enough.

## CNCF ModelPack (model-spec)

Sources: [model-spec](https://github.com/modelpack/model-spec/blob/main/docs/spec.md). Licence:
Apache-2.0.

A domain spec built the way OCI 1.1 intends: `artifactType: application/vnd.cncf.model.manifest.v1+json`,
config `application/vnd.cncf.model.config.v1+json`, and typed layers
`application/vnd.cncf.model.{weight,weight.config,doc,code,dataset}.v1.{raw,tar,tar+gzip,tar+zstd}`.

**Adopted.** The shape of the weave contract: one `artifactType`, one config media type, a small
family of typed layer media types with explicit compression suffixes
([09-artifact-contract-v1.md](09-artifact-contract-v1.md)).

## weaveplatform-agent-modules release pipeline (internal)

Sources: [`docs/release-pipeline.md`](https://github.com/deploymenttheory/weaveplatform-agent-modules/blob/main/docs/release-pipeline.md),
[`.github/workflows/module-release.yml`](https://github.com/deploymenttheory/weaveplatform-agent-modules/blob/main/.github/workflows/module-release.yml),
[weaveplatform-manifest trust chain](https://github.com/deploymenttheory/weaveplatform-manifest/blob/main/docs/trust-chain.md).
Local checkouts: `weaveplatform-agent-modules@main docs/release-pipeline.md`,
`.github/workflows/module-release.yml:106-128`.

This is the only part of the weave ecosystem that already publishes OCI artifacts. release-please
tags `<module>/vX.Y.Z`; the workflow builds a matrix from `module.manifest.json`, stamps a sidecar
manifest with per-artifact `{os, arch, digest, size}`, and runs
`oras push ghcr.io/${OWNER}/weaveplatform-modules/${MODULE}:${VERSION} --artifact-type application/vnd.weave.module ./*`
with `GITHUB_TOKEN`. It then sends `repository_dispatch` (`module-published`) to weaveplatform-manifest,
which opens a promotion PR that re-signs `channels/stable.json`; merging is the promotion act. The
trust chain is an offline Ed25519 root key (embedded in core) endorsing an annual signing key that
signs the channel manifest, deliberately with "no Fulcio, no Rekor, no CA" so air-gapped hosts verify
with nothing but the root.

**Adopted.** The whole shape: ORAS push with an explicit `artifactType` under `application/vnd.weave.*`,
digests as the primitive the channel manifest records, dispatch into weaveplatform-manifest, and a
promotion PR as the human decision ([decision 0006](decisions/0006-trust-attestations-and-channel-manifest.md),
[decision 0007](decisions/0007-publication-pipeline.md)). The images pipeline sends an
`image-published` event alongside `module-published`.

**Avoided.** Nothing. The images pipeline extends it.

## Naming observed in the wild

Probed on 2026-10-02 unless stated.

| Publisher | Repository pattern | Tags | Platform/variant mechanism |
|---|---|---|---|
| cirruslabs (Tart) | `ghcr.io/cirruslabs/macos-<codename>-{vanilla,base}`, `…-xcode`, `macos-runner` | `latest`, `<macos_version>`, `<xcode_version>`; `macos-tahoe-base` exposes only `latest` | One manifest per tag; `os`/`arch` in the stub config |
| trycua (Lume) | `ghcr.io/trycua/macos-sequoia-{vanilla,cua}`, `ghcr.io/trycua/windows` | `latest`, `15.3`; Windows `2022-disk-<YYYYMMDD-sha7>` | Index annotations `ai.cua.image.*` |
| Podman | `quay.io/podman/machine-os` | `5.6` (Podman version) | One index; non-GOARCH platform + `disktype` annotation |
| kubevirt/containerdisks | `quay.io/containerdisks/<distro>` | `44`, `44-1.7`, `44-2604290212`, `latest` | Manifest list per arch |
| Fedora bootc | `quay.io/fedora/fedora-bootc` | `46`, `46-aarch64` | Index per tag plus arch-suffixed tags |
| Homebrew bottles | `ghcr.io/homebrew/core/<formula>` | `<version>` | Index with `os.version: "macOS 15.7"` and `ref.name` such as `1.25.0_2.arm64_sequoia` |
| weaveplatform-agent-modules | `ghcr.io/deploymenttheory/weaveplatform-modules/<module>` | `X.Y.Z` | Sidecar manifest lists per-OS/arch binaries |

The weave repository and tag scheme that follows from this is in
[09-artifact-contract-v1.md](09-artifact-contract-v1.md) and
[decision 0002](decisions/0002-registry-repositories-tags-visibility.md).
