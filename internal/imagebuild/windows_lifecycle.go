package imagebuild

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// Only OS calls sit behind this boundary. The same lifecycle and shutdown
// checks run on the production host and in deterministic tests on every OS.
type windowsNativeCalls struct {
	createDisk  func(string) error
	createState func(string) error
	grant       func(string, string) error
	create      func(string, string) (uintptr, error)
	observe     func(uintptr, func()) error
	start       func(uintptr) error
	terminate   func(uintptr) error
	close       func(uintptr)
	connect     func(context.Context, string, <-chan struct{}) (net.Conn, error)
}

func installWindowsWith(
	ctx context.Context,
	r WindowsInstallRequest,
	api windowsNativeCalls,
) (result WindowsInstallResult, err error) {
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	progress := newNativeProgress(ctx, r.Log, "install windows/"+r.Arch+" (HCS)")
	finish := progress.start()
	defer func() { finish(err) }()
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("native Windows installation cancelled: %w", err)
	}
	id := uuid.NewString()
	progress.step("creating disk and isolated firmware state")
	disk, state := filepath.Join(r.Directory, "disk.vhdx"), filepath.Join(r.Directory, "build.vmgs")
	if err := api.createDisk(disk); err != nil {
		return result, err
	}
	if err := api.createState(state); err != nil {
		return result, fmt.Errorf("create build firmware state (elevation required): %w", err)
	}
	for _, path := range []string{disk, state, r.ISO, r.Seed} {
		if path == "" {
			continue
		}
		if err := api.grant(id, path); err != nil {
			return result, fmt.Errorf("grant VM media access: %w", err)
		}
	}
	pipe := `\\.\pipe\weaveoci-image-` + id
	document := windowsDocument(disk, r.ISO, r.Seed, state, pipe)
	if err := writeJSON(filepath.Join(r.Directory, "hcs.json"), document); err != nil {
		return result, err
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return result, fmt.Errorf("encode HCS configuration: %w", err)
	}
	progress.step("creating HCS virtual machine")
	system, err := api.create(id, string(raw))
	if system != 0 {
		defer api.close(system)
	}
	if err != nil {
		return result, err
	}
	exited := make(chan struct{})
	var once sync.Once
	if err := api.observe(system, func() { once.Do(func() { close(exited) }) }); err != nil {
		return result, fmt.Errorf("observe HCS lifecycle: %w", err)
	}
	progress.step("starting HCS virtual machine")
	if err := api.start(system); err != nil {
		return result, err
	}
	defer func() {
		select {
		case <-exited:
		default:
			_ = api.terminate(system)
		}
	}()
	// Connecting before setup reaches audit mode captures the receipt without a
	// guest agent or a network connection. Closing the pipe interrupts reads.
	progress.step("connecting COM1; Windows Setup may be silent until audit mode")
	conn, err := api.connect(ctx, pipe, exited)
	if err != nil {
		return result, err
	}
	defer conn.Close()
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopClose()
	log, err := os.OpenFile(
		filepath.Join(r.Directory, "serial.log"),
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return result, fmt.Errorf("create serial log: %w", err)
	}
	defer log.Close()
	progress.step("waiting for installation and generalization receipt; streaming COM1")
	scanner := bufio.NewScanner(io.TeeReader(conn, io.MultiWriter(log, progress)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if message, ok := strings.CutPrefix(line, "WEAVE-IMAGE-ERROR "); ok {
			return result, fmt.Errorf("%w: Windows guest sealing failed: %s", ErrInput, message)
		}
		if !strings.HasPrefix(line, r.Marker+" ") {
			continue
		}
		result, err = windowsReceipt(line, r.Marker)
		if err != nil {
			return result, err
		}
		break
	}
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("native Windows installation timed out or cancelled: %w", err)
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("read installation serial receipt: %w", err)
	}
	if !result.Generalized {
		return result, fmt.Errorf(
			"%w: Windows stopped without a generalization receipt; inspect serial.log",
			ErrInput,
		)
	}
	progress.step("generalization receipt verified; waiting for guest shutdown")
	select {
	case <-exited:
		return result, nil
	case <-ctx.Done():
		return result, fmt.Errorf("wait for generalized guest shutdown: %w", ctx.Err())
	}
}
