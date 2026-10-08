package macsetup

import (
	"fmt"
	"time"
)

// GreetingNudgeAfter is how long the first screens of Setup Assistant may stay
// unreadable before the greeting is dismissed with Space. The animated greeting
// is handwriting that OCR often cannot read at all; waiting for a readable frame
// lost five minutes and then timed out.
const GreetingNudgeAfter = 20 * time.Second

// NudgeGreeting replaces the plan for an unreadable screen seen before Setup
// Assistant has made the account with a Space press, which dismisses the
// greeting. It acts only on a blank frame or one with no setup control on it, so
// Space never lands on a setup page's focused button.
func NudgeGreeting(s Screen, p Plan, setupDone bool, unreadableFor time.Duration) Plan {
	if setupDone || unreadableFor < GreetingNudgeAfter {
		return p
	}
	if p.Stage != "wake" && (p.Stage != "unknown" || s.hasSetupControl()) {
		return p
	}
	return Plan{Stage: p.Stage, Actions: []Action{key("space")}}
}

// hasSetupControl reports any Setup Assistant or login control, matched as
// tolerantly as a click would match it.
func (s Screen) hasSetupControl() bool {
	for _, control := range setupControls {
		if len(s.locate(control)) > 0 {
			return true
		}
	}
	return false
}

// RetryAfter is how long a stage's actions wait before they are repeated on an
// unchanged screen. The greeting, a blank screen and the lock screen are retried
// sooner: a key press there is harmless, and a missed one otherwise costs half a
// minute each time.
func RetryAfter(stage string) time.Duration {
	switch stage {
	case "wake", "unknown", "greeting", "lock-screen", "lock-screen-user":
		return 10 * time.Second
	default:
		return 30 * time.Second
	}
}

// stageWaits names what each waiting stage is waiting for, for the error when
// the wait runs out.
var stageWaits = map[string]string{
	"wake":              "a readable screen (the display stayed blank)",
	"unknown":           "a screen it recognises",
	"greeting":          "the greeting to give way to the Language page",
	"lock-screen":       "the lock screen to show its password field",
	"lock-screen-user":  "the lock screen to show the account's password field",
	"login":             "the login to be accepted",
	"account-creating":  "macOS to finish creating the account",
	"updates-loading":   "the software update page to load",
	"terms-loading":     "the terms to load",
	"migration-loading": "the migration page to load",
	"desktop":           "the guest to accept SSH as the configured account",
}

// StageTimeout describes a stage that made no verified progress within limit,
// naming what it waited for.
func StageTimeout(stage string, limit time.Duration) string {
	what, ok := stageWaits[stage]
	if !ok {
		what = "the " + stage + " page to advance"
	}
	return fmt.Sprintf(
		"no verified progress at %s within %s: waited for %s; inspect the HTTP observer",
		stage, limit, what,
	)
}
