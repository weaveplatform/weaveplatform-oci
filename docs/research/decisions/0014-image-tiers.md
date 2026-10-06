# 0014: Image tiers: base images and derived images

Status: Accepted

## Context

A guest image either is the vendor's operating system as shipped, or has weave software
and workload tooling installed on top. Consumers need both: the guestweave acceptance
suite and from-source users want the plain OS, while `weave exec`, the clipboard and
hostweave jobs need the weave agent and its modules already in the guest. The first
published image, `ubuntu-24.04`, said nothing about which it was, and the contract's
examples mixed the two (`macos-26-vanilla` was described as a fresh install with the
agent baked in).

## Decision

Images are published in tiers, each in its own repository
`weave-images/<family>-<major>-<tier>` ([0002](0002-registry-repositories-tags-visibility.md)):

| Tier | Contents | Built from |
|---|---|---|
| `base` | the vendor operating system, unmodified; no weave software | vendor media, verified against the vendor's signatures |
| `agent` | base + weave-agent core + the full `weave-<os>-*` module set | a base image, by digest |
| a named layer (`xcode-26`, `runner`) | agent + workload tooling | an agent image or another layer, by digest |

The contract records the tier in `guest.variant` and the parent in `build.base`
(reference and platform manifest digest), mirrored into the standard
`org.opencontainers.image.base.name` and `.base.digest` manifest annotations
([contract §4.4](../09-artifact-contract-v1.md#44-image-tiers-and-lineage)). A base names
no parent; every other tier must. A derived tier lists what it added in
`build.sourceMedia` with kind `package`.

A derived image is built by pulling its parent by digest, booting it with a provisioning
recipe, powering it off, scrubbing per-instance identity (cloud-init state, SSH host
keys, machine-id) and publishing the result. Publishing a parent triggers rebuilds of its
children; promotion refuses a child whose parent digest is not itself promoted.

The initial agent set is presence, exec, power, time, metrics, clipboard, session
and display, pinned independently by platform in `images/packages.lock.json`.
The macOS module prefix is `weave-macos-`; Linux and Windows use `weave-linux-`
and `weave-windows-`. osquery is deferred to a later layer. Linux images remain
headless; session-dependent capabilities can wait for a graphical session.

Sealing also removes the agent's `store.db` (including WAL/SHM files),
`store.key` and host-specific `channel.pub`. The platform provisioning the VM
supplies its own host trust and accounts. Installed software and the accepted
manifest sequence remain in the image; a builder must not disable trust checks
to make an image boot.

## Rationale

- **Separate repositories, not tags**, so retention, visibility and promotion differ per
  tier, and a consumer's choice of tier is explicit in the reference it pins.
- **Full disks per tier, not overlays.** Disks are fixed 512 MiB guest-LBA chunks, so a
  derived disk shares every untouched chunk with its parent by digest: the registry
  stores and the cache downloads only what changed, with no overlay format for
  consumers to understand.
- **Lineage in standard annotations** lets registries, `oras` and the promotion pipeline
  follow it without the weave config.

## Constraints

- Distribution trademark rules apply to derived tiers. Canonical's intellectual property
  policy requires approval to use the Ubuntu name for a modified image, so whether a
  public `ubuntu-24.04-agent` may carry that name is an open question
  ([Q30](../12-open-questions.md)); until it is resolved, derived Linux tiers are
  published private to the organisation.
- macOS and Windows tiers are org-private whatever the tier (licensing, [0002](0002-registry-repositories-tags-visibility.md)).

## Verification

`pkg/spec` rule 9 (`validateLineage`) and rule 10 (lineage annotations) with tests in
`pkg/spec/spec_test.go` (`TestValidateSemanticRules`, `TestDerivedImageCarriesLineage`);
`weaveoci bundle init --variant --base` in `internal/cli/build_test.go`.
