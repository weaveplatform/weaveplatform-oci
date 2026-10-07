package imagebuild

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBootProgressHeartbeat(t *testing.T) {
	var live bytes.Buffer
	now := time.Now()
	p := &bootProgress{
		out:         &live,
		started:     now,
		platform:    "linux/arm64",
		accelerator: "tcg",
		timeout:     time.Minute,
	}
	p.status(now, "starting QEMU")
	if !strings.Contains(live.String(), "serial-bytes=0 last-serial=none yet") {
		t.Fatal(live.String())
	}
	_, err := (bootStream{progress: p}).Write([]byte("diagnostic\n"))
	must(t, err)
	_, err = (bootStream{progress: p, serial: true}).Write([]byte("guest"))
	must(t, err)
	p.lastSerial = now
	ticks, stop, done := make(chan time.Time), make(chan struct{}), make(chan struct{})
	go func() { defer close(done); p.watch(ticks, stop) }()
	ticks <- now.Add(30 * time.Second)
	close(stop)
	<-done
	for _, want := range []string{"diagnostic\n", "guest", "QEMU running", "accelerator=tcg", "elapsed=30s remaining=30s", "serial-bytes=5 last-serial=30s ago"} {
		if !strings.Contains(live.String(), want) {
			t.Fatalf("missing %q in %s", want, live.String())
		}
	}
	p.status(now.Add(2*time.Minute), "failed")
	if !strings.Contains(live.String(), "elapsed=2m0s remaining=0s") {
		t.Fatal(live.String())
	}
	p.out = brokenBootLog{}
	if _, err := (bootStream{progress: p}).Write(
		[]byte("output"),
	); !errors.Is(
		err,
		io.ErrClosedPipe,
	) {
		t.Fatal("lost output error", err)
	}
}

type brokenBootLog struct{}

func (brokenBootLog) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestBootStreamsBeforeExitAndPreservesReports(t *testing.T) {
	fakeFirmware(t)
	var live bytes.Buffer
	fake := &fakeQEMU{t: t}
	report := filepath.Join(t.TempDir(), "report")
	tools := Tools{
		Log: &live,
		Run: func(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error {
			if strings.HasPrefix(name, "qemu-system") {
				_, err := io.WriteString(stdout, "guest starting\n")
				must(t, err)
				_, err = io.WriteString(stderr, "QEMU diagnostic\n")
				must(t, err)
				// Output must already be visible while the native process is running.
				for _, want := range []string{"guest starting", "QEMU diagnostic", "starting QEMU", "accelerator="} {
					if !strings.Contains(live.String(), want) {
						t.Fatal("buffered live output", want)
					}
				}
			}
			return fake.run(ctx, stdout, stderr, name, args...)
		},
	}
	result, err := tools.BootLinux(
		t.Context(),
		BootOptions{Bundle: bootBundle(t, "arm64"), Report: report, Timeout: time.Minute},
	)
	must(t, err)
	if !result.Passed ||
		!strings.Contains(live.String(), "passed: guest shut down and boot identity verified") {
		t.Fatal("missing verified completion", live.String())
	}
	serial, err := os.ReadFile(filepath.Join(report, "serial.log"))
	must(t, err)
	diagnostic, err := os.ReadFile(filepath.Join(report, "qemu.log"))
	must(t, err)
	if !strings.HasPrefix(string(serial), "guest starting\n") ||
		strings.Contains(string(serial), "accelerator=") ||
		string(diagnostic) != "QEMU diagnostic\n" {
		t.Fatal("report files contaminated", string(serial), string(diagnostic))
	}
}

func TestBootTimeoutReportsCause(t *testing.T) {
	fakeFirmware(t)
	var live bytes.Buffer
	fake := &fakeQEMU{t: t}
	tools := Tools{
		Log: &live,
		Run: func(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error {
			if strings.HasPrefix(name, "qemu-system") {
				<-ctx.Done()
				return errors.New("process killed")
			}
			return fake.run(ctx, stdout, stderr, name, args...)
		},
	}
	result, err := tools.BootLinux(
		t.Context(),
		BootOptions{
			Bundle:  bootBundle(t, "arm64"),
			Report:  filepath.Join(t.TempDir(), "report"),
			Timeout: time.Millisecond,
		},
	)
	if !errors.Is(err, context.DeadlineExceeded) || result.Passed ||
		!strings.Contains(live.String(), "failed: context deadline exceeded") {
		t.Fatal("timeout cause missing", result, err, live.String())
	}
}
