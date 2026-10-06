#!/usr/bin/env bash
# Usage: workspace.sh init | check [required-GiB] | run <command> [args...]
# Never fall back to the internal disk when the image volume is unavailable.
set -euo pipefail

volume=/Volumes/KING
root="$volume/weave-images"
work="$root/work"
bundle="$root/workspace.sparsebundle"
reserve_kib=$((32 * 1024 * 1024))

fail() { echo "image workspace: $*" >&2; exit 1; }
mounted() { mount | grep -F " on $1 (" >/dev/null; }
free_kib() { df -Pk "$1" | awk 'END {print $4}'; }
capacity() {
  local required=${1:-0}
  [[ "$required" =~ ^[0-9]+$ ]] || fail 'required space must be whole GiB'
  local need=$((required * 1024 * 1024))
  [ "$(free_kib "$volume")" -ge "$((need + reserve_kib))" ] || fail "KING needs ${required} GiB plus the 32 GiB reserve"
  [ "$(free_kib "$work")" -ge "$need" ] || fail "workspace needs ${required} GiB"
}
check() {
  mounted "$volume" || fail 'KING is not mounted'
  mounted "$work" || fail 'APFS workspace is not mounted; run workspace.sh init'
  mount | grep -F " on $work (apfs," >/dev/null || fail 'workspace is not APFS'
  # Verify the mount belongs to our image, rather than an unrelated APFS volume.
  hdiutil info | awk -v backing="$bundle" -v target="$work" '
    /^image-path[[:space:]]*:/ {
      sub(/^image-path[[:space:]]*:[[:space:]]*/, "")
      ours = ($0 == backing)
    }
    ours && $NF == target { found = 1 }
    END { exit !found }
  ' || fail 'workspace is not backed by the KING sparse-bundle'
  capacity "${1:-0}"
}

case "${1:-check}" in
  init)
    mounted "$volume" || fail 'KING is not mounted'
    [ "$(free_kib "$volume")" -ge "$reserve_kib" ] || fail 'KING has less than the 32 GiB reserve'
    mkdir -p "$root" "$work"
    if [ ! -e "$bundle" ]; then
      hdiutil create -size 400g -type SPARSEBUNDLE -fs APFS -volname WeaveImages "$bundle"
    fi
    if ! mounted "$work"; then
      [ -z "$(ls -A "$work")" ] || fail 'refusing to hide files beneath the mount point'
      if diskutil image attach --help >/dev/null 2>&1; then
        diskutil image attach --mountPoint "$work" --mountOptions nobrowse,owners "$bundle"
      else
        hdiutil attach "$bundle" -mountpoint "$work" -nobrowse -owners on
      fi
    fi
    check
    mkdir -p "$work"/{media,cache,builds,images,tmp,reports}
    echo "$work"
    ;;
  check) check "${2:-0}" ;;
  run)
    shift
    [ "$#" -gt 0 ] || fail 'run requires a command'
    check "${WEAVE_BUILD_REQUIRED_GIB:-0}"
    export WEAVE_IMAGE_WORKSPACE="$work"
    export WEAVEOCI_CACHE="$work/cache/oci"
    export TMPDIR="$work/tmp/"
    export GOCACHE="$work/cache/go-build" GOMODCACHE="$work/cache/go-mod"
    export XDG_CACHE_HOME="$work/cache"
    export GOWORK=off
    exec "$@"
    ;;
  *) fail 'usage: workspace.sh init | check [required-GiB] | run <command> [args...]' ;;
esac
