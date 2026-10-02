package vhd_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/weaveplatform/weaveplatform-oci/pkg/disk/vhd"
)

// The fixtures are the last 512 bytes of fixed VHDs made by
// `qemu-img create -f vpc -o subformat=fixed` (qemu 10): an independent
// implementation of the same specification.
func TestQemuFootersParseAndGeometryAgrees(t *testing.T) {
	for name, size := range map[string]int64{
		"qemu-fixed-64M.footer":   67125248,
		"qemu-fixed-3584M.footer": 3758211072,
		"qemu-fixed-40G.footer":   42951106560,
	} {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		f, err := vhd.ParseFooter(b)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if f.Size != size || vhd.GeometryFor(f.Size) != f.Geometry {
			t.Fatalf(
				"%s: size %d geometry %+v, ours %+v",
				name,
				f.Size,
				f.Geometry,
				vhd.GeometryFor(f.Size),
			)
		}
	}
	// qemu's force_size writes the "use the current size" marker geometry,
	// which is still a valid footer.
	b, _ := os.ReadFile(filepath.Join("testdata", "qemu-fixed-forcesize-64M.footer"))
	if f, err := vhd.ParseFooter(b); err != nil || f.Size != 64<<20 {
		t.Fatalf("%+v %v", f, err)
	}
}

func TestGeometryBranches(t *testing.T) {
	for size, want := range map[int64]vhd.Geometry{
		1 << 20:   {Cylinders: 30, Heads: 4, SectorsPerTrack: 17},
		200 << 20: {Cylinders: 825, Heads: 16, SectorsPerTrack: 31},
		300 << 20: {Cylinders: 609, Heads: 16, SectorsPerTrack: 63},
		2 << 40:   {Cylinders: 65535, Heads: 16, SectorsPerTrack: 255},
	} {
		if g := vhd.GeometryFor(size); g != want {
			t.Fatalf("%d: %+v, want %+v", size, g, want)
		}
	}
}

func TestFooterRoundTrip(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	b, err := vhd.NewFooter(8<<30, now)
	if err != nil {
		t.Fatal(err)
	}
	f, err := vhd.ParseFooter(b)
	if err != nil || f.Size != 8<<30 || !f.Created.Equal(now) || f.UniqueID == [16]byte{} {
		t.Fatalf("%+v %v", f, err)
	}
	if string(b[28:32]) != "wvoc" || binary.BigEndian.Uint64(b[16:24]) != 0xFFFFFFFFFFFFFFFF {
		t.Fatal("creator or data offset wrong")
	}
	// before 2000 the timestamp clamps to zero
	if b, _ := vhd.NewFooter(512, time.Unix(0, 0)); binary.BigEndian.Uint32(b[24:28]) != 0 {
		t.Fatal("timestamp not clamped")
	}
}

func TestSizesRefused(t *testing.T) {
	for _, s := range []int64{0, -512, 513, vhd.MaxSize + 512} {
		if _, err := vhd.NewFooter(s, time.Now()); !errors.Is(err, vhd.ErrSize) {
			t.Fatalf("%d: %v", s, err)
		}
	}
}

func TestBadFooters(t *testing.T) {
	good, _ := vhd.NewFooter(1<<20, time.Now())
	mut := func(f func(b []byte)) []byte {
		b := append([]byte{}, good...)
		f(b)
		return b
	}
	resum := func(b []byte) {
		var sum uint32
		for i, c := range b {
			if i < 64 || i >= 68 {
				sum += uint32(c)
			}
		}
		binary.BigEndian.PutUint32(b[64:68], ^sum)
	}
	for name, b := range map[string][]byte{
		"short":     good[:100],
		"cookie":    mut(func(b []byte) { b[0] = 'x' }),
		"checksum":  mut(func(b []byte) { b[100] ^= 1 }),
		"dynamic":   mut(func(b []byte) { binary.BigEndian.PutUint32(b[60:64], 3); resum(b) }),
		"odd size":  mut(func(b []byte) { binary.BigEndian.PutUint64(b[48:56], 1000); resum(b) }),
		"zero size": mut(func(b []byte) { binary.BigEndian.PutUint64(b[48:56], 0); resum(b) }),
	} {
		if _, err := vhd.ParseFooter(b); !errors.Is(err, vhd.ErrFooter) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestAppendAndRaw(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "disk.vhd")
	raw := bytes.Repeat([]byte{0xA5, 0x5A}, 1<<19) // 1 MiB
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := vhd.Append(path, time.Now()); err != nil {
		t.Fatal(err)
	}
	r, fh, size, err := vhd.Raw(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fh.Close() }()
	got, _ := io.ReadAll(r)
	if size != int64(len(raw)) || !bytes.Equal(got, raw) {
		t.Fatalf("raw view: %d bytes", size)
	}
}

func TestAppendAndRawFailures(t *testing.T) {
	dir := t.TempDir()
	odd := filepath.Join(dir, "odd")
	_ = os.WriteFile(odd, make([]byte, 1000), 0o600)
	if err := vhd.Append(odd, time.Now()); !errors.Is(err, vhd.ErrSize) {
		t.Fatalf("odd size: %v", err)
	}
	if err := vhd.Append(filepath.Join(dir, "missing"), time.Now()); err == nil {
		t.Fatal("missing file appended")
	}
	if _, _, _, err := vhd.Raw(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing file read")
	}
	tiny := filepath.Join(dir, "tiny")
	_ = os.WriteFile(tiny, []byte("x"), 0o600)
	if _, _, _, err := vhd.Raw(tiny); !errors.Is(err, vhd.ErrFooter) {
		t.Fatalf("tiny: %v", err)
	}
	// a raw disk with no footer
	if _, _, _, err := vhd.Raw(odd); !errors.Is(err, vhd.ErrFooter) {
		t.Fatalf("no footer: %v", err)
	}
	// a footer whose size disagrees with the file
	wrong := filepath.Join(dir, "wrong")
	f, _ := vhd.NewFooter(4096, time.Now())
	_ = os.WriteFile(wrong, append(make([]byte, 512), f...), 0o600)
	if _, _, _, err := vhd.Raw(wrong); !errors.Is(err, vhd.ErrFooter) {
		t.Fatalf("size mismatch: %v", err)
	}
}
