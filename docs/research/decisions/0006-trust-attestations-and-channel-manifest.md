# 0006: Trust: attestations at build, channel manifest at promotion

Status: Proposed

## Context

A VM image is tens of gigabytes that a host boots with full trust, so the question "who
built this, from what, and is it the version we promoted" must be answerable by every
consumer, including an air-gapped one. The platform already has a trust chain for agent
modules: an offline root key endorses an annual signing key, the signing key signs a
channel manifest, and core verifies the chain with a root public key embedded in the
binary, "no Fulcio, no Rekor, no CA". Images built in GitHub Actions can also receive
Sigstore-bundled SLSA provenance through `actions/attest`, which records the exact
workflow, inputs and runner that produced them. GHCR has no referrers API, so any
registry-attached signature lands on a fallback tag. The project owner chose both
mechanisms.

## Decision

**At build time: provenance attestation.** The publish workflow runs `actions/attest`
with `subject-name` set to the repository (no tag) and `subject-digest` set to the
index digest, with `push-to-registry: true`. The predicate is SLSA provenance v1 whose
`buildDefinition.externalParameters` record the template repository, ref and digest,
the OS build and the variant, and whose `resolvedDependencies` record the IPSW, ISO,
ESD, cloud image or bootc image with digest, the unattend or kickstart digest, the
provisioning script digests, the guest-agent package digest and the tool versions. On
GHCR the bundle is stored under the `sha256-<hex>` fallback tag; it is also stored in
GitHub's attestation API. An SBOM attestation is attached when an in-guest inventory
was collected. The workflow verifies its own attestation with `gh attestation verify
--bundle-from-oci` before dispatching promotion.

**At promotion time: channel manifest.** The workflow sends a `repository_dispatch`
(`image-published`) to `weaveplatform-manifest` with the repository, tag and index
digest. Promotion is a pull request that adds or replaces the entry in
`channels/stable.json` under a new `images` section and re-signs it; merging is the
promotion act. Pinned channels are created with `weavemanifest pin` and never mutated.
The channel entry carries repository, tag, index digest, per-platform manifest digests,
the attestation's certificate identity, and the build date.

**Verification points.**

| Who | When | Checks |
|---|---|---|
| publish workflow | after push | its own attestation resolves and verifies |
| promotion reviewer | before merge | attestation identity matches the expected workflow and ref |
| hostweave server | when an image version is resolved or pinned | digest is in the configured channel; attestation verified if reachable; evidence recorded on `ImageVersion` |
| hostweave agent and guestweave | at pull, before reassembly | manifest digest equals the channel entry; every blob digest matches its descriptor; attestation verified when `--verify=attestation` |
| air-gapped device | at import | channel manifest chain against the embedded root; bundle against a downloaded trusted root |

The channel manifest is mandatory for hostweave: a digest not present in the channel is
not dispatchable. For guestweave the default is `--verify=channel` when a channel is
configured, otherwise `none` with a warning ([0003](0003-consumer-modes.md)).

**Verification in Go.** The shared module's `verify` package discovers bundles through
the referrers API, then the fallback tag, then the GitHub attestation API; verifies with
sigstore-go against a trusted root that is refreshed through TUF online and shipped as a
file offline; pins the OIDC issuer to `https://token.actions.githubusercontent.com` and
the certificate SAN to the publishing workflow path and ref by regular expression; and
verifies channel manifests through the extracted `weaveplatform-manifest` verifier.
Verification is always against the manifest digest that will be pulled, never a tag.

## Rationale

The two mechanisms answer different questions. The attestation proves how an image was
built and binds it to a workflow identity that an outsider can check with public
tooling. The channel manifest proves that the organisation chose this build for this
channel, and it works where Sigstore cannot: on a device with no network and nothing
but the root key that core already embeds. Reusing the channel also keeps images on the
same promotion workflow as modules, which hosts already follow. Writing the attestation
as a referrer costs one workflow step and nothing on the consumer that does not want
it.

Alternatives considered:

