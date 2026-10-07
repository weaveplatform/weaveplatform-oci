# 0015: One image release with target-specific delivery

Status: Accepted (design; native and cloud acceptance remains outstanding)

## Context

The deployment targets are hyperscalers, private clouds, Windows devices and
macOS devices running Windows guests. Disk-container support does not establish
guest compatibility: architecture, firmware, drivers, first-boot provisioning and
the management transport also have to match the destination.

## Decision

Keep decision 0001's raw guest-sector OCI contract. Build and run Windows guests
on Windows using VHDX through native virtdisk/HCS APIs. Convert at delivery
boundaries automatically; users select a target rather than a chain of formats.
Fixed VHD remains an Azure OS-image import format and a temporary bridge between
virtdisk and raw sectors. It is not the Windows builder's working disk.

The shared release recipe pins OS media, architecture, agent core and modules.
Each target output records the exact source index and platform-manifest digest,
target preparation, file hashes or provider image identifiers, and its own
acceptance evidence. A format conversion is not a validated target image.
Provider-native builds may use a pinned provider base with the same package lock;
they must record that actual base and must not claim byte identity with another
provider's image. Different target preparations need distinct image references,
not ambiguous duplicate OS/architecture entries in a single OCI index.

| Destination | Local/import representation | Deployable result |
|---|---|---|
| Guestweave Windows / Hyper-V | VHDX | Fresh VM and differencing disk |
| Guestweave macOS Windows backend | Raw disk, matching architecture | Fresh native VM |
| Azure | Fixed VHD for OS-disk import, or native build | Compute Gallery image version |
| AWS | Raw or supported native import | Registered AMI |
| Google Cloud | Supported import, including archive containing disk.raw | Compute Engine image |
| OpenStack / KVM | Raw or QCOW2, depending on deployment | Registered and boot-tested image |
| VMware | VMDK plus required VM/OVF metadata | Template or OVA |

`weaveoci image export` implements disk preparation, with `acceptance: pending`
in its receipt. It does not register cloud resources, prepare provider guest
agents, create an OVA descriptor, or certify a target. In particular an exported
VMDK alone is not an OVA or deployable vSphere template.

## Trust and validation

Publish the exact accepted OCI layout; never repack it after recording its
acceptance digest. A report must cover every included platform and two distinct
clone identities. Schema-1 evidence applies only to existing Linux base checks;
native and agent acceptance requires schema 2 with platform bindings and explicit
version, reboot, identity, sealing, trust and module checks. Sign reports in the
publication workflow and verify their signature before admission at promotion.
Local report validation is not report authentication.

Derived images require an exact platform-manifest parent already present in the
verified destination channel. Recheck at promotion because channel state can
change between building and review. A skipped test is never a passing test.

For direct cloud guests, define and test a reachable authenticated management
transport; an HvSocket or virtio channel controlled by the hypervisor host cannot
be assumed accessible to the cloud tenant. Nested guests on a Weave-controlled
host are a different target topology.

## Alternatives

- VHDX as the universal distribution format: appropriate for Hyper-V but still
  requires conversion for Azure OS images and the current macOS Windows backend.
- Raw OCI with conversion entirely in consumers: preserves portability but does
  not handle provider guest preparation or registered cloud image identities.
- Shared release inputs with validated target outputs: selected. Keep a common
  portable representation while making target readiness an explicit property.

## Verification

Unit tests exercise container creation/conversion boundaries, archive contents,
digest binding, failed/partial acceptance and platform-specific parent lineage.
Native and cloud suites must test two independent clones, reboot persistence and
agent operations on the final deployment. Their deferred execution is tracked in
[Windows incident #38](https://github.com/weaveplatform/weaveplatform-oci/issues/38)
and [cloud incident #39](https://github.com/weaveplatform/weaveplatform-oci/issues/39).

## References

Research checked 2026-10-07:

- [Azure Windows image preparation](https://learn.microsoft.com/en-us/azure/virtual-machines/windows/prepare-for-upload-vhd-image)
- [Azure VHDX upload restrictions](https://learn.microsoft.com/en-us/azure/virtual-machines/windows/disks-upload-vhd-to-managed-disk-powershell)
- [Azure OS/data disk restrictions](https://learn.microsoft.com/en-us/azure/virtual-machines/linux/tutorial-manage-disks)
- [AWS VM Import formats and architecture limits](https://docs.aws.amazon.com/vm-import/latest/userguide/prerequisites.html)
- [Google image import formats](https://docs.cloud.google.com/migrate/virtual-machines/docs/5.0/migrate/migration-strategy)
- [Google guest adaptations](https://docs.cloud.google.com/migrate/virtual-machines/docs/5.0/resources/vm-adaptations)
- [OpenStack image API](https://docs.openstack.org/api-ref/image/v2/index.html)
- [Kubernetes Image Builder: provider-specific outputs with shared configuration](https://github.com/kubernetes-sigs/image-builder/blob/main/docs/book/src/capi/capi.md)
- [GitHub runner-images: native Azure image builds](https://github.com/actions/runner-images/blob/main/docs/create-image-and-azure-resources.md)
- Guestweave Windows `48fe424`: `internal/vhdx/vhdx.go`; macOS `1ae4c29`: `internal/hypervisor/hardware/ahci.go`.
