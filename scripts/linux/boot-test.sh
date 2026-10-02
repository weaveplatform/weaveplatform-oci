#!/usr/bin/env bash
# Boots a linux/amd64 weave bundle under QEMU the way hostweave's QEMU
# runtime will (decision 0005): the raw base stays read-only, the guest
# writes to a qcow2 overlay, firmware is OVMF with fresh variables, and the
# instance is provisioned by a NoCloud seed. The seed's runcmd prints a
# marker on the serial console and powers off; the test passes when the
# marker appears.
#
#   boot-test.sh <bundle-dir> [timeout-seconds]
#
# Needs qemu-system-x86_64, qemu-img, OVMF (ovmf) and cloud-localds
# (cloud-image-utils). Uses KVM when /dev/kvm is usable, TCG otherwise.
set -euo pipefail

bundle=${1:?bundle directory}
timeout_s=${2:-900}
disk="$bundle/$(jq -r '.disks[] | select(.role=="system") | .path' "$bundle/bundle.json")"
[ -f "$disk" ] || { echo "no system disk in $bundle" >&2; exit 2; }

ovmf_code=/usr/share/OVMF/OVMF_CODE_4M.fd
ovmf_vars=/usr/share/OVMF/OVMF_VARS_4M.fd
[ -f "$ovmf_code" ] || { echo "OVMF not found at $ovmf_code" >&2; exit 2; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
marker="WEAVE-BOOT-OK-$(od -An -N8 -tx8 /dev/urandom | tr -d ' ')"

qemu-img create -q -f qcow2 -b "$(realpath "$disk")" -F raw "$work/overlay.qcow2" 16G
cp "$ovmf_vars" "$work/vars.fd"
cat > "$work/user-data" <<UD
#cloud-config
runcmd:
  - echo "$marker" > /dev/ttyS0
  - systemctl poweroff
UD
printf 'instance-id: weave-boot-test\nlocal-hostname: weave-boot-test\n' > "$work/meta-data"
cloud-localds "$work/seed.img" "$work/user-data" "$work/meta-data"

accel=tcg
if [ -r /dev/kvm ] && [ -w /dev/kvm ]; then accel=kvm; fi
echo "booting $disk ($accel, timeout ${timeout_s}s)"

set +e
timeout "$timeout_s" qemu-system-x86_64 \
  -machine q35,accel="$accel" -cpu max -smp 2 -m 2048 \
  -drive if=pflash,format=raw,readonly=on,file="$ovmf_code" \
  -drive if=pflash,format=raw,file="$work/vars.fd" \
  -drive file="$work/overlay.qcow2",if=virtio,format=qcow2 \
  -drive file="$work/seed.img",if=virtio,format=raw \
  -nic user,model=virtio-net-pci \
  -display none -serial file:"$work/serial.log" -no-reboot
rc=$?
set -e

if grep -q "$marker" "$work/serial.log"; then
  echo "boot test passed: the guest ran its NoCloud seed and powered off (qemu exit $rc)"
  exit 0
fi
echo "boot test failed (qemu exit $rc); last serial output:" >&2
tail -n 60 "$work/serial.log" >&2
exit 1
