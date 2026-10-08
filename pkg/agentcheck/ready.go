package agentcheck

import (
	"context"
	"fmt"
	"time"

	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/protocol/hvchannel"
	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/weaveclient"
)

// WaitModules waits for asynchronous module startup without accepting a failed
// probe. Wrong versions and unexpected modules are terminal errors. The caller
// must supply the clone's deadline and then perform Probe or ProbeLifecycle.
func WaitModules(ctx context.Context, client *weaveclient.Client, expected Expected) error {
	return waitModules(ctx, client.Modules, expected)
}

func waitModules(
	ctx context.Context,
	read func(context.Context) (weaveclient.ModulesSnapshot, error),
	e Expected,
) error {
	if len(e.Modules) != 8 {
		return fmt.Errorf("%w: eight pinned modules required", ErrProbe)
	}
	for {
		snapshot, err := read(ctx)
		if err != nil {
			return fmt.Errorf("module readiness: %w", err)
		}
		ready := len(snapshot.Modules) == 8
		seen := map[string]bool{}
		for _, m := range snapshot.Modules {
			found := false
			for capability, want := range e.Modules {
				if m.Address != "weave."+capability {
					continue
				}
				if seen[capability] || m.ID != want.ID || m.Version != want.Version {
					return fmt.Errorf("%w: module registry differs from lock", ErrProbe)
				}
				seen[capability], found = true, true
				waiting := e.Headless && (capability == "display" || capability == "clipboard") &&
					m.State == weaveclient.ModuleStateWaitingForSession
				if !waiting &&
					(m.State != weaveclient.ModuleStateRunning || m.Protocol == 0 || m.Health.Status != hvchannel.HealthHealthy) {
					ready = false
				}
				break
			}
			if !found {
				return fmt.Errorf("%w: unexpected installed module", ErrProbe)
			}
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("await healthy modules: %w", ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}
