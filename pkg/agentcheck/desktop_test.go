package agentcheck

import (
	"context"
	"os"
	"testing"

	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/weaveclient"
	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/weavewire"
	"github.com/weaveplatform/weaveplatform-oci/pkg/imagecheck"
)

func desktopCaller(failure string, advertised bool) caller {
	var clipboard []weavewire.ClipboardItem
	mode := weavewire.DisplayMode{Width: 1024, Height: 768}
	return func(ctx context.Context, kind string, request, result any) error {
		if failure == kind {
			return os.ErrPermission
		}
		switch out := result.(type) {
		case *weavewire.SessionCurrentResponse:
			out.Session = &weavewire.SessionInfo{
				ID:      "session",
				User:    "acceptance",
				Console: true,
				State:   weavewire.SessionActive,
			}
			if failure == "session absent" {
				out.Session = nil
			}
			if failure == "session remote" {
				out.Session.Remote = true
			}
		case *weavewire.ClipboardSetResponse:
			clipboard = request.(weavewire.ClipboardSetRequest).Items
		case *weavewire.ClipboardGetResponse:
			out.Items = clipboard
			if failure == "clipboard mismatch" {
				out.Items = nil
			}
		case *weavewire.DisplayListResponse:
			out.Displays = []weavewire.DisplayInfo{
				{ID: "Virtual-1", Primary: advertised, Current: mode},
			}
			if advertised {
				out.Displays[0].Modes = []weavewire.DisplayMode{{Width: 800, Height: 600}}
			}
			if failure == "display absent" {
				out.Displays = nil
			}
			if failure == "display invalid" {
				out.Displays[0].ID = ""
			}
		case *weavewire.DisplaySetResponse:
			if failure != "display unchanged" {
				r := request.(weavewire.DisplaySetRequest)
				mode = weavewire.DisplayMode{Width: r.Width, Height: r.Height}
			}
		default:
			return fakeCall(ctx, kind, request, result)
		}
		return nil
	}
}

func TestDesktopAcceptancePerformsRoundtrips(t *testing.T) {
	for _, advertised := range []bool{false, true} {
		e := expectation()
		e.Desktop, e.ConsoleUser = true, "acceptance"
		call := desktopCaller("", advertised)
		r, err := probe(
			t.Context(),
			e,
			func(context.Context) (weaveclient.ModulesSnapshot, error) { return registry(e), nil },
			call,
			func(context.Context, []string) (string, error) { return e.CoreVersion, nil },
		)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"desktop-session", "clipboard-roundtrip", "display-roundtrip"} {
			if r.Operations[name].Status != imagecheck.Passed {
				t.Fatal(name, r)
			}
		}
		var displays weavewire.DisplayListResponse
		if err := call(t.Context(), weavewire.KindDisplayList, nil, &displays); err != nil {
			t.Fatal(err)
		}
		if displays.Displays[0].Current.Width != 1024 {
			t.Fatal("original display mode was not restored")
		}
	}
}

func TestDesktopRefusesMissingSessionAndAcknowledgementsWithoutEffects(t *testing.T) {
	for _, failure := range []string{"session absent", "session remote", "clipboard mismatch", "display absent", "display invalid", "display unchanged", weavewire.KindSessionCurrent, weavewire.KindClipboardSet, weavewire.KindClipboardGet, weavewire.KindDisplayList, weavewire.KindDisplaySet} {
		t.Run(failure, func(t *testing.T) {
			r := Result{Operations: map[string]imagecheck.Outcome{}}
			if err := probeDesktop(
				t.Context(),
				desktopCaller(failure, false),
				"acceptance",
				&r,
			); err == nil {
				t.Fatal("accepted failed GUI operation")
			}
		})
	}
	for _, mode := range []string{"no user", "headless", "failed operation"} {
		e := expectation()
		e.Desktop = true
		if mode != "no user" {
			e.ConsoleUser = "acceptance"
		}
		if mode == "headless" {
			e.Headless = true
		}
		if _, err := probe(
			t.Context(),
			e,
			func(context.Context) (weaveclient.ModulesSnapshot, error) { return registry(e), nil },
			desktopCaller("clipboard mismatch", true),
			func(context.Context, []string) (string, error) { return e.CoreVersion, nil },
		); err == nil {
			t.Fatal(mode)
		}
	}
}

func TestHeadlessOutcomeRecordsLimitation(t *testing.T) {
	e := expectation()
	e.Headless = true
	call := func(ctx context.Context, kind string, in, out any) error {
		if kind == weavewire.KindClipboardStat {
			return &weaveclient.GuestError{Code: weavewire.CodeUnsupported, Msg: "no session"}
		}
		return fakeCall(ctx, kind, in, out)
	}
	r, err := probe(
		t.Context(),
		e,
		func(context.Context) (weaveclient.ModulesSnapshot, error) { return registry(e), nil },
		call,
		func(context.Context, []string) (string, error) { return e.CoreVersion, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if r.Operations["clipboard"].Status != imagecheck.ExpectedUnavailable ||
		r.Operations["clipboard"].Reason == "" {
		t.Fatal(r)
	}
}
