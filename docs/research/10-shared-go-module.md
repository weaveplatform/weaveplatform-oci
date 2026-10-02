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
| Sigstore | `github.com/sigstore/sigstore-go` v1.3.0 | bundle verification for attestations and for cosign key signatures (`root.NewTrustedPublicKeyMaterial`, `root.NewExpiringKey`, `verify.NewVerifier(tm, verify.WithNoObserverTimestamps())`, `verify.WithKey()`); discovery is written here (see [05-supply-chain.md](05-supply-chain.md), <https://github.com/sigstore/sigstore-go/blob/v1.3.0/pkg/root/trusted_material.go>) |
| Signing | `github.com/sigstore/sigstore/pkg/signature` and its KMS providers | loads file, `env://` and KMS keys (`awskms://`, `gcpkms://`, `azurekms://`, `hashivault://`) for the private profile's cosign-compatible key signatures without shelling out to cosign (<https://github.com/sigstore/sigstore>) |
| Acceptance tests | `github.com/cucumber/godog` | Gherkin acceptance suites, the convention hostweave already uses (decision 0018); version pinned at implementation *(unverified: current release not checked)* (<https://github.com/cucumber/godog>) |
| Integration containers | `github.com/testcontainers/testcontainers-go` v0.44.0 | starts `weave-zot` and `registry:3.1.2` for acceptance and integration tests; there is no zot module, so the generic API with `wait.ForHTTP("/readyz")` is used (<https://github.com/testcontainers/testcontainers-go/tree/v0.44.0>) |

Dependencies that are deliberately **not** used: `google/go-containerregistry` as the
transport (image-centric, single-PATCH uploads, open large-blob issues; it stays in
hostweave for container images and as an in-memory test registry), `containerd`,
`distribution/distribution` (its v3 client is internal), and any zstd binding that
needs CGO.

## 2. Package map

Public packages (importable by hostweave and the guestweave CLIs) live under `pkg/`; packages used only by this project live under `internal/`. The CLI entry point is `cmd/weaveoci`.

| Package | Responsibility | Depends on |
|---|---|---|
| `spec` | Media-type and annotation constants; config types; validator (JSON Schema + semantic rules); `Inspect` that turns a manifest/index into a `Description` without fetching chunks; platform helpers; canonical zero-chunk digests; embedded schema and fixtures | image-spec types, jsonschema |
| `chunk` | Chunker: raw reader → chunk descriptors (zero detection, zstd encode, compressed and uncompressed digests). Reassembler: chunk blobs → sparse file with hole punching behind an interface | klauspost zstd |
| `pack` | Bundle directory ↔ manifest/index. Builds the config, chunk layers and state layers for one manifest; builds the index from several manifests; wraps `oras.PackManifest(PackManifestVersion1_1)` | spec, chunk, oras-go |
| `client` | Registry access: profiles (host, organisation, insecure, mirrors), credential chain, resolve tag → digest, fetch with Range resume and retry, push with HEAD-skip and cross-repo mount, referrers discovery with tag fallback, tag listing | oras-go, spec |
| `cache` | Content-addressed store: `blobs/sha256/<hex>`, `refs/` index of resolved references, in-use pins, LRU garbage collection, quota, disk-space guard interface, oci-layout import and export | spec, oras-go oci store |
| `verify` | Sigstore bundle verification with a trusted root; channel-manifest verification through an extracted `weaveplatform-manifest` verifier; a policy combinator that says which evidence is required for which operation | sigstore-go, weaveplatform-manifest (prerequisite) |
| `disk/vhd` | Raw disk → VHD or VHDX for the Windows consumer, and the reverse for push | — |
| `profile` | Loads and validates the deployment profile file: profile kind (`github`, `private`, `hybrid`), registries and mirrors, signing provider (`github-attestation`, `cosign-key`, `none`), verification policy and channel trust anchors ([13-deployment-profiles.md](13-deployment-profiles.md)) | spec, client (types only), verify (types only) |
| `sign` | Signing providers behind one interface. `cosign-key` builds a Sigstore bundle with a file, `env://` or KMS key and no transparency log, and pushes it as an OCI 1.1 referrer (artifactType `application/vnd.dev.sigstore.bundle.v0.3+json`) that cosign v3 verifies with `--key --insecure-ignore-tlog`. `github-attestation` only checks that the workflow produced one, because `actions/attest` does the signing | sigstore, client |
| `publish` | The publish pipeline as a library: refuse an existing build tag, pack, validate, push all blobs then manifests then index, sign through the profile's provider, verify the signature, re-pull and compare digests, emit the promotion request (`repository_dispatch` payload or a channel-entry file) | pack, client, sign, verify, profile |
| `channel` | Channel manifests (phase 2): byte-compatible key, signature and manifest files with the weavemanifest and agent-core formats; chain verification (root endorses signing key, signing key signs the exact manifest bytes); expiry and anti-rollback; the `images` section; `Promote`, `New`, `Sign`, `Endorse`, `GenerateKey`; loading the four files from a path or URL ([05-supply-chain.md](05-supply-chain.md)) | go-digest |
| `cmd/weaveoci` | Reference CLI over the packages | all |
| `.github/workflows/` | Reusable build and publish workflows | — |

Dependency direction is strictly downward: `spec` imports nothing from the module,
`publish` is the only package that imports `sign` and `pack` together,
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
    Key         *KeyResult         // cosign key signature verified against a configured public key
}

type Policy struct {
    RequireChannel     bool
    RequireAttestation bool
    Issuer             string // https://token.actions.githubusercontent.com
    SANRegexp          string // ^https://github.com/deploymenttheory/weaveplatform-oci/.github/workflows/publish.yml@refs/.*$
    RequireKey         bool              // private profile: a cosign key signature must verify
    PublicKeys         []crypto.PublicKey // trusted signing keys; key hints select among them during rotation
}

func Attestation(ctx context.Context, bundle []byte, subject digest.Digest, root *root.TrustedRoot, p Policy) (*AttestationResult, error)
func Channel(ctx context.Context, manifest, sig []byte, keys ChannelKeys, subject digest.Digest) (*ChannelResult, error)
// Key verifies a cosign key-signed bundle with no transparency log and no observer timestamps.
func Key(ctx context.Context, bundle []byte, subject digest.Digest, keys []crypto.PublicKey) (*KeyResult, error)
func Evaluate(p Policy, e Evidence) error // the combinator
```

### 3.7 `profile`

```go
package profile

type Kind string // "github" | "private" | "hybrid"

type SigningProvider string // "github-attestation" | "cosign-key" | "none"

type Profile struct {
    Name      string
    Kind      Kind
    Registry  client.Profile      // canonical host and organisation
    Mirrors   []string            // read-only, tried first (hybrid)
    Signing   Signing             // provider plus key reference for cosign-key
    Verify    verify.Policy       // what consumers require on pull
    Channel   ChannelAnchors      // root public keys and channel URL or file
}

type Signing struct {
    Provider SigningProvider
    KeyRef   string // file path, env://NAME, awskms://…, gcpkms://…, azurekms://…, hashivault://…
    PubRef   string // public key used by verifiers in the private profile
}

func Load(path string) ([]Profile, error) // YAML; unknown keys are errors, as in hostweave's config loader
func (p Profile) Validate() error          // e.g. private requires cosign-key, github forbids cosign-key without opt-in
```

### 3.8 `sign` and `publish`

```go
package sign

type Signer interface {
    // Sign creates and pushes a signature referrer for subject; returns the referrer descriptor.
    Sign(ctx context.Context, repo string, subject ocispec.Descriptor) (ocispec.Descriptor, error)
}

func NewCosignKey(ctx context.Context, keyRef string, c *client.Client) (Signer, error)
func NewGitHubAttestation(c *client.Client, workflowIdentity string) Signer // verifies presence only

package publish

type Request struct {
    Profile  profile.Profile
    Bundle   string   // bundle directory or oci-layout
    Repo     string   // weave-images/ubuntu-24.04
    Tags     []string // build tag first; channel tags are refused
}

type Result struct {
    Index     ocispec.Descriptor
    Platforms []ocispec.Descriptor
    Signature ocispec.Descriptor
    Promotion PromotionRequest // dispatch payload or channel-entry JSON
}

func Run(ctx context.Context, r Request) (Result, error)
```

### 3.9 `disk/vhd`

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
| `pull <ref> [--to <dir>] [--verify channel\|signature\|both\|none]` | Resolve, verify, fetch into the cache, optionally unpack a bundle |
| `inspect <ref\|layout> [--strict] [--deep]` | Print the `Description`; `--strict` runs the conformance checklist; `--deep` verifies chunk content |
| `verify <ref> --policy <file>` | Discover referrers (API, then fallback tag), verify bundles, check channel membership |
| `export-layout <ref> <dir>` / `import-layout <dir>` | Air-gap transfer with digests preserved |
| `republish <src-bundle-or-legacy-ref> <ref>` | One-off conversion of an existing local bundle (or a previously pulled image) into a v1 artifact |
| `gc [--keep <bytes>]` | LRU garbage collection of the local cache honouring pins |
| `publish <bundle> <repo> --tag <build-tag> [--profile <name>]` | Runs the whole publish pipeline from the `publish` package on any CI system; the GitHub workflows are thin wrappers around it |
| `sign <ref@digest> [--key <ref>]` | Signs an existing image with the profile's provider; cosign-compatible bundle as a referrer |
| `profile show\|validate [--file <path>]` | Prints the resolved profile or validates a profile file |
| `healthcheck [--url http://127.0.0.1:5000/readyz]` | Exits 0 when the URL returns 200; copied into the distroless `weave-zot` image as its `HEALTHCHECK`, because that image has no shell, curl or wget |

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
`publish.yml` installs or runs the `ghcr.io/deploymenttheory/weaveoci` image and calls
`weaveoci publish`; it adds only the GitHub-specific steps (`actions/attest`, the cross-repo
dispatch). A further workflow, `release-images.yml`, builds and publishes the `weaveoci` and
`weave-zot` container images ([0012](decisions/0012-container-images.md)).

## 6.1 Implementation notes (phase 2, 2026-10-02)

- **Signing builds bundles from protobuf types.** `pkg/sign` assembles the Sigstore
  v0.3 bundle itself (DSSE pre-authentication encoding, ECDSA P-256 over SHA-256, a
  public-key hint of `base64(sha256(PKIX DER))`) instead of calling sigstore-go's
  `pkg/sign`, which pulls rekor's PGP support and the deprecated
  `golang.org/x/crypto/openpgp` (GO-2026-5932, no fix). Verification still uses
  sigstore-go's verifier, and `cosign verify --key … --insecure-ignore-tlog`
  accepts the result (acceptance feature `phase2_push_pull_sign`).
- **KMS providers live in the binary.** `cmd/weaveoci` registers the AWS, Azure, GCP
  and Vault providers; library consumers do not inherit the cloud SDKs. gRPC is
  pinned at v1.83.1 for GO-2026-6348, which the GCP provider made reachable.
- **`verify` stays transport-free.** It takes a `Source` (referrers plus fetch);
  `client.Bind(ref)` provides one for a registry and `verify.StoreSource` one for the
  cache or an imported layout, where referrers are graph predecessors.
- **oras-go v2.6.2 GC bug.** `oci.Store.GC` loops forever when a referrer's subject
  is no longer tagged (its loop variable is shadowed). `pkg/cache` deletes an
  evicted image's referrers before collecting, so the bug is never reached; it should
  be reported upstream ([12-open-questions.md](12-open-questions.md) Q27).
- **Pull resumes at blob granularity.** `client.Pull` skips blobs the destination
  holds, so an interrupted pull refetches at most the chunks in flight; ranged
  resume inside a 512 MiB chunk is not implemented.

## 7. Repository layout

| Path | Contents |
|---|---|
| `pkg/spec`, `pkg/chunk`, `pkg/pack`, `pkg/conformance`, `pkg/client`, `pkg/cache`, `pkg/verify`, `pkg/sign`, `pkg/publish`, `pkg/profile`, `pkg/disk/vhd` | Public library packages (`spec`, `chunk`, `pack` and `conformance` exist since phase 1) |
| `internal/cli`, `internal/buildinfo`, `internal/testbundle` | Project-only packages: CLI commands, version metadata, the test bundle generator |
| `cmd/weaveoci/` | Thin `main`; commands live in `internal/cli` so they are unit-tested |
| `internal/testbundle/` | Generator for small sparse contract bundles used by unit and acceptance tests |
| `deploy/zot/` | `Dockerfile` (`FROM ghcr.io/project-zot/zot:v2.1.21@sha256:…`, copies config roles and the `weaveoci` healthcheck binary), `config/private.json`, `config/mirror.json`, `compose.yaml` with volumes, TLS and healthcheck ([13-deployment-profiles.md](13-deployment-profiles.md)) |
| `test/acceptance/` | godog features per migration phase (`features/phase1_pack_inspect.feature` and so on), step definitions driving the built `weaveoci` binary, and testcontainers fixtures for `weave-zot` and `registry:3.1.2` |
| `Makefile`, `.testcoverage.yml` | Local parity with CI: `make test`, `accept`, `cover` (≥95% total, ≥90% per package), `lint`, `vuln`, `build`, `image-zot`, `fixtures`, `gate` |
| `scripts/` | Helper scripts in any language when Make is not enough (none needed yet) |
| `images/<repository>/` | Image definitions consumed by the build workflows ([0007](decisions/0007-publication-pipeline.md)) |
| `.github/workflows/` | Quality gate, reusable build and publish workflows, container image release |
Details and runner constraints are in [07-build-pipelines.md](07-build-pipelines.md).

### 7.1 Bundle directory format

`weaveoci pack` reads, and `weaveoci unpack` writes, a bundle directory: raw disk
files, state files and a `bundle.json` naming them. Paths are relative to the
bundle and may not escape it; unknown keys are errors. The producer supplies the
config fields it chooses; `pack` computes disk sizes, chunk counts, zero counts and
state media types, and derives the contract annotations. Packing an unpacked bundle
reproduces the original manifest digest.

```json
{
  "schemaVersion": 1,
  "guest": {"os": "darwin", "arch": "arm64", "osVersion": "26.0", "osBuild": "25A354", "variant": "vanilla"},
  "firmware": {"type": "apple", "secureBoot": false, "tpm": "none", "hardwareModel": "<base64>"},
  "resources": {"cpu": {"min": 2, "default": 4}, "memory": {"min": 4294967296, "default": 8589934592}},
  "provisioning": {"credentialHint": "set-at-first-boot"},
  "build": {"template": "macos-26-vanilla", "templateRef": "<repo>@<commit>", "sourceMedia": [], "created": "2026-10-02T08:00:00Z"},
  "disks": [{"name": "disk0", "role": "system", "path": "disk.img"}],
  "state": [{"name": "auxstorage", "path": "nvram.bin", "semantics": "carry", "required": true}],
  "annotations": {"org.opencontainers.image.version": "26.0-25A354-r1", "org.opencontainers.image.revision": "<commit>", "org.opencontainers.image.source": "<url>"}
}
```

### 7.2 HTTP and CLI conventions

- Any REST API or HTTP test double in this repository uses [chi](https://github.com/go-chi/chi),
  as hostweave does. The first such surface is the fault-injecting test registry
  planned for phase 2's `client` tests.
- The CLI uses cobra without viper. Configuration (the phase 2 profile file) is
  strict YAML where unknown keys are errors, with flag over environment over file
  precedence, following hostweave's `internal/config`.

## 8. Testing strategy

- **Fixtures** under `pkg/spec/testdata`: every example in the contract (macOS, Windows,
  Linux manifests and configs; the index), plus negative cases (ecid present, gap in
  chunk indexes, state layer without config entry, non-GOARCH platform). Fixtures are
  normative: a contract change and its fixture change land in one commit.
- **Conformance suite** (`pkg/conformance`): a Go test helper that any consumer can
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
  multi-GiB sparse image against `weave-zot` and `registry:3.1.2` containers, and against
  GHCR in a nightly opt-in job; measure Range resume. zot does not check that layers exist
  when the config media type is not the OCI image config, so `client` must push every
  chunk and state blob before the manifest that references them; a test asserts that
  order. The zot read and write timeouts (60 s by default) and `gcDelay` must exceed the
  longest push, which the `weave-zot` config roles set.
- **Acceptance tests** (`test/acceptance`, godog): every migration phase ships at least
  one feature file that drives the real `weaveoci` binary against real registries started
  with testcontainers-go v0.44.0: `weave-zot` (referrers API path) through the generic
  container API with `wait.ForHTTP("/readyz")`, and `registry:3.1.2` (fallback-tag path)
  through the `registry` module. A phase is not done until its features pass in CI
  ([0013](decisions/0013-quality-gates.md)).
- **Verification tests**: recorded Sigstore bundles and a test trusted root; channel
  manifests signed with test keys from `weavemanifest keygen`.
- **Coverage gate**: merged coverage of unit, integration and platform runs must be at
  least 95 % in total and at least 90 % per package, enforced by a script in `scripts/`
  in the quality-gate workflow, as hostweave does (decision 0018); tests run with
  `-race -shuffle on`; `golangci-lint` and `govulncheck` run in the same gate
  ([0013](decisions/0013-quality-gates.md)).
- **Platform tests**: the module's sparse writers have build-tagged implementations for
  darwin, linux and windows. CI runs the unit suite on `ubuntu-latest`, `macos-latest`
  and `windows-latest`, and merges the three coverage profiles before the gate, so
  platform-specific code counts towards 95 %. The Foundation purgeable-space disk guard
  stays in guestweave-cli-macos and HCS attach behaviour in guestweave-cli-windows.

## 9. Non-goals

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