- **Sigstore only.** Simpler, but consumers need a current trusted root, Rekor v2 entries
  require a timestamp authority, offline verification needs bundles downloaded in
  advance, and it introduces a second trust root beside the one core already carries.
  Rejected as the sole mechanism.
- **Channel manifest only.** Fully offline and already built, but no build provenance,
  no interoperability with `gh attestation verify`, Kyverno or policy-controller, and
  it cannot say which runner or inputs produced the bytes. Rejected as the sole
  mechanism.
- **cosign key-based signing with a KMS key.** Workable on any registry, but it adds a
  key to manage next to the minisign keys and gives weaker identity than keyless
  workflow certificates. Not adopted; cosign remains an optional extra signature.
- **Notation.** X.509 trust policies with no transparency log fit an enterprise CA, not
  a GitHub-only baseline. Not adopted.

## Constraints

- Attestations for private repositories need GitHub Enterprise Cloud and use GitHub's
  private Sigstore instance; the plan tier must be confirmed.
- macOS images build on self-hosted Apple-silicon runners, so verification cannot use
  `--deny-self-hosted-runners`, and the provenance level is Build L2, not L3, for those
  images.
- Whether `gh attestation verify --bundle-from-oci` resolves the fallback tag for an
  artifact with a custom config media type is untested.
- The channel-manifest schema is owned by `weaveplatform-api`; the `images` section is a
  schema change there, and the verifier must be extracted from
  `weaveplatform-agent/internal/manifestverify` before the shared module can import it.
- The predicate contents are controlled by the workflow; only the certificate identity
  and the verified timestamps are trustworthy on their own.
- Sigstore rotates its trusted root a few times a year; devices that verify offline must
  receive a refreshed `trusted_root.json` through the same channel mechanism.
- cosign versions before 3.0.4 and 2.6.2 carried a bundle-verification flaw
  (GHSA-whqx-f9j3-ch6m); the module pins sigstore-go and tracks advisories.

## Verification

- `verify` package tests: a bundle produced by this repository's own `publish.yml`
  verifies against a pinned trusted root and fails with a wrong SAN regex; a channel
  manifest signed with a test chain verifies and a tampered digest is rejected; the
  discovery order is exercised against an in-process registry with and without the
  referrers endpoint.
- Workflow test: `publish.yml` fails if its self-verification step fails.
- hostweave `internal/server/images_test.go`: resolving a digest absent from the channel
  is rejected; evidence fields populated on success.
- Live evidence, 2026-10-02: GHCR referrers endpoint returns 404, so the fallback tag
  path is the one that will run; `actions/attest` v4.2.2 documents `push-to-registry`
  and its permissions.

## References

- [05-supply-chain.md](../05-supply-chain.md), [04-registries-and-github.md](../04-registries-and-github.md)
- `actions/attest`: <https://github.com/actions/attest>; attestation docs: <https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/use-artifact-attestations>; offline: <https://docs.github.com/en/enterprise-cloud@latest/actions/how-tos/secure-your-work/use-artifact-attestations/verify-attestations-offline>; `gh attestation verify`: <https://cli.github.com/manual/gh_attestation_verify>
- Sigstore: <https://blog.sigstore.dev/cosign-3-0-available/>, <https://github.com/sigstore/sigstore-go>, <https://raw.githubusercontent.com/sigstore/sigstore-go/main/docs/verification.md>, fallback tag behaviour <https://raw.githubusercontent.com/sigstore/sigstore-js/main/packages/oci/src/image.ts>
- SLSA v1.2: <https://slsa.dev/spec/>
- Channel manifest chain: `deploymenttheory/weaveplatform-manifest@main docs/trust-chain.md`, `README.md`; schema `deploymenttheory/weaveplatform-api@main schema/channel-manifest.schema.json`; verifier `deploymenttheory/weaveplatform-agent@main internal/manifestverify`
- Module promotion precedent: `deploymenttheory/weaveplatform-agent-modules@main docs/release-pipeline.md`
