package agentcheck

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/weaveclient"
)

func TestModuleReadinessWaitsForHealthButRefusesWrongInputs(t *testing.T) {
	for _, mode := range []string{"valid", "starting", "headless", "missing", "wrong-version", "unexpected", "duplicate", "read-error", "bad-lock"} {
		t.Run(mode, func(t *testing.T) {
			e := expectation()
			e.Headless = true
			if mode == "bad-lock" {
				e.Modules = nil
			}
			ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
			defer cancel()
			calls := 0
			err := waitModules(ctx, func(context.Context) (weaveclient.ModulesSnapshot, error) {
				calls++
				s := registry(e)
				switch mode {
				case "starting":
					if calls == 1 {
						s.Modules[0].State = "starting"
					}
				case "headless":
					s.Modules[5].State = weaveclient.ModuleStateWaitingForSession
					s.Modules[7].State = weaveclient.ModuleStateWaitingForSession
				case "missing":
					s.Modules = nil
				case "wrong-version":
					s.Modules[0].Version = "old"
				case "unexpected":
					s.Modules[0].Address = "weave.unknown"
				case "duplicate":
					s.Modules[1] = s.Modules[0]
				case "read-error":
					return s, os.ErrPermission
				}
				return s, nil
			}, e)
			if (err == nil) != (mode == "valid" || mode == "starting" || mode == "headless") {
				t.Fatal(mode, err)
			}
		})
	}
	e := expectation()
	client := weaveclient.New(
		t.Context(),
		connection(t, nil, services(e, new(atomic.Bool))...),
		weaveclient.Options{},
	)
	defer client.Close()
	if err := WaitModules(t.Context(), client, e); err != nil {
		t.Fatal(err)
	}
}
