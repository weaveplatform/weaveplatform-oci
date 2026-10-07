package macsetup

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func screen(labels ...string) Screen {
	s := Screen{Width: 2048, Height: 1536}
	for i, label := range labels {
		s.Text = append(s.Text, Text{Value: label, X: 100, Y: 100 + i*50, Width: 200, Height: 30})
	}
	return s
}

func TestRecognizeEverySupportedPage(t *testing.T) {
	c := DefaultConfig()
	c.Password = "private!123"
	cases := []struct {
		stage   string
		labels  []string
		actions int
	}{
		{
			"apple-account-confirmation",
			[]string{
				"Are you sure you want to skip signing in with an Apple Account?",
				"Skip",
				"Don't Skip",
			},
			1,
		},
		{
			"apple-account-confirmation",
			[]string{
				"Are you sure you want to skip",
				"signing in with an Apple",
				"Account?",
				"Skip",
				"Don't Skip",
			},
			1,
		},
		{
			"terms-confirmation",
			[]string{"I have read and agree to the license", "Agree", "Agree"},
			1,
		},
		{"terms-confirmation", []string{"I have read and agree"}, 0},
		{"location-confirmation", []string{"Location Services", "Don't Use"}, 1},
		{
			"filevault-confirmation",
			[]string{"Mac Data Will Not Be Securely Encrypted", "Continue"},
			1,
		},
		{"remote-login-authorization", []string{"Sharing", "Modify Settings"}, 3},
		{"welcome", []string{"Get Started"}, 1},
		{"welcome", []string{"Welcome to Mac"}, 1},
		{"apple-account-later", []string{"Sign in Later in Settings"}, 1},
		{"apple-account", []string{"Apple Account", "Other Sign-In Options"}, 1},
		{"apple-account", []string{"Apple Account", "Other Sign-In Options v"}, 1},
		{"apple-account", []string{"Sign In to your Apple ID", "Set Up Later"}, 1},
		{"filevault", []string{"Your Mac is Ready for FileVault", "Not Now"}, 1},
		{"updates", []string{"Update Mac Automatically", "Only Download Automatically"}, 1},
		{"updates-loading", []string{"Update Mac Automatically"}, 0},
		{"age-range", []string{"Age Range", "Adult", "Continue"}, 1},
		{"appearance", []string{"Choose Your Look", "Continue"}, 1},
		{"terms", []string{"Terms and Conditions", "Agree", "Agree"}, 1},
		{"terms-loading", []string{"Terms and Conditions"}, 0},
		{"account", []string{"Create a Mac Account", "Full Name", "Continue"}, 13},
		{"account", []string{"Create a Mac Account", "Full name:", "Continue"}, 13},
		{
			"account-creating",
			[]string{"Create a Mac Account", "Creating account...", "Full name:", "Continue"},
			0,
		},
		{"account", []string{"Create a Computer Account", "Full Name", "Continue"}, 13},
		{"region", []string{"Country or Region", "United States", "Continue"}, 2},
		{"region-search", []string{"Country or Region"}, 0},
		{"region-reveal", []string{"Country or Region", "Uganda", "Continue"}, 7},
		{"region-search", []string{"Country or Region", "United Kingdom"}, 2},
		{
			"region-search",
			[]string{"Country or Region", "United Kingdom", "United Kingdom", "Afghanistan"},
			2,
		},
		{"language", []string{"Language", "English"}, 2},
		{"migration", []string{"Transfer Your Data to This Mac", "Set up as new", "Continue"}, 2},
		{"migration", []string{"Transfer Your Data to This Mac", "O Set up as new", "Continue"}, 2},
		{"migration-loading", []string{"Transfer Your Data to This Mac"}, 0},
		{"languages", []string{"Written and Spoken Languages", "Continue"}, 1},
		{"accessibility", []string{"Accessibility", "Not Now"}, 1},
		{"privacy", []string{"Data & Privacy", "Continue"}, 1},
		{"location", []string{"Enable Location Services", "Continue"}, 1},
		{"timezone", []string{"Select Your Time Zone", "Continue"}, 1},
		{"analytics", []string{"Analytics", "Continue"}, 1},
		{"screen-time", []string{"Screen Time", "Set Up Later"}, 1},
		{"siri", []string{"Siri", "Set Up Later"}, 1},
		{"siri", []string{"Siri"}, 0},
		{"login", []string{"Enter Password", "weave"}, 4},
		{"desktop", []string{"Finder"}, 0},
		{"greeting", []string{"hello"}, 1},
		{"unknown", []string{"Unexpected screen", "Continue"}, 0},
		{"unknown", []string{"Accessibility", "Continue"}, 0},
		{"unknown", []string{"Liquid Glass", "Continue"}, 0},
		{"unknown", []string{"System Settings", "Accessibility", "Not Now"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.stage+strings.Join(tc.labels, "-"), func(t *testing.T) {
			plan, err := Next(screen(tc.labels...), c)
			if err != nil || plan.Stage != tc.stage || len(plan.Actions) != tc.actions {
				t.Fatalf("plan=%+v err=%v", plan, err)
			}
			for _, a := range plan.Actions {
				if strings.Contains(a.Value, c.Password) && !a.Secret {
					t.Fatal("credential action is not marked secret")
				}
			}
		})
	}
}

func TestAmbiguousOrMissingControlsNeverAuthorizeInput(t *testing.T) {
	for _, labels := range [][]string{{"Get Started", "Get Started"}, {"Age Range"}, {"Create a Mac Account"}, {"Country or Region", "United States", "United States", "Continue"}, {"Transfer Your Data", "Set up as new", "Set up as new", "Continue"}} {
		_, err := Next(screen(labels...), DefaultConfig())
		if err == nil {
			t.Fatalf("accepted ambiguous or incomplete controls: %v", labels)
		}
	}
}

func TestObservedResumeSequenceDoesNotDependOnPriorSteps(t *testing.T) {
	c := DefaultConfig()
	c.Password = "private!123"
	pages := [][]string{
		{"Update Mac Automatically", "Only Download Automatically"},
		{"Sign In to Your Apple Account", "Other Sign-In Options"},
		{"Sign in Later in Settings"},
		{"Are you sure you want to skip signing in", "Skip"},
		{"Your Mac is Ready for FileVault", "Not Now"},
		{"Mac Data Will Not Be Securely Encrypted", "Continue"},
		{"Get Started"},
		{"Finder"},
	}
	for start := range pages {
		for _, page := range pages[start:] {
			p, e := Next(screen(page...), c)
			if e != nil || p.Stage == "unknown" {
				t.Fatalf("cannot resume at %v: %v", page, e)
			}
		}
	}
}

func TestTermsModalUsesTopmostAgree(t *testing.T) {
	s := screen("I have read and agree", "Agree", "Agree")
	s.Text[1].Y = 900
	s.Text[2].Y = 200
	p, e := Next(s, DefaultConfig())
	if e != nil || p.Actions[0].Y != 215 {
		t.Fatalf("selected background button: %+v %v", p, e)
	}
}

func TestCheckpointDoesNotContainAccountSecret(t *testing.T) {
	raw, err := json.Marshal(State{Status: "running", Stage: "account"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "password") {
		t.Fatal("state exposes credential fields")
	}
}

func TestRecordedCountryListSelectsVisibleUnitedStates(t *testing.T) {
	raw, err := os.ReadFile("testdata/sequoia-country.json")
	if err != nil {
		t.Fatal(err)
	}
	var observed Screen
	if err = json.Unmarshal(raw, &observed); err != nil {
		t.Fatal(err)
	}
	plan, err := Next(observed, DefaultConfig())
	if err != nil || plan.Stage != "region" || len(plan.Actions) != 2 ||
		plan.Actions[0].Kind != "click" ||
		plan.Actions[1].Kind != "click" {
		t.Fatalf("%+v %v", plan, err)
	}
}

func TestRecordedSetupPages(t *testing.T) {
	for file, want := range map[string]string{"macos27-spotlight.json": "terminal-launch", "macos27-liquid-glass.json": "liquid-glass", "tahoe-filevault-confirmation.json": "filevault-confirmation", "tahoe-restart-notice.json": "restart-notice", "tahoe-spotlight.json": "terminal-launch", "tahoe-apple-account-confirmation.json": "apple-account-confirmation", "tahoe-apple-account-menu.json": "apple-account-later", "tahoe-country-search.json": "region-search", "sequoia-migration.json": "migration", "sequoia-apple-account-confirmation.json": "apple-account-confirmation"} {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/" + file)
			if err != nil {
				t.Fatal(err)
			}
			var observed Screen
			if err = json.Unmarshal(raw, &observed); err != nil {
				t.Fatal(err)
			}
			plan, err := Next(observed, DefaultConfig())
			if err != nil || plan.Stage != want || len(plan.Actions) == 0 {
				t.Fatalf("%+v %v", plan, err)
			}
		})
	}
}

