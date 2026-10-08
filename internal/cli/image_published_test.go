package cli

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestPromotionEntryStrictDecoding(t *testing.T) {
	file := filepath.Join(t.TempDir(), "entry.json")
	if _, err := readPromotionEntry(file); err == nil {
		t.Fatal("missing entry")
	}
	for _, raw := range []string{`{"repository":"images/base","tag":"r1","digest":"sha256:abc"}`, `{`, `{"unknown":true}`, `{} {}`, `{} junk`} {
		if err := os.WriteFile(file, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		entry, err := readPromotionEntry(file)
		if (err == nil) != (entry.Repository == "images/base") {
			t.Fatal(raw, entry, err)
		}
	}
}

func TestPublishedAdmissionCommandRequiresReviewedPolicy(t *testing.T) {
	for _, args := range [][]string{{}, {"entry"}, {"entry", "--policy", "missing", "--out", t.TempDir()}} {
		cmd := newImagePublished(func(any) error { t.Fatal("unexpected acceptance"); return nil })
		cmd.SetContext(t.Context())
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatal("accepted", args)
		}
	}
}
