# 0003: Consumer modes: hostweave OCI-only, guestweave from-source or OCI

Status: Proposed

## Context

hostweave is a broker. It dispatches jobs to hosts it may never have seen before and it
must be able to prove which bytes a job ran on. Today a `Workload.Image` can be a free
string naming a local VM, the moby runtime skips the pull when a tag is already present,
and the QEMU runtime reads a `HOSTWEAVE_QEMU_IMAGES=name=path` map, so stale or unpinned
content can run. guestweave-cli-macos and guestweave-cli-windows are developer tools
that must work on a laptop with no registry at all: they install macOS from an IPSW,
Windows from media produced by go-sdk-winmediafoundry, and Linux from an ISO or a native
kernel plus rootfs. The project owner requires that hostweave use only OCI and that
guestweave keep every from-source mode.

## Decision

**hostweave consumes images only through OCI.**

- Every `Workload` and `Supply` that names an image carries a resolved
  `ImageVersion` whose `Reference` is `<repo>@sha256:<digest>` and whose evidence
  includes the channel-manifest entry ([0006](0006-trust-attestations-and-channel-manifest.md)).
  A free-form image string is rejected at submission.
- The server inspects an artifact with `spec.Inspect` from the shared module
  ([0004](0004-shared-go-module.md)); the four hard-coded formats in
  `pkg/images/vm.go` are removed and `ImageVersion.Format` becomes `weave-guest-v1`.
- Image indexes are accepted for VM artifacts; the server records every child platform.
- The QEMU runtime pulls through the shared cache and client, so it is an OCI consumer
  like the others, and the scheduler's VM filter consults `attr.driver.qemu.formats`
  and `attr.driver.qemu.platforms` as it does for guestweave.
- The guestweave driver is unchanged in shape: it shells out to `weave pull`, and reads
  `weave capabilities`, which must report `image_formats: ["weave-guest-v1"]` for the host
  to be eligible. A guestweave binary that reports only legacy formats is not eligible
  for VM jobs.
- The moby runtime always pulls by digest with the platform selected from the resolved
  version; "already present by tag" is no longer a reason to skip.
- hostweave never reads a local VM template on a host. The `local` image source in
  `pkg/types/image.go` is removed rather than finished.

**guestweave keeps two independent sources.**

- From source: `create --from-ipsw`, `create --from-windows`, `create --linux` with an
  ISO, native Linux on Windows, `clone` from a local VM or snapshot, `import` of an
  archive. None of these touch a registry and none require a profile, credentials or a
  channel manifest.
- From OCI: `pull`, `clone <ref>`, `push`, `images`, `fqn`, `login`, through the shared
  module. Verification is `--verify=channel|signature|both|none`, defaulting to `channel`
  when a channel manifest is configured and to `none` otherwise, with a warning.
- A VM created from source can be pushed, and the result is a conformant artifact.
  A VM pulled from OCI is an ordinary VM afterwards; `run`, `set`, `snapshot`, `delete`
  and `prune` do not know where it came from.
- `weave capabilities` reports `host_os`, `host_arch`, `guest_platforms[]`,
  `image_formats: ["weave-guest-v1"]`, `verify_modes[]` and `capabilities_version: 2`.

## Rationale

A broker that cannot prove what ran cannot bill, audit or reproduce a job, so an
unpinned path in hostweave is a defect rather than a convenience. Making every runtime
an OCI consumer removes the three unrelated fetch paths that exist today and lets the
scheduler reason about image compatibility with one attribute namespace. guestweave is
the opposite case: its value on a developer's machine is that it needs nothing but the
host OS and vendor media, and forcing a registry on it would cost adoption for no gain.
Keeping both modes behind the same `weave` verbs, with the shared module behind the OCI
verbs, satisfies both without a second code path for the artifact itself.

Alternatives considered:

- **hostweave accepts local templates registered per host.** This is the deferred
  "host-owned local template registration" in hostweave's image-lifecycle plan. Rejected:
  it reintroduces host-scoped state the server cannot verify, and a template that matters
  can be pushed to a private repository in minutes.
- **guestweave requires a channel manifest for every pull.** Stronger by default, but it
  makes a laptop pull from a colleague's private repository impossible without platform
  infrastructure. Rejected; the default warns instead.
- **hostweave talks to a registry on the agent's behalf and streams blobs.** Rejected:
  it moves tens of gigabytes through the broker for no benefit; the agent pulls directly
  with per-assignment credentials, which is the existing design.

## Constraints

- hostweave's `RegistryConnection` is scoped to exactly one repository. Mirrors require
  either a profile shape on the connection or a per-agent profile list; which one is an
  open question tracked in [12-open-questions.md](../12-open-questions.md).
- The guestweave capability contract is currently version 1 on unmerged companion
  branches (`feat/hostweave-image-contract`). This record requires version 2.
- Windows guests on Windows hosts still depend on the resident guest agent for exec and
  transfer; nothing here changes that contract.
- Removing the `local` source is a schema change in hostweave's OpenAPI and store
  migrations; it is additive-only until the first consumer release.

## Verification

- hostweave: `pkg/images` tests replace the four format fixtures with `weave-guest-v1`
  fixtures from the shared module; `internal/server/images_test.go` asserts that a
  free-form image string is rejected; `pkg/scheduler/image_test.go` gains a QEMU case.
- hostweave agent: `agent/runtime/guestweave/capabilities_test.go` asserts ineligibility
  when `image_formats` lacks `weave-guest-v1`; `agent/runtime/qemu` tests pull from an
  in-process registry into the cache and create the overlay.
- guestweave: acceptance scenario "create from IPSW with no network" passes with the
  registry client disabled; `weave capabilities` JSON schema test.
- Existing evidence: `deploymenttheory/hostweave@main agent/runtime/moby/moby.go:311-339`
  (pull skipped when image present), `agent/runtime/qemu/qemu.go:63,230-240` (path map),
  `pkg/scheduler/filter.go:36-98` (VM filter ignores QEMU).

## References

- [03-current-state.md](../03-current-state.md), [08-target-architecture.md](../08-target-architecture.md), [10-shared-go-module.md](../10-shared-go-module.md)
- hostweave decision 0010 and 0029: `deploymenttheory/hostweave@main docs/research/decisions/0010-vm-runtimes-guestweave-and-qemu.md`, `0029-image-identities-and-builds.md`
- hostweave image lifecycle status: `deploymenttheory/hostweave@main docs/implementation/image-lifecycle.md`
- `deploymenttheory/hostweave@main agent/runtime/guestweave/capabilities.go`, `pkg/types/image.go:117-226`
