//go:build windows

package virtualdisk

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/weaveplatform/weaveplatform-oci/pkg/disk/vhd"
)

func TestNativeRoundTrip(t *testing.T) {
	root := t.TempDir()
	// Exercise a Windows-authored fixed container too, independently of the
	// portable footer writer. Both paths must work through the same binding.
	control := filepath.Join(root, "control.vhd")
	if err := native(request{Destination: control, Size: 4 << 20, Format: FixedVHD}); err != nil {
		t.Fatal("create native control", err)
	}
	if err := Convert(t.Context(), control, filepath.Join(root, "control.vhdx"), VHDX); err != nil {
		t.Fatal("convert native control", err)
	}
	raw := filepath.Join(root, "source.vhd")
	data := make([]byte, 4<<20)
	copy(data[512:], "sector content survives both conversions")
	if err := os.WriteFile(raw, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := vhd.Append(raw, time.Now()); err != nil {
		t.Fatal(err)
	}
	x := filepath.Join(root, "local.vhdx")
	if err := Convert(t.Context(), raw, x, VHDX); err != nil {
		ours, _ := os.ReadFile(raw)
		nativeBytes, _ := os.ReadFile(control)
		t.Logf("portable footer: %x", ours[len(ours)-vhd.FooterSize:])
		t.Logf("native footer: %x", nativeBytes[len(nativeBytes)-vhd.FooterSize:])
		t.Fatal(err)
	}
	out := filepath.Join(root, "export.vhd")
	if err := Convert(t.Context(), x, out, FixedVHD); err != nil {
		t.Fatal(err)
	}
	r, f, size, err := vhd.Raw(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := io.ReadAll(r)
	if err != nil || size != int64(len(data)) || !bytes.Equal(got, data) {
		t.Fatal("disk sectors changed", err, size)
	}
	if err := Create(t.Context(), filepath.Join(root, "empty.vhdx"), 4<<20); err != nil {
		t.Fatal(err)
	}
	if err := Create(t.Context(), filepath.Join(root, "missing", "disk.vhdx"), 4<<20); err == nil {
		t.Fatal("invalid native path accepted")
	}
}
