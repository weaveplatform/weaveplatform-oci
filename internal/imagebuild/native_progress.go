package imagebuild

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

// nativeProgress shares one synchronized output stream between native stages,
// heartbeat polling and guest serial output. Percentages come only from the SDK.
type nativeProgress struct {
	mu                            sync.Mutex
	out                           io.Writer
	label, stage                  string
	started, deadline, lastSerial time.Time
	serialBytes                   int64
	fraction                      func() float64
}

func newNativeProgress(ctx context.Context, out io.Writer, label string) *nativeProgress {
	if out == nil {
		out = io.Discard
	}
	deadline, _ := ctx.Deadline()
	return &nativeProgress{out: out, label: label, started: time.Now(), deadline: deadline}
}

func (p *nativeProgress) step(stage string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stage = stage
	p.status(time.Now())
}

// status is called with mu held.
func (p *nativeProgress) status(now time.Time) {
	remaining := "no deadline"
	if !p.deadline.IsZero() {
		remaining = max(time.Duration(0), p.deadline.Sub(now)).Round(time.Second).String()
	}
	extra := ""
	if p.fraction != nil {
		extra = fmt.Sprintf(" restore=%.1f%%", p.fraction()*100)
	}
	if p.serialBytes > 0 {
		extra += fmt.Sprintf(
			" serial-bytes=%d last-serial=%s ago",
			p.serialBytes,
			now.Sub(p.lastSerial).Round(time.Second),
		)
	}
	_, _ = fmt.Fprintf(
		p.out,
		"\n[%s] %s: %s elapsed=%s remaining=%s%s\n",
		now.UTC().Format(time.RFC3339),
		p.label,
		p.stage,
		now.Sub(p.started).Round(time.Second),
		remaining,
		extra,
	)
}

func (p *nativeProgress) restoreFraction(fraction func() float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fraction = fraction
	p.stage = "Apple restore in progress"
	p.status(time.Now())
}

func (p *nativeProgress) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(b) > 0 {
		p.serialBytes += int64(len(b))
		p.lastSerial = time.Now()
	}
	n, err := p.out.Write(b)
	if err != nil {
		return n, fmt.Errorf("stream native guest output: %w", err)
	}
	return n, nil
}

func (p *nativeProgress) watch(ticks <-chan time.Time, stop <-chan struct{}) {
	for {
		select {
		case now := <-ticks:
			p.mu.Lock()
			p.status(now)
			p.mu.Unlock()
		case <-stop:
			return
		}
	}
}

func (p *nativeProgress) start() func(error) {
	ticker := time.NewTicker(30 * time.Second)
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() { defer close(stopped); p.watch(ticker.C, stop) }()
	return func(err error) {
		ticker.Stop()
		close(stop)
		<-stopped
		if err != nil {
			p.step("failed: " + err.Error())
		} else {
			p.step("completed")
		}
	}
}
