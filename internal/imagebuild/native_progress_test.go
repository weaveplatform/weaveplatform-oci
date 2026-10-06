package imagebuild

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestNativeProgress(t *testing.T) {
	var log bytes.Buffer
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	p := newNativeProgress(ctx, &log, "restore macos/arm64")
	p.step("loading image")
	p.restoreFraction(func() float64 { return 0.375 })
	if !strings.Contains(log.String(), "restore=37.5%") {
		t.Fatal(log.String())
	}
	_, err := p.Write([]byte("guest progress\n"))
	must(t, err)
	if !strings.Contains(log.String(), "guest progress\n") {
		t.Fatal("guest output buffered")
	}
	ticks, stop, done := make(chan time.Time), make(chan struct{}), make(chan struct{})
	go func() { defer close(done); p.watch(ticks, stop) }()
	ticks <- p.started.Add(2 * time.Minute)
	close(stop)
	<-done
	for _, want := range []string{"elapsed=2m0s remaining=0s", "serial-bytes=15", "last-serial="} {
		if !strings.Contains(log.String(), want) {
			t.Fatal("missing heartbeat detail", want, log.String())
		}
	}
	finish := p.start()
	finish(nil)
	if !strings.Contains(log.String(), "completed") {
		t.Fatal(log.String())
	}
	q := newNativeProgress(t.Context(), &log, "install windows/amd64")
	q.step("waiting for Setup")
	q.start()(context.Canceled)
	if !strings.Contains(log.String(), "remaining=no deadline") ||
		!strings.Contains(log.String(), "failed: context canceled") {
		t.Fatal(log.String())
	}
	q.out = brokenBootLog{}
	if _, err := q.Write([]byte("log")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	newNativeProgress(t.Context(), nil, "silent").step("running")
}
