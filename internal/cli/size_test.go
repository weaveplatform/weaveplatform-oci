package cli

import "testing"

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"1GiB": 1 << 30, "512MiB": 512 << 20, "4096": 4096} {
		if got, err := parseSize(in); err != nil || got != want {
			t.Fatalf("%s: %d %v", in, got, err)
		}
	}
	for _, in := range []string{"", "-1GiB", "x", "1TB"} {
		if _, err := parseSize(in); err == nil {
			t.Fatalf("%s accepted", in)
		}
	}
}
