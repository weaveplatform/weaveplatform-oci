package cli

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/weaveplatform/weaveplatform-oci/internal/imagebuild"
	"github.com/weaveplatform/weaveplatform-oci/internal/testbundle"
	"github.com/weaveplatform/weaveplatform-oci/pkg/chunk"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
)

func TestLinuxAgentAcceptanceCommandFailsClosed(t *testing.T) {
	dir := t.TempDir()
	bundle := filepath.Join(dir, "bundle")
	if err := testbundle.Write(bundle, testbundle.Options{OS: "linux", Arch: "arm64"}); err != nil {
		t.Fatal(err)
	}
	b, err := pack.LoadBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	store, err := pack.OpenLayout(t.Context(), filepath.Join(dir, "layout"))
	if err != nil {
		t.Fatal(err)
	}
	child, err := pack.Manifest(t.Context(), b, store, chunk.Options{})
	if err != nil {
		t.Fatal(err)
	}
	root, err := pack.Index(t.Context(), store, []ocispec.Descriptor{child}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Tag(t.Context(), root, "r1"); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"usage", "timeout", "lock", "layout", "validation"} {
		t.Run(mode, func(t *testing.T) {
			load := func() (imagebuild.Lock, error) {
				if mode == "lock" {
					return imagebuild.Lock{}, errors.New("bad lock")
				}
				return imagebuild.Lock{}, nil
			}
			cmd := newImageAgentValidate(
				imagebuild.Tools{},
				load,
				func(any) error { t.Fatal("failed acceptance emitted success"); return nil },
				io.Discard,
			)
			cmd.SetContext(context.Background())
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			args := []string{
				filepath.Join(dir, "layout"),
				"--tag",
				"r1",
				"--out",
				filepath.Join(t.TempDir(), "report"),
			}
			if mode == "usage" {
				args = args[:1]
			}
			if mode == "timeout" {
				args = append(args, "--timeout", "0s")
			}
			if mode == "layout" {
				args[0] = filepath.Join(t.TempDir(), "missing-layout")
			}
			cmd.SetArgs(args)
			if err := cmd.Execute(); err == nil {
				t.Fatal("accepted", mode)
			}
		})
	}
}
