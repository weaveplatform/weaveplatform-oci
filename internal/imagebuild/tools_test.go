package imagebuild

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeToolHelper(t *testing.T) {
	if os.Args[len(os.Args)-1] != "image-helper" {
		return
	}
	fmt.Print("native output")
	os.Exit(0)
}

func TestNativeToolsAndFileRefusals(t *testing.T) {
	tools := Tools{Log: io.Discard}
	output, err := tools.output(
		t.Context(),
		os.Args[0],
		"-test.run=TestNativeToolHelper",
		"--",
		"image-helper",
	)
	must(t, err)
	if !strings.HasPrefix(string(output), "native output") {
		t.Fatal(string(output))
	}
	if err := tools.run(t.Context(), nil, filepath.Join(t.TempDir(), "missing-tool")); err == nil {
		t.Fatal("missing executable")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "source")
	must(t, os.WriteFile(src, []byte("source"), 0o600))
	if copyFile("missing", filepath.Join(dir, "out")) == nil {
		t.Fatal("missing source")
	}
	if copyFile(src, src) == nil {
		t.Fatal("overwritten source")
	}
	if copyFile(dir, filepath.Join(dir, "bad-copy")) == nil {
		t.Fatal("copied directory as file")
	}
	if newDirectory(filepath.Join(src, "child")) == nil {
		t.Fatal("parent is file")
	}
	if writeJSON(dir, map[string]string{}) == nil {
		t.Fatal("wrote JSON over directory")
	}
	if writeJSON(filepath.Join(dir, "json"), make(chan int)) == nil {
		t.Fatal("encoded channel")
	}
	if writeNewJSON(filepath.Join(dir, "json"), make(chan int)) == nil {
		t.Fatal("encoded channel")
	}
	if copyTree("missing", filepath.Join(dir, "tree")) == nil {
		t.Fatal("missing payload")
	}
	if copyTree(dir, src) == nil {
		t.Fatal("payload destination is file")
	}
	if got := shellQuote("a'b"); got != "'a'\"'\"'b'" {
		t.Fatal(got)
	}
	if err := (Tools{Run: func(context.Context, io.Writer, io.Writer, string, ...string) error { return nil }}).seed(
		t.Context(),
		src,
		"",
		"",
		"",
	); err == nil {
		t.Fatal("seed under file")
	}
}
