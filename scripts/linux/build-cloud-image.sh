#!/usr/bin/env bash
# Builds weave bundles from a distribution cloud image definition
# (images/linux/<name>/image.env): resolve the newest serial, fetch and
# verify the image against the distribution's signed checksums, convert it
# to a sparse raw disk, and write one bundle per architecture.
#
#   build-cloud-image.sh <image-dir> <out-dir>
#
# Environment:
#   WEAVEOCI     weaveoci binary (default: weaveoci on PATH)
#   SERIAL       pin a serial instead of the newest (rebuilding a tag)
#   REVISION     build revision, the r<N> of the tag (default 1)
#   ARCHES       override the definition's architectures
#   REVISION_SHA commit of the definition (default: git rev-parse HEAD)
#   SOURCE_URL   repository URL (default: this repository on GitHub)
#
# Prints TAG=<tag> and SERIAL=<serial> last, and appends them to
# $GITHUB_OUTPUT when it is set.
set -euo pipefail

image_dir=${1:?image directory}
out=${2:?output directory}
weaveoci=${WEAVEOCI:-weaveoci}
def="$image_dir/image.env"

val() { sed -n "s/^$1=//p" "$def" | tail -n1; }
for k in REPOSITORY DISTRO OS_VERSION BASE_URL MEDIUM CHECKSUMS SIGNATURE KEYRING FINGERPRINTS ARCHES CPU_MIN CPU MEMORY_MIN MEMORY CREDENTIAL_HINT; do
  [ -n "$(val "$k")" ] || { echo "$def: $k is missing" >&2; exit 2; }
done

base=$(val BASE_URL)
serial=${SERIAL:-}
if [ -z "$serial" ]; then
  serial=$(curl -fsSL "$base/current/unpacked/build-info.txt" | sed -n 's/^serial=//p')
fi
[[ "$serial" =~ ^[0-9]{8}(\.[0-9]+)?$ ]] || { echo "unexpected serial '$serial'" >&2; exit 1; }
revision=${REVISION:-1}
tag="$(val OS_VERSION)-${serial}-r${revision}"
sha=${REVISION_SHA:-$(git -C "$image_dir" rev-parse HEAD)}
source_url=${SOURCE_URL:-https://github.com/weaveplatform/weaveplatform-oci}
repo_slug=${source_url#https://github.com/}

fingerprints=()
for f in $(val FINGERPRINTS); do fingerprints+=(--fingerprint "$f"); done

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$out"

for arch in ${ARCHES:-$(val ARCHES)}; do
  medium=$(val MEDIUM); medium=${medium//\{arch\}/$arch}
  echo "== $arch: $base/$serial/$medium"
  "$weaveoci" source fetch "$base/$serial/$medium" \
    --checksums "$base/$serial/$(val CHECKSUMS)" \
    --signature "$base/$serial/$(val SIGNATURE)" \
    --keyring "$image_dir/$(val KEYRING)" "${fingerprints[@]}" \
    --kind cloud-image --out "$work/$arch.img" --record "$work/$arch.source.json"
  # Cloud images are qcow2; the contract carries raw guest LBAs. qemu-img
  # writes holes for zero runs, so the raw file stays sparse.
  qemu-img convert -p -O raw "$work/$arch.img" "$work/$arch.raw"
  rm -f "$work/$arch.img"
  "$weaveoci" bundle init "$out/$arch" --disk "$work/$arch.raw" --source "$work/$arch.source.json" \
    --os linux --arch "$arch" --os-version "$(val OS_VERSION)" --os-build "$serial" --distro "$(val DISTRO)" \
    --firmware uefi --cpu-min "$(val CPU_MIN)" --cpu "$(val CPU)" \
    --memory-min "$(val MEMORY_MIN)" --memory "$(val MEMORY)" --credential-hint "$(val CREDENTIAL_HINT)" \
    --template "$image_dir" --template-ref "$repo_slug@$sha" \
    --image-version "$tag" --revision "$sha" --source-url "$source_url"
done

echo "TAG=$tag"
echo "SERIAL=$serial"
echo "REPOSITORY=$(val REPOSITORY)"
if [ -n "${GITHUB_OUTPUT:-}" ]; then
  { echo "tag=$tag"; echo "serial=$serial"; echo "repository=$(val REPOSITORY)"; } >> "$GITHUB_OUTPUT"
fi
