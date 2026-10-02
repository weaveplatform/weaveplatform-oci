# weave-zot

The reference private registry for weave guest artifacts: upstream
[zot](https://zotregistry.dev) v2.1.21, pinned by digest, with two config
roles and a health check. Design: [decision 0011](../../docs/research/decisions/0011-deployment-profiles-and-reference-registry.md),
[decision 0012](../../docs/research/decisions/0012-container-images.md),
[deployment profiles](../../docs/research/13-deployment-profiles.md).

| Role | Config | Use |
|---|---|---|
| `private` (default) | `/etc/zot/roles/private.json` | Canonical store. htpasswd auth; `publisher` may create but not update tags (immutability); `admin` may do everything, including uploading cosign public keys |
| `mirror` | `/etc/zot/roles/mirror.json` | Site mirror of `ghcr.io/weaveplatform/weave-images/**`, synced on demand with digests preserved; anonymous read |

Settings that matter for multi-GiB artifacts: HTTP read and write timeouts are
disabled (zot's default is 60 s), garbage collection waits 24 h before
deleting unreferenced blobs so a long push cannot lose its early chunks, and
untagged manifests are removed by retention, so always push with a tag.

## Published image

Each release publishes `ghcr.io/weaveplatform/weave-zot` (and the
`ghcr.io/weaveplatform/weaveoci` CLI image) for linux/amd64 and linux/arm64,
tagged `X.Y.Z`, `X.Y` and `latest`, signed keyless and with an SPDX SBOM
attestation. Check an image before running it:

```sh
cosign verify ghcr.io/weaveplatform/weave-zot:latest \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github\.com/weaveplatform/weaveplatform-oci/\.github/workflows/release-images\.yml@refs/tags/v'
```

## Compose

`compose.yaml` runs the private role, and the mirror role under the `mirror`
profile. `WEAVE_ZOT_PORT` (default 5000) and `WEAVE_ZOT_MIRROR_PORT` (default
5001) move the host ports; on macOS, port 5000 belongs to AirPlay Receiver.

## Building locally

```sh
docker build -f deploy/zot/Dockerfile -t weave-zot .      # from the repository root (the Dockerfile copies the module, including pkg/ and internal/)
docker run --rm weave-zot verify /etc/zot/roles/private.json
docker run --rm weave-zot verify /etc/zot/roles/mirror.json
```
