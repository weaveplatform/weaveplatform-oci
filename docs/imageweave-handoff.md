# Imageweave → weaveplatform-oci

Imageweave constructs and seals VM disks with Packer. OCI snapshots completed
outputs into its existing bundle contract, packages and verifies the exact bytes,
and handles registry transport and authenticated admission. Importing a candidate
does not execute a builder or imply runtime qualification.

## Use

Complete a pinned Imageweave Packer build first. On that build host, with its
manifest and artifacts still together:

```sh
weaveoci bundle import-imageweave "$WORK/manifest.json" \
  --recipe-commit "$IMAGEWEAVE_COMMIT" \
  --source-uri "$CANONICAL_SOURCE_URI" \
  --version "$CANDIDATE_VERSION" \
  --out "$WORK/oci-bundle"

weaveoci pack "$WORK/oci-bundle" --out "$WORK/oci-layout" \
  --tag "$CANDIDATE_VERSION"
weaveoci inspect "$WORK/oci-layout" --deep --strict
```

`IMAGEWEAVE_COMMIT` is the full 40-character commit used for the recipe.
`CANONICAL_SOURCE_URI` is the credential-free HTTPS vendor media location,
including when the build consumed a cached local file. The source SHA256 is
taken from the Packer manifest. These caller-supplied provenance fields are
claims; the importer cannot authenticate the checkout or media publisher.

For native templates, use `candidate/packer-manifest.json`; its sibling
`build-result.json` must implement Imageweave's native result schema 1, including
firmware and first-boot policy. Older development receipts lacking that policy
are rejected rather than silently assigned defaults.

Set `WORK` to an absolute workspace path on storage with sufficient free space
for build, import and validation output. The output directory must be new and
its parent must exist. Import
copies files and preserves zero ranges as sparse holes, reporting byte progress
to stderr every 30 seconds and on file completion. It needs space for a
separate bundle as well as the producer output. Source files remain untouched.
Packer's absolute paths are interpreted on the producing host. This adapter
does not relocate Windows paths onto a macOS/Linux host.

## Supported producer outputs

| Output | Imported | Excluded |
|---|---|---|
| Ubuntu/Fedora QEMU guest-base | `disk.raw`, OS/source metadata and firmware input hashes | Packer's build-specific `efivars.fd` |
| macOS Apple VZ base | `disk0.img`, `auxstorage.bin`, Apple hardware model and CPU/memory requirements | Per-instance machine identifier and build workspace |
| Windows HCS base | `disk0.img`, exact edition/build/architecture; fresh firmware/TPM policy | Working VHD/VHDX, installation media, audit scripts and instance state |

Only one build from `last_run_uuid` is accepted. Ambiguous manifests, unknown
builders, inconsistent native results, unexpected artifacts, size changes,
nonregular files and paths escaping the manifest directory are rejected.
Native firmware/first-boot policy and installed version must agree with the
declared source and guest. Windows must report successful generalization.

## Version 1 import receipt

`imageweave-import.json` records `schemaVersion: 1`, `producer: imageweave`,
`qualification: unverified`, recipe commit, Packer manifest SHA256, selected
run UUID and builder type, the OCI bundle metadata, and SHA256/size for each
imported file. OCI records its digest in the platform-manifest annotation
`io.weave.imageweave.handoff.digest`.

This is an **import receipt**, not a new Imageweave build-attestation format.
It consumes Imageweave's existing Packer manifest and native result JSON without
a Go dependency on Imageweave or its platform SDKs. Packer does not provide disk
hashes in these manifests: the importer hashes the bytes as it copies them.
It therefore freezes the imported snapshot; it cannot prove that those bytes
have not changed since Packer completed. Authenticate build provenance separately
before promotion. Executed tool versions are not inferred from a recipe commit.

The local bundle includes optional `chunkDigests` on each disk and `digest` on
state entries. Packaging compares these with the bytes actually compressed/read,
so a changed imported disk or state file fails before a platform manifest is
created. Existing bundles without these optional pins still work. These are
local packaging fields; the published guest-config schema is unchanged.

Receipts and hashes provide integrity binding, not trust. A caller controlling
all the unsigned inputs can replace them. Existing signed build and acceptance
policy remains responsible for producer identity and promotion authorization.
An import never emits `runtime_verified` or a passing acceptance report.

## Acceptance and publication

For Linux, Imageweave owns the validator that exercises the final artifact:

```sh
imageweave image validate-linux "$WORK/oci-bundle" \
  --arches arm64 --out "$WORK/oci-validation"
```

The migrated validator packages and deeply verifies the candidate before booting
independent clones. Qualification must check first boot, persistent identity
after reboot, guest OS identity and removal of build access against the exact
index. Schema-1 smoke reports remain historical evidence; new admission requires
schema 3 and authenticated attestations.

