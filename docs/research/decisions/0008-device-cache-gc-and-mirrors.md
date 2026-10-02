# 0008: Device cache, garbage collection and mirrors

Status: Proposed

## Context

Every consumer caches differently. guestweave-cli-macos keeps
`cache/OCIs/<host>/<namespace>/<digest>` with tag links, layer deduplication, LRU
pruning and a disk-space guard that sees purgeable APFS space. guestweave-cli-windows
keeps `OCIs\index.json`, a blob store and one materialised `disk.vhdx` per digest that
differencing children depend on, and refuses to prune a parent in use. hostweave prunes
nothing: the moby engine's store grows, guestweave's cache is never pruned by the agent,
and QEMU base images are operator-managed. No consumer knows a mirror, and an office of
Macs each pulling a 27 GB image from GHCR is the expected failure mode.

## Decision

**One cache layout**, implemented once in the shared module's `cache` package and used
by both CLIs and the hostweave agent:

```
<cache>/
  blobs/sha256/<hex>            compressed blobs exactly as stored in the registry
  manifests/sha256/<hex>        manifests and indexes
  refs/<host>/<repo>/<tag>      -> manifest digest (tag link, rewritten on re-resolve)
  materialised/sha256/<hex>/    consumer-specific derived files (raw disk, VHD/VHDX)
  pins/<id>                     in-use pins held by running VMs, clones and attempts
  index.json                    sizes, last-access times, quota state
```

Blobs are content-addressed and shared across images; a chunk present in two images is
stored once. `materialised/` holds what a hypervisor needs that is not the blob (the
reassembled sparse raw disk, or the Windows parent VHD or VHDX) and is keyed by the
manifest digest.

**Garbage collection.** `weaveoci gc` and the equivalent library call remove entries
least recently used first, never remove a pinned entry or a blob referenced by a pinned
manifest, and stop at a configurable quota (default 100 GiB). A pull checks free space
before the first byte through a `DiskSpaceGuard` interface; the macOS implementation
counts purgeable space and triggers an LRU reclaim, the generic implementation uses
`statfs`. hostweave's agent runs GC after every attempt completes and reports cache size
and hit ratio as `attr.cache.*` fingerprints so the scheduler can prefer hosts that
already hold an image.

**Mirrors.** A registry profile is `{name, host, organisation, insecure, default,
mirrors: [host...]}`. Resolution tries mirrors in order and falls back to the canonical
host; digests are compared across sources so a mirror cannot substitute content. The
reference site mirror is `weave-zot` in its mirror role
([0011](0011-deployment-profiles-and-reference-registry.md),
[0012](0012-container-images.md)): `sync` on demand with `preserveDigest: true` and
`http.compat: ["docker2s2"]`. A Harbor proxy-cache project is a supported alternative;
both preserve referrers. hostweave's `RegistryConnection` gains
the same `mirrors` list.

**Air gap.** `weaveoci export-layout <ref> <dir>` writes an `oci-layout` directory
including referrers; `weaveoci import-layout <dir>` loads it into the cache and the
local tag links. Verification on import uses the embedded channel root and a shipped
trusted root ([0006](0006-trust-attestations-and-channel-manifest.md)).

**Prewarm.** hostweave pools may declare images to prewarm; the agent pulls them into
the cache when idle and pins them while the pool is active.

## Rationale

A content-addressed cache keyed by digest is what makes the chunked contract pay off:
the second image of the same OS build downloads only the chunks that changed, and a
Windows parent VHDX or a macOS raw disk is materialised once per digest regardless of
how many clones depend on it. Separating `blobs/` from `materialised/` lets the generic
package own eviction while each hypervisor owns its derived files. Mirrors as an ordered
list on a profile match what every consumer already has (guestweave profiles) and what
hostweave lacks (a connection scoped to one repository with no fallback). `oci-layout`
is the standard interchange every tool reads, so the air-gap path needs no weave-specific
format.

Alternatives considered:

- **Keep each consumer's cache.** Rejected; three layouts, three GC policies and no
  shared pin semantics.
- **Peer-to-peer distribution (Spegel, Dragonfly, Kraken).** Built for containerd nodes
  in a cluster; none runs on a developer's Mac or Windows laptop. Rejected; a site
  mirror covers the bandwidth case.
- **Lazy loading (eStargz, SOCI, Nydus).** Tar-level mechanisms for container rootfs;
  a hypervisor needs the whole disk before boot. Rejected.
- **Materialising only on first boot.** Saves disk for images that are pulled but never
  run; costs the first boot a full reassembly. Rejected for v1; pull materialises.

## Constraints

- The Windows parent VHD or VHDX cannot be evicted while any differencing child exists;
  the pin must be held for the lifetime of every child, which guestweave-cli-windows
  already does and the hostweave agent must do per attempt.
- The macOS purgeable-space check depends on Foundation's volume-capacity API and stays
  in guestweave-cli-macos behind the interface.
- Quota is per device, not per user; two users sharing a Mac share the cache and the
  pins.
- A zot mirror must set `preserveDigest: true`, which zot only accepts with
  `http.compat: ["docker2s2"]`; without it digests change and verification fails.
- zot defaults to 60 s HTTP read and write timeouts since v2.1.17 and garbage-collects
  unreferenced blobs older than `gcDelay`; the mirror role disables the timeouts and sets
  `gcDelay` above the longest sync of a multi-GiB image.
- zot's on-demand sync can hang on stale staging data
  ([#4357](https://github.com/project-zot/zot/issues/4357)); consumers fall through to the
  next mirror or the canonical host on timeout.
- Mirror traffic to GHCR itself still needs a classic PAT ([0002](0002-registry-repositories-tags-visibility.md)).

## Verification

- `cache` tests: LRU order, pin protection, quota stop, blob sharing across two
  manifests, `index.json` recovery after a crash mid-write.
- `client` tests: mirror fall-through on connection failure; digest mismatch between
  mirror and canonical is rejected.
- Round trip: `export-layout` then `import-layout` on a clean cache yields the same
  digests and the same referrers.
- hostweave agent: GC runs after `Destroy`; `attr.cache.*` fingerprints appear.
- Acceptance: pull through `weave-zot` in the mirror role started by testcontainers-go
  and compare digests and referrers with the upstream ([0013](0013-quality-gates.md)).
- Migration phase 8 drill against a zot `sync` instance and an `oci-layout` tarball.
- Existing evidence: `deploymenttheory/guestweave-cli-macos@main internal/vm/storage/oci.go`,
  `internal/vm/storage/diskspace.go`; `deploymenttheory/guestweave-cli-windows@main internal/oci/cache/cache.go`
  (in-use parent protection); hostweave has no cache management
  (`deploymenttheory/hostweave@main agent/health.go:157-166` is the only disk check).

## References

- [06-large-artifacts.md](../06-large-artifacts.md), [04-registries-and-github.md](../04-registries-and-github.md)
- OCI image layout: <https://github.com/opencontainers/image-spec/blob/v1.1.1/image-layout.md>
- zot mirroring: <https://zotregistry.dev/v2.1.21/articles/mirroring/>; Harbor proxy cache: <https://goharbor.io/docs/main/administration/configure-proxy-cache/>; distribution proxy: <https://distribution.github.io/distribution/recipes/mirror/>
- ORAS layout commands: <https://github.com/oras-project/oras>
