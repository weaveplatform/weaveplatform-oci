package handoff

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	"oras.land/oras-go/v2/content/memory"

	"github.com/weaveplatform/weaveplatform-oci/pkg/chunk"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestAdditionalImportRefusals(t *testing.T) {
	o, _ := fixture(t, "ubuntu")
	o.Manifest = filepath.Join(t.TempDir(), "missing", "manifest.json")
	if _, err := Import(t.Context(), o); err == nil {
		t.Fatal("missing root")
	}
	o.Manifest = filepath.Join(t.TempDir(), "manifest.json")
	if _, err := Import(t.Context(), o); err == nil {
		t.Fatal("missing manifest")
	}
	for _, r := range []interface{ Read([]byte) (int, error) }{brokenReader{}, strings.NewReader(strings.Repeat("x", maxMetadata+1))} {
		if _, err := readBounded(r); err == nil {
			t.Fatal("metadata read failure ignored")
		}
	}
	for _, raw := range []string{"{", `{"schemaVersion":1,"qualification":"unverified","osVersion":"1","inputs":{"arch":"amd64","family":"unknown","sourceSHA256":"` + strings.Repeat("b", 64) + `","sourceBuild":"20260927"}}`} {
		_, m := fixture(t, "ubuntu")
		f := baseBundle(m.Builds[0], o)
		if err := nativeMetadata([]byte(raw), m.Builds[0], &f); err == nil {
			t.Fatal("invalid native JSON/family")
		}
	}
	o, m := fixture(t, "macos")
	for n := range m.Builds[0].Files {
		if filepath.Base(m.Builds[0].Files[n].Name) == "auxstorage.bin" {
			f, err := os.OpenFile(m.Builds[0].Files[n].Name, os.O_WRONLY, 0)
			must(t, err)
			must(t, f.Truncate(pack.MaxStateSize+1))
			must(t, f.Close())
			m.Builds[0].Files[n].Size = pack.MaxStateSize + 1
		}
	}
	must(t, os.WriteFile(o.Manifest, encode(t, m), 0o600))
	if _, err := Import(t.Context(), o); err == nil {
		t.Fatal("oversized Apple state")
	}
}

func encode(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	must(t, err)
	return raw
}

func snapshot(
	ctx context.Context,
	root *os.Root,
	src artifact,
	dst string,
) (File, []string, error) {
	return snapshotFile(ctx, root, src, dst, nil)
}

func TestCopyProgress(t *testing.T) {
	var log bytes.Buffer
	progress := copyProgress(&log, "disk", 1024)
	progress(512)
	progress(1024)
	if !strings.Contains(log.String(), "1024/1024 bytes copied") {
		t.Fatal(log.String())
	}
}

