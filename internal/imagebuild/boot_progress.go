package imagebuild

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// bootProgress serializes live serial, diagnostic and heartbeat output. The
// report files retain the original bytes, independent of these status lines.
type bootProgress struct {
	mu                    sync.Mutex
	out                   io.Writer
	started, lastSerial   time.Time
	serialBytes           int64
	platform, accelerator string
	timeout               time.Duration
}

type bootStream struct {
	progress *bootProgress
	serial   bool
}

func (s bootStream) Write(b []byte) (int, error) {
	p := s.progress
	p.mu.Lock()
	defer p.mu.Unlock()
	if s.serial && len(b) > 0 {
		p.serialBytes += int64(len(b))
		p.lastSerial = time.Now()
	}
	n, err := p.out.Write(b)
	if err != nil {
		return n, fmt.Errorf("stream QEMU output: %w", err)
	}
	return n, nil
}

func (p *bootProgress) status(now time.Time, state string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	elapsed := now.Sub(p.started).Round(time.Second)
	remaining := max(time.Duration(0), p.timeout-now.Sub(p.started)).Round(time.Second)
	idle := "none yet"
	if !p.lastSerial.IsZero() {
		idle = now.Sub(p.lastSerial).Round(time.Second).String() + " ago"
	}
	_, _ = fmt.Fprintf(
		p.out,
		"\n[%s] boot %s: %s accelerator=%s elapsed=%s remaining=%s serial-bytes=%d last-serial=%s\n",
		now.UTC().Format(time.RFC3339),
		p.platform,
		state,
		p.accelerator,
		elapsed,
		remaining,
		p.serialBytes,
		idle,
	)
}

func (p *bootProgress) watch(ticks <-chan time.Time, stop <-chan struct{}) {
	for {
		select {
		case now := <-ticks:
			p.status(now, "QEMU running; awaiting guest shutdown")
		case <-stop:
			return
		}
	}
}

func (t Tools) progress(format string, args ...any) {
	if t.Log != nil {
		_, _ = fmt.Fprintf(
			t.Log,
			"[%s] %s\n",
			time.Now().UTC().Format(time.RFC3339),
			fmt.Sprintf(format, args...),
		)
	}
}
