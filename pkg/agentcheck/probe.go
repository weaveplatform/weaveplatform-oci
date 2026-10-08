// Package agentcheck exercises a running image's agent through the shared SDK.
// It does not create VMs or infer boot acceptance from an installed binary.
package agentcheck

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/protocol/hvchannel"
	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/weaveclient"
	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/weavewire"
	"github.com/weaveplatform/weaveplatform-oci/pkg/imagecheck"
)

var ErrProbe = errors.New("agent acceptance failed")

// Expected pins the image's platform and all eight module versions. Modules
// maps capability (presence, exec, ...) to its locked ID and version.
type Expected struct {
	OS, Arch, CoreVersion string
	Modules               map[string]Module
	Headless              bool
	// Desktop requires active console, clipboard and display round trips.
	Desktop     bool
	ConsoleUser string
}

type Module struct{ ID, Version string }

// Result contains only checks actually performed here. The caller must add
// boot, sealing, per-clone identity and observed power-cycle evidence.
type Result struct {
	Checks     map[string]bool               `json:"checks"`
	Operations map[string]imagecheck.Outcome `json:"operations"`
}

var capabilities = []string{
	"presence",
	"exec",
	"power",
	"time",
	"metrics",
	"clipboard",
	"session",
	"display",
}

// Connect proves the other clone's key is explicitly rejected, then opens a
// fresh connection authenticated with this clone's key. Silence is a failure.
// dial must connect to the same guest for both attempts.
func Connect(
	ctx context.Context,
	dial func(context.Context) (io.ReadWriteCloser, error),
	own, foreign ed25519.PrivateKey,
) (*weaveclient.Client, error) {
	if dial == nil || len(own) != ed25519.PrivateKeySize ||
		len(foreign) != ed25519.PrivateKeySize ||
		string(own.Public().(ed25519.PublicKey)) == string(foreign.Public().(ed25519.PublicKey)) {
		return nil, fmt.Errorf("%w: independent clone keys required", ErrProbe)
	}
	wrong, err := open(ctx, dial)
	if err != nil {
		return nil, err
	}
	refused := wrong.Authenticate(ctx, foreign)
	_ = wrong.Close()
	if !errors.Is(refused, weaveclient.ErrAuthRefused) {
		return nil, fmt.Errorf("%w: foreign key was not explicitly refused: %v", ErrProbe, refused)
	}
	good, err := open(ctx, dial)
	if err != nil {
		return nil, err
	}
	if err := good.Authenticate(ctx, own); err != nil {
		_ = good.Close()
		return nil, fmt.Errorf("authenticate clone: %w", err)
	}
	return good, nil
}

func open(
	ctx context.Context,
	dial func(context.Context) (io.ReadWriteCloser, error),
) (*weaveclient.Client, error) {
	conn, err := dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to guest: %w", err)
	}
	return weaveclient.New(ctx, conn, weaveclient.Options{}), nil
}

// Probe checks registry versions, service health and seven capability
// operations. Power is tested separately by Power, whose caller observes the
// actual shutdown/reboot through the hypervisor or provider.
func Probe(ctx context.Context, c *weaveclient.Client, e Expected) (Result, error) {
	return probe(
		ctx,
		e,
		c.Modules,
		c.Call,
		func(ctx context.Context, argv []string) (string, error) { return execute(ctx, c, argv) },
	)
}

type caller func(context.Context, string, any, any) error

