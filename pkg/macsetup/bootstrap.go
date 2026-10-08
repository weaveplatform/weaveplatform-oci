package macsetup

import "strings"

const SharingCommand = "defaults write NSGlobalDomain AppleKeyboardUIMode -int 3; open 'x-apple.systempreferences:com.apple.Sharing-Settings.extension?Services_RemoteLogin'"

// bootstrapPlan advances only after observing the intended application. A
// Spotlight query alone does not prove that its selected result is Terminal.
func bootstrapPlan(s Screen, _ Config) (Plan, error) {
	if (s.has("Terminal") && s.has("Shell")) || s.menuPrefix("Terminal Shell") {
		return Plan{
			Stage:   "remote-login-open",
			Actions: []Action{key("ctrl+c"), typed(SharingCommand, false), key("enter")},
		}, nil
	}
	if s.terminalQuery() {
		matches := s.exact("Terminal")
		if len(matches) > 0 {
			t := matches[0]
			for _, m := range matches {
				if m.X < t.X {
					t = m
				}
			}
			return Plan{
				Stage: "terminal-launch",
				Actions: []Action{
					{Kind: "click", X: t.X + t.Width/2, Y: t.Y + t.Height/2},
					key("enter"),
				},
			}, nil
		}
		return Plan{Stage: "terminal-search"}, nil
	}
	for _, t := range s.Text {
		if strings.EqualFold(strings.TrimSpace(t.Value), "Remote Login: Off") {
			done := s.exact("Done")
			if len(done) != 1 {
				return Plan{Stage: "remote-login"}, nil
			}
			return Plan{
				Stage: "remote-login",
				Actions: []Action{
					{Kind: "click", X: done[0].X + done[0].Width/2, Y: t.Y + t.Height/2},
				},
			}, nil
		}
	}
	if s.has("Remote Login: On") && s.has("Done") {
		return clickPlan(s, "remote-login-enabled", "Done")
	}
	return Plan{}, nil
}

func (s Screen) terminalQuery() bool {
	if s.has("Terminal.app") {
		return true
	}
	if !s.has("Applications") {
		return false
	}
	for _, label := range s.Text {
		query := strings.TrimSpace(label.Value)
		query = strings.NewReplacer("—", "-", "–", "-").Replace(query)
		query = strings.TrimPrefix(strings.Join(strings.Fields(query), ""), "Q")
		if strings.EqualFold(query, "Terminal.app-Terminal") {
			return true
		}
	}
	return false
}

func (s Screen) menuPrefix(label string) bool {
	for _, t := range s.Text {
		if t.Y < s.Height/20 &&
			strings.HasPrefix(strings.ToLower(t.Value), strings.ToLower(label)) {
			return true
		}
	}
	return false
}
