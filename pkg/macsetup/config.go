// Package macsetup defines deterministic macOS onboarding independent of its UI transport.
package macsetup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/crypto/ssh"
)

var (
	ErrOnboarding = errors.New("macOS onboarding")
	accountName   = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,30}$`)
)

// Config describes the account and desktop required by the caller. Passwords are
// stored only in the VM's private configuration, never in progress or reports.
type Config struct {
	User           string   `json:"user"`
	Password       string   `json:"password,omitempty"`
	PasswordFile   string   `json:"passwordFile,omitempty"`
	AuthorizedKeys []string `json:"authorizedKeys,omitempty"`
	AutoLogin      bool     `json:"autoLogin"`
	KeepAwake      bool     `json:"keepAwake"`
	Version        string   `json:"version,omitempty"`
	Build          string   `json:"build,omitempty"`
}

// DefaultConfig supplies the local VM account; the caller generates its password.
func DefaultConfig() Config { return Config{User: "weave", AutoLogin: true, KeepAwake: true} }

// Validate rejects values that cannot safely be entered into Setup Assistant.
func (c Config) Validate() error {
	if !accountName.MatchString(c.User) {
		return fmt.Errorf("%w: invalid account name", ErrOnboarding)
	}
	if len(c.Password) < 4 || strings.ContainsAny(c.Password, "\r\n\x00") {
		return fmt.Errorf(
			"%w: password must contain at least four characters and no line breaks",
			ErrOnboarding,
		)
	}
	for _, r := range c.Password {
		if r < 32 || r > 126 {
			return fmt.Errorf(
				"%w: password must use printable ASCII for guest keyboard input",
				ErrOnboarding,
			)
		}
	}
	for _, key := range c.AuthorizedKeys {
		if _, _, _, _, err := ssh.ParseAuthorizedKey(
			[]byte(key),
		); err != nil ||
			strings.ContainsAny(key, "\r\n") {
			return fmt.Errorf("%w: invalid SSH public key", ErrOnboarding)
		}
	}
	return nil
}

// Load reads caller-supplied configuration, resolving a password file without
// exposing its contents in errors. Unknown fields fail before VM mutation.
func Load(path string) (Config, error) {
	c := DefaultConfig()
	f, err := os.Open(path)
	if err != nil {
		return c, fmt.Errorf("%w: read configuration: %w", ErrOnboarding, err)
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&c); err != nil {
		return c, fmt.Errorf("%w: invalid configuration JSON", ErrOnboarding)
	}
	if err = decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return c, fmt.Errorf("%w: configuration must contain one JSON object", ErrOnboarding)
	}
	if c.PasswordFile != "" {
		if c.Password != "" {
			return c, fmt.Errorf("%w: use password or passwordFile, not both", ErrOnboarding)
		}
		passwordPath := c.PasswordFile
		if !filepath.IsAbs(passwordPath) {
			passwordPath = filepath.Join(filepath.Dir(path), passwordPath)
		}
		raw, e := os.ReadFile(passwordPath)
		if e != nil {
			return c, fmt.Errorf("%w: cannot read password file", ErrOnboarding)
		}
		c.Password = strings.TrimRight(string(raw), "\r\n")
		c.PasswordFile = ""
	}
	return c, c.Validate()
}

// Quote preserves a value as one shell argument in guest commands.
func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
