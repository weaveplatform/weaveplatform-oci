package cli_test

import (
	"strings"
	"testing"
)

func TestImageInputsAndUsage(t *testing.T) {
	flags := []string{
		"--catalogue",
		"../../images/catalogue.json",
		"--lock",
		"../../images/packages.lock.json",
	}
	for _, command := range []string{"matrix", "check-lock"} {
		args := append([]string{"image", command}, flags...)
		code, out, stderr := run(t, args...)
		if code != 0 {
			t.Fatalf("%s: %d %s", command, code, stderr)
		}
		if !strings.Contains(
			out,
			map[string]string{"matrix": "ubuntu-26.04", "check-lock": "true"}[command],
		) {
			t.Fatal(out)
		}
	}
	args := append([]string{"image", "check-lock", "--require-packages", "linux/arm64"}, flags...)
	if code, _, stderr := run(
		t,
		args...); code == 0 ||
		!strings.Contains(stderr, "installer missing") {
		t.Fatalf("%d %s", code, stderr)
	}
	for _, args := range [][]string{{"image", "lock"}, {"image", "boot-linux", "missing"}, {"image", "validate-linux"}, {"image", "validate-linux", "missing"}, {"image", "build-linux-agent"}} {
		if code, _, stderr := run(t, args...); code != 2 {
			t.Errorf("%v: %d %s", args, code, stderr)
		}
	}
	for _, args := range [][]string{{"image", "matrix", "--catalogue", "missing"}, {"image", "check-lock", "--catalogue", "missing"}, {"image", "boot-linux", "missing", "--report", t.TempDir()}, {"image", "validate-linux", "missing", "--out", t.TempDir()}} {
		if code, _, _ := run(t, args...); code == 0 {
			t.Errorf("accepted %v", args)
		}
	}
}

func TestNativeImageVersionUsage(t *testing.T) {
	for _, args := range [][]string{
		{"image", "ipsw"},
		{"image", "ipsw", "--version", "latest"},
		{"image", "windows"},
		{"image", "windows", "--from-windows", "invalid-26h2"},
		{"image", "windows", "--from-windows", "pro-26h2", "--arch", "x64"},
		{"image", "windows", "--from-windows", "pro-26h2", "--out", "lock.json"},
		{"image", "build-windows"},
		{"image", "build-windows", "--from-windows", "pro-26h2"},
	} {
		if code, _, stderr := run(t, args...); code != 2 {
			t.Errorf("%v: %d %s", args, code, stderr)
		}
	}
	for _, command := range []string{"ipsw", "windows", "build-windows"} {
		if code, out, stderr := run(
			t,
			"image",
			command,
			"--help",
		); code != 0 ||
			!strings.Contains(out, "Flags:") {
			t.Fatalf("%s: %d %s %s", command, code, out, stderr)
		}
	}
}
