package macsetup

import (
	"fmt"
	"strings"
)

// Text is one OCR line in guest framebuffer coordinates.
type Text struct {
	Value               string
	X, Y, Width, Height int
}

// Screen is a fresh, read-only observation of the guest.
type Screen struct {
	Width, Height int
	Text          []Text
	Checks        map[string]bool `json:"checks,omitempty"`
}

// Action is guest input. Secret text must never enter logs or checkpoints.
type Action struct {
	Kind, Value string
	X, Y        int
	Secret      bool
}

// Plan identifies the current page and the bounded actions that advance it.
type Plan struct {
	Stage   string
	Actions []Action
}

func (s Screen) exact(label string) []Text {
	var out []Text
	for _, t := range s.Text {
		candidate := strings.ReplaceAll(strings.TrimSpace(t.Value), "’", "'")
		if label == "Other Sign-In Options" {
			candidate = strings.TrimSpace(strings.TrimSuffix(candidate, " v"))
			candidate = strings.TrimSpace(strings.TrimSuffix(candidate, "⌄"))
		}
		if label == "Sign in Later in Settings" || label == "Terminal.app" {
			// Vision reads Tahoe gear and search icons as a separate Q.
			candidate = strings.TrimPrefix(candidate, "Q ")
		}
		if label == "Enter Password" && candidate == "nter Password" {
			candidate = "Enter Password"
		}
		if label == "Full Name" {
			candidate = strings.TrimSuffix(candidate, ":")
		}
		if label == "Set up as new" {
			candidate = strings.TrimPrefix(strings.TrimPrefix(candidate, "O "), "o ")
		}
		if strings.EqualFold(candidate, label) {
			out = append(out, t)
		}
	}
	return out
}
func (s Screen) has(label string) bool { return len(s.exact(label)) > 0 }

// WithLoginIdentity recovers an exact username from a second OCR pass over the
// same frame. Keep the original password control: contrast can erase its text.
func (s Screen) WithLoginIdentity(contrast Screen, user string) Screen {
	if s.Width != contrast.Width || s.Height != contrast.Height ||
		!s.has("Enter Password") || s.has(user) {
		return s
	}
	identity := contrast.exact(user)
	if len(identity) != 1 {
		return s
	}
	s.Text = append(append([]Text(nil), s.Text...), identity[0])
	return s
}

func (s Screen) contains(label string) bool {
	lines := make([]string, 0, len(s.Text))
	for _, t := range s.Text {
		lines = append(lines, strings.ToLower(t.Value))
	}
	return strings.Contains(strings.Join(lines, " "), strings.ToLower(label))
}

func key(v string) Action                { return Action{Kind: "key", Value: v} }
func typed(v string, secret bool) Action { return Action{Kind: "type", Value: v, Secret: secret} }
func click(s Screen, label string) (Action, error) {
	m := s.locate(label)
	if len(m) != 1 {
		return Action{}, fmt.Errorf(
			"%w: expected one %q control, found %d",
			ErrOnboarding,
			label,
			len(m),
		)
	}
	t := m[0]
	return Action{Kind: "click", X: t.X + t.Width/2, Y: t.Y + t.Height/2}, nil
}

func clickPlan(s Screen, stage, label string) (Plan, error) {
	a, err := click(s, label)
	return Plan{Stage: stage, Actions: []Action{a}}, err
}

// Next recognizes the observed page, not a caller-selected resume offset. More
// specific modal confirmations take precedence over their background pages.
func Next(s Screen, c Config) (Plan, error) {
	if len(s.Text) == 0 {
		return Plan{Stage: "wake", Actions: []Action{key("shift")}}, nil
	}
	for _, recognize := range []func(Screen, Config) (Plan, error){confirmationPlan, identityPlan, initialPlan, bootstrapPlan, preferencesPlan} {
		plan, err := recognize(s, c)
		if err != nil || plan.Stage != "" {
			return plan, err
		}
	}
	return Plan{Stage: "unknown"}, nil
}

