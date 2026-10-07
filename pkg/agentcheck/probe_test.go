package agentcheck

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/protocol/hvchannel"
	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/weaveclient"
	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/weaveexec"
	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/weavemodule"
	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/weavemodule/weavemoduletest"
	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/weavewire"
)

func expectation() Expected {
	e := Expected{OS: "linux", Arch: "arm64", CoreVersion: "0.9.11", Modules: map[string]Module{}}
	for _, capability := range capabilities {
		e.Modules[capability] = Module{"weave-test-" + capability, "0.0.0-test"}
	}
	return e
}

func registry(e Expected) weaveclient.ModulesSnapshot {
	s := weaveclient.ModulesSnapshot{Revision: 1}
	for _, capability := range capabilities {
		m := e.Modules[capability]
		s.Modules = append(
			s.Modules,
			hvchannel.ModuleInfo{
				ID:       m.ID,
				Version:  m.Version,
				Address:  "weave." + capability,
				State:    weaveclient.ModuleStateRunning,
				Protocol: 1,
				Health:   hvchannel.ModuleHealth{Status: hvchannel.HealthHealthy},
			},
		)
	}
	return s
}

func fakeCall(_ context.Context, _ string, _ any, out any) error {
	if hello, ok := out.(*weavewire.HelloResponse); ok {
		hello.OS, hello.Arch = "linux", "arm64"
	}
	if sample, ok := out.(*weavewire.TimeResponse); ok {
		sample.UnixNano = 1
	}
	if sample, ok := out.(*weavewire.MetricsResponse); ok {
		sample.MemoryTotalBytes = 4096
	}
	return nil
}

func TestProbeRefusesMissingStaleOrUnhealthyModules(t *testing.T) {
	for _, mode := range []string{"os", "arch", "core absent", "module absent", "registry error", "extra module", "ID", "version", "crashed", "protocol", "health", "duplicate", "hello error", "hello platform", "exec error", "core version", "time error", "empty time", "empty metrics"} {
		t.Run(mode, func(t *testing.T) {
			e := expectation()
			s := registry(e)
			var registryErr, errorRun error
			call := caller(fakeCall)
			version := e.CoreVersion
			switch mode {
			case "os":
				e.OS = "other"
			case "arch":
				e.Arch = "other"
			case "core absent":
				e.CoreVersion = ""
			case "module absent":
				delete(e.Modules, "exec")
			case "registry error":
				registryErr = os.ErrPermission
			case "extra module":
				s.Modules = append(s.Modules, hvchannel.ModuleInfo{})
			case "ID":
				s.Modules[0].ID = "wrong"
			case "version":
				s.Modules[0].Version = "old"
			case "crashed":
				s.Modules[0].State = "backoff"
			case "protocol":
				s.Modules[0].Protocol = 0
			case "health":
				s.Modules[0].Health.Status = "unhealthy"
			case "duplicate":
				s.Modules[1] = s.Modules[0]
			case "hello error":
				call = func(context.Context, string, any, any) error { return os.ErrPermission }
			case "hello platform":
				call = func(context.Context, string, any, any) error { return nil }
			case "exec error":
				errorRun = os.ErrPermission
			case "core version":
				version = "old"
			case "time error":
				call = func(ctx context.Context, k string, p, out any) error {
					if k == weavewire.KindTimeGet {
						return os.ErrPermission
					}
					return fakeCall(ctx, k, p, out)
				}
			case "empty time", "empty metrics":
				call = func(ctx context.Context, k string, p, out any) error {
					if (mode == "empty time" && k == weavewire.KindTimeGet) ||
						(mode == "empty metrics" && k == weavewire.KindMetricsSample) {
						return nil
					}
					return fakeCall(ctx, k, p, out)
				}
			}
			r, err := probe(
				t.Context(),
				e,
				func(context.Context) (weaveclient.ModulesSnapshot, error) { return s, registryErr },
				call,
				func(context.Context, []string) (string, error) { return version, errorRun },
			)
			if err == nil {
				t.Fatal("accepted invalid agent", r)
			}
		})
	}
}

