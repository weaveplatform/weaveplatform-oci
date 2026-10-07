package virtualdisk

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/weaveplatform/weaveplatform-oci/pkg/disk/vhd"
)

func TestSourceFormatUsesBytes(t *testing.T) {
	dir := t.TempDir()
	x := filepath.Join(dir, "source-without-extension")
	if err := os.WriteFile(x, []byte("vhdxfile"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := sourceFormat(x); err != nil || got != VHDX {
		t.Fatal(got, err)
	}
	fixed := filepath.Join(dir, "fixed.raw")
	if err := os.WriteFile(fixed, make([]byte, 512), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := vhd.Append(fixed, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, err := sourceFormat(fixed); err != nil || got != FixedVHD {
		t.Fatal(got, err)
	}
	for _, input := range []string{"missing", "short", "corrupt"} {
		path := filepath.Join(dir, input)
		if input != "missing" {
			size := 1
			if input == "corrupt" {
				size = 1024
			}
			if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := sourceFormat(path); err == nil {
			t.Fatal("invalid source accepted", input)
		}
	}
}