func confirmationPlan(s Screen, c Config) (Plan, error) {
	switch {
	case s.contains("Your computer was restarted") && s.contains("because of a problem"):
		return clickPlan(s, "restart-notice", "Ignore")
	case s.contains("Are you sure you want to skip") && s.contains("signing in"):
		return clickPlan(s, "apple-account-confirmation", "Skip")
	case s.contains("I have read and agree"):
		// OCR includes the disabled background Agree; the modal is above it.
		matches := s.exact("Agree")
		if len(matches) == 0 {
			return Plan{Stage: "terms-confirmation"}, nil
		}
		t := matches[0]
		for _, m := range matches {
			if m.Y < t.Y {
				t = m
			}
		}
		return Plan{
			Stage:   "terms-confirmation",
			Actions: []Action{{Kind: "click", X: t.X + t.Width/2, Y: t.Y + t.Height/2}},
		}, nil
	case s.has("Don't Use") && s.contains("Location Services"):
		return clickPlan(s, "location-confirmation", "Don't Use")
	case s.contains("Mac Data Will Not Be") && s.contains("Securely Encrypted"):
		return clickPlan(s, "filevault-confirmation", "Continue")
	case s.has("Modify Settings") && s.has("Sharing"):
		a, err := click(s, "Modify Settings")
		return Plan{
			Stage:   "remote-login-authorization",
			Actions: []Action{key("cmd+a"), typed(c.Password, true), a},
		}, err
	case s.has("Get Started"):
		return clickPlan(s, "welcome", "Get Started")
	case s.has("Welcome to Mac"):
		return Plan{Stage: "welcome", Actions: []Action{key("space")}}, nil
	default:
		return Plan{}, nil
	}
}

func identityPlan(s Screen, c Config) (Plan, error) {
	switch {
	case s.has("Sign in Later in Settings"):
		return clickPlan(s, "apple-account-later", "Sign in Later in Settings")
	case s.has("Other Sign-In Options") && s.contains("Apple Account"):
		return clickPlan(s, "apple-account", "Other Sign-In Options")
	case s.contains("Sign In") && (s.contains("Apple Account") || s.contains("Apple ID")) && s.has("Set Up Later"):
		return clickPlan(s, "apple-account", "Set Up Later")
	case s.has("Your Mac is Ready for FileVault"):
		return clickPlan(s, "filevault", "Not Now")
	case s.has("Update Mac Automatically"):
		if s.has("Only Download Automatically") {
			return clickPlan(s, "updates", "Only Download Automatically")
		}
		return Plan{Stage: "updates-loading"}, nil
	case s.has("Age Range"):
		// Newer builds advance immediately when Adult is selected.
		return clickPlan(s, "age-range", "Adult")
	case s.has("Choose Your Look"):
		return clickPlan(s, "appearance", "Continue")
	case s.has("Terms and Conditions"):
		matches := s.exact("Agree")
		if len(matches) == 0 {
			return Plan{Stage: "terms-loading"}, nil
		}
		t := matches[len(matches)-1]
		return Plan{
			Stage:   "terms",
			Actions: []Action{{Kind: "click", X: t.X + t.Width/2, Y: t.Y + t.Height/2}},
		}, nil
	default:
		return Plan{}, nil
	}
}

