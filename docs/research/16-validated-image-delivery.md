# Validated image delivery

Branch: `feat/validated-image-delivery`, based on main after PR #40.

This phase completes the Linux agent and desktop images, macOS native builders,
authenticated promotion and Guestweave consumption. Construction stays in OCI
through native libraries. All local image artifacts and build caches stay under
`/Volumes/KING/weave-images/` using the existing APFS workspace.

## Accepted decisions

- Keep Ubuntu 20.04/26.04 on amd64/arm64; add Xfce/X11 as a named `desktop`
  tier derived from `agent`. Base images remain vendor images. Both derived
  tiers include all eight modules, with independently pinned package inputs.
- Complete macOS 26/27 arm64 onboarding, agent installation and sealing. Share
  the reusable onboarding state machine with Guestweave without invoking its CLI.
- Authenticate acceptance attestations against reviewed policy before admitting
  their contents. Promote the exact tested index; recheck current parent lineage.
- Build candidates automatically; retain reviewed, signed stable-channel PRs.
- Complete both consumers' integration and tests. Windows live VM execution
  remains deferred under [#38](https://github.com/weaveplatform/weaveplatform-oci/issues/38).
  Cloud execution remains under [#39](https://github.com/weaveplatform/weaveplatform-oci/issues/39).
  A deferred run never generates passing evidence.

## Research informing implementation

| Source | Application |
| --- | --- |
| [Tart](https://tart.run/quick-start/) | Verified pull followed by independent VM clones; retain Weave's artifact contract. |
| [HCP Packer ancestry](https://developer.hashicorp.com/hcp/docs/packer/manage/ancestry) | Exact parents, dependency rebuilds and current-channel staleness checks. |
| [ORAS copying](https://oras.land/docs/commands/oras_cp/) | Preserve the artifact and its evidence when copying indexes and referrers. |
| [OCI Distribution](https://github.com/opencontainers/distribution-spec/blob/main/spec.md) | Referrers API discovery with the specified tag fallback. |
| [bootc-image-builder](https://github.com/osbuild/image-builder/blob/main/bootc-image-builder/README.md) | Separate OS content from disk export; continue the implemented Ubuntu cloud-image source. |
| [KubeVirt storage](https://kubevirt.io/user-guide/storage/disks_and_volumes/) | Immutable reusable image content and separate persistent per-VM writes. |
| [SLSA verification](https://slsa.dev/spec/v1.2/verifying-artifacts) | Authenticate subject, builder identity and expected claims before trusting evidence. |

## Work and evidence tracker

- [ ] Signed module installer releases and refreshed package lock.
- [ ] Schema-3 acceptance profiles and explicit operation outcomes.
- [ ] Authenticated admission and current-parent checks.
- [ ] Linux QEMU agent lifecycle acceptance.
- [ ] Locked Xfce desktop construction and real GUI acceptance.
- [ ] Shared macOS onboarding and native agent builder.
- [ ] Native macOS/HCS inspection and two-clone adapters.
- [ ] Exact-layout publication, dependency rebuild planning and channel integration.
- [ ] Guestweave dependency pins and native consumer acceptance.

Keep total OCI statement coverage at least 95%, with separate 95% gates for
imagebuild, imagecheck, agentcheck and virtualdisk. New shared and changed
companion packages also require 95%. Run race/shuffle, lint and matching-OS native
tests. Record real VM acceptance separately from unit coverage.

The checklist records completed and verified work only. Package preparation,
successful restore, mocked VM responses and disk conversion do not establish
image or consumer acceptance.
