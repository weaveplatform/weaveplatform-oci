package imagebuild

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWindowsNativeLifecycle(t *testing.T) {
	for _, mode := range []string{"success", "cancelled", "config-write", "disk", "state", "grant", "create", "observe", "start", "connect", "log", "no-receipt", "bad-receipt", "unsealed", "oversized-serial", "read-timeout", "shutdown-timeout"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			if mode == "config-write" {
				must(t, os.Mkdir(filepath.Join(dir, "hcs.json"), 0o700))
			}
			timeout := 5 * time.Second
			if strings.HasSuffix(mode, "timeout") {
				timeout = 80 * time.Millisecond
			}
			marker := "WEAVE-IMAGE-READY-0123456789abcdef01234567"
			if mode == "log" {
				must(t, os.WriteFile(filepath.Join(dir, "serial.log"), nil, 0o600))
			}
			var events []string
			var onExit func()
			connected := false
			writerDone := make(chan struct{})
			fail := func(stage string) error {
				events = append(events, stage)
				if mode == stage {
					return ErrInput
				}
				return nil
			}
			api := windowsNativeCalls{
				createDisk:  func(string) error { return fail("disk") },
				createState: func(string) error { return fail("state") },
				grant:       func(string, string) error { return fail("grant") },
				create: func(_, document string) (uintptr, error) {
					var doc map[string]any
					must(t, json.Unmarshal([]byte(document), &doc))
					if doc["ShouldTerminateOnLastHandleClosed"] != true {
						t.Fatal("VM can escape builder")
					}
					return 42, fail("create")
				},
				observe:   func(_ uintptr, exit func()) error { onExit = exit; return fail("observe") },
				start:     func(uintptr) error { return fail("start") },
				terminate: func(uintptr) error { return fail("terminate") },
				close:     func(uintptr) { events = append(events, "close") },
				connect: func(ctx context.Context, _ string, _ <-chan struct{}) (net.Conn, error) {
					if err := fail("connect"); err != nil {
						return nil, err
					}
					connected = true
					client, server := net.Pipe()
					go func() {
						defer close(writerDone)
						defer server.Close()
						if mode == "read-timeout" {
							<-ctx.Done()
							return
						}
						payload := `{"osVersion":"10.0.26200.1","build":"26200.1","edition":"Enterprise","release":"25H2","generalized":true}`
						if mode == "bad-receipt" {
							payload = "invalid"
						}
						if mode == "unsealed" {
							payload = `{"generalized":false}`
						}
						line := "firmware log\n" + marker + " " + payload + "\n"
						if mode == "no-receipt" {
							line = "setup failed\n"
						}
						if mode == "oversized-serial" {
							line = strings.Repeat("x", 128*1024)
						}
						_, _ = io.WriteString(server, line)
						if mode == "shutdown-timeout" {
							<-ctx.Done()
							return
						}
						onExit()
					}()
					return client, nil
				},
			}
			result, err := installWindowsWith(
				ctx,
				WindowsInstallRequest{
					Directory: dir,
					Marker:    marker,
					Timeout:   timeout,
				},
				api,
			)
			if connected {
				select {
				case <-writerDone:
				case <-time.After(time.Second):
					t.Fatal("serial worker leaked")
				}
			}
			if mode == "success" {
				must(t, err)
				if !result.Generalized {
					t.Fatal("missing receipt")
				}
				if slices.Contains(events, "terminate") {
					t.Fatal("forced shutdown of completed guest")
				}
			} else if err == nil {
				t.Fatal("accepted failed install")
			}
			created := slices.Contains(events, "create")
			if created != slices.Contains(events, "close") {
				t.Fatal("native handle leak", events)
			}
			if slices.Contains(events, "terminate") && events[len(events)-1] != "close" {
				t.Fatal("handle closed before termination", events)
			}
		})
	}
}
