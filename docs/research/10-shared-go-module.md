# Shared Go module

Research baseline 2026-10-02. This document describes the Go module that
weaveplatform-oci will provide so that hostweave, guestweave-cli-macos and
guestweave-cli-windows share one implementation of the
[artifact contract](09-artifact-contract-v1.md). It records the target design; no code
exists yet. [Decision 0004](decisions/0004-shared-go-module.md) approves the shape,
[migration](11-migration.md) sequences its adoption, and
[target architecture](08-target-architecture.md) places it among the other components.

## 1. Module identity

| Item | Value | Why |
|---|---|---|
| Module path | `github.com/deploymenttheory/weaveplatform-oci` | the repository that owns the spec owns the reference implementation |
| Go | 1.27 | hostweave's baseline (`deploymenttheory/hostweave@main`, decision 0020) |
| CGO | off (`CGO_ENABLED=0`) | the agent-modules convention; cross-compiles from any host; guestweave-cli-macos keeps its purego bindings separate |
| Registry client | `oras.land/oras-go/v2` v2.6.2 | artifact-first API, OCI 1.1 (`artifactType`, `subject`, referrers with tag fallback), file/memory/oci-layout stores, Range fetch; see the library comparison in [01-oci-primer.md](01-oci-primer.md) |
| zstd | `github.com/klauspost/compress/zstd` | pure Go (no CGO), the de-facto Go zstd used by containerd and oras tooling, writes the frame content-size field the contract requires, and decodes concurrently (<https://github.com/klauspost/compress>) |
| JSON Schema | `github.com/santhosh-tekuri/jsonschema/v6` | pure Go, draft 2020-12 support including `if/then` and `contentEncoding`, embeds the schema at compile time (<https://github.com/santhosh-tekuri/jsonschema>) |
| Digests | `github.com/opencontainers/go-digest`, `github.com/opencontainers/image-spec/specs-go/v1` | the OCI types oras-go already depends on |
| Sigstore | `github.com/sigstore/sigstore-go` v1.3.0 | bundle verification; discovery is written here (see [05-supply-chain.md](05-supply-chain.md)) |

Dependencies that are deliberately **not** used: `google/go-containerregistry` as the
transport (image-centric, single-PATCH uploads, open large-blob issues; it stays in
hostweave for container images and as an in-memory test registry), `containerd`,
`distribution/distribution` (its v3 client is internal), and any zstd binding that
needs CGO.

## 2. Package map

Packages sit at the repository root, replacing the template's `workload/` directory.

| Package | Responsibility | Depends on |
|---|---|---|
| `spec` | Media-type and annotation constants; config types; validator (JSON Schema + semantic rules); `Inspect` that turns a manifest/index into a `Description` without fetching chunks; platform helpers; canonical zero-chunk digests; embedded schema and fixtures | image-spec types, jsonschema |
| `chunk` | Chunker: raw reader → chunk descriptors (zero detection, zstd encode, compressed and uncompressed digests). Reassembler: chunk blobs → sparse file with hole punching behind an interface | klauspost zstd |
| `pack` | Bundle directory ↔ manifest/index. Builds the config, chunk layers and state layers for one manifest; builds the index from several manifests; wraps `oras.PackManifest(PackManifestVersion1_1)` | spec, chunk, oras-go |
| `client` | Registry access: profiles (host, organisation, insecure, mirrors), credential chain, resolve tag → digest, fetch with Range resume and retry, push with HEAD-skip and cross-repo mount, referrers discovery with tag fallback, tag listing | oras-go, spec |
| `cache` | Content-addressed store: `blobs/sha256/<hex>`, `refs/` index of resolved references, in-use pins, LRU garbage collection, quota, disk-space guard interface, oci-layout import and export | spec, oras-go oci store |
| `verify` | Sigstore bundle verification with a trusted root; channel-manifest verification through an extracted `weaveplatform-manifest` verifier; a policy combinator that says which evidence is required for which operation | sigstore-go, weaveplatform-manifest (prerequisite) |
| `disk/vhd` | Raw disk → VHD or VHDX for the Windows consumer, and the reverse for push | — |
| `cmd/weaveoci` | Reference CLI over the packages | all |
| `.github/workflows/` | Reusable build and publish workflows | — |

Dependency direction is strictly downward: `spec` imports nothing from the module,
`client` and `cache` never import `pack`, and `verify` never imports `client` (it is
handed bytes). That keeps hostweave's server free of chunking code and the guestweave
CLIs free of Sigstore when verification is off.

## 3. Public API sketches

Signatures only; names are the proposal, not a commitment.

### 3.1 `spec`

```go
package spec

const (
    MediaTypeConfig         = "application/vnd.weave.guest.config.v1+json"
    MediaTypeDiskChunk      = "application/vnd.weave.guest.disk.v1.raw+zstd"
    MediaTypeAuxStorage     = "application/vnd.weave.guest.state.auxstorage.v1"
    MediaTypeUEFIVars       = "application/vnd.weave.guest.state.uefivars.v1"
    MediaTypeFirmwarePolicy = "application/vnd.weave.guest.state.firmware-policy.v1+json"
    ArtifactType            = MediaTypeConfig

    AnnotationPrefix     = "com.deploymenttheory.weave.guest."
    AnnotationChunkIndex = AnnotationPrefix + "disk.chunk.index"
    // … one constant per key in contract §5, §6, §8
    ChunkSize = 512 << 20
)

// Config mirrors contract §4; field order is fixed so Marshal is deterministic.
type Config struct {
    SchemaVersion int          `json:"schemaVersion"`
    Guest         Guest        `json:"guest"`
    Firmware      Firmware     `json:"firmware"`
    Disks         []Disk       `json:"disks"`
    State         []StateEntry `json:"state"`
    Resources     Resources    `json:"resources"`
    Provisioning  Provisioning `json:"provisioning"`
    Build         Build        `json:"build"`
}

// Description is what a consumer needs to decide whether it can use an image,
// computed from manifest and config alone (no chunk fetch).
type Description struct {
    Digest       digest.Digest
    Platform     ocispec.Platform
    Config       Config
    Disks        []DiskLayout      // chunk descriptors grouped per disk, in index order
    State        []StateLayer
    TotalSize    int64             // sum of logical sizes
    FetchSize    int64             // compressed bytes a cold pull must download (zero chunks excluded)
    Annotations  map[string]string
}

func ParseConfig(raw []byte) (Config, error)           // schema + semantic rules
func Validate(cfg Config) error                         // semantic rules only
func Inspect(m ocispec.Manifest, cfg []byte) (Description, error)
func InspectIndex(idx ocispec.Index) ([]ocispec.Descriptor, error) // VM children only, validated
func SelectChild(idx ocispec.Index, p ocispec.Platform) (ocispec.Descriptor, error)
func ZeroChunkDigest(length int64) (compressed, uncompressed digest.Digest)
func Schema() []byte                                    // embedded JSON Schema
```

### 3.2 `chunk`

```go
package chunk

type Descriptor struct {
    Index              int
    Offset             int64
    Size               int64          // uncompressed
    Compressed         ocispec.Descriptor
    UncompressedDigest digest.Digest
    Zero               bool
}

type Sink interface { // where compressed chunk bytes go (a blob store or a push session)
    Put(ctx context.Context, d ocispec.Descriptor, r io.Reader) error
    Exists(ctx context.Context, d ocispec.Descriptor) (bool, error)
}

type Options struct {
    ChunkSize   int64 // spec.ChunkSize; smaller values only for tests
    ZstdLevel   int   // default 3
    Concurrency int
}

// Split reads a raw disk of logicalSize bytes and emits one Descriptor per chunk.
func Split(ctx context.Context, disk io.ReaderAt, logicalSize int64, sink Sink, o Options) ([]Descriptor, error)

type Source interface { // where compressed chunk bytes come from (cache or registry)
    Fetch(ctx context.Context, d ocispec.Descriptor, offset int64) (io.ReadCloser, error)
}

// SparseWriter abstracts hole punching (F_PUNCHHOLE, fallocate, FSCTL_SET_ZERO_DATA).
type SparseWriter interface {
    io.WriterAt
    PunchHole(off, length int64) error
    Truncate(size int64) error
}

// Assemble writes chunks into w, skipping zero chunks and verifying every chunk.
// Existing byte ranges that already match their digest are not re-fetched (resume).
func Assemble(ctx context.Context, chunks []Descriptor, src Source, w SparseWriter, concurrency int) error
```

### 3.3 `pack`

```go
package pack

type Bundle struct {
    Config spec.Config
    Disks  []DiskFile  // {Name, Path} raw disk files
    State  []StateFile // {Name, MediaType, Path, Semantics}
}

// Manifest chunks every disk into store, writes config and state blobs, and returns
// the manifest descriptor. Annotations are generated from cfg per contract §8.
func Manifest(ctx context.Context, b Bundle, store content.Storage, o chunk.Options) (ocispec.Descriptor, error)

// Index assembles child manifests (already in store) into one index with platforms
// and child annotations derived from each manifest's config.
func Index(ctx context.Context, children []ocispec.Descriptor, store content.Storage, ann map[string]string) (ocispec.Descriptor, error)

// Unpack is the inverse: given a manifest in src, materialise the bundle at dir.
func Unpack(ctx context.Context, m ocispec.Descriptor, src content.Fetcher, dir string, w func(string) chunk.SparseWriter) (Bundle, error)
```

### 3.4 `client`

```go
package client

type Profile struct {
    Name         string
    Host         string   // ghcr.io
    Organization string   // deploymenttheory/weave-images
    Insecure     bool
    Mirrors      []string // tried in order before Host for reads
    Default      bool
}

type Credentials interface { // chain: docker config → env WEAVE_REGISTRY_* → platform hook → prompt
    Get(ctx context.Context, host string) (auth.Credential, error)
}

type Client struct { /* oras-go remote.Repository per host, retry policy, profiles */ }

func New(profiles []Profile, creds Credentials, o Options) *Client
func (c *Client) Resolve(ctx context.Context, ref string) (ocispec.Descriptor, string, error)        // tag → digest, returns repo@digest
func (c *Client) FetchManifest(ctx context.Context, repo string, d ocispec.Descriptor) ([]byte, error)
func (c *Client) FetchBlob(ctx context.Context, repo string, d ocispec.Descriptor, offset int64) (io.ReadCloser, error) // Range
func (c *Client) BlobExists(ctx context.Context, repo string, d ocispec.Descriptor) (bool, error)
func (c *Client) Push(ctx context.Context, repo string, root ocispec.Descriptor, store content.ReadOnlyStorage, o PushOptions) error // HEAD-skip, mount, retries
func (c *Client) Tag(ctx context.Context, repo string, d ocispec.Descriptor, tags ...string) error
func (c *Client) Tags(ctx context.Context, repo string) ([]string, error)
func (c *Client) Referrers(ctx context.Context, repo string, subject ocispec.Descriptor, artifactType string) ([]ocispec.Descriptor, error) // API, then sha256-<hex> tag
func ParseReference(profiles []Profile, ref string, profile string) (Reference, error) // guestweave's resolution order
```

### 3.5 `cache`

```go
package cache

type Store struct { /* root dir, index, pins, quota */ }

type Guard interface { // platform disk-space guard; macOS implementation lives in guestweave-cli-macos
    Available(path string) (int64, error)
}

func Open(root string, quota int64, g Guard) (*Store, error)
func (s *Store) Fetcher() content.Fetcher                       // for pack.Unpack / chunk.Assemble
func (s *Store) Storage() content.Storage                       // for pack.Manifest
func (s *Store) Pull(ctx context.Context, c *client.Client, ref string, pol verify.Policy) (ocispec.Descriptor, error) // resolve, verify, fetch index+manifests+blobs
func (s *Store) Pin(ref string, owner string) error             // an in-use VM keeps blobs alive
func (s *Store) Unpin(owner string) error
func (s *Store) GC(ctx context.Context, keep int64) (freed int64, err error) // LRU over unpinned roots
func (s *Store) ExportLayout(ctx context.Context, root ocispec.Descriptor, dir string) error
func (s *Store) ImportLayout(ctx context.Context, dir string) ([]ocispec.Descriptor, error)
```

Cache layout: `blobs/sha256/<hex>` (shared by every image), `index.json` (an OCI
layout index of roots with `org.opencontainers.image.ref.name`), `refs/<host>/<repo>/<tag>`
→ digest, `pins/<owner>` → root digest, `access.log` for LRU. This replaces both
guestweave-cli-macos's `cache/OCIs/<host>/<ns>/<digest>` and guestweave-cli-windows's
`OCIs\index.json` + per-digest `disk.vhdx` layouts with one shape; see
[06-large-artifacts.md](06-large-artifacts.md) and
[decision 0008](decisions/0008-device-cache-gc-and-mirrors.md).

### 3.6 `verify`

```go
package verify

type Evidence struct {
    Attestation *AttestationResult // sigstore bundle verified against TrustedRoot
    Channel     *ChannelResult     // digest listed in a verified channel manifest
}

type Policy struct {
    RequireChannel     bool
    RequireAttestation bool
    Issuer             string // https://token.actions.githubusercontent.com
    SANRegexp          string // ^https://github.com/deploymenttheory/weaveplatform-oci/.github/workflows/publish.yml@refs/.*$
}

func Attestation(ctx context.Context, bundle []byte, subject digest.Digest, root *root.TrustedRoot, p Policy) (*AttestationResult, error)
func Channel(ctx context.Context, manifest, sig []byte, keys ChannelKeys, subject digest.Digest) (*ChannelResult, error)
func Evaluate(p Policy, e Evidence) error // the combinator
```

### 3.7 `disk/vhd`

```go
package vhd

// WriteFixedVHD appends the 512-byte footer that turns a raw image into a fixed VHD.
func WriteFixedVHD(raw *os.File, uuid [16]byte) error
// WriteDynamicVHDX streams raw bytes into a new dynamic VHDX, skipping zero blocks.
func WriteDynamicVHDX(ctx context.Context, raw io.ReaderAt, size int64, out *os.File) error
// ReadRaw exposes a VHD/VHDX payload as raw guest LBA space for push.
func ReadRaw(f *os.File) (io.ReaderAt, int64, error)
```

Only one of the two writers will ship in v1. A fixed VHD is raw bytes plus a footer, so
it is trivial and loses nothing, but it is not sparse on NTFS unless the file is marked
sparse; a dynamic VHDX is compact but is a real format to implement. The choice is an
[open question](12-open-questions.md) resolved by a spike on HCS behaviour with sparse
fixed VHDs.

## 4. Consumer mapping

| Consumer | Calls | Replaces or deletes |
|---|---|---|
| hostweave server (`pkg/images`) | `spec.InspectIndex`, `spec.SelectChild`, `spec.Inspect` for VM purpose; `client.Resolve`/`FetchManifest` or keep its ggcr transport for container images; `client.Referrers` + `verify.Attestation`/`verify.Channel` to record evidence | `pkg/images/vm.go` `inspectVM` (the tart/lume/VHDX-v2 classifier) → `spec.Inspect`; `pkg/images/registry.go` gains index acceptance for VM purpose; `pkg/types/image.go` `ImageVersion` gains `ArtifactType`, `OSVersion`, `TotalSize`, `Attestation`, `ChannelDigestPinned`; `RegistryConnection` gains `Mirrors`; `api/openapi.yaml` image schemas extended |
| hostweave agent, QEMU runtime (`agent/runtime/qemu`) | `cache.Open`/`Pull` with the assignment's registry credentials; `pack.Unpack` to a raw base file; then `qemu-img create -f qcow2 -b <base>` per attempt as today | `Options.Images` map and `HOSTWEAVE_QEMU_IMAGES`; `base()` path lookup; fingerprint adds `attr.driver.qemu.formats = ["weave-guest-v1"]` and `attr.driver.qemu.platforms`; `pkg/scheduler/filter.go:36-98` consults them |
| hostweave agent, guestweave runtime (`agent/runtime/guestweave`) | nothing new; still shells out to `weave pull`/`clone` | `capabilities.go` expects `image_formats` to contain `weave-guest-v1`; the per-process credential env stays |
| guestweave-cli-macos | `client` (profiles move here from `internal/registry/resolver.go`), `cache`, `pack.Manifest`/`Unpack`, `chunk` with an APFS `SparseWriter` and the Foundation-based `cache.Guard`, `verify` behind `--verify` | delete `internal/oci/{codec_tart,codec_lume_chunked,codec_lume_sharded,codec_lume_lz4,format,lume_manifest,oci_registry,oci_manifest,oci_layerizer_disk,oci_layerizer_diskv2,locallayercache,oci_authentication,oci_authenticationkeeper,oci_wwwauthenticate,oci_remotename,oci_digest}.go` and `internal/vm/storage/{oci,lume,registry}.go`; push writes raw `disk.img` (ASIF converted to raw) plus `nvram.bin` as `auxstorage`, so Windows-on-macOS VMs become pushable too; keep `internal/ipsw`, `internal/vm/vm.go` install path, `pkg/iso`, `internal/fsutil/clone.go` |
| guestweave-cli-windows | `client`, `cache`, `pack.Unpack` → `disk/vhd` → cached parent VHD(X) → differencing child as today; `pack.Manifest` reads raw through `vhd.ReadRaw` | delete `internal/oci/{oci,layer}.go`, `internal/oci/cache/cache.go` and the `vnd.guestweave.vm.*.v2` constants; `oci-source.json` keeps pinning the parent; keep `internal/winmedia`, `internal/linuxmedia`, `internal/nativebuild` |
| publication workflows | `cmd/weaveoci pack/push/verify` | the hand-written ORAS steps a per-OS workflow would otherwise need |

Nothing in the table changes guestweave's from-source paths (`--from-ipsw`,
`--from-windows`, ISO, local clone); the module is only on the OCI path, and a CLI
built without a registry profile behaves exactly as today.

## 5. `cmd/weaveoci`

| Verb | Semantics |
|---|---|
| `pack <bundle-dir> --out <oci-layout>` | Chunk disks, write config and state blobs, emit manifest and index into an OCI layout; prints the index digest |
| `push <oci-layout-or-dir> <ref> [--tag …]` | Push with HEAD-skip and mount; refuses a `-r<rev>` tag that already resolves |
| `pull <ref> [--to <dir>] [--verify channel\|attestation\|both\|none]` | Resolve, verify, fetch into the cache, optionally unpack a bundle |
| `inspect <ref\|layout> [--strict] [--deep]` | Print the `Description`; `--strict` runs the conformance checklist; `--deep` verifies chunk content |
| `verify <ref> --policy <file>` | Discover referrers (API, then fallback tag), verify bundles, check channel membership |
| `export-layout <ref> <dir>` / `import-layout <dir>` | Air-gap transfer with digests preserved |
| `republish <src-bundle-or-legacy-ref> <ref>` | One-off conversion of an existing local bundle (or a previously pulled image) into a v1 artifact |
| `gc [--keep <bytes>]` | LRU garbage collection of the local cache honouring pins |

The CLI is the reference consumer the conformance suite runs; the guestweave CLIs do
not shell out to it.

## 6. Reusable workflows

| Workflow | Runs on | Inputs | Secrets / permissions | Produces |
|---|---|---|---|---|
| `build-linux.yml` | `ubuntu-latest` (KVM) | `distro`, `version`, `arch` matrix, `source: cloud-image\|bootc`, `agent_version` | `packages: write`, `id-token: write`, `attestations: write`, `artifact-metadata: write` | raw disk → bundle → `publish.yml` |
| `build-windows.yml` | `ubuntu-latest` (QEMU/KVM) or self-hosted | `edition`, `release`, `arch`, `agent_version` | as above + Windows media source credentials if any | bundle |
| `build-macos.yml` | self-hosted Apple-silicon bare metal, `max-parallel: 1` | `version`, `variant`, `agent_version` | as above | bundle |
| `publish.yml` | `ubuntu-latest` | `bundle` artifact or layout, `repository`, `tags` | `packages: write`, attestation permissions, `RELEASE_PLEASE_PAT` for the cross-repo dispatch | `weaveoci pack`, `push`, `actions/attest push-to-registry`, `weaveoci verify` self-check, `repository_dispatch image-published` to weaveplatform-manifest |

Callers pass `uses: deploymenttheory/weaveplatform-oci/.github/workflows/publish.yml@v1`.
Details and runner constraints are in [07-build-pipelines.md](07-build-pipelines.md).

## 7. Testing strategy

- **Fixtures** under `spec/testdata`: every example in the contract (macOS, Windows,
  Linux manifests and configs; the index), plus negative cases (ecid present, gap in
  chunk indexes, state layer without config entry, non-GOARCH platform). Fixtures are
  normative: a contract change and its fixture change land in one commit.
- **Conformance suite** (`spec/conformance`): a Go test helper that any consumer can
  run against its own output (`conformance.Run(t, index, fetcher)`), mirroring how
  hostweave's `pkg/runtime/runtimetest` and `pkg/store/storetest` contract suites work.
- **Fake registry**: oras-go's `content/memory` and `content/oci` stores for unit
  tests; an in-process HTTP registry for `client` tests. go-containerregistry's
  `pkg/registry` in-memory registry, already used by hostweave's
  `pkg/images/registry_test.go`, is the fallback if oras-go's test doubles prove too
  thin for Range and chunked-upload behaviour.
- **Chunk tests**: round-trip random and sparse disks at a reduced `ChunkSize`; verify
  hole punching through a fake `SparseWriter`; verify resume by corrupting one range.
- **Large-blob integration** (`-tags registry_integration`): push and pull a
  multi-GiB sparse image against a local `zot` container and against GHCR in a nightly
  job; measure Range resume.
- **Verification tests**: recorded Sigstore bundles and a test trusted root; channel
  manifests signed with test keys from `weavemanifest keygen`.
- **Coverage floor**: 90 % per package, enforced in CI as hostweave does
  (decision 0018); `-race -shuffle on`.
- **Platform tests**: the APFS sparse writer and Foundation disk guard are tested in
  guestweave-cli-macos, the NTFS sparse writer and `disk/vhd` behaviour against HCS in
  guestweave-cli-windows; the module's own CI is Linux-only.

## 8. Non-goals

- A general-purpose container-image library; hostweave keeps go-containerregistry for
  the moby runtime.
- Content-defined chunking or any lazy-loading snapshotter; see
  [06-large-artifacts.md](06-large-artifacts.md) for why.
- Provisioning payloads (cloud-init seeds, unattend side volumes); the media type is
  reserved and nothing implements it in v1.
- Running or converting hypervisor-specific disk formats other than the raw ↔ VHD(X)
  path the Windows consumer needs; qcow2 overlays remain `qemu-img`'s job in the agent.
- Importing the agent-modules verifier wholesale: `verify` consumes a small extracted
  package from weaveplatform-manifest, which is a prerequisite tracked in
  [open questions](12-open-questions.md).

## References

- oras-go v2: <https://github.com/oras-project/oras-go> (v2.6.2); `PackManifest`: <https://github.com/oras-project/oras-go/blob/v2/pack.go>; Range fetch: <https://github.com/oras-project/oras-go/blob/v2/registry/remote/repository.go>
- klauspost/compress zstd: <https://github.com/klauspost/compress>
- santhosh-tekuri/jsonschema: <https://github.com/santhosh-tekuri/jsonschema>
- sigstore-go: <https://github.com/sigstore/sigstore-go>; verification guide: <https://github.com/sigstore/sigstore-go/blob/main/docs/verification.md>
- go-containerregistry in-memory registry used by hostweave tests: `deploymenttheory/hostweave@main`, `pkg/images/registry_test.go`
- hostweave contract-suite precedent: `deploymenttheory/hostweave@main`, `pkg/runtime/runtimetest`, `pkg/store/storetest`; coverage policy decision 0018; Go baseline decision 0020
- Files replaced in guestweave-cli-macos: `deploymenttheory/guestweave-cli-macos@main`, `internal/oci/*`, `internal/vm/storage/{oci,lume,registry}.go`, `internal/registry/resolver.go`
- Files replaced in guestweave-cli-windows: `deploymenttheory/guestweave-cli-windows@main`, `internal/oci/{oci,layer}.go`, `internal/oci/cache/cache.go`
- Files changed in hostweave: `deploymenttheory/hostweave@main`, `pkg/images/vm.go`, `pkg/images/registry.go`, `pkg/types/image.go`, `agent/runtime/qemu/qemu.go`, `agent/runtime/guestweave/capabilities.go`, `pkg/scheduler/filter.go:36-98`, `api/openapi.yaml`
- Related: [09-artifact-contract-v1.md](09-artifact-contract-v1.md), [08-target-architecture.md](08-target-architecture.md), [11-migration.md](11-migration.md), decisions [0001](decisions/0001-vm-artifact-contract.md), [0004](decisions/0004-shared-go-module.md), [0008](decisions/0008-device-cache-gc-and-mirrors.md), [0009](decisions/0009-guest-state-carry-vs-regenerate.md)