func fixture(t *testing.T, family string) (Options, manifest) {
	t.Helper()
	dir := t.TempDir()
	o := Options{
		Manifest:     filepath.Join(dir, "manifest.json"),
		Out:          filepath.Join(t.TempDir(), "bundle"),
		RecipeCommit: strings.Repeat("a", 40),
		SourceURI:    "https://vendor.example/image",
		Version:      "test-r1",
	}
	b := build{
		Name:       "guest",
		Builder:    "qemu",
		Time:       1791379681,
		ArtifactID: "VM",
		RunID:      "test-run",
		Data: map[string]string{
			"qualification":        "unverified",
			"source_sha256":        strings.Repeat("b", 64),
			"source_build":         "20260927",
			"family":               family,
			"release":              "26.04",
			"arch":                 "arm64",
			"purpose":              "guest-base",
			"target":               "qemu",
			"firmware_code_sha256": strings.Repeat("c", 64),
			"firmware_vars_sha256": strings.Repeat("d", 64),
		},
	}
	files := map[string][]byte{
		"disk.raw":   make([]byte, 1024),
		"efivars.fd": []byte("build-specific identity"),
	}
	if family == "fedora" {
		b.Data["release"] = "44"
	}
	if family == "macos" || family == "windows-11" {
		b.Builder = "imageweave-native"
		r := nativeResult{
			SchemaVersion: 1,
			Qualification: "unverified",
			OSVersion:     "26.6.2",
			Files:         []string{"disk0.img", "auxstorage.bin"},
			FirstBoot:     "setup-assistant",
		}
		r.Inputs.Family, r.Inputs.Arch, r.Inputs.Release = family, "arm64", "26"
		r.Inputs.SourceSHA256 = b.Data["source_sha256"]
		r.Inputs.SourceBuild = "25G83"
		b.Data["source_build"] = "25G83"
		r.Firmware.Type, r.Firmware.TPM, r.Firmware.CloneIdentity = "apple", "none", "new-machine-identifier; copy auxiliary storage"
		// Decode the producer's actual v1 field spelling (MacRestoreResult has no tags).
		must(
			t,
			json.Unmarshal(
				[]byte(
					`{"HardwareModel":"YnBsaXN0MDA=","CPUCountMin":2,"CPUCount":4,"MemorySizeMin":4294967296,"MemorySize":8589934592}`,
				),
				&r.Mac,
			),
		)
		files = map[string][]byte{
			"disk0.img":      make([]byte, 1024),
			"auxstorage.bin": []byte("auxiliary storage"),
		}
		if family == "windows-11" {
			r.Mac = nil
			r.Files = []string{"disk0.img"}
			r.FirstBoot = "windows-oobe"
			r.Inputs.Release, r.Inputs.SourceBuild, r.Inputs.Edition = "26H2", "28000.1", "enterprise"
			r.OSVersion = "10.0.28000.1"
			b.Data["source_build"] = "28000.1"
			r.Firmware.Type, r.Firmware.TPM, r.Firmware.SecureBoot, r.Firmware.CloneIdentity = "uefi", "required", true, "regenerate firmware and TPM state"
			must(
				t,
				json.Unmarshal(
					[]byte(
						`{"osVersion":"10.0.28000.1","build":"28000.1","edition":"Enterprise","release":"26H2","arch":"arm64","generalized":true}`,
					),
					&r.Windows,
				),
			)
			delete(files, "auxstorage.bin")
		}
		files["build-result.json"] = encode(t, r)
	}
	for name, data := range files {
		if strings.HasPrefix(name, "disk") {
			copy(data, "guest disk")
		}
		must(t, os.WriteFile(filepath.Join(dir, name), data, 0o600))
		b.Files = append(b.Files, artifact{Name: filepath.Join(dir, name), Size: int64(len(data))})
	}
	m := manifest{Builds: []build{b}, LastRun: b.RunID}
	must(t, os.WriteFile(o.Manifest, encode(t, m), 0o600))
	return o, m
}

func TestImportPackUnpackAndTamper(t *testing.T) {
	for _, family := range []string{"ubuntu", "fedora", "macos", "windows-11"} {
		t.Run(family, func(t *testing.T) {
			o, _ := fixture(t, family)
			r, err := Import(t.Context(), o)
			must(t, err)
			if r.SchemaVersion != 1 || r.Qualification != "unverified" ||
				r.RecipeCommit != o.RecipeCommit ||
				len(r.Files) == 0 {
				t.Fatal(r)
			}
			b, err := pack.LoadBundle(o.Out)
			must(t, err)
			if _, err := os.Stat(filepath.Join(o.Out, "efivars.fd")); !os.IsNotExist(err) {
				t.Fatal("build-specific EFI state leaked")
			}
			raw, err := os.ReadFile(filepath.Join(o.Out, "imageweave-import.json"))
			must(t, err)
			if b.File.Annotations["io.weave.imageweave.handoff.digest"] != digest.FromBytes(raw).
				String() {
				t.Fatal("receipt not bound")
			}
			store := memory.New()
			m, err := pack.Manifest(t.Context(), b, store, chunk.Options{})
			must(t, err)
			out := filepath.Join(t.TempDir(), "unpacked")
			_, err = pack.Unpack(t.Context(), store, m, out, chunk.AssembleOptions{})
			must(t, err)
			original, err := os.ReadFile(filepath.Join(o.Out, "disk0.img"))
			must(t, err)
			actual, err := os.ReadFile(filepath.Join(out, "disk0.img"))
			must(t, err)
			if !bytes.Equal(original, actual) {
				t.Fatal("round trip changed disk")
			}
			original[0] ^= 1
			must(t, os.WriteFile(filepath.Join(o.Out, "disk0.img"), original, 0o600))
			if _, err = pack.Manifest(t.Context(), b, store, chunk.Options{}); err == nil {
				t.Fatal("changed imported disk accepted")
			}
			original[0] ^= 1
			must(t, os.WriteFile(filepath.Join(o.Out, "disk0.img"), original, 0o600))
			b.File.Disks[0].ChunkDigests = []string{}
			if _, err = pack.Manifest(t.Context(), b, store, chunk.Options{}); err == nil {
				t.Fatal("chunk count mismatch accepted")
			}
			b, err = pack.LoadBundle(o.Out)
			must(t, err)
			if len(b.File.State) > 0 {
				must(
					t,
					os.WriteFile(
						filepath.Join(o.Out, b.File.State[0].Path),
						[]byte("changed"),
						0o600,
					),
				)
				if _, err = pack.Manifest(t.Context(), b, store, chunk.Options{}); err == nil {
					t.Fatal("changed state accepted")
				}
			}
			if _, err = Import(t.Context(), o); err == nil {
				t.Fatal("existing destination overwritten")
			}
		})
	}
}

