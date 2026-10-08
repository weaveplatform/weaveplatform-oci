package macsetup

import "testing"

func TestBootstrapRequiresTerminalRatherThanAWebSearch(t *testing.T) {
	for _, tc := range []struct {
		labels  []string
		stage   string
		actions int
	}{
		{[]string{"Safari", "Help"}, "desktop", 0},
		{[]string{"Safari", "Terminal.app", "Search the Web"}, "terminal-search", 0},
		{[]string{"Terminal.app", "Terminal", "Applications"}, "terminal-launch", 2},
		{[]string{"Q Terminal.app - Terminal", "Terminal", "Applications"}, "terminal-launch", 2},
		{[]string{"Q Terminal.app- Terminal", "Terminal", "Applications"}, "terminal-launch", 2},
		{[]string{"Terminal.app — Terminal", "Terminal", "Applications"}, "terminal-launch", 2},
		{[]string{"Terminal.app – Terminal", "Terminal", "Applications"}, "terminal-launch", 2},
		{[]string{"Q Terminal.app - Terminal", "Applications", "Search The Web"}, "terminal-search", 0},
		{[]string{"Q Terminal.app - Terminal", "Terminal", "Safari", "Help"}, "desktop", 0},

		{[]string{"Terminal", "Shell", "Help"}, "remote-login-open", 3},
		{[]string{"Sharing", "Remote Login: Off", "Done"}, "remote-login", 1},
		{[]string{"Sharing", "Remote Login: On", "Done"}, "remote-login-enabled", 1},
	} {
		p, e := Next(screen(tc.labels...), DefaultConfig())
		if e != nil || p.Stage != tc.stage || len(p.Actions) != tc.actions {
			t.Fatalf("%v: %+v %v", tc.labels, p, e)
		}
	}
}

func TestCombinedTerminalMenu(t *testing.T) {
	s := screen("Terminal Shell Edit View", "Window", "Help")
	s.Text[0].Y = 15
	p, e := Next(s, DefaultConfig())
	if e != nil || p.Stage != "remote-login-open" {
		t.Fatalf("%+v %v", p, e)
	}
	s.Text[0].Y = 400
	p, e = Next(s, DefaultConfig())
	if e != nil || p.Stage != "unknown" {
		t.Fatal("web content treated as Terminal menu")
	}
}

func TestRemoteLoginUsesObservedSheetAlignment(t *testing.T) {
	s := screen("Sharing", "Remote Login: Off", "Done")
	s.Text[1] = Text{Value: "Remote Login: Off", X: 692, Y: 400, Width: 216, Height: 24}
	s.Text[2] = Text{Value: "Done", X: 1360, Y: 1063, Width: 61, Height: 19}
	p, e := Next(s, DefaultConfig())
	if e != nil || len(p.Actions) != 1 || p.Actions[0].X != 1390 || p.Actions[0].Y != 412 {
		t.Fatalf("%+v %v", p, e)
	}
	s.Text = s.Text[:2]
	p, e = Next(s, DefaultConfig())
	if e != nil || len(p.Actions) != 0 {
		t.Fatal("clicked a toggle without its sheet bounds")
	}
}

func TestBlankFrameWakesWithoutTyping(t *testing.T) {
	p, e := Next(screen(), DefaultConfig())
	if e != nil || p.Stage != "wake" || len(p.Actions) != 1 || p.Actions[0].Value != "shift" {
		t.Fatalf("%+v %v", p, e)
	}
}

func TestLowContrastLoginRequiresConfiguredUser(t *testing.T) {
	s := screen("weave", "nter Password", "Your password is required to", "log in")
	p, e := Next(s, DefaultConfig())
	if e != nil || p.Stage != "login" {
		t.Fatalf("%+v %v", p, e)
	}
	s.Text[0].Value = "another-user"
	p, e = Next(s, DefaultConfig())
	if e != nil || len(p.Actions) != 0 {
		t.Fatal("typed into another user's session")
	}
}
