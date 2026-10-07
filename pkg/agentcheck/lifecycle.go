package agentcheck

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"io"
	"maps"
	"regexp"

	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/weaveclient"
)

// Snapshot is guest evidence collected independently of the expected image
// metadata. KeyDigest is SHA-256 of the machine-bound store.key, never the key.
// BootID must change on a real boot (Linux boot_id, Windows boot timestamp, or
// macOS kern.bootsessionuuid). Identity contains the persistent guest identities.
type Snapshot struct {
	BootID           string            `json:"bootID"` //nolint:tagliatelle // Matches boot identity terminology.
	Identity         map[string]string `json:"identity"`
	StoreKeyDigest   string            `json:"storeKeyDigest"`
	ManifestSequence uint64            `json:"manifestSequence"`
	BuildCredentials bool              `json:"buildCredentials"`
}

// Lifecycle supplies a guest connection and hypervisor/provider observations.
// ArmPower must subscribe before returning, avoiding races with a fast reboot;
// its returned function waits for the actual requested transition. For reboot
// it must also wait until a new connection can be established. Close must free
// subscriptions even when the request fails. Inspect must read the running
// guest, rather than construct a Snapshot from expected metadata.
type Lifecycle struct {
	Dial     func(context.Context) (io.ReadWriteCloser, error)
	Inspect  func(context.Context, *weaveclient.Client) (Snapshot, error)
	ArmPower func(context.Context, bool) (wait func(context.Context) error, close func(), err error)
}

// LifecycleResult contains verified first-boot and post-reboot state. Comparing
// First.StoreKeyDigest across clones remains the two-clone validator's job.
type LifecycleResult struct {
	Result
	First, Rebooted Snapshot
}

// ProbeLifecycle authenticates, exercises every module, reboots, verifies the
// persistent identities/store and renewed trust, then shuts down. Sequence is
// the high-water mark recorded when the image was sealed. A lower mark on first
// boot or after reboot fails, so cleaning a store cannot disable anti-rollback.
func ProbeLifecycle(
	ctx context.Context,
	vm Lifecycle,
	own, foreign ed25519.PrivateKey,
	expected Expected,
	sequence uint64,
) (r LifecycleResult, err error) {
	r.Checks = map[string]bool{}
	if vm.Dial == nil || vm.Inspect == nil || vm.ArmPower == nil {
		return r, fmt.Errorf("%w: connection, inspection and power observers required", ErrProbe)
	}
	c, err := Connect(ctx, vm.Dial, own, foreign)
	if err != nil {
		return r, err
	}
	defer c.Close()
	r.Result, err = Probe(ctx, c, expected)
	if err != nil {
		return r, err
	}
	r.Checks["host-trust"], r.Checks["reject-cross-clone-trust"] = true, true
	r.First, err = vm.Inspect(ctx, c)
	if err != nil {
		return r, fmt.Errorf("inspect first boot: %w", err)
	}
	if err := checkSnapshot(r.First, sequence); err != nil {
		return r, err
	}
	if err := observedPower(ctx, c, vm, true); err != nil {
		return r, err
	}
	_ = c.Close()
	c, err = Connect(ctx, vm.Dial, own, foreign)
	if err != nil {
		return r, fmt.Errorf("trust after reboot: %w", err)
	}
	defer c.Close()
	// A second full probe catches services that only worked in the installer
	// session, modules not enabled at boot, and transient module start failures.
	if _, err := Probe(ctx, c, expected); err != nil {
		return r, fmt.Errorf("agent after reboot: %w", err)
	}
	r.Rebooted, err = vm.Inspect(ctx, c)
	if err != nil {
		return r, fmt.Errorf("inspect reboot: %w", err)
	}
	if err := checkSnapshot(r.Rebooted, r.First.ManifestSequence); err != nil {
		return r, err
	}
	if r.First.BootID == r.Rebooted.BootID || !maps.Equal(r.First.Identity, r.Rebooted.Identity) ||
		r.First.StoreKeyDigest != r.Rebooted.StoreKeyDigest {
		return r, fmt.Errorf(
			"%w: reboot did not change boot ID while preserving identities and store key",
			ErrProbe,
		)
	}
	r.Checks["reboot-identity"], r.Checks["trust-after-reboot"] = true, true
	r.Checks["manifest-sequence"], r.Checks["no-build-credentials"] = true, true
	if err := observedPower(ctx, c, vm, false); err != nil {
		return r, err
	}
	r.Checks["module-power-operation"], r.Checks["shutdown"] = true, true
	return r, nil
}

func observedPower(ctx context.Context, c *weaveclient.Client, vm Lifecycle, restart bool) error {
	wait, closeObserver, err := vm.ArmPower(ctx, restart)
	if closeObserver != nil {
		defer closeObserver()
	}
	if err != nil {
		return fmt.Errorf("subscribe to power transition: %w", err)
	}
	if wait == nil || closeObserver == nil {
		return fmt.Errorf("%w: incomplete power observer", ErrProbe)
	}
	return Power(ctx, c, restart, wait)
}

var storeDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func checkSnapshot(s Snapshot, sequence uint64) error {
	if s.BootID == "" || len(s.Identity) == 0 || !storeDigest.MatchString(s.StoreKeyDigest) ||
		s.ManifestSequence < sequence || s.BuildCredentials {
		return fmt.Errorf(
			"%w: missing identity/store evidence, residual credentials or regressed manifest sequence",
			ErrProbe,
		)
	}
	for name, value := range s.Identity {
		if name == "" || value == "" {
			return fmt.Errorf("%w: empty persistent identity", ErrProbe)
		}
	}
	return nil
}