func TestHeadlessRequiresExplicitTypedUnavailability(t *testing.T) {
	e := expectation()
	e.Headless = true
	for _, err := range []error{&weaveclient.GuestError{Code: weavewire.CodeUnsupported}, &weaveclient.DeliveryError{Reason: hvchannel.ReasonNotRunning, State: weaveclient.ModuleStateWaitingForSession}} {
		if !expectedHeadless(e, "clipboard", err) {
			t.Fatal(err)
		}
		if expectedHeadless(e, "metrics", err) {
			t.Fatal("masked service failure")
		}
	}
	for _, err := range []error{context.DeadlineExceeded, weaveclient.ErrNoSession, weaveclient.ErrModuleNotInstalled, &weaveclient.DeliveryError{Reason: hvchannel.ReasonNotRunning, State: "backoff"}, &weaveclient.GuestError{Code: "denied"}} {
		if expectedHeadless(e, "clipboard", err) {
			t.Fatal("masked fault or silence", err)
		}
	}
	s := registry(e)
	for i := range s.Modules {
		if s.Modules[i].Address == "weave.clipboard" || s.Modules[i].Address == "weave.display" {
			s.Modules[i].State = weaveclient.ModuleStateWaitingForSession
			s.Modules[i].Protocol = 0
		}
	}
	call := func(ctx context.Context, k string, p, out any) error {
		if k == weavewire.KindClipboardStat || k == weavewire.KindDisplayList {
			return &weaveclient.DeliveryError{
				Reason: hvchannel.ReasonNotRunning,
				State:  weaveclient.ModuleStateWaitingForSession,
			}
		}
		return fakeCall(ctx, k, p, out)
	}
	r, err := probe(
		t.Context(),
		e,
		func(context.Context) (weaveclient.ModulesSnapshot, error) { return s, nil },
		call,
		func(context.Context, []string) (string, error) { return e.CoreVersion, nil },
	)
	if err != nil || !r.Checks["module-display-operation"] || r.Checks["module-power-operation"] {
		t.Fatal(r, err)
	}
}

type service struct {
	capability weavewire.Capability
	kind       string
	reply      any
	err        error
}

func (s service) Capability() weavewire.Capability { return s.capability }
func (s service) Register(r *weavemodule.Registrar) error {
	r.Handle(s.kind, func(context.Context, []byte) ([]byte, error) {
		if s.err != nil {
			return nil, s.err
		}
		return weavewire.EncodePayload(s.reply)
	})
	return nil
}

type starter struct {
	output string
	code   int
	err    error
}

func (s starter) Start(context.Context, weavewire.ExecRequest) (weaveexec.Process, error) {
	if s.err != nil {
		return nil, s.err
	}
	return process{s.output, s.code}, nil
}

type process struct {
	output string
	code   int
}

func (p process) Stdout() io.Reader { return strings.NewReader(p.output) }

func (process) Stderr() io.Reader            { return strings.NewReader(strings.Repeat("diagnostic", 8192)) }
func (process) Stdin() io.WriteCloser        { return nil }
func (process) PID() int                     { return 42 }
func (process) Resize(uint16, uint16) error  { return nil }
func (process) Signal(string) error          { return nil }
func (p process) Wait() (int, string, error) { return p.code, "", nil }
func (process) Close() error                 { return nil }

func connection(
	t *testing.T,
	trusted ed25519.PublicKey,
	services ...weavemodule.Service,
) io.ReadWriteCloser {
	t.Helper()
	guest, host := net.Pipe()
	core := weavemoduletest.NewCore(guest, trusted)
	for _, s := range services {
		core.Serve(t, s)
	}
	go core.Run()
	t.Cleanup(func() { _ = host.Close(); _ = core.Close() })
	return host
}

