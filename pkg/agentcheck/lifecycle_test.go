package agentcheck

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/weaveclient"
	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/weaveexec"
	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/weavemodule"
	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/weavewire"
)

type powerService struct{ armed *atomic.Bool }

func (powerService) Capability() weavewire.Capability { return weavewire.Power }
func (p powerService) Register(r *weavemodule.Registrar) error {
	for _, kind := range []string{weavewire.KindPowerRestart, weavewire.KindPowerShutdown} {
		r.Handle(kind, func(context.Context, []byte) ([]byte, error) {
			if !p.armed.Load() {
				return nil, fmt.Errorf("power request issued before observer subscription")
			}
			return weavewire.EncodePayload(weavewire.PowerResponse{Accepted: true})
		})
	}
	return nil
}

func services(e Expected, armed *atomic.Bool) []weavemodule.Service {
	return []weavemodule.Service{
		service{
			weavewire.Presence,
			weavewire.KindPresenceHello,
			weavewire.HelloResponse{OS: e.OS, Arch: e.Arch},
			nil,
		},
		weaveexec.NewService(starter{output: e.CoreVersion + "\n"}),
		powerService{armed},
		service{weavewire.Time, weavewire.KindTimeGet, weavewire.TimeResponse{UnixNano: 1}, nil},
		service{
			weavewire.Metrics,
			weavewire.KindMetricsSample,
			weavewire.MetricsResponse{MemoryTotalBytes: 4096},
			nil,
		},
		service{
			weavewire.Session,
			weavewire.KindSessionCurrent,
			weavewire.SessionCurrentResponse{},
			nil,
		},
		service{
			weavewire.Clipboard,
			weavewire.KindClipboardStat,
			weavewire.ClipboardStatResponse{},
			nil,
		},
		service{weavewire.Display, weavewire.KindDisplayList, weavewire.DisplayListResponse{}, nil},
	}
}

func TestLifecycleRealSDKFramingAndFailureEvidence(t *testing.T) {
	for _, mode := range []string{"valid", "ready", "reboot-ready", "nil-dial", "nil-inspect", "nil-power", "dial", "probe", "inspect", "credentials", "arm", "nil-wait", "nil-close", "restart", "reconnect", "reboot-probe", "reboot-inspect", "sequence", "same-boot", "changed-identity", "changed-key", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			e := expectation()
			own, foreign := key(t), key(t)
			var armed atomic.Bool
			boot, inspections, subscriptions, closes := 1, 0, 0, 0
			vm := Lifecycle{
				Ready: func(context.Context, *weaveclient.Client) error {
					if mode == "ready" || (mode == "reboot-ready" && boot == 2) {
						return os.ErrPermission
					}
					return nil
				},
				Dial: func(context.Context) (io.ReadWriteCloser, error) {
					if mode == "dial" || (mode == "reconnect" && boot == 2) {
						return nil, os.ErrPermission
					}
					modules := services(e, &armed)
					if mode == "probe" || (mode == "reboot-probe" && boot == 2) {
						modules = modules[:1]
					}
					return connection(t, own.Public().(ed25519.PublicKey), modules...), nil
				},
				Inspect: func(context.Context, *weaveclient.Client) (Snapshot, error) {
					inspections++
					if mode == "inspect" || (mode == "reboot-inspect" && inspections == 2) {
						return Snapshot{}, os.ErrPermission
					}
					s := Snapshot{
						BootID:           fmt.Sprintf("boot-%d", boot),
						Identity:         map[string]string{"machineID": "machine"},
						StoreKeyDigest:   "sha256:" + strings.Repeat("a", 64),
						ManifestSequence: 9,
					}
					if mode == "credentials" {
						s.BuildCredentials = true
					}
					if boot == 2 {
						switch mode {
						case "same-boot":
							s.BootID = "boot-1"
						case "changed-identity":
							s.Identity["machineID"] = "other"
						case "changed-key":
							s.StoreKeyDigest = "sha256:" + strings.Repeat("b", 64)
						case "sequence":
							s.ManifestSequence = 8
						}
					}
					return s, nil
				},
				ArmPower: func(_ context.Context, restart bool) (func(context.Context) error, func(), error) {
					subscriptions++
					armed.Store(true)
					closeObserver := func() { closes++; armed.Store(false) }
					wait := func(context.Context) error {
						if (mode == "restart" && restart) || (mode == "shutdown" && !restart) {
							return os.ErrPermission
						}
						if restart {
							boot++
						}
						return nil
					}
					if mode == "arm" {
						return nil, closeObserver, os.ErrPermission
					}
					if mode == "nil-wait" {
						wait = nil
					}
					if mode == "nil-close" {
						closeObserver = nil
					}
					return wait, closeObserver, nil
				},
			}
			switch mode {
			case "nil-dial":
				vm.Dial = nil
			case "nil-inspect":
				vm.Inspect = nil
			case "nil-power":
				vm.ArmPower = nil
			}
			r, err := ProbeLifecycle(ctx, vm, own, foreign, e, 9)
			if mode == "valid" {
				if err != nil || !r.Checks["trust-after-reboot"] ||
					!r.Checks["module-power-operation"] ||
					!r.Checks["no-build-credentials"] ||
					inspections != 2 {
					t.Fatal(r, err)
				}
				if r.Checks["fresh-agent-store"] || r.Checks["boot"] {
					t.Fatal("claimed checks owned by clone/hypervisor validator", r)
				}
			} else if err == nil {
				t.Fatal("accepted failed lifecycle", r)
			}
			if mode != "nil-close" && closes != subscriptions {
				t.Fatal("observer leaked", closes, subscriptions)
			}
		})
	}
}

func TestSnapshotRequiresIdentitySealingAndSequenceEvidence(t *testing.T) {
	for _, mode := range []string{"boot", "identity", "identity-name", "identity-value", "key", "sequence", "credentials"} {
		t.Run(mode, func(t *testing.T) {
			s := Snapshot{
				BootID:           "boot",
				Identity:         map[string]string{"machine": "id"},
				StoreKeyDigest:   "sha256:" + strings.Repeat("a", 64),
				ManifestSequence: 7,
			}
			switch mode {
			case "boot":
				s.BootID = ""
			case "identity":
				s.Identity = nil
			case "identity-name":
				s.Identity[""] = "id"
			case "identity-value":
				s.Identity["machine"] = ""
			case "key":
				s.StoreKeyDigest = "raw-secret"
			case "sequence":
				s.ManifestSequence = 6
			case "credentials":
				s.BuildCredentials = true
			}
			if err := checkSnapshot(s, 7); err == nil {
				t.Fatal("accepted incomplete snapshot", s)
			}
		})
	}
}
