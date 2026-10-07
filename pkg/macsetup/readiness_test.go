package macsetup

import (
	"errors"
	"testing"
	"time"
)

func TestVerifyIdentity(t *testing.T) {
	c := Config{User: "weave", Version: "26.6.2", Build: "25G83"}
	for _, tc := range []struct {
		name, output   string
		identity, fail bool
	}{
		{"ready", "26.6.2\n25G83\nweave\nVirtualMac2,1\n/sbin/launchd", false, false},
		{"short", "26.6.2", true, true},
		{"version", "26.7\n25G83\nweave\nVirtualMac2,1", true, true},
		{"build", "26.6.2\nwrong\nweave\nVirtualMac2,1", true, true},
		{"host", "26.6.2\n25G83\nweave\nMac16,1", true, true},
		{"loginwindow", "26.6.2\n25G83\nroot\nVirtualMac2,1", false, true},
		{"setup", "26.6.2\n25G83\nweave\nVirtualMac2,1\n/System/Library/CoreServices/Setup Assistant.app/Contents/MacOS/Setup Assistant", false, true},
		{"user setup", "26.6.2\n25G83\nweave\nVirtualMac2,1\nweave            /System/Library/CoreServices/Setup Assistant.app/Contents/MacOS/Setup Assistant", false, true},
		{"root setup", "26.6.2\n25G83\nweave\nVirtualMac2,1\nroot /System/Library/CoreServices/Setup Assistant.app/Contents/MacOS/Setup Assistant", false, true},
		// macOS 27 26A434 leaves the pre-login instance running on the desktop.
		{"leftover pre-login setup", "26.6.2\n25G83\nweave\nVirtualMac2,1\n_mbsetupuser     /System/Library/CoreServices/Setup Assistant.app/Contents/MacOS/Setup Assistant\n_mbsetupuser /System/Library/CoreServices/Setup Assistant.app/Contents/Resources/mbuseragent\nweave /System/Library/CoreServices/Finder.app/Contents/MacOS/Finder", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := VerifyIdentity(tc.output, c)
			if (err != nil) != tc.fail || errors.Is(err, ErrIdentity) != tc.identity {
				t.Fatalf("state=%+v err=%v", s, err)
			}
		})
	}
}

func TestReadinessRequiresUninterruptedQuietPeriod(t *testing.T) {
	var gate Readiness
	now := time.Now()
	for _, tc := range []struct {
		seconds     int
		ready, want bool
	}{{0, true, false}, {10, true, false}, {19, true, false}, {20, true, true}, {21, false, false}, {22, true, false}, {41, true, false}, {42, true, true}} {
		if got := gate.Observe(
			now.Add(time.Duration(tc.seconds)*time.Second),
			tc.ready,
		); got != tc.want {
			t.Fatalf("at %d got %v", tc.seconds, got)
		}
	}
}

func TestReconcile(t *testing.T) {
	saved := Config{User: "weave", Password: "test-pass", Version: "15.6.1", Build: "24G90"}
	requested := saved
	requested.Version = ""
	requested.Build = ""
	requested.KeepAwake = true
	got, err := Reconcile(saved, requested)
	if err != nil || got.Version != saved.Version || got.Build != saved.Build || !got.KeepAwake {
		t.Fatalf("%+v %v", got, err)
	}
	for _, mutate := range []func(*Config){func(c *Config) { c.User = "other" }, func(c *Config) { c.Password = "other" }, func(c *Config) { c.Version = "26" }, func(c *Config) { c.Build = "wrong" }} {
		bad := saved
		mutate(&bad)
		if _, err = Reconcile(saved, bad); !errors.Is(err, ErrIdentity) {
			t.Fatal(err)
		}
	}
	if _, err = Reconcile(Config{User: "weave", Password: "test-pass"}, saved); err != nil {
		t.Fatal(err)
	}
}
