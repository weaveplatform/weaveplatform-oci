# ubuntu-24.04

Ubuntu 24.04 LTS (noble) built from Canonical's cloud images and published as
`ghcr.io/weaveplatform/weave-images/ubuntu-24.04` (decision 0005, cloud-image
path; Ubuntu has no bootc base).

| Step | What |
|---|---|
| Resolve | The newest serial from `current/unpacked/build-info.txt`, then the dated directory `noble/<serial>/`, so a build is reproducible from its tag |
| Verify | `weaveoci source fetch`: `SHA256SUMS.gpg` must be a valid signature by the pinned key `D2EB 4462 6FDD C30B 513D 5BB7 1A5D 6C4C 7DB8 7C81` (UEC Image Automatic Signing Key), and the image must match `SHA256SUMS` |
| Convert | `qemu-img convert -O raw`; the raw disk is the cloud image's virtual size and sparse |
| Bundle | `weaveoci bundle init`: linux, `distro` ubuntu, `osBuild` the serial, UEFI, no state blobs, the verified source recorded in `build.sourceMedia` |
| Tag | `24.04-<serial>-r<revision>` |

The image is Canonical's unmodified cloud image in a different container
format, so it carries no instance identity: `/etc/machine-id` is empty and
SSH host keys are generated on first boot by cloud-init. Every consumer
provisions it with a NoCloud seed.

When the weave agent is baked in, the image becomes a modified Ubuntu image
and must be renamed to comply with Canonical's trademark policy
(<https://canonical.com/legal/intellectual-property-policy>).

The signing key was taken from `keyserver.ubuntu.com` and its fingerprint
checked against the signature packets of the published `SHA256SUMS.gpg`.