func key(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestConnectRejectsCrossCloneTrust(t *testing.T) {
	own, foreign := key(t), key(t)
	for _, mode := range []string{"valid", "bad key length", "same key", "first dial", "second dial", "guest trusts wrong key", "guest trusts neither", "silent"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			a, b := own, foreign
			if mode == "bad key length" {
				a = nil
			}
			if mode == "same key" {
				b = a
			}
			calls := 0
			dial := func(context.Context) (io.ReadWriteCloser, error) {
				calls++
				if mode == "first dial" || (mode == "second dial" && calls == 2) {
					return nil, os.ErrPermission
				}
				trusted := own.Public().(ed25519.PublicKey)
				if mode == "guest trusts wrong key" {
					trusted = foreign.Public().(ed25519.PublicKey)
				}
				if mode == "guest trusts neither" {
					trusted = key(t).Public().(ed25519.PublicKey)
				}
				if mode == "silent" {
					guest, host := net.Pipe()
					t.Cleanup(func() { _ = guest.Close(); _ = host.Close() })
					return host, nil
				}
				return connection(t, trusted), nil
			}
			c, err := Connect(ctx, dial, a, b)
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				_ = c.Close()
			} else if err == nil {
				_ = c.Close()
				t.Fatal("accepted invalid trust")
			}
		})
	}
}

func TestSDKProbeAndPowerOverRealFraming(t *testing.T) {
	for _, osName := range []string{"linux", "darwin", "windows"} {
		t.Run(osName, func(t *testing.T) {
			e := expectation()
			e.OS = osName
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			svcs := []weavemodule.Service{
				service{
					weavewire.Presence,
					weavewire.KindPresenceHello,
					weavewire.HelloResponse{OS: e.OS, Arch: e.Arch},
					nil,
				},
				weaveexec.NewService(starter{output: e.CoreVersion + "\n"}),
				service{
					weavewire.Power,
					weavewire.KindPowerShutdown,
					weavewire.PowerResponse{Accepted: true},
					nil,
				},
				service{
					weavewire.Time,
					weavewire.KindTimeGet,
					weavewire.TimeResponse{UnixNano: time.Now().UnixNano()},
					nil,
				},
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
					weavewire.Display,
					weavewire.KindDisplayList,
					weavewire.DisplayListResponse{},
					nil,
				},
				service{
					weavewire.Clipboard,
					weavewire.KindClipboardStat,
					weavewire.ClipboardStatResponse{},
					nil,
				},
			}
			c := weaveclient.New(ctx, connection(t, nil, svcs...), weaveclient.Options{})
			defer c.Close()
			r, err := Probe(ctx, c, e)
			if err != nil || !r.Checks["module-exec-operation"] || len(r.Checks) != 17 {
				t.Fatal(r, err)
			}
			observed := false
			if err := Power(
				ctx,
				c,
				false,
				func(context.Context) error { observed = true; return nil },
			); err != nil ||
				!observed {
				t.Fatal(err)
			}
		})
	}
}

func TestPowerRequiresObservedTransition(t *testing.T) {
	for _, mode := range []string{"restart", "refused", "request fails", "no observer", "observer fails"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			kind := weavewire.KindPowerShutdown
			restart := mode == "restart"
			if restart {
				kind = weavewire.KindPowerRestart
			}
			var requestErr error
			if mode == "request fails" {
				requestErr = os.ErrPermission
			}
			c := weaveclient.New(
				ctx,
				connection(
					t,
					nil,
					service{
						weavewire.Power,
						kind,
						weavewire.PowerResponse{Accepted: mode != "refused"},
						requestErr,
					},
				),
				weaveclient.Options{},
			)
			defer c.Close()
			observe := func(context.Context) error {
				if mode == "observer fails" {
					return os.ErrPermission
				}
				return nil
			}
			if mode == "no observer" {
				observe = nil
			}
			err := Power(ctx, c, restart, observe)
			if (err == nil) != restart {
				t.Fatal(mode, err)
			}
		})
	}
}

func TestExecProbeFailuresAndBoundedOutput(t *testing.T) {
	for _, mode := range []string{"exit", "oversized", "start fails", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			s := starter{output: "1"}
			switch mode {
			case "exit":
				s.code = 1
			case "oversized":
				s.output = strings.Repeat("x", 100000)
			case "start fails":
				s.err = os.ErrPermission
			case "cancelled":
				cancel()
			}
			c := weaveclient.New(
				ctx,
				connection(t, nil, weaveexec.NewService(s)),
				weaveclient.Options{},
			)
			defer c.Close()
			if _, err := execute(ctx, c, []string{"version"}); err == nil {
				t.Fatal("accepted failed command")
			}
		})
	}
	if expectedHeadless(Expected{}, "clipboard", errors.New("failure")) {
		t.Fatal("masked failure")
	}
}
