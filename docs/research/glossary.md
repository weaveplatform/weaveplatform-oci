# Glossary

Terms used across the research set. Primer-level detail is in
[01-oci-primer.md](01-oci-primer.md); the contract that gives the weave-specific terms
their exact meaning is [09-artifact-contract-v1.md](09-artifact-contract-v1.md).

**Acceptance test.** A godog (Gherkin) feature that drives the built `weaveoci` binary against real registries started with testcontainers. Every implementation phase ships its own features under `test/acceptance/features/`. See [0013](decisions/0013-quality-gates.md).

**Annotation.** A string-to-string map on a manifest, index or descriptor. Keys are
reverse-DNS; `org.opencontainers.*` is reserved. See [01-oci-primer.md](01-oci-primer.md).

**artifactType.** The OCI 1.1 manifest field that says what a non-container artifact is.
Required when the config is the empty descriptor. See [01-oci-primer.md](01-oci-primer.md).

**ASIF.** Apple Sparse Image Format, the disk-image format Virtualization.framework
prefers on macOS 26 and later. Treated as a host-side encoding of a raw disk in this
design. See [06-large-artifacts.md](06-large-artifacts.md).

**Attestation.** A signed statement about an artifact, such as SLSA provenance or an SBOM,
stored as a Sigstore bundle. See [05-supply-chain.md](05-supply-chain.md).

**Auxiliary storage.** Virtualization.framework's per-VM store for macOS guests
(`nvram.bin` in guestweave), created from the hardware model at install time and carried
with the disk. See [09-artifact-contract-v1.md](09-artifact-contract-v1.md).

**bootc.** "Bootable containers": a Linux OS packaged as an OCI image that updates itself
in place. See [02-prior-art.md](02-prior-art.md).

**Bundle (Sigstore).** The single JSON envelope holding a signature, certificate and
transparency-log proof. See [05-supply-chain.md](05-supply-chain.md).

**Bundle (VM).** The on-disk directory a guestweave CLI keeps for one VM: config, disk and
state files. See [03-current-state.md](03-current-state.md).

**CAS.** Content-addressed storage: blobs stored and named by their digest, as in a
registry or the local cache. See [06-large-artifacts.md](06-large-artifacts.md).

**Channel manifest.** The minisign-signed document in weaveplatform-manifest that lists
promoted versions; extended here to list promoted image digests. See
[05-supply-chain.md](05-supply-chain.md).

**Chunk.** A fixed-size slice of guest disk address space, stored as one compressed OCI
layer. See [06-large-artifacts.md](06-large-artifacts.md).

**containerDisk.** KubeVirt's convention of shipping a qcow2 or raw disk at `/disk/`
inside an ordinary container image. Prior art only. See [02-prior-art.md](02-prior-art.md).

**Coverage gate.** The CI check that fails unless merged coverage from the Linux, macOS and Windows runs is at least 95 % in total and at least 90 % per package. See [0013](decisions/0013-quality-gates.md).

**Deployment profile.** A named configuration that sets the canonical registry, mirrors, signing provider and verification policy: `github`, `private` or `hybrid`. The artifact contract and the code are the same in all three. See [13-deployment-profiles.md](13-deployment-profiles.md).

**Descriptor.** A typed pointer `{mediaType, digest, size, …}` to a blob. See
[01-oci-primer.md](01-oci-primer.md).

**Differencing VHDX.** A VHDX whose unwritten blocks fall through to a parent file; how
guestweave-cli-windows clones a cached image. See [03-current-state.md](03-current-state.md).

**Digest.** `sha256:<hex>` of a blob's exact bytes; the content address.

**Digest pinning.** Referring to an image as `repo@sha256:…` rather than by tag, so the
bytes cannot change underneath a consumer. Mandatory in hostweave.

**ECID.** The per-machine identifier Virtualization.framework assigns to a macOS guest
(`VZMacMachineIdentifier`). Never shipped in a published image; regenerated on clone.

**Fallback tag.** The `sha256-<hex>` tag a client maintains when a registry has no
referrers API, so referrers can still be found. GHCR requires it. See
[05-supply-chain.md](05-supply-chain.md).

**GHCR.** GitHub Container Registry, `ghcr.io`. See
[04-registries-and-github.md](04-registries-and-github.md).

**hardwareModel.** The `VZMacHardwareModel` a macOS disk was installed against. A guest
boots only on a compatible model, so it is carried in the config.

**HCS.** Windows Host Compute Service, the layer guestweave-cli-windows drives directly.

**Healthcheck endpoint.** zot's unauthenticated `/livez`, `/readyz` and `/startupz` paths. `weave-zot` probes `/readyz` with `weaveoci healthcheck` because the distroless image has no shell or curl. See [13-deployment-profiles.md](13-deployment-profiles.md).

