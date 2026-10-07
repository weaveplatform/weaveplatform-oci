package macsetup

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// ErrIdentity means the observed guest is not the selected restore or trusted VM.
var ErrIdentity = errors.New("guest identity mismatch")

// VerifyIdentity checks native guest output independently of the screen planner.
func VerifyIdentity(output string, c Config) (State, error) {
	var state State
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 4 {
		return state, fmt.Errorf("%w: incomplete guest identity", ErrIdentity)
	}
	state.Version, state.Build, state.ConsoleUser = lines[0], lines[1], lines[2]
	if !strings.HasPrefix(lines[3], "VirtualMac") ||
		(c.Version != "" && c.Version != state.Version) ||
		(c.Build != "" && c.Build != state.Build) {
		return state, fmt.Errorf(
			"%w: guest OS or hardware does not match selected restore",
			ErrIdentity,
		)
	}
	if state.ConsoleUser != c.User {
		return state, fmt.Errorf("%w: configured console session is not ready", ErrOnboarding)
	}
	for _, line := range lines[4:] {
		owner, command := processOwner(line)
		if filepath.Base(command) != "Setup Assistant" {
			continue
		}
		// macOS 27 can leave the pre-login Setup Assistant, owned by
		// _mbsetupuser, running after the configured user owns the console.
		// Only an instance in the user's session means setup is unfinished.
		if owner == setupUser {
			continue
		}
		return state, fmt.Errorf("%w: Setup Assistant is still running", ErrOnboarding)
	}
	return state, nil
}

// setupUser owns Setup Assistant before any account exists.
const setupUser = "_mbsetupuser"

// processOwner splits a `ps -axo user=,comm=` line into owner and command. A
// line that starts with a path has no owner column.
func processOwner(line string) (string, string) {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "/") {
		return "", line
	}
	owner, command, _ := strings.Cut(line, " ")
	return owner, strings.TrimSpace(command)
}

// Readiness requires a settled desktop across repeated native observations.
// Any failed observation restarts the quiet period.
type Readiness struct{ since time.Time }

func (r *Readiness) Observe(now time.Time, ready bool) bool {
	if !ready {
		r.since = time.Time{}
		return false
	}
	if r.since.IsZero() {
		r.since = now
		return false
	}
	return now.Sub(r.since) >= 20*time.Second
}

// Reconcile preserves the restore and account identity when setup is repeated.
func Reconcile(saved, requested Config) (Config, error) {
	if saved.User != requested.User || saved.Password != requested.Password {
		return Config{}, fmt.Errorf(
			"%w: setup cannot change an existing VM account; use its saved configuration",
			ErrIdentity,
		)
	}
	if (requested.Version != "" && saved.Version != "" && requested.Version != saved.Version) ||
		(requested.Build != "" && saved.Build != "" && requested.Build != saved.Build) {
		return Config{}, fmt.Errorf("%w: setup cannot change an existing restore", ErrIdentity)
	}
	if saved.Version != "" {
		requested.Version = saved.Version
	}
	if saved.Build != "" {
		requested.Build = saved.Build
	}
	return requested, nil
}

// Pending reports that a native desktop has been observed and is settling.
func (r *Readiness) Pending() bool { return !r.since.IsZero() }
