# Image catalogue and local builds

`catalogue.json` records the requested base and agent tiers for macOS 26/27
(arm64), Windows 11 26H2 Professional (amd64/arm64), and Ubuntu 20.04/26.04
(amd64/arm64). The existing Ubuntu 24.04 definition remains available. A
catalogue entry is a build target, not evidence that an image has been built
or published.

GHCR is the distribution destination: `ghcr.io/weaveplatform/weave-images`.
KING holds local source media, build disks, caches, OCI layouts and reports.
Local layouts are not automatically published or promoted to a release channel.

Target disk preparation is separate from acceptance. Windows builds use VHDX;
OCI still stores raw guest sectors. See [decision 0015](../docs/research/decisions/0015-target-image-delivery.md).

```sh
weaveoci image export /path/to/layout --expected-digest sha256:... \
  --platform windows/amd64 --target guestweave-windows --out /path/to/new-output
```

Targets are `guestweave-windows`, `guestweave-macos`, `azure`, `aws`, `gcp`,
`openstack` and `vsphere`. The command produces disk files and `export.json`,
binding hashes to the source index and platform manifest. It marks acceptance
pending. Cloud registration, provider guest preparation and native boot evidence
are additional requirements; VMDK export alone does not produce an OVA.
Windows uses virtdisk for VHDX export; other hosts use `qemu-img` for container
conversion. Raw, fixed VHD and Google tar.gz output are produced directly in Go.

Publish accepted bytes without repacking:

```sh
weaveoci publish --layout /path/to/candidate/layout --layout-ref BUILD_TAG \
  --expected-digest sha256:... --acceptance /path/to/candidate/acceptance.json \
  --repository IMAGE_REPOSITORY --tag BUILD_TAG --promotion-out promotion.json
```

Derived-image publication requires the parent platform digest in the signed
channel configured in the selected profile. Report validation does not itself
authenticate a report: trusted publication must attest the report, and promotion
must verify that attestation. The legacy bundle publication path remains for
base-image compatibility; derived images require accepted layouts.

