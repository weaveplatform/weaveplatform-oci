package macsetup

import (
	"encoding/json"
	"os"
	"testing"
)

// widgetDesktop is the first macOS 27.0.1 (26A434) desktop frame from the
// release end-to-end run: widget text only, no readable menu bar. The VM was
// not kept, so the fixture is rebuilt from that frame's OCR text ("Location
// Access Needed", a calendar, Photos) in the widgets' default positions.
func widgetDesktop(t *testing.T) Screen {
	t.Helper()
	raw, err := os.ReadFile("testdata/macos27-widget-desktop.json")
	if err != nil {
		t.Fatal(err)
	}
	var s Screen
	if err = json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestWidgetOnlyDesktopIsTheDesktop(t *testing.T) {
	plan, err := Next(widgetDesktop(t), DefaultConfig())
	if err != nil || plan.Stage != "desktop" || len(plan.Actions) != 0 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	for _, labels := range [][]string{
		{"Location Access Needed", "Photos"},
		{"Location Access Needed", "Monday"},
		{"No events today", "Photos"},
		{"No more events today", "No Photos"},
		{"SUNDAY", "12", "Photos", "Location Access Needed"},
	} {
		if plan, err := Next(
			screen(labels...),
			DefaultConfig(),
		); err != nil ||
			plan.Stage != "desktop" {
			t.Errorf("%q: plan=%+v err=%v", labels, plan, err)
		}
	}
}

// Widget text is not enough on its own: one cue, or any Setup Assistant or
// login control beside them, is not the desktop.
func TestWidgetCuesNeverOverrideSetupPages(t *testing.T) {
	for _, labels := range [][]string{
		{"Location Access Needed"},
		{"Photos"},
		{"MONDAY", "5"},
		{"Location Access Needed", "Photos", "Continue"},
		{"Location Access Needed", "Photos", "Not Now"},
		{"Location Access Needed", "Photos", "Set Up Later"},
		{"Monday", "Photos", "Enter Password"},
		{"Photos", "Monday", "Back"},
	} {
		plan, err := Next(screen(labels...), DefaultConfig())
		if err != nil || plan.Stage == "desktop" {
			t.Errorf("%q: plan=%+v err=%v", labels, plan, err)
		}
	}
	// A recognised setup page still wins over the widgets behind it.
	plan, err := Next(
		screen("Location Access Needed", "Photos", "Enable Location Services", "Continue"),
		DefaultConfig(),
	)
	if err != nil || plan.Stage != "location" {
		t.Errorf("plan=%+v err=%v", plan, err)
	}
}

func TestDesktopFocusUsesTheMenuBarWhenReadable(t *testing.T) {
	s := Screen{Width: 2048, Height: 1536, Text: []Text{
		{Value: "Finder", X: 108, Y: 17, Width: 81, Height: 21},
	}}
	got := DesktopFocus(s)
	if len(got) != 2 || got[0] != (Action{Kind: "click", X: 148, Y: 27}) ||
		got[1] != key("escape") {
		t.Fatalf("focus = %+v", got)
	}
}

// Without a menu bar, focus is Escape then a click on bare desktop, away from
// every widget.
func TestDesktopFocusClicksEmptyDesktopWithoutAMenuBar(t *testing.T) {
	s := widgetDesktop(t)
	got := DesktopFocus(s)
	if len(got) != 2 || got[0] != key("escape") || got[1].Kind != "click" {
		t.Fatalf("focus = %+v", got)
	}
	x, y := got[1].X, got[1].Y
	if y < s.Height/20 || y > s.Height*9/10 {
		t.Errorf("click (%d,%d) is on the menu bar or Dock", x, y)
	}
	for _, w := range s.Text {
		if x >= w.X && x <= w.X+w.Width && y >= w.Y && y <= w.Y+w.Height {
			t.Errorf("click (%d,%d) lands on %q", x, y, w.Value)
		}
	}

	// Text at the first candidate moves the click to the next clear one.
	s.Text = append(s.Text, Text{Value: "Macintosh HD", X: 1700, Y: 760, Width: 160, Height: 20})
	if x2, y2, ok := s.emptyDesktopPoint(); !ok || (x2 == x && y2 == y) {
		t.Errorf("point = (%d,%d) %v, want another clear point", x2, y2, ok)
	}
}

func TestDesktopFocusWithNoClearPointOnlyEscapes(t *testing.T) {
	full := Screen{
		Width:  2048,
		Height: 1536,
		Text:   []Text{{Value: "x", X: 0, Y: 0, Width: 2048, Height: 1536}},
	}
	if got := DesktopFocus(full); len(got) != 1 || got[0] != key("escape") {
		t.Errorf("focus = %+v", got)
	}
	if got := DesktopFocus(Screen{}); len(got) != 1 {
		t.Errorf("focus without a frame size = %+v", got)
	}
}

func TestSetupAssistantDoneOnlyAfterTheAccount(t *testing.T) {
	for _, stage := range []string{"account-creating", "appearance", "login", "desktop"} {
		if !SetupAssistantDone(stage) {
			t.Errorf("%s: want done", stage)
		}
	}
	for _, stage := range []string{"", "wake", "unknown", "greeting", "language", "region", "account", "privacy"} {
		if SetupAssistantDone(stage) {
			t.Errorf("%s: want not done", stage)
		}
	}
}
