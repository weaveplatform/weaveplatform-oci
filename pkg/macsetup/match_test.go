package macsetup

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// The macOS 27 Analytics page whose Continue button OCR read only as "nue": the
// plan clicks the middle of the whole button rather than waiting for a timeout.
func TestAnalyticsContinueReadAsAFragment(t *testing.T) {
	s := Screen{Width: 2048, Height: 1536, Text: []Text{
		{Value: "Analytics", X: 930, Y: 300, Width: 190, Height: 40},
		{
			Value:  "Help Apple improve its products and services",
			X:      700,
			Y:      400,
			Width:  650,
			Height: 24,
		},
		{Value: "nue", X: 1800, Y: 1400, Width: 45, Height: 22},
	}}
	p, err := Next(s, DefaultConfig())
	if err != nil || p.Stage != "analytics" || len(p.Actions) != 1 {
		t.Fatalf("plan = %+v, %v", p, err)
	}
	// "nue" is 3 of "continue"'s 8 letters: the button starts 5 letters (75px) to
	// its left, and is 120px wide.
	if a := p.Actions[0]; a.Kind != "click" || a.X != 1725+60 || a.Y != 1411 {
		t.Fatalf("click = %+v", a)
	}
}

func TestTolerantMatch(t *testing.T) {
	box := func(v string) Text { return Text{Value: v, X: 100, Y: 10, Width: 10 * len([]rune(v)), Height: 20} }
	for _, tc := range []struct {
		value, label string
		want         bool
		x, width     int
	}{
		{"nue", "Continue", true, 50, 80},       // clipped start
		{"Contin", "Continue", true, 100, 80},   // clipped end
		{"Contlnue", "Continue", true, 100, 80}, // one misread letter
		{"continue ›", "Continue", true, 100, 100},
		{"Continue", "Continue", true, 100, 80},
		{"Set Up Laler", "Set Up Later", true, 100, 120},
		{"ue", "Continue", false, 0, 0},   // too little of it
		{"tin", "Continue", false, 0, 0},  // the middle of the label
		{"Use", "Don't Use", false, 0, 0}, // a whole word: another control
		{"Not", "Not Now", false, 0, 0},   // a whole word
		{"Agre", "Agree", false, 0, 0},    // short labels are exact only
		{"Disagree", "Agree", false, 0, 0},
		{"Cancel", "Continue", false, 0, 0},
		{"Contrast", "Continue", false, 0, 0},
		{"", "Continue", false, 0, 0},
		{"Continue to the next page", "Continue", false, 0, 0},
	} {
		got, ok := tolerantMatch(box(tc.value), tc.label)
		if ok != tc.want {
			t.Errorf("tolerantMatch(%q, %q) = %v", tc.value, tc.label, ok)
			continue
		}
		if ok && (got.X != tc.x || got.Width != tc.width) {
			t.Errorf("tolerantMatch(%q, %q) box = x %d width %d, want %d %d",
				tc.value, tc.label, got.X, got.Width, tc.x, tc.width)
		}
	}
	// A fragment at the screen's left edge is not moved off it.
	if got, ok := tolerantMatch(
		Text{Value: "nue", X: 10, Width: 30},
		"Continue",
	); !ok ||
		got.X != 0 {
		t.Fatalf("edge fragment = %+v %v", got, ok)
	}
}

// An exact match wins over tolerant ones, and two tolerant matches are as
// ambiguous as two exact ones.
func TestLocatePrefersExactAndRefusesAmbiguity(t *testing.T) {
	s := screen("Contin", "Continue")
	if m := s.locate("Continue"); len(m) != 1 || m[0].Value != "Continue" {
		t.Fatalf("locate = %+v", m)
	}
	if _, err := click(screen("Contin", "nue"), "Continue"); err == nil {
		t.Fatal("clicked one of two partial matches")
	}
	// Pages are still recognised by exact text only.
	if screen("Analytic").has("Analytics") {
		t.Fatal("a misread title recognised a page")
	}
}

// The account page's fields are found from its Full Name label even when OCR
// clipped it.
func TestAccountPageWithAClippedFullNameLabel(t *testing.T) {
	s := screen("Create a Mac Account", "ull Name", "Continue")
	p, err := Next(s, DefaultConfig())
	if err != nil || p.Stage != "account" || len(p.Actions) != 13 {
		t.Fatalf("plan = %+v, %v", p, err)
	}
}

func TestGreetingFrames(t *testing.T) {
	button := Text{Value: "Continue", X: 970, Y: 1290, Width: 108, Height: 21}
	for _, tc := range []struct {
		name string
		text []Text
		want string
	}{
		{"hello", []Text{{Value: "hello"}}, "greeting"},
		{"misread hello", []Text{{Value: "hellc"}}, "greeting"},
		{"another language", []Text{{Value: "Bonjour"}, {Value: "~"}}, "greeting"},
		{"hello beside other text", []Text{{Value: "hello"}, {Value: "Sign in with your Apple Account"}}, "greeting"},
		{"button with handwriting glyphs", []Text{button, {Value: "he"}, {Value: "hola"}}, "greeting"},
		{"clipped button", []Text{{Value: "ontinue", X: 985, Y: 1290, Width: 93, Height: 21}}, "greeting"},
		{"button with a page", []Text{button, {Value: "Unexpected page"}}, "unknown"},
		{"greeting word among page text", []Text{{Value: "Hola"}, {Value: "Unexpected page"}}, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Next(Screen{Width: 2048, Height: 1536, Text: tc.text}, DefaultConfig())
			if err != nil || p.Stage != tc.want {
				t.Fatalf("plan=%+v err=%v", p, err)
			}
		})
	}
	if greetingWord("") || greetingWord("Continue") {
		t.Fatal("greetingWord accepted a non-greeting")
	}
}

