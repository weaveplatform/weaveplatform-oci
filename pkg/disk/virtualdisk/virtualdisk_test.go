package virtualdisk

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRequests(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	valid := request{Destination: filepath.Join(root, "new.vhdx"), Size: 1 << 20, Format: VHDX}
	for name, mutate := range map[string]func(*request){
		"destination":    func(r *request) { r.Destination = "" },
		"nul":            func(r *request) { r.Source = "bad\x00" },
		"format":         func(r *request) { r.Format = "raw" },
		"size":           func(r *request) { r.Size = 0 },
		"alignment":      func(r *request) { r.Size++ },
		"fixed creation": func(r *request) { r.Format = FixedVHD },
		"existing":       func(r *request) { r.Destination = source },
		"missing source": func(r *request) { r.Source = source + "missing" },
		"directory":      func(r *request) { r.Source = root },
	} {
		t.Run(name, func(t *testing.T) {
			r := valid
			mutate(&r)
			err := run(
				t.Context(),
				r,
				func(request) error { t.Fatal("native call on invalid input"); return nil },
			)
			if err == nil {
				t.Fatal("accepted invalid request")
			}
		})
	}
	for _, format := range []Format{VHDX, FixedVHD} {
		r := valid
		r.Source, r.Size, r.Format = source, 0, format
		if err := run(t.Context(), r, func(got request) error {
			if got != r {
				t.Fatal(got)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := run(
		t.Context(),
		valid,
		func(request) error { return os.ErrPermission },
	); !errors.Is(
		err,
		os.ErrPermission,
	) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := Create(ctx, valid.Destination, valid.Size); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := Convert(ctx, source, valid.Destination, VHDX); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	if err := run(
		ctx,
		valid,
		func(request) error { cancel(); return nil },
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := Create(t.Context(), valid.Destination, valid.Size); !errors.Is(err, ErrHost) {
			t.Fatal(err)
		}
		if err := Convert(t.Context(), source, valid.Destination, VHDX); !errors.Is(err, ErrHost) {
			t.Fatal(err)
		}
	}
}
