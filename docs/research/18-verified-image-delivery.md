# Verified image delivery and Hostweave consumption

Research date: **2026-10-08**. Scope: Imageweave builds, OCI authenticates and
delivers artifacts, Hostweave pins their identities, and a compatible runtime
executes them. This report records implementation evidence and the next bounded
integration steps. **End-to-end Hostweave execution of an Imageweave OCI artifact
has not been demonstrated.** Unit tests and image boot qualification do not prove
that handoff.

## Start with the consumer

There are two different image identities. A VM workload uses an OCI repository
and immutable digest. A cloud host uses an AWS AMI, Azure image reference or GCP
image resource in the relevant location. OCI publication does not create any of
those cloud resources, and a cloud machine does not automatically provide a
compatible nested VM runtime. Hostweave already separates `host`, `vm` and
`container` purposes and records provider connection and location for host
images. Its cloud inspectors currently cover AMIs, Azure Marketplace and GCP
images/families; they do not make an OCI image bootable by a cloud provider.
Sources: [image identities](https://github.com/weaveplatform/hostweave/blob/73b0dc240c9c4e4711f8a8f306278af6f50537de/pkg/types/image.go),
[provider identity recheck](https://github.com/weaveplatform/hostweave/blob/73b0dc240c9c4e4711f8a8f306278af6f50537de/pkg/provider/images.go).

Hostweave pinning is an API-backed catalogue operation, not an automatic edit to
a Git file. `hwctl registries create`, `images create`, `images resolve` and
`images versions` accept or display JSON/YAML documents. Resolution records the
requested selector, concrete reference, format, platforms and evidence in an
immutable `ImageVersion`. A job selects `workload.image_version_id`; the server
fills its digest reference and server-owned `resolved_image` snapshot. A
conflicting explicit image is rejected. Retries retain that saved input.
Sources: [CLI](https://github.com/weaveplatform/hostweave/blob/73b0dc240c9c4e4711f8a8f306278af6f50537de/internal/cli/hwctl/images.go),
[binding](https://github.com/weaveplatform/hostweave/blob/73b0dc240c9c4e4711f8a8f306278af6f50537de/internal/server/image_bindings.go),
[snapshot tests](https://github.com/weaveplatform/hostweave/blob/73b0dc240c9c4e4711f8a8f306278af6f50537de/internal/server/images_test.go).

## Authenticate a candidate before deciding channel admission

These are separate decisions:

| Decision | Required evidence | What success means |
|---|---|---|
| Candidate authentication | Deep artifact conformance; authenticated build provenance and acceptance statement bound to the exact index digest; reviewed issuer/workflow identities; matching tag, platforms and current acceptance schema | This candidate and its reported tests came from the configured identities |
| Channel admission | Authenticated candidate plus the verified existing channel, public trust anchors and policy-selected current parent identities | The candidate satisfies this channel's admission rules |
| Runtime qualification | Execute the pinned bytes on the named compatible runtime and record results | That tested combination actually ran |

Candidate authentication must be usable without inventing a release-channel root
or asking a builder to generate a long-lived signing key. It must not return a
channel-admitted result, mutate a channel or assert parent currency. Admission
retains those additional checks, including rechecking current parents at
promotion time. At the audited OCI revision, `imagecheck.Admit` combines signed
evidence with channel and current-parent verification; `verify-candidate` is the
in-flight separation of its evidence-only portion. It is not an existing
`v0.1.3` CLI guarantee.
Sources: [admission implementation](https://github.com/weaveplatform/weaveplatform-oci/blob/c6375c7d917bc346a3b66ff7d547fc6c4f6bffc9/pkg/imagecheck/admission.go),
[admission CLI](https://github.com/weaveplatform/weaveplatform-oci/blob/c6375c7d917bc346a3b66ff7d547fc6c4f6bffc9/internal/cli/image_admission.go).

The candidate path must fail on wrong subjects, identities or predicates, missing
evidence, malformed reports and failed clone checks. A decoded JSON report alone
cannot authorize it. Publication verification must inspect the resulting remote
digest as well as the local layout; successful authentication of local evidence
does not prove registry availability or runtime consumption. OCI remains
responsible for artifact delivery and verification; Imageweave remains
responsible for construction and acceptance orchestration.

### Public and private GitHub attestations are different trust systems

GitHub documents that public repositories use the Sigstore Public Good Instance,
including a public transparency log; private repositories use GitHub's instance,
which has no transparency log. Obtaining an attestation is not verification, and
an authenticated build is not automatically an approved image.
[GitHub artifact attestations](https://docs.github.com/en/actions/concepts/security/artifact-attestations).

The audited OCI identity verifier requires a transparency-log entry and an
observer timestamp. Its admission CLI additionally requires a signed certificate
timestamp (SCT). Preserve those requirements for the public-GitHub candidate
path. The SCT concerns Fulcio certificate transparency; it is distinct from the
Rekor artifact-signature log. A trusted-root file for GitHub's private instance
does not make its log-free bundles compatible with this verifier. Private GitHub
attestation support requires a separately designed and tested trust policy; it
is **not implemented or claimed here**. No fallback should silently disable the
public checks.
Sources: [OCI verifier](https://github.com/weaveplatform/weaveplatform-oci/blob/c6375c7d917bc346a3b66ff7d547fc6c4f6bffc9/pkg/verify/attest.go),
[Sigstore certificate issuing](https://docs.sigstore.dev/certificate_authority/certificate-issuing-overview/),
[Sigstore bundle timestamps](https://docs.sigstore.dev/about/bundle/).

## Repository gaps at the audited revisions

| Component | Existing contract | Gap for this delivery path |
|---|---|---|
| Hostweave registry inspector | Resolves repository-scoped selectors; reads small configuration blobs; rejects VM indexes and recognizes Tart/Lume/VHDX-v2 | Recognize `weave-guest-v1` indexes/configuration and carry verification evidence without claiming a boot |
| Hostweave placement | Recorded VM versions use Guestweave format/platform attributes | Include the selected QEMU runtime's actual platform, format and firmware support |
| Hostweave dispatch | Selects the first healthy runtime supporting the workload kind | Match the whole workload; Guestweave and QEMU can both advertise `vm` |
| Hostweave QEMU | Local qcow2 bases, per-attempt overlay and pinned SSH host key | Verified OCI fetch/cache/unpack, raw backing, bundle firmware requirements and useful boot diagnostics |
| macOS Guestweave | Already uses the OCI library for `weave-guest-v1`, with profile verification and host-architecture selection | Align Hostweave inspection with that current format and require the intended trust policy |
| Windows Guestweave | Audited main still uses Guestweave VHDX-v2 | Implement and qualify the weave artifact consumer; do not infer it from macOS support |

Code evidence:
[Hostweave inspector](https://github.com/weaveplatform/hostweave/blob/73b0dc240c9c4e4711f8a8f306278af6f50537de/pkg/images/registry.go),
[VM formats](https://github.com/weaveplatform/hostweave/blob/73b0dc240c9c4e4711f8a8f306278af6f50537de/pkg/images/vm.go),
[scheduler](https://github.com/weaveplatform/hostweave/blob/73b0dc240c9c4e4711f8a8f306278af6f50537de/pkg/scheduler/filter.go),
[supervisor](https://github.com/weaveplatform/hostweave/blob/73b0dc240c9c4e4711f8a8f306278af6f50537de/agent/supervisor/supervisor.go),
[QEMU](https://github.com/weaveplatform/hostweave/blob/73b0dc240c9c4e4711f8a8f306278af6f50537de/agent/runtime/qemu/qemu.go),
[macOS consumer](https://github.com/weaveplatform/guestweave-cli-macos/blob/f77fdaafd218cfa55b12dc1d4e814d4e2d874e46/internal/imagesource/oci/registry.go),
[Windows format](https://github.com/weaveplatform/guestweave-cli-windows/blob/48fe4242d4d8b884e1bc464287f9a6c8f72ca275/internal/oci/oci.go).

The macOS consumer permits unverified pulls when no verification policy is
configured. Hostweave currently passes repository credentials, not a complete
artifact-verification policy. Therefore a digest-pinned dispatch alone must not
be described as verified consumption.
Sources: [profile defaults](https://github.com/weaveplatform/guestweave-cli-macos/blob/f77fdaafd218cfa55b12dc1d4e814d4e2d874e46/internal/imagesource/oci/profile.go),
[credential handoff](https://github.com/weaveplatform/hostweave/blob/73b0dc240c9c4e4711f8a8f306278af6f50537de/agent/runtime/guestweave/registry.go).

## Next implementation slices and acceptance criteria

1. **Separate candidate verification in OCI.** Reuse authenticated-statement and
   conformance checks. Keep channel admission strict. Add positive public-Sigstore
   fixtures and negative tests for subject, workflow, predicate, timestamps,
   missing SCT/log material, incomplete clone evidence and stale schema.
2. **Connect Imageweave publication.** Keep its Go library dependency pinned to
   the official `v0.1.3` release. Pin the companion CLI containing
   `verify-candidate` to an exact reviewed commit until a release contains it.
   Record both versions; do not claim the new command exists in `v0.1.3`.
   Exercise build, publication, authenticated candidate verification and remote
   digest verification without requiring a channel signing key.
3. **Add Hostweave catalogue and runtime support.** Reuse OCI public packages for
   weave artifact inspection and verified pulling rather than another decoder.
   Preserve repository-scoped credentials and immutable job snapshots. Make
   placement and dispatch agree on supported workload/platform/format. Support
   raw-backed QEMU overlays and explicit firmware requirements. Test cache
   mutation, digest mismatch, denied trust, missing platform, cancellation,
   cleanup and mixed-driver selection; a refusal must happen before execution.
4. **Prove the first complete path.** Use Ubuntu amd64 and an enrolled Linux amd64
   QEMU host. Resolve the published digest into a VM image version, submit two
   independent jobs selecting it, and exercise boot, command execution, artifact
   collection and destruction. Verify distinct identities between clones and
   stable identities through a reboot of each clone, plus absence of build
   credentials. Record index/child digests, OS version, runtime/firmware versions,
   accelerator and output. Mark the result only for this tested combination.

Require at least **95% test coverage** for the changed production packages and
the aggregate gate; do not satisfy it by excluding new code. Existing Hostweave
tests already cover tag pinning, frozen snapshots, credential isolation and
runtime lifecycle. Extend those tests and run the real VM checks separately;
mocked runtime success cannot qualify an image.
Sources: [registry tests](https://github.com/weaveplatform/hostweave/blob/73b0dc240c9c4e4711f8a8f306278af6f50537de/pkg/images/registry_test.go),
[QEMU smoke test](https://github.com/weaveplatform/hostweave/blob/73b0dc240c9c4e4711f8a8f306278af6f50537de/agent/runtime/qemu/smoke_test.go).

A local HTTPS registry, local Hostweave server/database and a compatible enrolled
host can demonstrate the runtime handoff without cloud accounts. Locally signed
test fixtures do not prove GitHub public-Sigstore publication; retain a separate
real publishing-workflow check for that stage. Cloud import/launch and Windows
or macOS qualification remain distinct tests. GitHub-hosted ARM machines do not
guarantee nested KVM: GitHub explicitly does not support nested virtualization.
Record accelerator capability and bound any TCG fallback rather than assuming
that matching CPU architecture supplies hardware acceleration.
[GitHub runner support](https://docs.github.com/en/actions/concepts/runners/github-hosted-runners#runner-images).
