package agentcheck

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"

	"github.com/weaveplatform/weaveplatform-agent-modules/sdk/weavewire"
	"github.com/weaveplatform/weaveplatform-oci/pkg/imagecheck"
)

// probeDesktop operates only on a disposable acceptance VM. It replaces its
// clipboard, changes a real console display and restores the original mode.
func probeDesktop(ctx context.Context, call caller, user string, r *Result) error {
	var current weavewire.SessionCurrentResponse
	if err := call(ctx, weavewire.KindSessionCurrent, nil, &current); err != nil {
		return fmt.Errorf("desktop session: %w", err)
	}
	if current.Session == nil || current.Session.User != user || !current.Session.Console ||
		current.Session.Remote || current.Session.State != weavewire.SessionActive || current.Session.ID == "" {
		return fmt.Errorf("%w: expected active local console user %s", ErrProbe, user)
	}
	r.Operations["desktop-session"] = imagecheck.Outcome{Status: imagecheck.Passed}
	if err := clipboardRoundtrip(ctx, call); err != nil {
		return err
	}
	r.Operations["clipboard-roundtrip"] = imagecheck.Outcome{Status: imagecheck.Passed}
	if err := displayRoundtrip(ctx, call); err != nil {
		return err
	}
	r.Operations["display-roundtrip"] = imagecheck.Outcome{Status: imagecheck.Passed}
	return nil
}

func clipboardRoundtrip(ctx context.Context, call caller) error {
	var nonce [24]byte
	_, _ = rand.Read(nonce[:])
	text := []byte(fmt.Sprintf("weave-clipboard-%x", nonce))
	request := weavewire.ClipboardSetRequest{
		Items: []weavewire.ClipboardItem{
			{Format: weavewire.ClipboardText, Size: int64(len(text)), Data: text},
		},
	}
	var set weavewire.ClipboardSetResponse
	if err := call(ctx, weavewire.KindClipboardSet, request, &set); err != nil {
		return fmt.Errorf("clipboard set: %w", err)
	}
	var get weavewire.ClipboardGetResponse
	if err := call(
		ctx,
		weavewire.KindClipboardGet,
		weavewire.ClipboardGetRequest{
			Formats:  []weavewire.ClipboardFormat{weavewire.ClipboardText},
			MaxBytes: 4096,
		},
		&get,
	); err != nil {
		return fmt.Errorf("clipboard get: %w", err)
	}
	if get.Streamed || len(get.Items) != 1 || get.Items[0].Format != weavewire.ClipboardText ||
		get.Items[0].Size != int64(len(text)) || !bytes.Equal(get.Items[0].Data, text) {
		return fmt.Errorf("%w: clipboard readback differs from fresh challenge", ErrProbe)
	}
	return nil
}

func displayRoundtrip(ctx context.Context, call caller) error {
	var before weavewire.DisplayListResponse
	if err := call(ctx, weavewire.KindDisplayList, nil, &before); err != nil {
		return fmt.Errorf("display list: %w", err)
	}
	if len(before.Displays) == 0 {
		return fmt.Errorf("%w: desktop has no displays", ErrProbe)
	}
	d := before.Displays[0]
	for _, candidate := range before.Displays {
		if candidate.Primary {
			d = candidate
			break
		}
	}
	if d.ID == "" || d.Current.Width <= 0 || d.Current.Height <= 0 {
		return fmt.Errorf("%w: invalid current display", ErrProbe)
	}
	mode := weavewire.DisplayMode{Width: 1024, Height: 768}
	if d.Current.Width == mode.Width && d.Current.Height == mode.Height {
		mode.Width, mode.Height = 1280, 800
	}
	for _, candidate := range d.Modes {
		if candidate.Width > 0 && candidate.Height > 0 &&
			(candidate.Width != d.Current.Width || candidate.Height != d.Current.Height) {
			mode = candidate
			break
		}
	}
	if err := setAndReadDisplay(ctx, call, d.ID, mode); err != nil {
		return err
	}
	return setAndReadDisplay(ctx, call, d.ID, d.Current)
}

func setAndReadDisplay(
	ctx context.Context,
	call caller,
	id string,
	mode weavewire.DisplayMode,
) error {
	var set weavewire.DisplaySetResponse
	req := weavewire.DisplaySetRequest{
		DisplayID: id,
		Width:     mode.Width,
		Height:    mode.Height,
		RefreshHz: mode.RefreshHz,
	}
	if err := call(ctx, weavewire.KindDisplaySet, req, &set); err != nil {
		return fmt.Errorf("display set: %w", err)
	}
	var after weavewire.DisplayListResponse
	if err := call(ctx, weavewire.KindDisplayList, nil, &after); err != nil {
		return fmt.Errorf("display readback: %w", err)
	}
	for _, d := range after.Displays {
		if d.ID == id && d.Current.Width == mode.Width && d.Current.Height == mode.Height {
			return nil
		}
	}
	return fmt.Errorf("%w: display did not change to %dx%d", ErrProbe, mode.Width, mode.Height)
}
