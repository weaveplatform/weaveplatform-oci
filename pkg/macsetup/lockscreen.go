package macsetup

import (
	"regexp"
	"strings"
)

// clockPattern is the lock screen's large clock: hours and minutes alone.
var clockPattern = regexp.MustCompile(`^\d{1,2}[:.]\d{2}$`)

// lockScreen recognises the macOS lock screen (and the login window, which looks
// the same since macOS 14): a large clock in the upper half, centred, under a
// line naming the weekday. The guest shows it after a hard stop and when an idle
// session locks during a long setup. The menu bar's clock is far smaller, so the
// desktop is not taken for it.
func (s Screen) lockScreen() bool {
	if s.Width <= 0 || s.Height <= 0 {
		return false
	}
	clock := false
	for _, t := range s.Text {
		centre := t.X + t.Width/2
		if clockPattern.MatchString(strings.TrimSpace(t.Value)) &&
			t.Height*25 >= s.Height && t.Y < s.Height/2 &&
			centre > s.Width*3/10 && centre < s.Width*7/10 {
			clock = true
			break
		}
	}
	if !clock {
		return false
	}
	for _, t := range s.Text {
		if weekdayLine(t.Value) {
			return true
		}
	}
	return false
}

// weekdayLine reports a date line that starts with a weekday, full or
// abbreviated ("Tuesday 6 October", "Tue 6 Oct").
func weekdayLine(v string) bool {
	fields := strings.Fields(v)
	if len(fields) == 0 {
		return false
	}
	first := strings.ToLower(strings.TrimRight(fields[0], ",."))
	for _, day := range weekdays {
		d := strings.ToLower(day)
		if first == d || first == d[:3] {
			return true
		}
	}
	return false
}

// lockScreenPlan gets the lock screen to its password field for the configured
// account. The password itself is typed only by the login stage, which needs the
// account's name read exactly beside the field. When the field is shown but the
// name is not readable, the screen is left unrecognised, so the second,
// high-contrast OCR pass gets its chance to read the name; a password is never
// typed for an account that was not verified.
func lockScreenPlan(s Screen, c Config) (Plan, error) {
	if len(s.locate("Enter Password")) > 0 {
		return Plan{}, nil
	}
	if len(s.exact(c.User)) == 1 {
		return clickPlan(s, "lock-screen-user", c.User)
	}
	return Plan{Stage: "lock-screen", Actions: []Action{key("space")}}, nil
}
