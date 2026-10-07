package macsetup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigValidation(t *testing.T) {
	valid := DefaultConfig()
	valid.Password = "a' b$\\123"
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Config){func(c *Config) { c.User = "root; id" }, func(c *Config) { c.Password = "abc" }, func(c *Config) { c.Password = "bad\npassword" }, func(c *Config) { c.Password = "bad🍎password" }, func(c *Config) { c.AuthorizedKeys = []string{"ssh-ed25519 nope"} }} {
		c := valid
		change(&c)
		if !errors.Is(c.Validate(), ErrOnboarding) {
			t.Fatal("accepted invalid configuration")
		}
	}
	if got := Quote("a'b"); got != "'a'\\''b'" {
		t.Fatalf("unsafe shell quote: %s", got)
	}
}

func TestLoadAndPrivateAtomicState(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	password := filepath.Join(dir, "password")
	if err := os.WriteFile(password, []byte("private123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		config,
		[]byte(`{"user":"runner","passwordFile":"password","autoLogin":false}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	c, err := Load(config)
	if err != nil || c.Password != "private123" || c.AutoLogin || c.PasswordFile != "" {
		t.Fatalf("config=%+v err=%v", c, err)
	}
	state := filepath.Join(dir, StateName)
	if err = Save(state, State{Stage: "account"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(state)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("checkpoint is not private: %v", err)
	}
	if err = Save(state, State{Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	if err = Save(filepath.Join(dir, "missing", "state"), State{}); err == nil {
		t.Fatal("accepted missing directory")
	}
	if err = Save(state, func() {}); err == nil {
		t.Fatal("accepted unencodable checkpoint")
	}
	if err = Save(dir, State{}); err == nil {
		t.Fatal("replaced directory with state")
	}
}

func TestConfigurationErrorsNeverExposeSecret(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config")
	cases := []string{
		`{"password":"private123","unknown":true}`,
		`{`,
		`{"password":"private123"} {}`,
		`{"password":"private123","passwordFile":"missing"}`,
		`{"passwordFile":"missing"}`,
	}
	for _, data := range cases {
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(p)
		if err == nil || strings.Contains(err.Error(), "private123") {
			t.Fatalf("bad error: %v", err)
		}
	}
	if _, err := Load(filepath.Join(dir, "absent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing-file cause lost: %v", err)
	}
}