**Image index.** An OCI document listing manifests, usually one per platform. See
[01-oci-primer.md](01-oci-primer.md).

**IPSW.** Apple's restore-image archive used to install macOS into a VM.

**Machine identifier.** The Linux-guest counterpart of the ECID (`machineidentifier.bin`).
Regenerated on clone.

**Manifest.** The OCI document naming an artifact's config and layers. See
[01-oci-primer.md](01-oci-primer.md).

**Media type.** The MIME-style string that types a config or layer, for example
`application/vnd.weave.guest.disk.v1.raw+zstd`.

**minisign chain.** The root-key → signing-key → channel-manifest trust chain in
weaveplatform-manifest. See [05-supply-chain.md](05-supply-chain.md).

**NVRAM.** Firmware variable storage. Means auxiliary storage on macOS guests,
`NVRAM.dat` for Windows guests on Hypervisor.framework, and UEFI variables (OVMF VARS or
`guest.vmgs`) elsewhere. See [09-artifact-contract-v1.md](09-artifact-contract-v1.md).

**OCI layout.** The on-disk form of a registry (`oci-layout`, `index.json`, `blobs/`)
used for export and air-gapped import. See [04-registries-and-github.md](04-registries-and-github.md).

**ORAS.** OCI Registry As Storage: the CLI and Go library (oras-go) for pushing and
pulling arbitrary artifacts. See [10-shared-go-module.md](10-shared-go-module.md).

**Overlay (qcow2).** A qcow2 file whose unwritten blocks fall through to a backing file;
how the QEMU runtime gives each attempt its own disk.

**Platform.** The `{os, architecture, variant, os.version, os.features}` object on an
index entry. See [09-artifact-contract-v1.md](09-artifact-contract-v1.md).

**Promotion.** The human-approved act of adding a published digest to the channel
manifest. See [07-build-pipelines.md](07-build-pipelines.md).

**Provenance.** A SLSA attestation recording how an artifact was built and from which
inputs. See [05-supply-chain.md](05-supply-chain.md).

**Raw disk.** A byte-for-byte image of a guest block device, with no container format.
The canonical disk encoding in the contract.

**Referrer.** A manifest whose `subject` points at another manifest: signatures, SBOMs,
attestations. See [01-oci-primer.md](01-oci-primer.md).

**Registry profile.** A named registry host, organisation and options (including mirrors)
in a consumer's configuration. See [04-registries-and-github.md](04-registries-and-github.md).

**Restore image.** Apple's term for an IPSW resolved through
`VZMacOSRestoreImage.fetchLatestSupported`; what guestweave-cli-macos uses instead of GDMF.

**SBOM.** Software bill of materials (SPDX or CycloneDX) attached as a referrer.

**Sidecar manifest.** The JSON file the modules pipeline stamps with per-artifact digests
before an ORAS push. See [03-current-state.md](03-current-state.md).

**Signing config (cosign).** The cosign v3 file that lists which Sigstore services to use. A signing config with no services is how the private profile signs with a key and no transparency log. See [05-supply-chain.md](05-supply-chain.md).

**SLSA.** Supply-chain Levels for Software Artifacts, the provenance framework GitHub
attestations implement. See [05-supply-chain.md](05-supply-chain.md).

**Sparse file.** A file whose all-zero regions occupy no storage; how reassembled disks
are written. See [06-large-artifacts.md](06-large-artifacts.md).

**Subject.** The manifest field that makes a manifest a referrer of another.

**Tart.** (historical; see [02-prior-art.md](02-prior-art.md)) The macOS VM tool whose
wire format guestweave-cli-macos currently ports. Not a dependency of the target design.

**Trust anchor.** A public key a consumer is configured to trust without further proof: the minisign channel root, or the cosign public key of the private profile. See [13-deployment-profiles.md](13-deployment-profiles.md).

**Trusted root.** The Sigstore document listing current certificate-authority,
transparency-log and timestamp keys needed to verify a bundle.

**UEFI variables.** The firmware variable store of a UEFI guest; a carried or regenerated
state blob depending on guest OS. See [09-artifact-contract-v1.md](09-artifact-contract-v1.md).

**vmgs.** The Hyper-V/HCS guest state file (`guest.vmgs`) holding UEFI variables and vTPM
state. Never shipped; regenerated per VM.

**vTPM.** A virtual TPM; its state is per-VM secret material and never part of an image.

**VZ.** Apple's Virtualization.framework.

**weave-zot.** The container image `ghcr.io/deploymenttheory/weave-zot`: upstream zot pinned by digest with weave config roles (`private`, `mirror`) and a healthcheck. The reference private registry and site mirror. See [0012](decisions/0012-container-images.md).

**zstd.** The Zstandard compressor used per chunk in the contract.