func initialPlan(s Screen, c Config) (Plan, error) {
	switch {
	case s.contains("Creating account"):
		return Plan{Stage: "account-creating"}, nil
	case s.has("Create a Mac Account") || s.has("Create a Computer Account"):
		a, err := click(s, "Full Name")
		if err != nil {
			return Plan{Stage: "account"}, err
		}
		// Target the field to the right of its persistent label, including
		// retries where a different field currently owns keyboard focus.
		label := s.locate("Full Name")[0]
		a.X = label.X + label.Width + 2*label.Height
		// Replace fields individually so retrying a partially filled page is safe.
		b, err := click(s, "Continue")
		actions := []Action{
			a,
			key("cmd+a"),
			typed(c.User, false),
			key("tab"),
			key("cmd+a"),
			typed(c.User, false),
			key("tab"),
			key("cmd+a"),
			typed(c.Password, true),
			key("tab"),
			key("cmd+a"),
			typed(c.Password, true),
			b,
		}
		return Plan{Stage: "account", Actions: actions}, err
	case s.has("Country or Region") || s.contains("Select Your Country"):
		return regionPlan(s)
	case s.has("Language") && s.has("English") && !s.has("System Settings"):
		a, e := click(s, "English")
		return Plan{Stage: "language", Actions: []Action{a, key("enter")}}, e
	case s.contains("Transfer Your Data"):
		if s.has("Set up as new") {
			a, e := click(s, "Set up as new")
			b, f := click(s, "Continue")
			if e != nil {
				return Plan{}, e
			}
			return Plan{Stage: "migration", Actions: []Action{a, b}}, f
		}
		return Plan{Stage: "migration-loading"}, nil
	case s.has("Written and Spoken Languages"):
		return clickPlan(s, "languages", "Continue")
	case s.has("Liquid Glass") && s.contains("Slide from clear to tinted") && s.has("Continue"):
		return clickPlan(s, "liquid-glass", "Continue")
	case s.has("Accessibility") && s.has("Not Now") && !s.has("System Settings"):
		return clickPlan(s, "accessibility", "Not Now")
	case s.has("Data & Privacy"):
		return clickPlan(s, "privacy", "Continue")
	default:
		return Plan{}, nil
	}
}

func preferencesPlan(s Screen, c Config) (Plan, error) {
	switch {
	case s.has("Enable Location Services"):
		return clickPlan(s, "location", "Continue")
	case s.has("Select Your Time Zone"):
		return clickPlan(s, "timezone", "Continue")
	case s.has("Analytics") && !s.has("System Settings"):
		return clickPlan(s, "analytics", "Continue")
	case s.has("Screen Time") && !s.has("System Settings"):
		return clickPlan(s, "screen-time", "Set Up Later")
	case s.siriSetup() && !s.has("System Settings"):
		if checked, known := s.Checks["Enable Ask Siri"]; known {
			if checked {
				return clickPlan(s, "siri-disable", "Enable Ask Siri")
			}
			return clickPlan(s, "siri-disabled", "Continue")
		}
		if s.has("Set Up Later") {
			return clickPlan(s, "siri", "Set Up Later")
		}
		return Plan{Stage: "siri"}, nil
	case s.has("Enter Password") && s.has(c.User):
		a, e := click(s, "Enter Password")
		return Plan{
			Stage:   "login",
			Actions: []Action{a, key("cmd+a"), typed(c.Password, true), key("enter")},
		}, e
	case s.has("Finder") || (s.has("Safari") && s.has("Help")) || s.menuPrefix("System Settings"):
		return Plan{Stage: "desktop"}, nil
	case s.lockScreen():
		return lockScreenPlan(s, c)
	case s.greetingText() || s.greetingButton():
		return Plan{Stage: "greeting", Actions: []Action{key("space")}}, nil
	case s.desktopWidgets():
		return Plan{Stage: "desktop"}, nil
	default:
		return Plan{}, nil
	}
}

func (s Screen) siriSetup() bool {
	if s.has("Siri") || s.has("Enable Ask Siri") {
		return true
	}
	// macOS 27 uses artwork instead of an OCR-readable Siri heading.
	// Require its explanatory copy and both controls before choosing to skip.
	return s.contains("Find information and get things done, simply by asking.") &&
		s.contains("About Siri, Dictation & Privacy") &&
		s.has("Set Up Later") && s.has("Continue")
}