Native construction and packaging round trips do not substitute for native
runtime acceptance. Windows execution remains tracked in
[incident #38](https://github.com/weaveplatform/weaveplatform-oci/issues/38),
and cloud acceptance in [#39](https://github.com/weaveplatform/weaveplatform-oci/issues/39).
A restored macOS disk awaiting Setup Assistant still needs a guest observation
and onboarding adapter before runtime qualification can pass.

Keep candidates unqualified until the applicable acceptance profile passes on
the exact artifact digest. Publication and channel admission use the existing
signed-evidence flow. Cloud provider image registration is a separate destination
contract; this adapter imports portable raw VM artifacts only.

## Ownership after the delivery migration

| Capability | Owner |
|---|---|
| Packer base construction and native builders | Imageweave |
| Media selectors/acquisition, catalog and package locks | Imageweave |
| Windows fallback, agent/desktop scenarios and rebuild planning | Imageweave; migrated commands preserve existing behavior while scenarios adopt Packer |
| Runtime boot/agent acceptance and destination export orchestration | Imageweave |
| Import receipt, artifact/chunk/state verification, registry transport, signing and admission | weaveplatform-oci |
| Selection, placement and digest pins | Hostweave |
| Execution | Compatible destination runtimes |

The migrated Imageweave implementation depends on a pinned released/committed
OCI public module. OCI has no dependency on Imageweave's Go module or platform
construction SDKs. The HCS media SDK and duplicate construction commands have
been removed from OCI. Its remaining Windows disk conversion support is an
artifact-consumption primitive, not an installer.

The following commands move from `weaveoci image` to `imageweave image`:
`matrix`, `lock`, `check-lock`, `ipsw`, `windows`, `build-windows`,
`build-linux-agent`, `build-windows-agent`, `build-linux-desktop`, `prepare-agent`,
`boot-linux`, `validate-linux`, `validate-agent-linux`, `export` and `rebuild-plan`.
Use command help for retained options. OCI keeps `verify-acceptance` and
`verify-published`. Removed construction subcommands fail explicitly.

Image build workflows, source scripts and catalog files move with their owner.
Merge the companion Imageweave migration before this OCI command removal.
Moving tested implementations does not qualify additional OS/version/host
combinations: the runner matrix and explicit acceptance results determine that.

## Candidate delivery and trust

The Imageweave candidate workflow builds using Packer, imports into an OCI
bundle, packages and qualifies the exact artifact, and preserves its reports.
Publication is a separate explicit operation. It requires reviewed admission
policy and trust material, signs build and acceptance statements for the exact
index, and runs `weaveoci image verify-published` against registry evidence.
A missing policy or unavailable runner must fail or remain unqualified; neither
can be replaced with a generated passing report. There is no automatic channel
promotion or new signing-key hierarchy in this migration.

## Delivery migration checkpoint — 2026-10-08

Companion implementation: [Imageweave PR #5](https://github.com/weaveplatform/imageweave/pull/5).
Its construction/runtime code is separated into Linux, Windows and macOS
packages, with common inputs/helpers and narrow CLI backend interfaces.

OCI's post-removal quality gate passed at **96.6% (4735/4904)**. Imageweave's
full gate passed at **96.9% (3383/3492)**, with every package above 95% and both
binaries built for all six platforms. The enhanced Linux validator passed four
real boots of the existing Ubuntu 26.04 arm64 artifact: each clone kept
its identity across reboot and the two clones had different identities.
The report is schema 3; it is local evidence, not a signed registry publication.
See the companion's [validation checkpoint](https://github.com/weaveplatform/imageweave/blob/feat/oci-delivery-migration/docs/delivery-validation-checkpoint.md)
for exact artifact digests and remaining qualification gaps.

## Local validation checkpoint — 2026-10-07

`make vet lint test accept cover build vuln` passed on macOS arm64, including
the real Docker registry acceptance suite and six-platform cross-compilation.
Combined statement coverage was **96.1% (7045/7328)**; `pkg/handoff` was **96.2%**.
The handoff package has an explicit 95% coverage gate. Existing package thresholds
were preserved. The edited native workflow also passed `actionlint`.

The tests exercise Ubuntu, Fedora, macOS and Windows metadata and pack/unpack
round trips, changed disk/state rejection, malformed and mismatched receipts,
ambiguous Packer runs, path traversal/symlink escape, cancellation and output
preservation. Native packaging tests do not boot native VMs.

A fresh Ubuntu 26.04 arm64 build from Imageweave commit
`f71cc85c97f3a8687a6248b8d5c09116046c645b` completed through Packer 1.16.0/QEMU
1.1.7 on HVF. It reused the cached, checksum-pinned vendor build `20260927`.
The new import command consumed its Packer manifest directly; no manual bundle
metadata was needed. The live run completed at 2026-10-07T15:59:59Z.

- Packer run: `2aaa4072-a2ff-169c-351e-cd8176eebb86`.
- Import: 25,769,803,776 logical disk bytes, hashed and copied in 2m52s.
- OCI index: `sha256:3a2d8ea66113334d7a3069fb1058e53f089865ca7125988dd0f57b4407f15cec`.
- Platform manifest: `sha256:7668cb489e639aa7b9a208eb469b6e35e53d0a5bdf41949a36ee958ae5d4ae91`.
- Clone machine IDs: `aacee86253a4499aa2363ff4eed68815` and `d548fcda962844bca3483e1e492e6808`.
- Both clones reported Ubuntu 26.04 and shut down; boot durations were 23.5s and 15.9s.

Artifacts, receipts and logs were retained in the operator's local workspace;
they are not published evidence available from this repository.
The tested local OCI binary SHA256 was
`143325ed91a44ad74e30b4f303f547ab96904e0c9bbd1208dd2c2d2994ac1229`;
it was built from the working branch before committing this handoff change.
The two disposable SSH build keys were removed. The final Linux validator report
is `oci-validation/acceptance.json`. This is a local schema-1 smoke report, not
signed promotion evidence or full reboot/credential-cleanup qualification.
No image was published and no channel or Hostweave pin was changed.