func TestAnimatedGreetingRequiresIsolatedAnchoredContinue(t *testing.T) {
	for _, tc := range []struct {
		name string
		text []Text
		want string
	}{
		{"english frame", []Text{{Value: "Continue", X: 970, Y: 1290, Width: 108, Height: 21}}, "greeting"},
		{"other page", []Text{{Value: "Continue", X: 970, Y: 1290, Width: 108, Height: 21}, {Value: "Unexpected page"}}, "unknown"},
		{"wrong position", []Text{{Value: "Continue", X: 100, Y: 100, Width: 108, Height: 21}}, "unknown"},
		{"unrecognized language", []Text{{Value: "Surdur", X: 984, Y: 1290, Width: 80, Height: 21}}, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Next(Screen{Width: 2048, Height: 1536, Text: tc.text}, DefaultConfig())
			if err != nil || p.Stage != tc.want {
				t.Fatalf("plan=%+v err=%v", p, err)
			}
		})
	}
	if (Screen{Text: []Text{{Value: "Continue"}}}).greetingButton() {
		t.Fatal("accepted invalid framebuffer")
	}
}

func TestCountryListRecoversAtArbitraryScrollPosition(t *testing.T) {
	s := Screen{Width: 2048, Height: 1536, Text: []Text{
		{Value: "Select Your Country or Region", X: 575, Y: 520, Width: 485, Height: 35},
		{Value: "Ecuador", X: 593, Y: 604, Width: 110, Height: 26},
		{Value: "Continue", X: 1603, Y: 1282, Width: 107, Height: 21},
	}}
	p, err := Next(s, DefaultConfig())
	if err != nil || p.Stage != "region-search" || len(p.Actions) != 2 ||
		p.Actions[1].Value != "u" ||
		p.Actions[1].Kind != "type" {
		t.Fatalf("plan=%+v err=%v", p, err)
	}
	s.Text = append(s.Text, Text{Value: "United States", X: 593, Y: 765, Width: 170, Height: 25})
	p, err = Next(s, DefaultConfig())
	if err != nil || p.Stage != "region" || len(p.Actions) != 2 {
		t.Fatalf("plan=%+v err=%v", p, err)
	}
}

