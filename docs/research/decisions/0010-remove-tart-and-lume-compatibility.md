# 0010: Remove Tart and Lume compatibility from guestweave-macos

Status: Proposed

## Context

guestweave-cli-macos began as a Go port of Tart. Its registry client
(`internal/oci/oci_registry.go`) is a port of Tart's Swift code, its push produces the
Tart wire format (`application/vnd.cirruslabs.tart.{config.v1,disk.v2,nvram.v1}`, LZ4
slices, an OCI image config stub) so that Tart could pull weave images and weave could
pull Tart images, and it carries four pull codecs: Tart, and three Lume encodings
(sharded, chunked, legacy LZ4) for the images trycua publishes. hostweave mirrors that
knowledge in `pkg/images/vm.go`. The project owner has decided that the platform will
have no dependency on Tart. Decision [0001](0001-vm-artifact-contract.md) defines the
replacement format.

## Decision

- guestweave-cli-macos stops producing the Tart wire format and stops consuming the Tart
  and Lume formats. It pushes and pulls only `weave-guest-v1` through the shared module
  ([0004](0004-shared-go-module.md)).
- The following are deleted, not deprecated: `internal/oci/codec_tart.go`,
  `codec_lume_chunked.go`, `codec_lume_sharded.go`, `codec_lume_lz4.go`, `format.go`,
  `lume_manifest.go`, `oci_manifest.go`, `oci_layerizer_disk.go`,
  `oci_layerizer_diskv2.go`, `oci_registry.go`, `locallayercache.go`,
  `oci_authentication.go`, `oci_authenticationkeeper.go`, `oci_wwwauthenticate.go`,
  `oci_remotename.go`, `oci_digest.go`, their tests and `internal/oci/testdata/`;
  `internal/vm/storage/lume.go`, `internal/vm/storage/oci.go`,
  `internal/vm/storage/registry.go`; and the `internal/docs/registries-and-image-formats.md`
  document, which is superseded by the contract.
- hostweave removes the `tart`, `lume-chunked` and `lume-sharded` cases from
  `pkg/images/vm.go` along with the `guestweave-vhdx-v2` case; the function is replaced
  by `spec.Inspect`.
- No wire compatibility is kept in either direction: `tart pull` of a weave image and
  `weave pull` of a Tart or Lume image are unsupported.
- Images already pushed in the Tart format to the organisation's own repositories are
  converted once with `weaveoci republish <legacy-ref> <new-ref>`, which reads a legacy
  manifest with a one-off decoder that lives only in `cmd/weaveoci/internal/legacy`,
  reassembles the raw disk, drops the ECID and MAC, packs a conformant artifact and
  pushes it. The decoder is deleted after the migration phase that uses it.
- The `org.cirruslabs.tart.*` annotations, the `org.cirruslabs.tart.disk.format` label
  and the `cirruslabs` string do not appear in any weave artifact, code path or
  recommendation after this decision.

## Rationale

The project owner's requirement is sufficient on its own. The technical record supports
it:

- **Ownership and maintenance.** Cirrus Labs joined OpenAI on 2026-04-07; Tart and
  Orchard now live under `github.com/openai` and Cirrus CI closed on 2026-06-01. The
  image-templates repository has an open question about who maintains it. A wire
  format controlled by a third party whose priorities changed this year is not a
  foundation.
- **Non-conformance.** Tart uses the OCI image config media type with custom layer
  types, which the OCI artifacts guidance names as historical non-conformance, and it
  sets no `artifactType`. Interoperating with it means reproducing that shape.
- **Identity in the artifact.** Tart ships the ECID and MAC inside its config layer,
  which [0009](0009-guest-state-carry-vs-regenerate.md) forbids.
- **Codec choice.** LZ4 is the only compression Tart defines; the contract uses zstd.
- **Licensing.** The public macOS images that the Lume codecs exist to pull are
  published in apparent conflict with the macOS SLA's redistribution clause; the
  organisation builds its own macOS images from Apple restore images and has no
  legitimate use for them.
- **Cost.** The Tart port is the largest single piece of OCI code in the weave projects
  and duplicates what the shared module provides.

Alternatives considered:

- **Keep Tart and Lume as pull-only codecs.** Preserves access to cirruslabs and trycua
  images at the cost of keeping four decoders and their fixtures alive, and keeps a
  dependency the owner does not want. Rejected.
- **Keep Tart as the push format for macOS only.** Would let Tart users consume weave
  images, but there are no such users to serve and it blocks the single contract.
  Rejected.
- **Deprecate over several releases.** Nothing in the fleet depends on the formats
  beyond the images the organisation pushed itself, which `republish` converts.
  Rejected in favour of a clean removal in the migration phase that adopts the module.

## Constraints

- Any image the organisation pushed in the Tart format must be inventoried before the
  codecs are deleted; `republish` needs the legacy decoder, which exists only until that
  phase completes.
- hostweave image versions whose `Format` is `tart`, `lume-chunked`, `lume-sharded` or
  `guestweave-vhdx-v2` become `unsupported` and cannot be selected after the server
  adopts `spec.Inspect`; jobs pinned to them must be re-pinned to republished versions.
- Tart's newer stacked ASIF overlay format is never supported; the lineage mechanism in
  the contract is consumer-side (APFS clones), not a wire format.

## Verification

- `grep -rniE "tart|lume|cirruslabs|trycua"` over guestweave-cli-macos source returns no
  matches after the migration phase; the same grep over `docs/research` in this
  repository matches only the prior-art and current-state reports, this record and the
  glossary.
- hostweave `pkg/images/vm_test.go` fixtures for the four legacy formats are deleted and
  replaced by `weave-guest-v1` fixtures.
- `cmd/weaveoci` test: `republish` converts a recorded legacy manifest fixture into an
  artifact that passes the conformance suite and contains no `ecid` or `macAddress`.
- Existing evidence: `weaveplatform/guestweave-cli-macos@main internal/oci/oci_manifest.go:17-44`
  (Tart media types kept as "a wire contract"), `internal/oci/format.go:111`
  (`DetectImageFormat`), `internal/docs/registries-and-image-formats.md` (formats
  table); `weaveplatform/hostweave@main pkg/images/vm.go:36-61`.

## References

- [02-prior-art.md](../02-prior-art.md) (Tart and Lume as prior art), [03-current-state.md](../03-current-state.md), [0001](0001-vm-artifact-contract.md), [0004](0004-shared-go-module.md), [0009](0009-guest-state-carry-vs-regenerate.md)
- Tart source, now under OpenAI: <https://github.com/openai/tart> (`Sources/tart/OCI/Manifest.swift`, `Sources/tart/OCI/Layerizer/DiskV2.swift`); Cirrus Labs joining OpenAI: <https://aiidelist.com/fr/blog/openai-brings-tart-team-into-agent-infrastructure>; maintenance question: <https://github.com/cirruslabs/macos-image-templates/issues/361>
- Lume media types: <https://github.com/trycua/cua/blob/main/libs/cua/crates/cua-image/src/media_types.rs>
- OCI artifacts guidance on config media types: <https://github.com/opencontainers/image-spec/blob/v1.1.1/artifacts-guidance.md>
- Apple macOS Tahoe SLA §2J: <https://www.apple.com/legal/sla/docs/macOSTahoe.pdf>