// The animated Tahoe greeting is handwritten and often has no OCR result.
// Wait for its English frame and require the sole Continue control in the
// lower centre, rather than accepting Continue on an unidentified setup page.
// Besides it, the frame may hold only greeting words and stray glyphs that OCR
// reads from the handwriting, never other text.
func (s Screen) greetingButton() bool {
	if s.Width <= 0 || s.Height <= 0 {
		return false
	}
	buttons := s.locate("Continue")
	if len(buttons) != 1 {
		return false
	}
	t := buttons[0]
	x, y := t.X+t.Width/2, t.Y+t.Height/2
	if x <= s.Width*4/10 || x >= s.Width*6/10 || y <= s.Height*8/10 || y >= s.Height*9/10 {
		return false
	}
	for _, other := range s.Text {
		if _, button := tolerantMatch(other, "Continue"); button {
			continue
		}
		if !greetingWord(other.Value) && len([]rune(strings.TrimSpace(other.Value))) > 2 {
			return false
		}
	}
	return true
}

// greetings are the words the greeting animation writes, in the languages it
// cycles through. OCR of the handwriting can drop or swap a letter.
var greetings = []string{
	"hello", "hallo", "hola", "bonjour", "ciao", "olá", "ola", "hej", "hei", "salut",
	"merhaba", "ahoj", "witaj", "aloha", "привет", "こんにちは", "你好", "안녕하세요",
}

// greetingWord reports whether v is one of the greeting's words, allowing one
// misread letter in the longer ones.
func greetingWord(v string) bool {
	n := []rune(normalizeLabel(v))
	if len(n) == 0 {
		return false
	}
	for _, g := range greetings {
		r := []rune(g)
		if string(n) == g || (len(r) >= 5 && editDistance(n, r) <= 1) {
			return true
		}
	}
	return false
}

// greetingText reports a frame of the greeting showing nothing but its words.
func (s Screen) greetingText() bool {
	if s.has("hello") {
		return true
	}
	found := false
	for _, t := range s.Text {
		switch {
		case greetingWord(t.Value):
			found = true
		case len([]rune(strings.TrimSpace(t.Value))) > 2:
			return false
		}
	}
	return found
}

// Recover a country list at any scroll position. A single U selects its U
// section without depending on macOS incremental-search timing. The next
// observation must locate United States before Continue can be selected.
func regionSearchPlan(s Screen) Plan {
	p := Plan{Stage: "region-search"}
	for _, title := range s.Text {
		if !strings.Contains(strings.ToLower(title.Value), "country or region") {
			continue
		}
		for _, row := range s.Text {
			if row.Y > title.Y+title.Height && row.Y < title.Y+s.Height/3 &&
				row.X >= title.X-s.Width/20 && row.X < title.X+s.Width/10 && row.Value != "Continue" {
				p.Actions = []Action{
					{Kind: "click", X: row.X + row.Width/2, Y: row.Y + row.Height/2},
					typed("u", false),
				}
				return p
			}
		}
	}
	return p
}

func regionPlan(s Screen) (Plan, error) {
	if s.has("United States") {
		a, e := click(s, "United States")
		b, f := click(s, "Continue")
		if e != nil {
			return Plan{}, e
		}
		return Plan{Stage: "region", Actions: []Action{a, b}}, f
	}
	if s.has("Uganda") {
		a, err := click(s, "Uganda")
		// Reveal the following rows; do not advance the page until the
		// next observation finds the exact requested country.
		return Plan{Stage: "region-reveal", Actions: []Action{
			a, key("down"), key("down"), key("down"),
			key("down"), key("down"), key("down"),
		}}, err
	}
	for _, country := range []string{"United Kingdom", "Afghanistan", "Albania", "Algeria", "Australia", "Canada"} {
		if len(s.exact(country)) == 1 {
			a, e := click(s, country)
			return Plan{
				Stage:   "region-search",
				Actions: []Action{a, typed("u", false)},
			}, e
		}
	}
	return regionSearchPlan(s), nil
}
