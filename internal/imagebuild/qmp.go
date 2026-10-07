package imagebuild

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

// armQEMUPower negotiates a dedicated QMP connection before the guest power
// request. The query-status response is a barrier: buffered earlier events
// cannot satisfy this observation. POWERDOWN is only an ACPI request and must
// never count as a completed shutdown.
func armQEMUPower(
	ctx context.Context,
	conn net.Conn,
	restart bool,
) (func(context.Context) error, func(), error) {
	var once sync.Once
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	closeObserver := func() { once.Do(func() { stop(); _ = conn.Close() }) }
	fail := func(err error) (func(context.Context) error, func(), error) {
		closeObserver()
		return nil, closeObserver, fmt.Errorf("arm QEMU power observation: %w", err)
	}
	decoder := json.NewDecoder(conn)
	var greeting map[string]json.RawMessage
	if err := decoder.Decode(&greeting); err != nil {
		return fail(err)
	}
	if len(greeting["QMP"]) == 0 {
		return fail(fmt.Errorf("%w: missing QMP greeting", ErrInput))
	}
	for _, command := range []string{"qmp_capabilities", "query-status"} {
		if err := json.NewEncoder(conn).
			Encode(map[string]string{"execute": command, "id": command}); err != nil {
			return fail(err)
		}
		for {
			var response qmpMessage
			if err := decoder.Decode(&response); err != nil {
				return fail(err)
			}
			if response.Event != "" || response.ID != command {
				continue
			}
			if response.Error != nil || response.Return == nil {
				return fail(fmt.Errorf("%w: QMP command %s rejected", ErrInput, command))
			}
			break
		}
	}
	wait := func(waitCtx context.Context) error {
		stopWait := context.AfterFunc(waitCtx, func() { _ = conn.Close() })
		defer stopWait()
		want := "SHUTDOWN"
		if restart {
			want = "RESET"
		}
		for {
			var event qmpMessage
			if err := decoder.Decode(&event); err != nil {
				return fmt.Errorf("observe guest %s: %w", want, err)
			}
			if event.Event == want && event.Data.Guest {
				return nil
			}
		}
	}
	return wait, closeObserver, nil
}

type qmpMessage struct {
	ID     string          `json:"id"`
	Event  string          `json:"event"`
	Return json.RawMessage `json:"return"`
	Error  json.RawMessage `json:"error"`
	Data   struct {
		Guest bool `json:"guest"`
	} `json:"data"`
}

// dialLocal waits for a new QEMU socket or for its listener to return after a
// reboot. The caller supplies a deadline for the whole clone, not per retry.
func dialLocal(ctx context.Context, path string) (net.Conn, error) {
	for {
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
		if err == nil {
			return conn, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("connect to QEMU: %w", ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}
