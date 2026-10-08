# macOS base and prepared image boundary

Decision date: 2026-10-08. Implementation status: OCI contract implemented;
native provisioning and runtime qualification belong to Imageweave.

## Problem and scope

A completed Apple restore is a base awaiting Setup Assistant. A usable desktop
template is a different product: it has an account, remote access and a configured
login session. Treating both as base loses this distinction and makes acceptance
ambiguous. The selected outputs are macOS 27, 26 and 15, each as base and prepared,
on ARM64 Apple virtualization. Intel support is not claimed. Windows follows this
phase. Imageweave constructs; OCI packages and authenticates; Hostweave pins;
compatible runtimes, including Guestweave, execute.

## Upstream findings

* [Tart image templates](https://github.com/cirruslabs/macos-image-templates),
  reviewed at `2ff087f9f058ecb39538bf8dfeb368c7b86d4f6a`, separate reusable
  configured macOS images from additional software layers. Their macOS 27
  template uses native guest provisioning; older templates drive Setup Assistant.
  Their naming of a configured image as vanilla is not our definition of base.
* [Tart's Packer builder](https://github.com/cirruslabs/packer-plugin-tart)
  keeps a VM running through Packer's communicator and provisioner lifecycle.
  Reuse this lifecycle, not its invocation of the Tart executable. Packer remains
  the orchestration layer and its [SSH communicator](https://developer.hashicorp.com/packer/docs/communicators/ssh)
  supplies file and shell provisioners.
* [Apple guest provisioning](https://developer.apple.com/documentation/virtualization/vzmacguestprovisioningoptions)
  supplies account creation, automatic login and remote login for the supported
  first boot. It is not an API for modifying an already configured guest. Use it
  only with a compatible macOS 27 host/guest combination.
* [Cua/Lume](https://cua.ai/docs/lume/concepts/how-lume-works), reviewed at
  `2e4736b3ebff61ef99e8c0c74270b5cd75894643`, also implements offline account
  preparation and guest finalization. This is a real alternative but requires
  maintaining APFS, dslocal and Preboot details across releases. It is not the
  selected backend for this phase.
* [Anka's Packer clone builder](https://developer.hashicorp.com/packer/integrations/veertuinc/veertu-anka/latest/components/builder/vm-clone)
  reinforces the base-clone/provision/stop boundary. It does not justify creating
  another orchestration engine inside OCI.

## Selected behavior

Base means pristine Apple restore: no added account, SSH configuration, agent or
modules. Prepared derives from the exact verified base platform digest and retains
the explicitly selected `weave` administrator with password `weave`, SSH and
automatic login. macOS 27 uses Apple's native first-boot options; 26 and 15 use
observed Setup Assistant UI automation. No offline account injection is selected.

Do not copy unrelated upstream settings that weaken Gatekeeper, SIP or trust.
There is no macOS equivalent of a general Windows Sysprep step. Preserve the
intended account and OS security state; remove temporary authorized keys, SSH
host keys and build-specific identity. Clone Apple auxiliary storage into an
independent writable file and generate new VM machine identifiers and MACs.

The native receipt is a claim, not proof that setup or cleanup worked. Qualification
boots two independently unpacked copies of the exact packed image, observes the OS
and identities, reboots each, and checks the intended account/session. For a base,
onboarding affects only disposable acceptance clones. Prepared first-boot checks
must run without repairing the image. Missing runners or failed observations keep
the candidate unqualified. Publication uses existing signed evidence and private
macOS repositories; neither an import nor a successful restore promotes a channel.

## Implementation boundary

OCI adds only native-v2 receipt interpretation, deep parent verification and a
prepared evidence profile. It acquires no Apple SDK or image-construction code.
Imageweave must implement the native lifecycle and Packer integration, then prove
all six outputs. Guestweave consumption is a separate acceptance exercise, never
the implementation of the builder.
