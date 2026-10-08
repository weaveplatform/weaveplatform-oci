package macsetup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Lock is the per-VM setup lock: an exclusive flock(2) on a file in the VM's
// directory. The kernel drops the flock when its holder exits, so a lock is never
// left held by a dead process; the file's text names the current holder, for the
// error a second setup gets.
type Lock struct {
	f *os.File
}

// Close releases the lock, clearing the holder record first so the file never
// names a process that no longer holds it.
func (l *Lock) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	_ = l.f.Truncate(0)
	err := l.f.Close()
	l.f = nil
	return err //nolint:wrapcheck // os.File.Close's error names the file
}

// lockAttempts bounds how often Acquire retries a lock file that was replaced
// between its open and its flock.
const lockAttempts = 5

// Acquire prevents concurrent setup without leaking the lock into the detached
// VM process. os.OpenFile sets close-on-exec; a raw syscall.Open does not.
//
// It never waits: a second setup (or a weave-agent install by `weave run`) is told
// who holds the lock instead. The lock file is opened without following
// symlinks, and is checked, once locked, to still be the file at path: a lock
// taken on a file that was removed or replaced meanwhile would guard nothing,
// since the next process would lock the new file.
func Acquire(path string) (*Lock, error) {
	for range lockAttempts {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
		if err != nil {
			return nil, fmt.Errorf("%w: open setup lock: %w", ErrOnboarding, err)
		}
		if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			holder := lockHolder(f)
			_ = f.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return nil, fmt.Errorf(
					"%w: setup is already active for this VM%s",
					ErrOnboarding,
					holder,
				)
			}
			return nil, fmt.Errorf("%w: setup lock unavailable: %w", ErrOnboarding, err)
		}
		if !sameFile(f, path) {
			_ = f.Close()
			continue
		}
		if err = recordHolder(f); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("%w: record setup lock holder: %w", ErrOnboarding, err)
		}
		return &Lock{f: f}, nil
	}
	return nil, fmt.Errorf(
		"%w: setup lock %s was replaced %d times while being taken",
		ErrOnboarding,
		path,
		lockAttempts,
	)
}

// sameFile reports whether f is still the file at path.
func sameFile(f *os.File, path string) bool {
	held, err := f.Stat()
	if err != nil {
		return false
	}
	current, err := os.Lstat(path)
	return err == nil && os.SameFile(held, current)
}

// holderPrefix starts the holder record, so a record can be told from any other
// content the file might have.
const holderPrefix = "weave "

// recordHolder writes who holds the lock: the weave command, its PID and when.
func recordHolder(f *os.File) error {
	command := "weave"
	if len(os.Args) > 1 {
		command = filepath.Base(os.Args[0]) + " " + os.Args[1]
	}
	record := fmt.Sprintf("%spid=%d since=%s command=%q\n", holderPrefix, os.Getpid(),
		time.Now().UTC().Format(time.RFC3339), command)
	if err := f.Truncate(0); err != nil {
		return err //nolint:wrapcheck // wrapped by Acquire
	}
	_, err := f.WriteAt([]byte(record), 0)
	return err //nolint:wrapcheck // wrapped by Acquire
}

// lockHolder describes the recorded holder, for the error a second setup gets;
// empty when nothing usable is recorded.
func lockHolder(f *os.File) string {
	buf := make([]byte, 512)
	n, _ := f.ReadAt(buf, 0)
	record := strings.TrimSpace(string(buf[:n]))
	if !strings.HasPrefix(record, holderPrefix) {
		return ""
	}
	fields := map[string]string{}
	for _, field := range strings.Fields(strings.TrimPrefix(record, holderPrefix)) {
		if k, v, ok := strings.Cut(field, "="); ok {
			fields[k] = v
		}
	}
	pid, err := strconv.Atoi(fields["pid"])
	if err != nil || pid <= 0 {
		return ""
	}
	desc := fmt.Sprintf(" (held by pid %d", pid)
	if since := fields["since"]; since != "" {
		desc += " since " + since
	}
	if !processAlive(pid) {
		// The recorded holder is gone, yet the lock is held: a process it started
		// inherited the descriptor and is holding it now.
		desc += ", which has exited; a process it started still holds the lock"
	}
	return desc + ")"
}

// processAlive reports whether pid names a running process.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
