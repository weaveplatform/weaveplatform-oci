package macsetup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSetupLockIsExclusiveButNotInheritedByVM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if other, e := Acquire(path); e == nil {
		other.Close()
		t.Fatal("concurrent setup acquired lock")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, "/bin/sleep", "4")
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(path)
	if err != nil {
		t.Fatalf("child inherited setup lock: %v", err)
	}
	second.Close()
	if _, err = Acquire(filepath.Join(t.TempDir(), "missing", "lock")); err == nil {
		t.Fatal("accepted missing lock directory")
	}
}

// A second setup is told who holds the lock, and the record is cleared on release.
func TestSetupLockNamesItsHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Acquire(path)
	if err == nil || !strings.Contains(err.Error(), "already active") ||
		!strings.Contains(err.Error(), fmt.Sprintf("held by pid %d since ", os.Getpid())) {
		t.Fatalf("contended Acquire = %v", err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	if err = first.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	if raw, _ := os.ReadFile(path); len(raw) != 0 {
		t.Fatalf("released lock still records %q", raw)
	}
	var none *Lock
	if err = none.Close(); err != nil {
		t.Fatal(err)
	}
}

// A record left by a holder that has exited (a stale record) is reported as such;
// a file with no record names nobody.
func TestLockHolderRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	write := func(s string) *os.File {
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		return f
	}
	child := exec.Command("/usr/bin/true")
	if err := child.Run(); err != nil {
		t.Fatal(err)
	}
	dead := child.ProcessState.Pid()
	got := lockHolder(
		write(
			fmt.Sprintf("weave pid=%d since=2026-10-06T00:00:00Z command=\"weave setup\"\n", dead),
		),
	)
	if !strings.Contains(got, fmt.Sprintf("pid %d since 2026-10-06T00:00:00Z", dead)) ||
		!strings.Contains(got, "has exited") {
		t.Fatalf("dead holder = %q", got)
	}
	for _, record := range []string{"", "garbage", "weave pid=x", "weave since=now"} {
		if got = lockHolder(write(record)); got != "" {
			t.Fatalf("record %q = %q", record, got)
		}
	}
}

// The lock file is never followed through a symlink: a link planted at its path
// (or copied in with a VM) cannot redirect the lock, or the holder record, elsewhere.
func TestSetupLockRefusesASymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "lock")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if l, err := Acquire(path); err == nil {
		l.Close()
		t.Fatal("followed a symlinked lock file")
	}
	if raw, _ := os.ReadFile(target); string(raw) != "keep" {
		t.Fatalf("symlink target was written: %q", raw)
	}
}

// A lock file removed while held is not the file a later setup locks; sameFile
// tells the two apart, so Acquire retries on the file now at the path.
func TestSameFileDetectsAReplacedLockFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !sameFile(f, path) {
		t.Fatal("the open file is not the file at its path")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if sameFile(f, path) {
		t.Fatal("a removed lock file matched")
	}
	if err = os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if sameFile(f, path) {
		t.Fatal("a replaced lock file matched")
	}
	l, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
}
