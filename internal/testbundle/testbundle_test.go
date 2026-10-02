package testbundle_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/weaveplatform/weaveplatform-oci/internal/testbundle"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

func TestWriteEveryOS(t *testing.T) {
	for _, osName := range []string{spec.OSDarwin, spec.OSWindows, spec.OSLinux} {
		dir := filepath.Join(t.TempDir(), osName)
		if err := testbundle.Write(
			dir,
			testbundle.Options{OS: osName, Arch: spec.ArchAMD64, ExtraDisk: true, Version: "v"},
		); err != nil {
			t.Fatal(err)
		}
		b, err := pack.LoadBundle(dir)
		if err != nil {
			t.Fatal(err)
		}
		if b.File.Guest.OS != osName || len(b.File.Disks) != 2 ||
			b.File.Annotations[spec.AnnotationVersion] != "v" {
			t.Fatalf("%+v", b.File)
		}
		st, _ := os.Stat(filepath.Join(dir, "disk0.img"))
		if st.Size() != 8<<20 {
			t.Fatalf("size %d", st.Size())
		}
	}
}

func TestWriteErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(file, nil, 0o600)
	cases := map[string]struct {
		dir string
		o   testbundle.Options
	}{
		"no os":      {t.TempDir(), testbundle.Options{Arch: "amd64"}},
		"unknown os": {t.TempDir(), testbundle.Options{OS: "plan9", Arch: "amd64"}},
		"bad offset": {
			t.TempDir(),
			testbundle.Options{OS: "linux", Arch: "amd64", DataAt: []int64{-1}},
		},
		"offset past end": {
			t.TempDir(),
			testbundle.Options{OS: "linux", Arch: "amd64", Size: 10, DataAt: []int64{10}},
		},
		"dir is a file": {
			filepath.Join(file, "x"),
			testbundle.Options{OS: "linux", Arch: "amd64"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if err := testbundle.Write(c.dir, c.o); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	if err := testbundle.Write(
		t.TempDir(),
		testbundle.Options{},
	); !errors.Is(
		err,
		testbundle.ErrOptions,
	) {
		t.Fatal(err)
	}
	// unwritable targets inside an existing directory
	for _, name := range []string{"disk0.img", "nvram.bin", "firmware-policy.json", "disk1.img"} {
		dir := t.TempDir()
		_ = os.Mkdir(filepath.Join(dir, name), 0o750)
		osName := map[string]string{"nvram.bin": "darwin", "firmware-policy.json": "windows"}[name]
		if osName == "" {
			osName = "linux"
		}
		if err := testbundle.Write(
			dir,
			testbundle.Options{OS: osName, Arch: "arm64", ExtraDisk: true},
		); err == nil {
			t.Fatalf("%s: directory in place of a file accepted", name)
		}
	}
	dir := t.TempDir()
	_ = os.Mkdir(filepath.Join(dir, pack.BundleFileName), 0o750)
	if err := testbundle.Write(dir, testbundle.Options{OS: "linux", Arch: "arm64"}); err == nil {
		t.Fatal("bundle.json as a directory accepted")
	}
}
