# 0009: Guest state: carry versus regenerate

Status: Proposed

## Context

A VM bundle is more than a disk. On macOS under Virtualization.framework it carries an
auxiliary storage file (`nvram.bin`), a hardware model and a machine identifier (ECID)
inside `config.json`, and a MAC address. A Windows guest under HCS carries `guest.vmgs`,
which holds UEFI variables and the virtual TPM state; under the macOS Hypervisor.framework
VMM it carries `NVRAM.dat` and a `tpm/` directory. A Linux guest under Virtualization.framework
carries `machineidentifier.bin`. Today the legacy macOS wire format ships the ECID and MAC inside the
artifact, the macOS push hard-codes `nvram.bin` and cannot push a Windows VM, and the
Windows format omits `guest.vmgs` without saying what a consumer should do instead.
Cloning regenerates identity only when asked (`--regenerate-random-mac`, `--keep-mac`).
Two VMs sharing a TPM state or an ECID is a correctness and security defect, so the
contract must say for every blob whether it travels with the image or is created on the
device.

## Decision

Every state blob a guest depends on is classified as **carry** (shipped in the artifact
as a typed state layer or a config field, identical for every instance) or
**regenerate** (never shipped; created by the consumer when a VM is instantiated from
the image). The config's `state[]` array declares each carried blob with its media type
and `required` flag; the validator rejects any config or layer that contains a
regenerate-class value.

| Guest and hypervisor | Blob | Semantics | Reason |
|---|---|---|---|
| macOS, Virtualization.framework | hardware model (`firmware.hardwareModel`, config field) | carry, required | the installed OS is bound to the model; VZ rejects a mismatch |
| macOS, Virtualization.framework | auxiliary storage (`nvram.bin`, layer `application/vnd.weave.guest.state.auxstorage.v1`) | carry, required | holds the EFI variables of the installed OS |
| macOS, Virtualization.framework | machine identifier (ECID) | regenerate, never shipped | unique per VM; VZ generates it |
| macOS | MAC address | regenerate | collisions on clone |
| Windows, HCS | `guest.vmgs` (UEFI variables + vTPM) | regenerate from the firmware-policy layer, never shipped | contains TPM secrets; the first run creates it, and that run needs elevation (hostweave decision 0010) |
| Windows, Hypervisor.framework arm64 | `NVRAM.dat` (layer `application/vnd.weave.guest.state.uefivars.v1`) | carry, optional | boot-entry convenience only; a consumer may discard it |
| Windows, QEMU | OVMF variable store | regenerate, or carry `uefivars` if the artifact has one | |
| Windows | `tpm/` | regenerate, never shipped | TPM state |
| Windows | firmware policy (`application/vnd.weave.guest.state.firmware-policy.v1+json`: secure boot on/off, TPM required) | carry, required | tells every consumer what to create |
| Linux, Virtualization.framework | `machineidentifier.bin` | regenerate | VZ generic machine identifier |
| Linux, QEMU | OVMF variable store | regenerate | |
| all | MAC address, SSH host keys, cloud-init `instance-id`, machine-id | regenerate; the build strips host keys and `/etc/machine-id` before packing | per-instance identity |
| all | suspend and snapshot state (`state.vzvmsave`, `suspend.vmrs`, `snapshots/`) | never shipped | an image is a stopped machine |

Consumers regenerate at instantiation, not at pull: the cached materialised disk is
shared, and the per-VM directory receives the generated identity, MAC, variable store
and TPM state. Clone of a local VM keeps the local semantics the CLIs already have, but
`clone` from an OCI reference always regenerates.

## Rationale

Identity that is unique by definition cannot be in a shared image without being wrong
for every instance but one. The macOS hardware model and auxiliary storage are the
opposite: they are properties of the installed OS, not of the instance, and omitting
them produces a disk that will not boot. Making the classification explicit in the
config lets the validator refuse an artifact that leaks a TPM or an ECID, lets a Windows
consumer know that it must create `guest.vmgs` from policy rather than fail on a missing
file, and lets the macOS push support Windows guests, which it cannot today. It also
removes the clone-time flags that currently decide identity by accident.

Alternatives considered:

- **Ship everything the bundle has, as the legacy macOS format does with the ECID and MAC.** Simplest, and
  wrong: every pulled VM would share an identifier Apple intends to be unique.
  Rejected.
- **Ship `guest.vmgs` for Windows so the first elevated run happens once at build.**
  Attractive operationally, but the file carries vTPM state and BitLocker-relevant
  secrets; sharing it across instances defeats the TPM. Rejected; the elevation
  requirement is documented in the operator runbook instead.
- **Leave the decision to each consumer.** That is the current state and the source of
  the inconsistency. Rejected.

## Constraints

- The first run of a Windows guest under HCS still needs elevation to create
  `guest.vmgs`; the hostweave agent does not elevate at job time, so a Windows host must
  perform that run as part of enrolment or image prewarm.
- Whether `NVRAM.dat` for Windows on Apple silicon is worth carrying at all is a
  judgement; it is optional so that both answers are conformant.
- Stripping SSH host keys and `machine-id` is a build-time obligation that the
  conformance validator cannot check from the artifact; the pipeline must do it and
  record it in provenance.
- Apple's hardware model is opaque data; the contract carries it base64-encoded and
  does not interpret it.

## Verification

- `spec` tests: `TestConformance_ConfigRejectsIdentityFields` rejects `ecid`,
  `machineIdentifier`, `macAddress`; `TestConformance_StateSemantics` rejects a
  `regenerate` entry with a layer attached and a required `carry` entry without one.
- Consumer tests: instantiating twice from one cached image yields two different ECIDs
  (macOS), two different `guest.vmgs` files (Windows), two different machine identifiers
  (Linux) and two different MACs.
- Pipeline test: a packed Linux image contains no `/etc/ssh/ssh_host_*` and an empty
  `/etc/machine-id`.
- Existing evidence: `deploymenttheory/guestweave-cli-macos@main internal/vm/storage/registry.go:114-126`
  (push hard-codes `nvram.bin`), `internal/vm/config/platformdarwin.go:69-70` (ECID and
  hardware model in config); `deploymenttheory/guestweave-cli-windows@main internal/vm/layout/layout.go`
  (`guest.vmgs` present in the bundle, absent from the artifact).

## References

- [09-artifact-contract-v1.md](../09-artifact-contract-v1.md), [03-current-state.md](../03-current-state.md)
- hostweave decision 0010 on Windows first-run elevation: `deploymenttheory/hostweave@main docs/research/decisions/0010-vm-runtimes-guestweave-and-qemu.md`
- Apple Virtualization documentation for `VZMacHardwareModel`, `VZMacMachineIdentifier` and `VZMacAuxiliaryStorage`: <https://developer.apple.com/documentation/virtualization>
