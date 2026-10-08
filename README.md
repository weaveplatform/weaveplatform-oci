# weaveplatform-oci

This repository is home to the image layer of the weave platform. Its purpose
is to give every weave project one way to package, publish, move and verify
virtual machine images for macOS, Windows and Linux guests, using standard OCI
registries as the transport. It defines the **weave guest artifact contract**,
implements it as a pure Go module, and ships the tools and pipelines built on
it: the `weaveoci` CLI, the `weave-zot` reference registry and publication workflows.

[Imageweave](https://github.com/weaveplatform/imageweave) owns image construction
through Packer. OCI imports its completed artifacts through the
[Imageweave handoff](docs/imageweave-handoff.md). Hostweave owns pinned image
selection and placement; compatible runtimes execute the resulting images.
Construction, media selection and runtime qualification live in Imageweave; OCI
retains artifact verification and authenticated admission. See the handoff guide
for migration commands and qualification limits.

The motivation for this project stems from the following factors:

- **One image format for the whole platform.** hostweave, guestweave for macOS
  and guestweave for Windows each grew their own way of storing VM images, none
  of which the others could read. A single contract means an image built once
  runs on any weave host that supports its guest.
- **VM disks are not container images.** A guest disk is tens of gigabytes of
  mostly empty block device with firmware state beside it. The contract stores
  it as fixed-size, independently compressed, digest-addressed chunks of the raw
  disk: pulls resume, empty space costs nothing on the wire or on disk, and
  unchanged chunks are never uploaded twice.
- **Identity never travels.** Machine identifiers, MAC addresses, TPM state and
  SMBIOS serials are regenerated for every VM made from an image, so two VMs
  from the same image never collide on a network or in an MDM.
- **Trust that works offline.** Images are signed at build time (GitHub
  attestations or a cosign key) and promoted through a signed channel manifest
  whose root key stays offline. A consumer checks both before it unpacks a
  byte, whether it is connected to GHCR, a site mirror or an air-gapped layout
  on a USB drive.
- **Any registry, no lock-in.** GHCR, a private registry such as the bundled
  `weave-zot`, Harbor or any OCI 1.1 registry, with mirrors and air-gap
  transfer. The format depends on no third-party VM image format or tool.

## What is in the repository

| Component | What it is |
|---|---|
| [Contract](docs/research/09-artifact-contract-v1.md) | The weave guest artifact contract v1 (`weave-guest-v1`): media types, config schema, chunk rules, state blobs, annotations, conformance checklist |
| [`pkg/`](pkg/) | The Go module consumers import: `spec`, `chunk`, `pack`, `conformance`, `client`, `cache`, `fetch`, `sign`, `verify`, `channel`, `publish`, `profile`, `source`, `disk/vhd` |
| `weaveoci` | The CLI: pack, inspect, push, pull, publish, sign, verify, channel management, air-gap export and import, verified source fetch |
| [`deploy/zot/`](deploy/zot/README.md) | `weave-zot`: zot preconfigured for multi-gigabyte VM artifacts, with private and mirror roles and a Compose deployment |
| [`images/`](images/) | Image definitions, in tiers: a `base` is the vendor OS unmodified, and derived tiers (`agent`, then workload layers) are built on it by digest. `linux/ubuntu-24.04-base` is built from Canonical's signed cloud images and published as `ghcr.io/weaveplatform/weave-images/ubuntu-24.04-base` |
| [`.github/workflows/`](.github/workflows/) | The quality gate, the reusable Linux image build, and releases of the `weaveoci` and `weave-zot` container images and binaries |

The consumers are [hostweave](https://github.com/weaveplatform/hostweave), which
runs ephemeral VM workloads from these images, and
[guestweave-cli-macos](https://github.com/weaveplatform/guestweave-cli-macos)
and [guestweave-cli-windows](https://github.com/weaveplatform/guestweave-cli-windows),
which pull, clone and push them and still build VMs from installation media
without any registry. Promotion runs through
[weaveplatform-release-channels](https://github.com/weaveplatform/weaveplatform-release-channels).

## Documentation

The design set lives in [`docs/research`](docs/research/README.md):

- [OCI primer](docs/research/01-oci-primer.md) and [prior art](docs/research/02-prior-art.md) — the standards and how others ship VM images
- [Registries and GitHub](docs/research/04-registries-and-github.md), [supply chain](docs/research/05-supply-chain.md), [large artifacts](docs/research/06-large-artifacts.md) — the constraints the design answers
- [Build pipelines](docs/research/07-build-pipelines.md) and [target architecture](docs/research/08-target-architecture.md) — how images are made and where they flow
- [Image catalogue and local builds](images/README.md) — KING workspace, package locks, base and agent candidates, and acceptance reports
- [Artifact contract v1](docs/research/09-artifact-contract-v1.md) — the format
- [Shared Go module](docs/research/10-shared-go-module.md) — the packages, the CLI and the bundle format
- [Deployment profiles](docs/research/13-deployment-profiles.md) — github, private and hybrid deployments, `weave-zot`, cosign keys
- [guestweave adoption](docs/research/14-guestweave-adoption.md) — standalone or registry-backed guestweave
- [Migration plan](docs/research/11-migration.md), [open questions](docs/research/12-open-questions.md), [decision records](docs/research/decisions/README.md) and the [glossary](docs/research/glossary.md)

## Installing

Each [release](https://github.com/weaveplatform/weaveplatform-oci/releases)
publishes `weaveoci` archives for macOS, Linux and Windows (amd64 and arm64)
with `SHA256SUMS`, and two signed container images:

```sh
docker run --rm ghcr.io/weaveplatform/weaveoci:latest version
docker run -d -p 5000:5000 ghcr.io/weaveplatform/weave-zot:latest
```

From source (Go 1.27, no cgo):

```sh
make build      # bin/weaveoci-<os>-<arch> for every release platform
```

## Using weaveoci

```sh
# pull a published image, verified per the profile, and unpack it as sparse raw disks
weaveoci pull weave-images/ubuntu-24.04-base:24.04-20260926-r1 --platform linux/arm64 --to ./ubuntu

# check an image against the contract
weaveoci export-layout weave-images/ubuntu-24.04-base:24.04-20260926-r1 ./layout
weaveoci inspect ./layout --strict --deep

# build an image from a raw disk, verified against the distribution's signed checksums
weaveoci source fetch <url> --checksums <url> --signature <url> --keyring keys.asc --fingerprint <fpr> --out disk.raw --record src.json
weaveoci bundle init ./bundle --disk disk.raw --source src.json --os linux --arch arm64 ...
weaveoci publish ./bundle --repository ubuntu-24.04-base --tag 24.04-20260926-r1 --promotion-out entry.json

# run a channel of your own and move images across an air gap
weaveoci channel promote stable.json entry.json && weaveoci channel sign signing.key stable.json
weaveoci export-layout <ref> ./usb && weaveoci import-layout ./usb
```

Which registry, signing provider and verification mode apply comes from a
deployment profile; see [deployment profiles](docs/research/13-deployment-profiles.md).
Run `weaveoci --help` for every verb.

## Quality gate

Every pull request must pass vet, blocking golangci-lint, unit tests on Linux,
macOS and Windows, the godog acceptance suite against `weave-zot` and
`registry:3` in Docker, govulncheck, a cross-compile, and merged coverage of at
least **95% total, 95% for handoff and evidence verification, and 90% for other packages**. `make gate` runs the same locally (with
`GOWORK=off`). Dependencies track their latest releases through Dependabot and
`deps-refresh.yml`, and merge automatically once the gate passes.

## License

MIT, see [LICENSE](LICENSE).