Native Windows and cloud acceptance execution is deferred at the owner's
request: [#38](https://github.com/weaveplatform/weaveplatform-oci/issues/38) and
[#39](https://github.com/weaveplatform/weaveplatform-oci/issues/39). This does not
waive unit tests, the 95% coverage requirement, or promotion gates.

Image orchestration is implemented in Go under `internal/imagebuild` and exposed
through `weaveoci image`. It uses the existing OCI pack, unpack and conformance
packages. Python is not required. Native tools provide virtualization, restore,
package signature verification and ISO creation.

## KING workspace

KING is exFAT. `scripts/images/workspace.sh init` creates or mounts a sparse
APFS image at `/Volumes/KING/weave-images/work`, backed by
`/Volumes/KING/weave-images/workspace.sparsebundle`. This provides sparse files,
Unix permissions and APFS clones on the external disk. Its logical limit is
400 GiB; the wrapper preserves a 32 GiB reserve on KING.

Run build commands through the wrapper. It refuses an absent or incorrect
mount and routes Go caches and temporary files to KING.
After reconnecting the disk, run `init` again. Do not put files in the empty
`work` directory while it is unmounted.

```sh
scripts/images/workspace.sh init
scripts/images/workspace.sh check 80
scripts/images/workspace.sh run make image-builder \
  BIN_DIR=/Volumes/KING/weave-images/work/cache
```

## Linux base images

The cloud-image builder resolves a vendor serial, verifies Canonical's detached
signature against the pinned key, downloads the matching checksummed image,
and converts it to a raw bundle for each architecture. Set `SERIAL` to rebuild
a particular vendor input and `REVISION` to change the immutable image tag.
Choose a new output directory for each candidate.

```sh
scripts/images/workspace.sh run \
  env WEAVEOCI=/Volumes/KING/weave-images/work/cache/weaveoci \
  scripts/linux/build-cloud-image.sh images/linux/ubuntu-26.04-base \
  /Volumes/KING/weave-images/work/builds/ubuntu-26.04-base/bundles

scripts/images/workspace.sh run \
  /Volumes/KING/weave-images/work/cache/weaveoci image validate-linux \
  /Volumes/KING/weave-images/work/builds/ubuntu-26.04-base/bundles/amd64 \
  /Volumes/KING/weave-images/work/builds/ubuntu-26.04-base/bundles/arm64 \
  --out /Volumes/KING/weave-images/work/images/ubuntu-26.04-base/candidate-1
```

Validation packs both platforms, verifies every blob, unpacks the OCI bytes,
and boots two fresh clones per platform. It requires the expected OS version,
no installed agent in a base image, orderly power-off and distinct machine IDs.
`acceptance.json` records the index digest, platform results and accelerators;
each boot preserves serial and QEMU logs. Temporary test disks are removed.
Guest serial output and QEMU diagnostics stream live. Every 30 seconds a boot
status line reports the platform, accelerator, elapsed/remaining time, serial
byte count and time since the last guest output. This distinguishes a running
emulator from a guest that has stopped producing output; success is reported
only after shutdown and boot-identity validation. The CLI keeps progress on
stderr and its final JSON on stdout; the Linux workflow combines both streams
into pipeline stdout and labels each architecture and clone.
TCG results are useful local checks but do not replace native KVM/Hyper-V
consumer acceptance.

Native macOS and Windows workflows also combine progress into pipeline stdout.
macOS restore reports Apple’s actual completion percentage and the current stage.
Windows HCS installation reports VM creation, serial connection, generalization
and shutdown, with guest COM1 output also preserved in `serial.log`. The sealing
script emits its stages and failures over COM1; Windows Setup can be silent
before audit mode. Both native operations emit a heartbeat every 30 seconds with
elapsed time and the remaining context deadline, when one is set. Windows does
not estimate an installation percentage. CLI progress remains on stderr so final
stdout JSON can still be consumed by other tools.

The reusable `build-linux.yml` performs those clone checks for both platforms,
including after a verified pull when publishing. `image-linux-catalogue.yml`
runs 20.04 and 26.04 weekly and on relevant pull requests. Manual dispatch can
publish an immutable candidate and request promotion through the existing
channel workflow. The new weekly schedule does not publish automatically.

## Released packages and agent candidates

`packages.lock.json` pins core and all eight modules by version, asset SHA256
and size for every catalogue platform. Resolving a new lock requires `gh`
access to the core and module repositories:

```sh
scripts/images/workspace.sh run /Volumes/KING/weave-images/work/cache/weaveoci image lock \
  --out /Volumes/KING/weave-images/work/builds/packages-next.json
weaveoci image check-lock --lock images/packages.lock.json
weaveoci image check-lock --lock images/packages.lock.json \
  --require-packages linux/arm64
```

A structurally valid lock may record missing installers as `null`. The
`--require-packages` check and Linux agent builder fail before any downloads
when installers or signed checksum evidence are missing. The initial lock
pins core 0.9.11; its Linux module releases have no `.deb` assets. The companion
change in `weaveplatform-agent-modules/.github/workflows/module-release.yml`
adds those assets and keyless-signed checksums. Publish releases through that
workflow, resolve a new lock, and review its diff before building agents.

```sh
scripts/images/workspace.sh run \
  /Volumes/KING/weave-images/work/cache/weaveoci image build-linux-agent \
  --base /Volumes/KING/weave-images/work/images/ubuntu-26.04-base/candidate-1/layout \
  --base-name ghcr.io/weaveplatform/weave-images/ubuntu-26.04-base \
  --arch arm64 --lock images/packages.lock.json \
  --cache /Volumes/KING/weave-images/work/cache/packages \
  --out /Volumes/KING/weave-images/work/builds/ubuntu-26.04-agent/candidate-1-arm64
```

The builder verifies core and module release signatures, checks the signed
checksums against the lock, and verifies module manifests against their binary
digests. It installs from an offline seed, checks installed package versions
and module binaries, removes per-instance OS and agent identity, and records
the exact parent **platform manifest** digest plus package provenance.
The result is a candidate: host-channel and module-readiness acceptance are
still required. The base validator deliberately refuses agent-tier bundles.

## Native media selection and builds

Guestweave keeps its native image acquisition. `weaveoci` independently uses the
same sources and libraries: Apple's IPSW catalogue and
`go-bindings-macosplatform` for Virtualization.framework; Microsoft's retail
media through `go-sdk-winmediafoundry`, and `go-bindings-win32` for HCS. Image
building does not invoke or import Guestweave.

The selectors match the consumer forms where supported:

| Platform | Select a version | Pin a repeatable input |
| --- | --- | --- |
| macOS | `image ipsw --version 26 --list`, or exact `--version 26.6.2` | `--out source-lock.json` records exact version, build, Apple URL, size and SHA256 |
| Windows | `image windows --from-windows pro-26h2 --arch amd64 --language en-US` | Add `--cache <directory> --out source-lock.json` to acquire and hash the media |
| Linux | Choose the release definition, set `SERIAL=<vendor-serial>` | The bundle records the signed vendor input and digest |

A macOS major selects the newest matching production release; a dotted version
requires an exact match. `--list` lists all matching production releases, not
betas. Windows supports `pro`, `home`, `enterprise` and `education`, explicit releases
such as `26h2`, numeric base builds such as `26200`, and the moving `latest`
selector. Enterprise/Education and numeric-build requests use Media Foundry’s
ESD catalogue; Pro/Home release requests use its retail ISO resolver. Resolution
fails if the source does not match the requested release, architecture or language.
Use `image windows --from-windows enterprise-latest --list` to inspect matching
ESD versions. The SDK currently maps marketing releases through 25H2; an ESD
release absent from that map requires an exact numeric build or an SDK update.
No build number is guessed for 26H2.

Windows retail links expire. Microsoft supplies no SHA256 through the retail
SDK: initial acquisition computes it after checking the full download size.
ESDs also verify the SHA1 published by Microsoft before recording SHA256.
Only an acquired lock contains the SHA256 required for replay. Cached media is
rehashed before reuse. An expired URL with missing cached bytes requires
resolving and reviewing a new lock; the builder does not silently replace it.

### macOS

Build with `make image-builder` to sign the local executable with Apple's
virtualization entitlement. Run the signed executable through the KING wrapper:

```sh
scripts/images/workspace.sh run /Volumes/KING/weave-images/work/cache/weaveoci image ipsw \
  --version 26 --list
scripts/images/workspace.sh run /Volumes/KING/weave-images/work/cache/weaveoci image build-macos \
  --version 26 --disk-size 85899345920
```

`build-macos` restores directly through Virtualization.framework on an Apple
silicon host. It verifies the IPSW digest and embedded version/build before
restoring; Apple determines host compatibility and restore eligibility. The
bundle contains a raw disk, hardware model and auxiliary storage, with no
source VM machine identifier or MAC address. The restored base remains at
Setup Assistant. Use `--version 27` for the other catalogue target.

IPSW downloads retain partial data with their pinned URL, size and checksum.
To resume an interrupted download, use `--resume --source-lock
<candidate>/source-lock.json` with the same version selector and revision.
Completed or partially restored candidates are not overwritten. `--disk-size`
is in bytes, defaults to 80 GiB and requires at least 64 GiB.

### Windows

Run elevated on a Windows HCS/Hyper-V host matching the guest architecture:

```powershell
weaveoci image windows --from-windows pro-26h2 --arch amd64 --language en-US
weaveoci image build-windows --from-windows pro-26h2 --arch amd64 --language en-US `
  --cache E:\weave-images\media --out E:\weave-images\builds\windows-26h2-base-r1
```

The paths are examples: choose the Windows host's available image storage;
macOS's `/Volumes/KING` path cannot be used on that host. The build directory
requires NTFS/ReFS. `--source-lock`, `--revision`, and `--timeout` control replay,
candidate naming and the installation deadline.

For ESD sources, Media Foundry inspects the contained edition and assembles the
bootable ISO directly in Go. The build checks that the requested edition actually
exists in the ESD before using its public edition-selection key.
The builder prepares unattended installation media, creates a native HCS VM
with Secure Boot and a vTPM, installs offline into a sparse fixed VHD, and runs
Sysprep in audit mode. It requires a completion receipt containing the installed
release, edition and build, then waits for shutdown before extracting the raw
disk. Build-specific firmware and TPM state stay outside the bundle. Diagnostic
HCS configuration, serial log, source lock and installation receipt remain in
the candidate directory. The bundle requests fresh firmware/TPM state from its
consumer and starts at OOBE. Generic edition-selection keys do not activate
Windows; normal licensing applies.

These native builders produce candidates. Windows installation has not yet
been exercised on shocone, and neither native builder yet supplies the two-clone
consumer acceptance gate or a macOS/Windows agent tier. Native fleet acceptance
and signed channel lineage checks remain required before promotion. Catalogue
entries are not evidence of successful builds.

`image-native-candidate.yml` provides manual native build jobs. Register dedicated
self-hosted runners with `weave-images` and the matching OS/architecture labels.
The macOS job uses the mounted KING workspace. A Windows runner must run elevated
and set `WEAVE_IMAGE_WORKSPACE` to its image storage before starting the runner.
These jobs retain local candidates and do not publish or promote them. No native
runner registration is implied by adding the workflow.

For Enterprise media, inspect the catalogue before selecting a release:

```sh
weaveoci image windows --from-windows enterprise-latest --arch amd64 --language en-US --list
weaveoci image windows --from-windows enterprise-26100 --arch amd64 --language en-US \
  --cache /Volumes/KING/weave-images/work/media/windows \
  --out /Volumes/KING/weave-images/work/builds/enterprise-source.json
```

A numeric selector pins the base build; the acquired lock also pins the exact
servicing revision through the filename and media digest. Use that lock with
`build-windows --source-lock` on a matching Windows host. Available releases
come from Microsoft's catalogue; `latest` means newest matching media returned
by that catalogue.

Prepare pinned native agent installers with:

```sh
weaveoci image prepare-agent --platform windows/amd64 \
  --cache "$WORK/cache/packages" --out "$WORK/payload-windows"
weaveoci image prepare-agent --platform darwin/arm64 \
  --cache "$WORK/cache/packages" --out "$WORK/payload-macos"
```

Preparation authenticates the core and module releases before creating the
payload. Module installers, manifests and binaries must all be covered by the
release's signed checksums; an older lock without this evidence fails preflight.
macOS core packages use their own Sigstore bundle. The generated
`install-agent.ps1` or `install-agent.sh` is for execution **inside the guest**,
with the payload directory as its argument. It checks installed versions and
binary hashes, stops the agent and removes its machine-bound store and channel
trust while preserving `manifest.sequence`. The builder still has to remove its
temporary login account and generalize the OS; payload preparation alone does
not produce or validate an agent image.
