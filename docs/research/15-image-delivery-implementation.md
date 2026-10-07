# Image delivery implementation tracker

Branch: `feat/image-delivery-validation`. Draft: [PR #40](https://github.com/weaveplatform/weaveplatform-oci/pull/40).
Design: [decision 0015](decisions/0015-target-image-delivery.md), preserving
decisions 0001, 0007, 0009, 0013 and 0014.

This tracks implementation separately from acceptance execution. Windows x64
and cloud environments are unavailable; the user requested code and test
implementation now, with those runs deferred under
[Windows incident #38](https://github.com/weaveplatform/weaveplatform-oci/issues/38)
and [cloud incident #39](https://github.com/weaveplatform/weaveplatform-oci/issues/39).
A skipped run cannot produce a passing report or authorize promotion.

## 1. Native base acceptance

Implemented: shared candidate validation deeply checks the exact packed index,
independently unpacks two clones per platform, creates fresh host challenges and
keys, bounds each run, preserves failed evidence and applies publication's report
admission rules. Schema 2 checks OS version/build/edition, platform digests,
independent identities, reboot persistence, fresh firmware and absent build
credentials. Linux's existing base workflow validates before publication.

Remaining: connect native VZ/HCS boot adapters and guest inspection to this
orchestration, including macOS Setup Assistant for 26/27. Windows needs both
matching architectures. Tests must observe each platform's actual identities,
rather than copy expected image metadata into a result. Boot adapters own VM
cleanup before returning, including on cancellation.

## 2. Linux agent images and package readiness

Implemented: Linux agent builder; authenticated installer preparation; shared SDK
checks for all eight module versions, readiness and operations; explicit refusal
of another clone's key; restart/shutdown observation; persistence of store keys,
guest identities, trust and the manifest sequence after reboot. Reports require
distinct SHA-256 store-key evidence across clones.

Remaining: publish module `.deb` installers and signed checksum evidence, refresh
the lock from those releases, connect the QEMU virtio channel and guest inspection
to the lifecycle suite, and run the resulting images. Current lock entries without
the required installer signatures fail before building a payload. Packaging tests
and signed release evidence are separate requirements.

## 3. Native agent builders

Implemented: macOS/Windows payload preparation and guest installation/sealing
recipes; Windows HCS parent-clone builder and CLI. Windows clones an unpacked
parent into VHDX, boots an offline audit-mode payload, requires generalization and
shutdown, exports raw sectors, and records exact parent lineage. Unit tests cover
the orchestration and native API boundary; the live installer run is deferred.

Remaining: macOS native clone/onboarding/provisioning/sealing lifecycle and its
builder CLI. A macOS payload alone is not a reusable image: remove temporary
accounts, login settings, SSH credentials and build-specific state before export.
Finish native guest inspection and connect both builders to first-boot acceptance.

## 4. Publication, channels and target delivery

Implemented: publish the exact accepted layout without repacking; require complete
acceptance and a parent platform digest present in a verified channel for derived
images. Linux's workflow attaches acceptance evidence to the published index.
Target disk export preserves source index/platform digests and output hashes;
raw, fixed VHD, VHDX, GCP disk archives, QCOW2 and stream-optimized VMDK remain
unvalidated containers until their destination acceptance passes.

Remaining: verify acceptance attestation identity/subject at promotion, recheck
lineage against current channel state, add native and agent publication workflows,
and trigger derived rebuilds on base/package changes. For cloud targets, implement
guest preparation, registration/import and cleanup using provider-specific
configuration, then bind two-instance acceptance to the registered image ID.
Disk conversion alone does not install guest agents or establish cloud readiness.
Direct cloud guests require an explicit reachable management transport; access to
the provider's hypervisor-side virtio/HvSocket cannot be assumed.

## 5. Guestweave consumption

Remaining: pin compatible OCI releases in both CLIs; pull promoted indexes through
their native image paths, verify signatures and digests, create two independent
VMs, and run the shared agent lifecycle checks against their channels. Construction
stays in this repository through the native libraries. The consumer tests must
exercise the final promoted artifacts, not producer-side source bundles.

## Verification policy

Keep total coverage at least 95%, with separate 95% gates for imagebuild,
imagecheck, agentcheck and virtualdisk. Run race/shuffle unit tests, strict lint,
native API tests on their operating systems, and meaningful negative tests for
stale evidence, incomplete modules, reused state, wrong trust, failed transitions,
bad signatures and export corruption. Preserve native/cloud test source and
failure diagnostics while execution is deferred; never substitute mocks for a
passing VM or provider report.