func TestMacOS27SiriArtworkPage(t *testing.T) {
	data, err := os.ReadFile("testdata/macos27-siri.json")
	if err != nil {
		t.Fatal(err)
	}
	var captured Screen
	if err := json.Unmarshal(data, &captured); err != nil {
		t.Fatal(err)
	}
	plan, err := Next(captured, DefaultConfig())
	if err != nil || plan.Stage != "siri" || len(plan.Actions) != 1 {
		t.Fatalf("captured Siri page: %+v %v", plan, err)
	}
	expected, err := click(captured, "Set Up Later")
	if err != nil || plan.Actions[0] != expected {
		t.Fatalf("must select the captured skip control: %+v %v", plan, err)
	}
	for _, missing := range []string{"Find information", "About Siri", "Set Up Later", "Continue"} {
		t.Run(missing, func(t *testing.T) {
			altered := captured
			altered.Text = nil
			for _, label := range captured.Text {
				if !strings.Contains(label.Value, missing) {
					altered.Text = append(altered.Text, label)
				}
			}
			plan, err := Next(altered, DefaultConfig())
			if err != nil || plan.Stage != "unknown" || len(plan.Actions) != 0 {
				t.Fatalf("incomplete evidence must not act: %+v %v", plan, err)
			}
		})
	}
}

func TestLoginUsesExactIdentityFromSameFrameContrast(t *testing.T) {
	data, err := os.ReadFile("testdata/macos27-login.json")
	if err != nil {
		t.Fatal(err)
	}
	var original Screen
	if err := json.Unmarshal(data, &original); err != nil {
		t.Fatal(err)
	}
	c := DefaultConfig()
	c.User = "labrunner"
	contrast := Screen{
		Width:  original.Width,
		Height: original.Height,
		Text:   []Text{{Value: c.User, X: 957, Y: 1289, Width: 136, Height: 23}},
	}
	combined := original.WithLoginIdentity(contrast, c.User)
	plan, err := Next(combined, c)
	if err != nil || plan.Stage != "login" || len(plan.Actions) != 4 {
		t.Fatalf("login recovery: %+v %v", plan, err)
	}
	control, err := click(original, "Enter Password")
	if err != nil || plan.Actions[0] != control {
		t.Fatalf("password control changed: %+v %v", plan, err)
	}
	if original.has(c.User) {
		t.Fatal("original observation mutated")
	}
	for _, test := range []struct {
		name      string
		alternate Screen
	}{
		{"wrong account", screen("another-user")},
		{"fuzzy account", screen("labrunriEr")},
		{"missing account", screen("Continue")},
		{"ambiguous account", screen(c.User, c.User)},
		{"different frame size", Screen{Width: 1, Height: 1, Text: contrast.Text}},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, err := Next(original.WithLoginIdentity(test.alternate, c.User), c)
			if err != nil || plan.Stage != "unknown" || len(plan.Actions) != 0 {
				t.Fatalf("unverified account accepted: %+v %v", plan, err)
			}
		})
	}
	missingControl := screen("labrunriEr")
	if missingControl.WithLoginIdentity(contrast, c.User).has(c.User) {
		t.Fatal("identity added without a password control")
	}
	known := screen(c.User, "Enter Password")
	if got := known.WithLoginIdentity(contrast, c.User); len(got.Text) != len(known.Text) {
		t.Fatal("existing identity duplicated")
	}
}
