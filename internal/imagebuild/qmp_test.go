package imagebuild

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQMPPowerRequiresFreshGuestTransition(t *testing.T) {
	for _, mode := range []string{"reboot", "shutdown", "eof", "cancel", "greeting", "missing-greeting", "write", "response", "error", "no-return"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			client, server := net.Pipe()
			defer server.Close()
			go func() {
				defer server.Close()
				if mode == "greeting" {
					return
				}
				enc, dec := json.NewEncoder(server), json.NewDecoder(server)
				if mode == "missing-greeting" {
					_ = enc.Encode(map[string]any{})
					return
				}
				_ = enc.Encode(map[string]any{"QMP": map[string]any{"version": "test"}})
				if mode == "write" {
					return
				}
				for range 2 {
					var command map[string]string
					if dec.Decode(&command) != nil {
						return
					}
					if mode == "response" {
						return
					}
					// A stale reset, a non-guest reset and an unrelated reply do not
					// establish the requested post-subscription transition.
					_ = enc.Encode(
						map[string]any{"event": "RESET", "data": map[string]bool{"guest": true}},
					)
					_ = enc.Encode(map[string]any{"id": "unrelated", "return": map[string]any{}})
					r := map[string]any{"id": command["id"], "return": map[string]any{}}
					if mode == "error" {
						r["error"] = map[string]string{"class": "GenericError"}
					}
					if mode == "no-return" {
						delete(r, "return")
					}
					_ = enc.Encode(r)
					if mode == "error" || mode == "no-return" {
						return
					}
				}
				if mode == "eof" {
					return
				}
				if mode == "cancel" {
					cancel()
					return
				}
				_ = enc.Encode(map[string]any{"event": "POWERDOWN"})
				_ = enc.Encode(
					map[string]any{"event": "RESET", "data": map[string]bool{"guest": false}},
				)
				event := "RESET"
				if mode == "shutdown" {
					event = "SHUTDOWN"
				}
				_ = enc.Encode(
					map[string]any{"event": event, "data": map[string]bool{"guest": true}},
				)
			}()
			wait, closeObserver, err := armQEMUPower(ctx, client, mode != "shutdown")
			defer closeObserver()
			if err == nil {
				err = wait(ctx)
			}
			if (err == nil) != (mode == "reboot" || mode == "shutdown") {
				t.Fatal(mode, err)
			}
			closeObserver() // Cleanup is idempotent, including after cancellation.
		})
	}
}

func TestQMPDialWaitsForListenerAndHonoursCancellation(t *testing.T) {
	dir, err := os.MkdirTemp("", "qmp-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "qmp.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	go func() {
		conn, e := listener.Accept()
		if e == nil {
			_, _ = io.Copy(io.Discard, conn)
			_ = conn.Close()
		}
	}()
	conn, err := dialLocal(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	ctx2, cancel2 := context.WithTimeout(t.Context(), 220*time.Millisecond)
	defer cancel2()
	if _, err := dialLocal(ctx2, path+"-missing"); err == nil {
		t.Fatal("missing socket accepted")
	}
}