func probe(
	ctx context.Context,
	e Expected,
	modules func(context.Context) (weaveclient.ModulesSnapshot, error),
	call caller,
	run func(context.Context, []string) (string, error),
) (Result, error) {
	r := Result{Checks: map[string]bool{}, Operations: map[string]imagecheck.Outcome{}}
	if (e.OS != "linux" && e.OS != "darwin" && e.OS != "windows") ||
		(e.Arch != "amd64" && e.Arch != "arm64") ||
		e.CoreVersion == "" ||
		len(e.Modules) != 8 {
		return r, fmt.Errorf("%w: complete platform/core/module expectations required", ErrProbe)
	}
	if e.Desktop && (e.Headless || e.ConsoleUser == "") {
		return r, fmt.Errorf("%w: desktop requires a console user and cannot be headless", ErrProbe)
	}
	snapshot, err := modules(ctx)
	if err != nil {
		return r, fmt.Errorf("read module registry: %w", err)
	}
	if len(snapshot.Modules) != 8 {
		return r, fmt.Errorf("%w: expected exactly eight installed modules", ErrProbe)
	}
	for _, capability := range capabilities {
		expected := e.Modules[capability]
		matches := 0
		for _, m := range snapshot.Modules {
			if m.Address != "weave."+capability {
				continue
			}
			matches++
			waiting := e.Headless && (capability == "clipboard" || capability == "display") &&
				m.State == weaveclient.ModuleStateWaitingForSession
			if expected.ID == "" || expected.Version == "" || m.ID != expected.ID ||
				m.Version != expected.Version ||
				(!waiting && (m.State != weaveclient.ModuleStateRunning || m.Protocol == 0 || m.Health.Status != hvchannel.HealthHealthy)) {
				return r, fmt.Errorf(
					"%w: module %s differs from lock or is unhealthy",
					ErrProbe,
					capability,
				)
			}
		}
		if matches != 1 {
			return r, fmt.Errorf("%w: missing or duplicate capability %s", ErrProbe, capability)
		}
		r.Checks["module-"+capability+"-installed"] = true
	}
	var hello weavewire.HelloResponse
	if err := call(ctx, weavewire.KindPresenceHello, nil, &hello); err != nil {
		return r, fmt.Errorf("presence: %w", err)
	}
	if hello.OS != e.OS || hello.Arch != e.Arch {
		return r, fmt.Errorf("%w: presence platform mismatch", ErrProbe)
	}
	r.Checks["agent-startup"], r.Checks["module-presence-operation"] = true, true
	r.Operations["presence"] = imagecheck.Outcome{Status: imagecheck.Passed}
	binary := map[string]string{"linux": "/usr/bin/weave-agent", "darwin": "/usr/local/libexec/weave/weave-agent", "windows": `C:\Program Files\Weave\weave-agent.exe`}[e.OS]
	version, err := run(ctx, []string{binary, "--version"})
	if err != nil {
		return r, fmt.Errorf("core version: %w", err)
	}
	if strings.TrimSpace(version) != e.CoreVersion {
		return r, fmt.Errorf("%w: running core version differs from lock", ErrProbe)
	}
	r.Checks["agent-version"], r.Checks["module-exec-operation"] = true, true
	r.Operations["exec"] = imagecheck.Outcome{Status: imagecheck.Passed}
	for _, op := range []struct {
		capability, kind string
		result           any
	}{
		{"time", weavewire.KindTimeGet, &weavewire.TimeResponse{}},
		{"metrics", weavewire.KindMetricsSample, &weavewire.MetricsResponse{}},
		{"session", weavewire.KindSessionCurrent, &weavewire.SessionCurrentResponse{}},
		{"clipboard", weavewire.KindClipboardStat, &weavewire.ClipboardStatResponse{}},
		{"display", weavewire.KindDisplayList, &weavewire.DisplayListResponse{}},
	} {
		err := call(ctx, op.kind, nil, op.result)
		if err != nil && !expectedHeadless(e, op.capability, err) {
			return r, fmt.Errorf("probe %s: %w", op.capability, err)
		}
		if value, ok := op.result.(*weavewire.TimeResponse); ok && value.UnixNano <= 0 {
			return r, fmt.Errorf("%w: clock returned no valid sample", ErrProbe)
		}
		if value, ok := op.result.(*weavewire.MetricsResponse); ok &&
			(value.MemoryTotalBytes == 0 || value.MemoryAvailableBytes > value.MemoryTotalBytes) {
			return r, fmt.Errorf("%w: metrics returned no valid memory sample", ErrProbe)
		}
		r.Checks["module-"+op.capability+"-operation"] = true
		outcome := imagecheck.Outcome{Status: imagecheck.Passed}
		if err != nil {
			outcome = imagecheck.Outcome{
				Status: imagecheck.ExpectedUnavailable,
				Reason: err.Error(),
			}
		}
		r.Operations[op.capability] = outcome
	}
	if e.Desktop {
		if err := probeDesktop(ctx, call, e.ConsoleUser, &r); err != nil {
			return r, err
		}
	}
	return r, nil
}

func expectedHeadless(e Expected, capability string, err error) bool {
	if !e.Headless ||
		(capability != "clipboard" && capability != "display" && capability != "session") {
		return false
	}
	var unavailable *weaveclient.GuestError
	if errors.As(err, &unavailable) && unavailable.Code == weavewire.CodeUnsupported {
		return true
	}
	var delivery *weaveclient.DeliveryError
	return errors.As(err, &delivery) && delivery.Reason == hvchannel.ReasonNotRunning &&
		delivery.State == weaveclient.ModuleStateWaitingForSession
}

func execute(ctx context.Context, c *weaveclient.Client, argv []string) (string, error) {
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	session, err := c.Exec(ctx, weavewire.ExecRequest{Argv: argv})
	if err != nil {
		return "", fmt.Errorf("start guest command: %w", err)
	}
	// Drain both streams concurrently: a full stderr must not stall stdout or
	// prevent the guest process from exiting. Version output is bounded.
	var output []byte
	var g errgroup.Group
	g.Go(func() error {
		var err error
		output, err = io.ReadAll(io.LimitReader(session.Stdout(), 4097))
		if err != nil {
			return err
		}
		_, err = io.Copy(io.Discard, session.Stdout())
		return err
	})
	g.Go(func() error { _, err := io.Copy(io.Discard, session.Stderr()); return err })
	code, waitErr := session.Wait(ctx)
	readErr := g.Wait()
	if err := errors.Join(waitErr, readErr); err != nil {
		return "", fmt.Errorf("guest command: %w", err)
	}
	if code != 0 || len(output) > 4096 {
		return "", fmt.Errorf("%w: guest command exit=%d or oversized output", ErrProbe, code)
	}
	return string(output), nil
}

// Power requires the caller to observe the requested transition; an agent
// acknowledgement alone never counts as a successful power operation.
func Power(
	ctx context.Context,
	c *weaveclient.Client,
	restart bool,
	observe func(context.Context) error,
) error {
	if observe == nil {
		return fmt.Errorf("%w: a power-transition observer is required", ErrProbe)
	}
	kind := weavewire.KindPowerShutdown
	if restart {
		kind = weavewire.KindPowerRestart
	}
	var response weavewire.PowerResponse
	if err := c.Call(
		ctx,
		kind,
		weavewire.PowerRequest{Reason: "weave image acceptance"},
		&response,
	); err != nil {
		return fmt.Errorf("power request: %w", err)
	}
	if !response.Accepted {
		return fmt.Errorf("%w: power transition was not accepted or has no observer", ErrProbe)
	}
	if err := observe(ctx); err != nil {
		return fmt.Errorf("observe guest power transition: %w", err)
	}
	return nil
}
