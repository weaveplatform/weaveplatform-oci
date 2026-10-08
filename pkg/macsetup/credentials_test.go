package macsetup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSSHUsesSavedAccountWithExplicitOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), ConfigName)
	c := DefaultConfig()
	c.User = "labrunner"
	c.Password = "generated-password"
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ user, password, wantUser, wantPassword string }{{"", "", "labrunner", c.Password}, {"labrunner", "", "labrunner", c.Password}, {"", "override", "labrunner", "override"}, {"other", "explicit", "other", "explicit"}} {
		u, p, e := SSHCredentials(path, tc.user, tc.password)
		if e != nil || u != tc.wantUser || p != tc.wantPassword {
			t.Fatal("incorrect credential resolution", e)
		}
	}
	if _, _, e := SSHCredentials(path, "other", ""); e == nil {
		t.Fatal("reused password for a different user")
	}
	if err := os.WriteFile(path, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, e := SSHCredentials(path, "", ""); e == nil {
		t.Fatal("invalid configuration ignored")
	}
}

func TestLegacySSHDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent")
	for _, tc := range []struct{ user, password string }{{"", ""}, {"custom", ""}, {"", "custom"}} {
		u, p, e := SSHCredentials(path, tc.user, tc.password)
		if e != nil || u == "" || p == "" {
			t.Fatal(e)
		}
		if tc.user == "" && u != "weave" {
			t.Fatal(u)
		}
		if tc.password == "" && p != "weave" {
			t.Fatal("wrong legacy password")
		}
	}
}
