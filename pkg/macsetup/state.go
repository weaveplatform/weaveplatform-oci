package macsetup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	ConfigName = "onboarding-config.json"
	StateName  = "onboarding.json"
)

// State records observations, never a replay position or credentials. Readiness
// must be verified again whenever setup is called, even after a previous success.
type State struct {
	SSHReady    bool      `json:"sshReady"`
	ExecReady   bool      `json:"execReady"`
	Status      string    `json:"status"`
	Stage       string    `json:"stage"`
	Updated     time.Time `json:"updated"`
	Version     string    `json:"version,omitempty"`
	Build       string    `json:"build,omitempty"`
	ConsoleUser string    `json:"consoleUser,omitempty"`
	Address     string    `json:"address,omitempty"`
	HostKey     string    `json:"hostKey,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// Save writes a private atomic checkpoint alongside the VM.
func Save(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("%w: encode checkpoint: %w", ErrOnboarding, err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".onboarding-*")
	if err != nil {
		return fmt.Errorf("%w: create checkpoint: %w", ErrOnboarding, err)
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(raw, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("%w: write checkpoint: %w", ErrOnboarding, err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("%w: close checkpoint: %w", ErrOnboarding, err)
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("%w: replace checkpoint: %w", ErrOnboarding, err)
	}
	return nil
}