func TestManifestRefusals(t *testing.T) {
	for name, mutate := range map[string]func(*Options, *manifest){
		"commit":          func(o *Options, _ *manifest) { o.RecipeCommit = "main" },
		"version":         func(o *Options, _ *manifest) { o.Version = "" },
		"out":             func(o *Options, _ *manifest) { o.Out = "" },
		"credentials":     func(o *Options, _ *manifest) { o.SourceURI = "https://user:secret@vendor.example/iso" },
		"query":           func(o *Options, _ *manifest) { o.SourceURI = "https://vendor.example/iso?token=x" },
		"bad URI":         func(o *Options, _ *manifest) { o.SourceURI = "://%" },
		"absent run":      func(_ *Options, m *manifest) { m.LastRun = "missing" },
		"multiple builds": func(_ *Options, m *manifest) { m.Builds = append(m.Builds, m.Builds[0]) },
		"timestamp":       func(_ *Options, m *manifest) { m.Builds[0].Time = 0 },
		"qualification":   func(_ *Options, m *manifest) { m.Builds[0].Data["qualification"] = "runtime_verified" },
		"source hash":     func(_ *Options, m *manifest) { m.Builds[0].Data["source_sha256"] = "bad" },
		"builder":         func(_ *Options, m *manifest) { m.Builds[0].Builder = "amazon-ebs" },
		"target":          func(_ *Options, m *manifest) { m.Builds[0].Data["target"] = "aws" },
		"extra file": func(_ *Options, m *manifest) {
			m.Builds[0].Files = append(m.Builds[0].Files, artifact{Name: "ssh-key", Size: 1})
		},
		"duplicate":  func(_ *Options, m *manifest) { m.Builds[0].Files = append(m.Builds[0].Files, m.Builds[0].Files[0]) },
		"wrong size": func(_ *Options, m *manifest) { m.Builds[0].Files[0].Size++ },
		"empty":      func(_ *Options, m *manifest) { m.Builds[0].Files = nil },
		"traversal":  func(_ *Options, m *manifest) { m.Builds[0].Files = []artifact{{Name: "../disk.raw", Size: 512}} },
		"missing":    func(_ *Options, m *manifest) { m.Builds[0].Files = []artifact{{Name: "missing/disk.raw", Size: 512}} },
	} {
		t.Run(name, func(t *testing.T) {
			o, m := fixture(t, "ubuntu")
			mutate(&o, &m)
			must(t, os.WriteFile(o.Manifest, encode(t, m), 0o600))
			if _, err := Import(t.Context(), o); err == nil {
				t.Fatal("accepted invalid input")
			}
		})
	}
	for _, raw := range []string{"{", `{"unknown":1}`, `{} {}`} {
		o, _ := fixture(t, "ubuntu")
		must(t, os.WriteFile(o.Manifest, []byte(raw), 0o600))
		if _, err := Import(t.Context(), o); err == nil {
			t.Fatal("accepted invalid JSON")
		}
	}
}