func loadScreen(t *testing.T, name string) Screen {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var s Screen
	if err = json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// The recorded macOS 27 login window has the lock screen's clock and date.
func TestLockScreen(t *testing.T) {
	c := DefaultConfig()
	c.User = "labrunner"
	recorded := loadScreen(t, "macos27-login.json")
	if !recorded.lockScreen() {
		t.Fatal("the recorded lock screen was not recognised")
	}
	// With the field shown but the account unverified, nothing is typed: the
	// screen stays unrecognised for the high-contrast identity pass.
	if p, err := Next(recorded, c); err != nil || p.Stage != "unknown" || len(p.Actions) != 0 {
		t.Fatalf("unverified lock screen = %+v %v", p, err)
	}

	clock := Text{Value: "11:42", X: 788, Y: 210, Width: 468, Height: 156}
	date := Text{Value: "Tue Sep 29", X: 917, Y: 147, Width: 214, Height: 41}
	user := Text{Value: c.User, X: 957, Y: 1286, Width: 136, Height: 27}
	for _, tc := range []struct {
		name   string
		text   []Text
		stage  string
		action string
	}{
		{"clock only", []Text{date, clock}, "lock-screen", "key"},
		{"account tile", []Text{date, clock, user}, "lock-screen-user", "click"},
		{"another account", []Text{date, clock, {Value: "someone", X: 957, Y: 1286, Width: 136, Height: 27}}, "lock-screen", "key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Next(Screen{Width: 2048, Height: 1536, Text: tc.text}, c)
			if err != nil || p.Stage != tc.stage || len(p.Actions) != 1 ||
				p.Actions[0].Kind != tc.action {
				t.Fatalf("plan = %+v %v", p, err)
			}
			for _, a := range p.Actions {
				if a.Secret {
					t.Fatal("typed a secret on the lock screen")
				}
			}
		})
	}
	// Verified account with its password field: the login stage types it.
	p, err := Next(Screen{Width: 2048, Height: 1536, Text: []Text{
		date, clock, user,
		{Value: "Enter Password", X: 909, Y: 1366, Width: 174, Height: 20},
	}}, c)
	if err != nil || p.Stage != "login" || len(p.Actions) != 4 || !p.Actions[2].Secret {
		t.Fatalf("verified login = %+v %v", p, err)
	}

	for _, tc := range []struct {
		name string
		text []Text
	}{
		{"menu bar clock", []Text{{Value: "Tue 6 Oct", X: 1700, Y: 4, Width: 90, Height: 18}, {Value: "09:41", X: 1800, Y: 4, Width: 50, Height: 18}}},
		{"no date", []Text{clock}},
		{"clock off centre", []Text{date, {Value: "11:42", X: 0, Y: 210, Width: 468, Height: 156}}},
		{"clock low down", []Text{date, {Value: "11:42", X: 788, Y: 1000, Width: 468, Height: 156}}},
		{"not a time", []Text{date, {Value: "1142", X: 788, Y: 210, Width: 468, Height: 156}}},
	} {
		if (Screen{Width: 2048, Height: 1536, Text: tc.text}).lockScreen() {
			t.Errorf("%s taken for the lock screen", tc.name)
		}
	}
	if (Screen{Text: []Text{date, clock}}).lockScreen() {
		t.Fatal("accepted an invalid framebuffer")
	}
	if weekdayLine("") || !weekdayLine("Tuesday, 6 October") || weekdayLine("Tues") {
		t.Fatal("weekdayLine")
	}
	if !SetupAssistantDone("lock-screen") {
		t.Fatal("the lock screen is only seen once the account exists")
	}
}

func TestNudgeGreeting(t *testing.T) {
	blank := Screen{Width: 2048, Height: 1536}
	glyphs := screen("~", "he")
	page := screen("Something new", "Continue")
	wake := Plan{Stage: "wake", Actions: []Action{key("shift")}}
	unknown := Plan{Stage: "unknown"}
	for _, tc := range []struct {
		name      string
		s         Screen
		p         Plan
		setupDone bool
		after     time.Duration
		space     bool
	}{
		{"blank greeting", blank, wake, false, GreetingNudgeAfter, true},
		{"unreadable greeting", glyphs, unknown, false, time.Minute, true},
		{"too soon", blank, wake, false, GreetingNudgeAfter - time.Second, false},
		{"after setup", blank, wake, true, time.Minute, false},
		{"unknown setup page", page, unknown, false, time.Minute, false},
		{"clipped control", screen("Something new", "ontinue"), unknown, false, time.Minute, false},
		{"recognised page", page, Plan{Stage: "analytics"}, false, time.Minute, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NudgeGreeting(tc.s, tc.p, tc.setupDone, tc.after)
			space := len(got.Actions) == 1 && got.Actions[0] == key("space")
			if space != tc.space || got.Stage != tc.p.Stage {
				t.Fatalf("NudgeGreeting = %+v", got)
			}
		})
	}
}

func TestRetryAfterAndStageTimeouts(t *testing.T) {
	if RetryAfter("greeting") >= RetryAfter("analytics") ||
		RetryAfter("lock-screen") != 10*time.Second {
		t.Fatal("RetryAfter")
	}
	msg := StageTimeout("greeting", 5*time.Minute)
	if !strings.Contains(msg, "greeting") || !strings.Contains(msg, "5m0s") ||
		!strings.Contains(msg, "Language page") {
		t.Fatalf("greeting timeout = %v", msg)
	}
	if msg = StageTimeout(
		"analytics",
		time.Minute,
	); !strings.Contains(
		msg,
		"the analytics page to advance",
	) {
		t.Fatalf("analytics timeout = %v", msg)
	}
}
