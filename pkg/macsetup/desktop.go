package macsetup

import "strings"

// setupControls are controls Setup Assistant and the login window show. None of
// them is on the desktop, so any one of them rules a widget-only desktop out.
var setupControls = []string{
	"Continue", "Not Now", "Set Up Later", "Back", "Skip", "Agree", "Get Started",
	"Don't Use", "Enter Password",
}

// desktopWidgets recognises the macOS 26+ desktop from its default widgets when
// the menu bar has no readable app name: the first frame after Setup Assistant
// can show only widget text (macOS 27.0.1 26A434 showed Weather's "Location
// Access Needed", a calendar and Photos). Two distinct widget cues are required,
// and no Setup Assistant or login control may be present.
func (s Screen) desktopWidgets() bool {
	for _, control := range setupControls {
		if s.has(control) {
			return false
		}
	}
	cues := 0
	for _, cue := range []bool{
		s.contains("Location Access Needed"), // Weather, before Location Services
		s.has("Photos") || s.contains("No Photos"),
		s.calendarWidget(),
	} {
		if cue {
			cues++
		}
	}
	return cues >= 2
}

var weekdays = []string{
	"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday",
}

// calendarWidget reports the Calendar widget: its weekday heading, or its
// empty-day text.
func (s Screen) calendarWidget() bool {
	for _, day := range weekdays {
		if s.has(day) {
			return true
		}
	}
	return s.contains("No events today") || s.contains("No more events today")
}

// emptyDesktopPoint is a point of the guest screen with no text near it, clear
// of the menu bar, the Dock and the left-hand widget column, where a click lands
// on the desktop itself. ok is false when every candidate has text near it.
func (s Screen) emptyDesktopPoint() (x, y int, ok bool) {
	if s.Width <= 0 || s.Height <= 0 {
		return 0, 0, false
	}
	margin := s.Width / 16
	for _, c := range [][2]int{{85, 50}, {85, 70}, {70, 60}, {55, 60}} {
		px, py := s.Width*c[0]/100, s.Height*c[1]/100
		clear := true
		for _, t := range s.Text {
			if px > t.X-margin && px < t.X+t.Width+margin &&
				py > t.Y-margin && py < t.Y+t.Height+margin {
				clear = false
				break
			}
		}
		if clear {
			return px, py, true
		}
	}
	return 0, 0, false
}

// DesktopFocus is the input that gives the guest's desktop keyboard focus before
// Spotlight is used: the first desktop after Setup Assistant may lack it. It
// clicks the observed app menu (then dismisses that menu) when one is readable;
// otherwise it dismisses whatever is open with Escape and clicks an empty part of
// the desktop, which activates Finder.
func DesktopFocus(s Screen) []Action {
	for _, t := range s.Text {
		v := strings.TrimSpace(t.Value)
		if t.Y < s.Height/20 && (v == "Finder" || v == "Safari" || v == "System Settings") {
			return []Action{
				{Kind: "click", X: t.X + t.Width/2, Y: t.Y + t.Height/2},
				key("escape"),
			}
		}
	}
	actions := []Action{key("escape")}
	if x, y, ok := s.emptyDesktopPoint(); ok {
		actions = append(actions, Action{Kind: "click", X: x, Y: y})
	}
	return actions
}

// lateSetupStages are pages Setup Assistant shows only once the account exists,
// and the stages after it. Once one has been seen, an unrecognised screen is far
// more likely the desktop with an unreadable menu bar than a new setup page.
var lateSetupStages = map[string]bool{
	"account-creating": true, "location": true, "location-confirmation": true,
	"timezone": true, "analytics": true, "screen-time": true, "siri": true,
	"siri-disable": true, "siri-disabled": true, "filevault": true,
	"filevault-confirmation": true, "appearance": true, "liquid-glass": true,
	"updates": true, "updates-loading": true, "login": true, "desktop": true,
	"terminal-search": true, "terminal-launch": true, "remote-login-open": true,
	"remote-login": true, "remote-login-enabled": true,
	"remote-login-authorization": true, "lock-screen": true, "lock-screen-user": true,
}

// SetupAssistantDone reports whether stage is only ever seen after Setup
// Assistant has created the account.
func SetupAssistantDone(stage string) bool { return lateSetupStages[stage] }
