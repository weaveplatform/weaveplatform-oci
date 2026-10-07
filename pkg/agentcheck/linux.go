package agentcheck

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/weaveclient"
)

// LinuxObservation reads the running guest through authenticated exec. The
// cloud-image serial comes from Ubuntu's build.info, not the expected config.
// Store keys never leave the guest; only their SHA-256 digest is returned.
type LinuxObservation struct {
	Snapshot
	Version, Build, Challenge string
}

const linuxInspectScript = `set -eu
. /etc/os-release
printf '%s\n' "$VERSION_ID"
awk '/^serial: / {print $2}' /etc/cloud/build.info
cat /proc/sys/kernel/random/boot_id
cat /etc/machine-id
test -s /var/lib/weave/store.key
sha256sum /var/lib/weave/store.key | awk '{print "sha256:" $1}'
if test -e /var/lib/weave/manifest.sequence; then cat /var/lib/weave/manifest.sequence; printf '\n'; else printf '0\n'; fi
if getent passwd weavebuild >/dev/null || getent passwd _weavebuild >/dev/null ||
   test -e /etc/sudoers.d/weave-build || test -s /root/.ssh/authorized_keys; then
  printf 'build-credentials\n'
else
  printf 'sealed\n'
fi
cat /var/lib/weave/acceptance.challenge
`

func InspectLinux(ctx context.Context, client *weaveclient.Client) (LinuxObservation, error) {
	output, err := execute(ctx, client, []string{"/bin/sh", "-c", linuxInspectScript})
	if err != nil {
		return LinuxObservation{}, fmt.Errorf("inspect Linux guest: %w", err)
	}
	return parseLinuxObservation(output)
}

func parseLinuxObservation(output string) (LinuxObservation, error) {
	var result LinuxObservation
	lines := strings.Fields(output)
	if len(lines) != 8 || (lines[6] != "sealed" && lines[6] != "build-credentials") {
		return result, fmt.Errorf("%w: incomplete Linux guest observation", ErrProbe)
	}
	sequence, err := strconv.ParseUint(lines[5], 10, 64)
	if err != nil {
		return result, fmt.Errorf("%w: invalid manifest sequence: %w", ErrProbe, err)
	}
	result.Version, result.Build = lines[0], lines[1]
	result.Challenge = lines[7]
	result.Snapshot = Snapshot{
		BootID: lines[2], Identity: map[string]string{"machineID": lines[3]},
		StoreKeyDigest: lines[4], ManifestSequence: sequence,
		BuildCredentials: lines[6] != "sealed",
	}
	if err := checkSnapshot(result.Snapshot, 0); err != nil {
		return result, err
	}
	return result, nil
}