func TestNativeRefusals(t *testing.T) {
	for _, family := range []string{"macos", "windows-11"} {
		for _, field := range []string{"schemaVersion", "qualification", "osVersion", "firmware", "firstBoot", "files", "inputs", "mac", "windows"} {
			t.Run(family+field, func(t *testing.T) {
				o, m := fixture(t, family)
				file := filepath.Join(filepath.Dir(o.Manifest), "build-result.json")
				raw, err := os.ReadFile(file)
				must(t, err)
				var r map[string]any
				must(t, json.Unmarshal(raw, &r))
				if field == "mac" && family == "windows-11" ||
					field == "windows" && family == "macos" {
					r[field] = map[string]any{}
				} else {
					delete(r, field)
				}
				raw = encode(t, r)
				must(t, os.WriteFile(file, raw, 0o600))
				for n := range m.Builds[0].Files {
					if m.Builds[0].Files[n].Name == file {
						m.Builds[0].Files[n].Size = int64(len(raw))
					}
				}
				must(t, os.WriteFile(o.Manifest, encode(t, m), 0o600))
				if _, err := Import(t.Context(), o); err == nil {
					t.Fatal("accepted inconsistent native receipt")
				}
			})
		}
	}
}

func TestPathsCancellationAndSparseCopy(t *testing.T) {
	o, m := fixture(t, "ubuntu")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Import(ctx, o); err == nil {
		t.Fatal("cancelled import")
	}
	if _, err := os.Stat(o.Out); !os.IsNotExist(err) {
		t.Fatal("partial bundle retained")
	}
	root, err := os.OpenRoot(filepath.Dir(o.Manifest))
	must(t, err)
	defer root.Close()
	data := make([]byte, 2<<20)
	data[len(data)-1] = 42
	must(t, os.WriteFile(filepath.Join(filepath.Dir(o.Manifest), "sparse"), data, 0o600))
	f, _, err := snapshot(
		t.Context(),
		root,
		artifact{Name: "sparse", Size: int64(len(data))},
		filepath.Join(t.TempDir(), "copy"),
	)
	must(t, err)
	if f.Digest != digest.FromBytes(data).String() {
		t.Fatal("sparse bytes changed")
	}
	for _, size := range []int64{1, int64(len(data)) + 1} {
		if _, _, err := snapshot(
			t.Context(),
			root,
			artifact{Name: "sparse", Size: size},
			filepath.Join(t.TempDir(), "copy"),
		); err == nil {
			t.Fatal("size change accepted")
		}
	}
	if _, _, err := snapshot(
		t.Context(),
		root,
		artifact{Name: "sparse", Size: 1},
		t.TempDir(),
	); err == nil {
		t.Fatal("directory overwritten")
	}
	if _, _, err := snapshot(
		t.Context(),
		root,
		artifact{Name: "missing", Size: 1},
		filepath.Join(t.TempDir(), "copy"),
	); err == nil {
		t.Fatal("missing source")
	}
	outside := filepath.Join(t.TempDir(), "disk.raw")
	must(t, os.WriteFile(outside, make([]byte, 1024), 0o600))
	if err := os.Symlink(outside, filepath.Join(filepath.Dir(o.Manifest), "link")); err == nil {
		m.Builds[0].Files = []artifact{{Name: "link/disk.raw", Size: 1024}}
		// A link to a directory and a link to a file must both remain confined.
		must(t, os.Remove(filepath.Join(filepath.Dir(o.Manifest), "link")))
		must(t, os.Symlink(filepath.Dir(outside), filepath.Join(filepath.Dir(o.Manifest), "link")))
		must(t, os.WriteFile(o.Manifest, encode(t, m), 0o600))
		if _, err := Import(t.Context(), o); err == nil {
			t.Fatal("escaped through symlink")
		}
	}
	if _, err := readMetadata(root, "."); err == nil {
		t.Fatal("directory as metadata")
	}
	must(
		t,
		os.WriteFile(
			filepath.Join(filepath.Dir(o.Manifest), "large"),
			make([]byte, maxMetadata+1),
			0o600,
		),
	)
	if _, err := readMetadata(root, "large"); err == nil {
		t.Fatal("unbounded metadata")
	}
	if err := validateBundle(pack.BundleFile{}, 512); err == nil {
		t.Fatal("invalid config accepted")
	}
	if err := validateBundle(pack.BundleFile{}, 513); err == nil {
		t.Fatal("unaligned disk accepted")
	}
}
