package spec_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/deploymenttheory/weaveplatform-oci/pkg/spec"
)

func TestMediaTypeStringsAreTheContract(t *testing.T) {
	// Byte-exact strings from docs/research/09-artifact-contract-v1.md §2.
	want := map[string]string{
		spec.MediaTypeConfig:         "application/vnd.weave.guest.config.v1+json",
		spec.MediaTypeDiskChunk:      "application/vnd.weave.guest.disk.v1.raw+zstd",
		spec.MediaTypeAuxStorage:     "application/vnd.weave.guest.state.auxstorage.v1",
		spec.MediaTypeUEFIVars:       "application/vnd.weave.guest.state.uefivars.v1",
		spec.MediaTypeFirmwarePolicy: "application/vnd.weave.guest.state.firmware-policy.v1+json",
		spec.MediaTypeSeed:           "application/vnd.weave.guest.state.seed.v1+tar",
	}
	for got, w := range want {
		if got != w {
			t.Errorf("media type %q, want %q", got, w)
		}
	}
	if spec.ArtifactType != spec.MediaTypeConfig {
		t.Error("artifactType must equal the config media type")
	}
	if spec.ChunkSize != 536870912 {
		t.Errorf("chunk size %d", spec.ChunkSize)
	}
}

func TestStateMediaType(t *testing.T) {
	for name, want := range map[string]string{
		spec.StateAuxStorage: spec.MediaTypeAuxStorage, spec.StateUEFIVars: spec.MediaTypeUEFIVars,
		spec.StateFirmwarePolicy: spec.MediaTypeFirmwarePolicy,
	} {
		if got, ok := spec.StateMediaType(name); !ok || got != want {
			t.Errorf("%s: %q %v", name, got, ok)
		}
	}
	if _, ok := spec.StateMediaType("seed"); ok {
		t.Error("seed must not be a v1 state")
	}
}

func TestChunkArithmetic(t *testing.T) {
	cases := []struct{ size, count, last int64 }{
		{
			1,
			1,
			1,
		},
		{spec.ChunkSize, 1, spec.ChunkSize},
		{spec.ChunkSize + 1, 2, 1},
		{3 * spec.ChunkSize, 3, spec.ChunkSize},
	}
	for _, c := range cases {
		if n := spec.ChunkCount(c.size); n != c.count {
			t.Errorf("ChunkCount(%d) = %d, want %d", c.size, n, c.count)
		}
		if l := spec.ChunkLength(c.size, c.count-1); l != c.last {
			t.Errorf("last chunk of %d = %d, want %d", c.size, l, c.last)
		}
	}
	if spec.ChunkCount(0) != 0 || spec.ChunkCount(-5) != 0 {
		t.Error("non-positive size must have no chunks")
	}
}

func TestZeroChunkDecodesAndMatchesConstants(t *testing.T) {
	if spec.ZeroChunk(0) != nil || spec.ZeroChunk(-1) != nil {
		t.Fatal("non-positive length must yield nil")
	}
	for _, n := range []int64{1, 4095, 131072, 131073, 3 << 20} {
		dec, err := zstd.NewReader(bytes.NewReader(spec.ZeroChunk(n)))
		if err != nil {
			t.Fatal(err)
		}
		out, err := io.ReadAll(dec)
		dec.Close()
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if int64(len(out)) != n || !bytes.Equal(out, make([]byte, n)) {
			t.Fatalf("n=%d decoded %d bytes", n, len(out))
		}
		var h zstd.Header
		if err := h.Decode(
			spec.ZeroChunk(n),
		); err != nil || !h.HasFCS ||
			h.FrameContentSize != uint64(n) {
			t.Fatalf("n=%d header %+v %v", n, h, err)
		}
		c, u := spec.ZeroChunkDigest(n)
		if c != digest.FromBytes(spec.ZeroChunk(n)) || u != digest.FromBytes(make([]byte, n)) {
			t.Fatalf("n=%d digests wrong", n)
		}
		c2, u2 := spec.ZeroChunkDigest(n) // cached path
		if c2 != c || u2 != u {
			t.Fatal("cache returned different digests")
		}
	}
	c, u := spec.ZeroChunkDigest(spec.ChunkSize)
	if c != spec.ZeroChunkCompressedDigest || u != spec.ZeroChunkUncompressedDigest {
		t.Fatalf("full-size zero chunk digests %s %s differ from the published constants", c, u)
	}
	if got := len(spec.ZeroChunk(spec.ChunkSize)); got != 14+4096*4 {
		t.Fatalf("full zero chunk is %d bytes", got)
	}
}

func TestSchemaIsACopy(t *testing.T) {
	a := spec.Schema()
	a[0] = 'X'
	if spec.Schema()[0] == 'X' {
		t.Fatal("Schema must return a copy")
	}
	var v map[string]any
	if err := json.Unmarshal(spec.Schema(), &v); err != nil || v["$id"] != spec.SchemaID {
		t.Fatalf("schema id %v %v", v["$id"], err)
	}
}

func TestConfigMarshalIsDeterministicAndRoundTrips(t *testing.T) {
	for _, osName := range []string{spec.OSDarwin, spec.OSWindows, spec.OSLinux} {
		c := validConfig(osName)
		a, err := c.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := c.Marshal()
		if !bytes.Equal(a, b) {
			t.Fatal("marshal not deterministic")
		}
		back, err := spec.ParseConfig(a)
		if err != nil {
			t.Fatalf("%s: %v", osName, err)
		}
		again, _ := back.Marshal()
		if !bytes.Equal(a, again) {
			t.Fatalf("%s: round trip changed bytes", osName)
		}
	}
	empty, err := spec.Config{}.Marshal()
	if err != nil || !strings.Contains(string(empty), `"state":[]`) ||
		!strings.Contains(string(empty), `"disks":[]`) ||
		!strings.Contains(string(empty), `"sourceMedia":[]`) {
		t.Fatalf("nil slices must encode as arrays: %s %v", empty, err)
	}
	if got := validConfig(spec.OSLinux).TotalSize(); got != 64<<30+100<<20 {
		t.Fatalf("total size %d", got)
	}
}

func configJSON(t *testing.T, c spec.Config, edit func(map[string]any)) []byte {
	t.Helper()
	raw, _ := c.Marshal()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(m)
	}
	return mustJSON(t, m)
}

func TestParseConfigRejectsForbiddenAndMalformedContent(t *testing.T) {
	sub := func(m map[string]any, k string) map[string]any { return m[k].(map[string]any) }
	cases := map[string]func(map[string]any){
		"ecid":              func(m map[string]any) { sub(m, "firmware")["ecid"] = "AAAA" },
		"machineIdentifier": func(m map[string]any) { m["machineIdentifier"] = "AAAA" },
		"macAddress":        func(m map[string]any) { m["macAddress"] = "00:11:22:33:44:55" },
		"display":           func(m map[string]any) { sub(m, "resources")["display"] = "1920x1080" },
		"bad os":            func(m map[string]any) { sub(m, "guest")["os"] = "macos" },
		"bad arch":          func(m map[string]any) { sub(m, "guest")["arch"] = "aarch64" },
		"chunk size":        func(m map[string]any) { m["disks"].([]any)[0].(map[string]any)["chunkSize"] = 1024 },
		"schema version":    func(m map[string]any) { m["schemaVersion"] = 2 },
		"bad digest":        func(m map[string]any) { sub(m, "build")["sourceMedia"].([]any)[0].(map[string]any)["digest"] = "md5:x" },
		"bad date":          func(m map[string]any) { sub(m, "build")["created"] = "yesterday" },
		"bad base64":        func(m map[string]any) { sub(m, "firmware")["hardwareModel"] = "!!!" },
		"no disks":          func(m map[string]any) { m["disks"] = []any{} },
		"missing guest":     func(m map[string]any) { delete(m, "guest") },
		"linux no distro": func(m map[string]any) {
			sub(m, "guest")["os"] = "linux"
			sub(m, "firmware")["type"] = "uefi"
			delete(sub(m, "firmware"), "hardwareModel")
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := spec.ParseConfig(configJSON(t, validConfig(spec.OSDarwin), edit))
			var ve *spec.ValidationError
			if !errors.As(err, &ve) || !errors.Is(err, spec.ErrInvalid) || !ve.HasRule(8) {
				t.Fatalf("want rule 8, got %v", err)
			}
		})
	}
	if _, err := spec.ParseConfig([]byte("{")); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}

func TestValidateSemanticRules(t *testing.T) {
	cases := map[string]func(*spec.Config){
		"darwin needs apple firmware": func(c *spec.Config) { c.Firmware.Type = "uefi" },
		"apple firmware only darwin":  func(c *spec.Config) { c.Guest.OS = spec.OSWindows; c.Firmware.HardwareModel = "" },
		"darwin needs hardware model": func(c *spec.Config) { c.Firmware.HardwareModel = "" },
		"hardware model not base64":   func(c *spec.Config) { c.Firmware.HardwareModel = "%%%" },
		"no hardware model elsewhere": func(c *spec.Config) { c.Guest.OS = spec.OSLinux; c.Guest.Distro = "x"; c.Firmware.Type = "uefi" },
		"chunk count":                 func(c *spec.Config) { c.Disks[0].ChunkCount++ },
		"chunk size":                  func(c *spec.Config) { c.Disks[0].ChunkSize = 1 },
		"zero chunks":                 func(c *spec.Config) { c.Disks[0].ZeroChunks = 1000 },
		"negative zero chunks":        func(c *spec.Config) { c.Disks[0].ZeroChunks = -1 },
		"boot disk role":              func(c *spec.Config) { c.Disks[0].Role = "data" },
		"no disks":                    func(c *spec.Config) { c.Disks = nil },
		"duplicate disk": func(c *spec.Config) {
			c.Disks = append(c.Disks, c.Disks[0])
		},
		"cpu min > default":    func(c *spec.Config) { c.Resources.CPU.Min = 99 },
		"memory min > default": func(c *spec.Config) { c.Resources.Memory.Min = 1 << 40 },
		"created":              func(c *spec.Config) { c.Build.Created = "not a time" },
		"linux distro":         func(c *spec.Config) { *c = validConfig(spec.OSLinux); c.Guest.Distro = "" },
		"state media type":     func(c *spec.Config) { c.State[0].MediaType = spec.MediaTypeUEFIVars },
		"duplicate state":      func(c *spec.Config) { c.State = append(c.State, c.State[0]) },
		"auxstorage carry":     func(c *spec.Config) { c.State[0].Semantics = spec.SemanticsRegenerate },
		"darwin auxstorage":    func(c *spec.Config) { c.State = nil },
		"firmware-policy regen": func(c *spec.Config) {
			*c = validConfig(spec.OSWindows)
			c.State[1].Semantics = spec.SemanticsCarry
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := validConfig(spec.OSDarwin)
			mutate(&c)
			err := spec.Validate(c)
			var ve *spec.ValidationError
			if !errors.As(err, &ve) || !ve.HasRule(9) {
				t.Fatalf("want rule 9, got %v", err)
			}
		})
	}
	for _, osName := range []string{spec.OSDarwin, spec.OSWindows, spec.OSLinux} {
		if err := spec.Validate(validConfig(osName)); err != nil {
			t.Errorf("%s: %v", osName, err)
		}
	}
}

func TestProblemsFormatting(t *testing.T) {
	p := spec.Problem{Rule: 3, Message: "m"}
	if p.String() != "rule 3: m" {
		t.Fatal(p.String())
	}
	p.Path = "x"
	if p.String() != "rule 3: x: m" {
		t.Fatal(p.String())
	}
	ve := &spec.ValidationError{Problems: []spec.Problem{p, {Rule: 4, Message: "n"}}}
	if !strings.Contains(ve.Error(), "rule 3: x: m; rule 4: n") || ve.HasRule(9) {
		t.Fatal(ve.Error())
	}
	if spec.Problems(nil) != nil || spec.Problems(io.EOF) != nil || len(spec.Problems(ve)) != 2 {
		t.Fatal("Problems extraction")
	}
}

func TestPlatformAndGuestAnnotations(t *testing.T) {
	d := spec.PlatformFor(validConfig(spec.OSDarwin))
	if d.OS != "darwin" || d.Architecture != "arm64" || d.OSVersion != "26.0" {
		t.Fatalf("%+v", d)
	}
	l := spec.PlatformFor(validConfig(spec.OSLinux))
	if l.OSVersion != "" {
		t.Fatal("linux platforms carry no os.version")
	}
	a := spec.GuestAnnotations(validConfig(spec.OSLinux))
	if a[spec.AnnotationDistro] != "ubuntu" || a[spec.AnnotationDiskTotalSize] != "68824334336" {
		t.Fatalf("%v", a)
	}
	if _, ok := spec.GuestAnnotations(validConfig(spec.OSDarwin))[spec.AnnotationDistro]; ok {
		t.Fatal("distro only for linux")
	}
}

func TestInspectValidManifests(t *testing.T) {
	for _, osName := range []string{spec.OSDarwin, spec.OSWindows, spec.OSLinux} {
		cfg := validConfig(osName)
		m, craw := manifestFor(t, cfg)
		raw := mustJSON(t, m)
		d, err := spec.Inspect(raw, craw)
		if err != nil {
			t.Fatalf("%s: %v", osName, err)
		}
		if d.Digest != digest.FromBytes(raw) || d.Size != int64(len(raw)) ||
			d.TotalSize != cfg.TotalSize() {
			t.Fatalf("%s: description %+v", osName, d)
		}
		if len(d.Disks) != 1 || int64(len(d.Disks[0].Chunks)) != cfg.Disks[0].ChunkCount ||
			len(d.State) != len(cfg.State) {
			t.Fatalf("%s: layout %+v", osName, d.Disks)
		}
		var want int64 = m.Config.Size
		for _, l := range m.Layers {
			if l.Annotations[spec.AnnotationChunkZero] != "true" {
				want += l.Size
			}
		}
		if d.FetchSize != want {
			t.Fatalf("%s: fetch size %d, want %d", osName, d.FetchSize, want)
		}
	}
}

func TestInspectRules(t *testing.T) {
	type mut func(m *ocispec.Manifest, cfg *[]byte)
	setAnn := func(i int, k, v string) mut {
		return func(m *ocispec.Manifest, _ *[]byte) {
			if v == "" {
				delete(m.Layers[i].Annotations, k)
				return
			}
			m.Layers[i].Annotations[k] = v
		}
	}
	cases := map[string]struct {
		rule int
		mut  mut
	}{
		"schema version": {6, func(m *ocispec.Manifest, _ *[]byte) { m.SchemaVersion = 1 }},
		"media type": {6, func(m *ocispec.Manifest, _ *[]byte) {
			m.MediaType = "application/vnd.docker.distribution.manifest.v2+json"
		}},
		"artifact type": {6, func(m *ocispec.Manifest, _ *[]byte) { m.ArtifactType = "" }},
		"config type": {
			6,
			func(m *ocispec.Manifest, _ *[]byte) { m.Config.MediaType = "application/vnd.oci.image.config.v1+json" },
		},
		"config digest": {
			7,
			func(m *ocispec.Manifest, _ *[]byte) { m.Config.Digest = fakeDigest("other") },
		},
		"config size": {7, func(m *ocispec.Manifest, _ *[]byte) { m.Config.Size++ }},
		"config too big": {
			7,
			func(m *ocispec.Manifest, _ *[]byte) { m.Config.Size = spec.MaxConfigSize + 1 },
		},
		"config schema": {8, func(_ *ocispec.Manifest, c *[]byte) {
			*c = bytes.Replace(
				*c,
				[]byte(`"schemaVersion":1`),
				[]byte(`"schemaVersion":1,"ecid":"x"`),
				1,
			)
		}},
		"config semantics": {9, func(_ *ocispec.Manifest, c *[]byte) {
			*c = bytes.Replace(*c, []byte(`"min":2`), []byte(`"min":20`), 1)
		}},
		"missing created": {
			10,
			func(m *ocispec.Manifest, _ *[]byte) { delete(m.Annotations, spec.AnnotationCreated) },
		},
		"created differs": {
			10,
			func(m *ocispec.Manifest, _ *[]byte) { m.Annotations[spec.AnnotationCreated] = "2020-01-01T00:00:00Z" },
		},
		"missing version": {
			10,
			func(m *ocispec.Manifest, _ *[]byte) { delete(m.Annotations, spec.AnnotationVersion) },
		},
		"os disagrees": {
			10,
			func(m *ocispec.Manifest, _ *[]byte) { m.Annotations[spec.AnnotationOS] = "linux" },
		},
		"missing osBuild": {
			10,
			func(m *ocispec.Manifest, _ *[]byte) { delete(m.Annotations, spec.AnnotationOSBuild) },
		},
		"long description": {10, func(m *ocispec.Manifest, _ *[]byte) {
			m.Annotations[spec.AnnotationDescription] = strings.Repeat("x", 513)
		}},
		"unknown layer": {
			11,
			func(m *ocispec.Manifest, _ *[]byte) { m.Layers[0].MediaType = "application/octet-stream" },
		},
		"seed layer": {
			11,
			func(m *ocispec.Manifest, _ *[]byte) { m.Layers[0].MediaType = spec.MediaTypeSeed },
		},
		"bad layer digest": {
			11,
			func(m *ocispec.Manifest, _ *[]byte) { m.Layers[0].Digest = "sha256:nothex" },
		},
		"zero layer size":   {11, func(m *ocispec.Manifest, _ *[]byte) { m.Layers[0].Size = 0 }},
		"missing disk name": {12, setAnn(0, spec.AnnotationDiskName, "")},
		"bad index":         {12, setAnn(0, spec.AnnotationChunkIndex, "x")},
		"gap":               {12, setAnn(1, spec.AnnotationChunkIndex, "7")},
		"offset":            {12, setAnn(1, spec.AnnotationChunkOffset, "1")},
		"size":              {12, setAnn(1, spec.AnnotationChunkSize, "1")},
		"bad digest":        {12, setAnn(1, spec.AnnotationChunkDigest, "nope")},
		"bad zero":          {12, setAnn(1, spec.AnnotationChunkZero, "yes")},
		"zero count":        {12, setAnn(1, spec.AnnotationChunkZero, "true")},
		"missing chunk": {
			12,
			func(m *ocispec.Manifest, _ *[]byte) { m.Layers = m.Layers[1:] },
		},
		"unknown disk": {12, setAnn(0, spec.AnnotationDiskName, "disk9")},
		"non-canonical zero": {
			12,
			func(m *ocispec.Manifest, _ *[]byte) { m.Layers[127].Digest = fakeDigest("z") },
		},
		"zero unc digest": {
			12,
			setAnn(127, spec.AnnotationChunkDigest, fakeDigest("u").String()),
		},
		"missing state": {
			13,
			func(m *ocispec.Manifest, _ *[]byte) { m.Layers = m.Layers[:len(m.Layers)-1] },
		},
		"state semantics": {13, func(m *ocispec.Manifest, _ *[]byte) {
			m.Layers[len(m.Layers)-1].Annotations[spec.AnnotationStateSemantics] = "regenerate"
		}},
		"state without entry": {13, func(m *ocispec.Manifest, _ *[]byte) {
			m.Layers[len(m.Layers)-1].Annotations[spec.AnnotationStateName] = "uefivars"
		}},
		"state twice": {13, func(m *ocispec.Manifest, _ *[]byte) {
			m.Layers = append(m.Layers, m.Layers[len(m.Layers)-1])
		}},
		"state media type": {
			13,
			func(m *ocispec.Manifest, _ *[]byte) { m.Layers[len(m.Layers)-1].MediaType = spec.MediaTypeUEFIVars },
		},
		"conflicting blob": {
			14,
			func(m *ocispec.Manifest, _ *[]byte) { m.Layers[1].Digest = m.Layers[0].Digest },
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m, craw := manifestFor(t, validConfig(spec.OSDarwin))
			if tc.rule != 7 {
				// edit the config bytes first, then keep the descriptor honest
				tc.mut(&m, &craw)
				m.Config.Digest, m.Config.Size = digest.FromBytes(craw), int64(len(craw))
			} else {
				tc.mut(&m, &craw) // rule 7 cases deliberately break the descriptor
			}
			_, err := spec.Inspect(mustJSON(t, m), craw)
			var ve *spec.ValidationError
			if !errors.As(err, &ve) || !ve.HasRule(tc.rule) {
				t.Fatalf("want rule %d, got %v", tc.rule, err)
			}
		})
	}
	if _, err := spec.Inspect([]byte("{"), nil); !errors.Is(err, spec.ErrInvalid) {
		t.Fatal("non-JSON manifest accepted")
	}
}

func TestInspectWindowsOptionalStateMissing(t *testing.T) {
	m, craw := manifestFor(t, validConfig(spec.OSWindows))
	// drop the optional uefivars layer: still a rule 13 problem (entry with no layer)
	var kept []ocispec.Descriptor
	for _, l := range m.Layers {
		if l.MediaType != spec.MediaTypeUEFIVars {
			kept = append(kept, l)
		}
	}
	m.Layers = kept
	_, err := spec.Inspect(mustJSON(t, m), craw)
	var ve *spec.ValidationError
	if !errors.As(err, &ve) || !ve.HasRule(13) {
		t.Fatalf("got %v", err)
	}
}

func TestInspectIndexAndSelect(t *testing.T) {
	arm := validConfig(spec.OSLinux)
	amd := validConfig(spec.OSLinux)
	amd.Guest.Arch = spec.ArchAMD64
	idx, raw := indexFor(t, arm, amd)
	kids, err := spec.InspectIndex(raw)
	if err != nil || len(kids) != 2 {
		t.Fatalf("%v %d", err, len(kids))
	}
	got, err := spec.SelectChild(idx, ocispec.Platform{OS: "linux", Architecture: "amd64"})
	if err != nil || got.Digest != idx.Manifests[1].Digest {
		t.Fatalf("select %v", err)
	}
	dIdx, _ := indexFor(t, validConfig(spec.OSDarwin))
	if _, err := spec.SelectChild(
		dIdx,
		ocispec.Platform{OS: "darwin", Architecture: "arm64", OSVersion: "26.0"},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := spec.SelectChild(
		dIdx,
		ocispec.Platform{OS: "darwin", Architecture: "arm64", OSVersion: "27.0"},
	); !errors.Is(
		err,
		spec.ErrNoMatchingPlatform,
	) {
		t.Fatal("os.version filter ignored")
	}
	idx.Manifests = append(idx.Manifests, ocispec.Descriptor{})
	if _, err := spec.SelectChild(
		idx,
		ocispec.Platform{OS: "windows", Architecture: "amd64"},
	); !errors.Is(
		err,
		spec.ErrNoMatchingPlatform,
	) {
		t.Fatal("expected no match")
	}
}

func TestInspectIndexRules(t *testing.T) {
	cases := map[string]struct {
		rule int
		mut  func(*ocispec.Index)
	}{
		"media type": {
			1,
			func(i *ocispec.Index) { i.MediaType = "application/vnd.docker.distribution.manifest.list.v2+json" },
		},
		"artifact type": {1, func(i *ocispec.Index) { i.ArtifactType = "" }},
		"empty":         {2, func(i *ocispec.Index) { i.Manifests = nil }},
		"child type":    {2, func(i *ocispec.Index) { i.Manifests[0].ArtifactType = "x" }},
		"no platform":   {2, func(i *ocispec.Index) { i.Manifests[0].Platform = nil }},
		"bad platform": {
			2,
			func(i *ocispec.Index) { i.Manifests[0].Platform.Architecture = "x86_64" },
		},
		"duplicate": {
			3,
			func(i *ocispec.Index) { i.Manifests = append(i.Manifests, i.Manifests[0]) },
		},
		"annotation": {
			4,
			func(i *ocispec.Index) { i.Manifests[0].Annotations[spec.AnnotationArch] = "amd64" },
		},
		"mixed os family": {5, func(i *ocispec.Index) {
			w := *i.Manifests[0].Platform
			w.OS = spec.OSWindows
			c := i.Manifests[0]
			c.Platform = &w
			c.Annotations = map[string]string{
				spec.AnnotationOS:   spec.OSWindows,
				spec.AnnotationArch: w.Architecture,
			}
			i.Manifests = append(i.Manifests, c)
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			idx, _ := indexFor(t, validConfig(spec.OSLinux))
			tc.mut(&idx)
			_, err := spec.InspectIndex(mustJSON(t, idx))
			var ve *spec.ValidationError
			if !errors.As(err, &ve) || !ve.HasRule(tc.rule) {
				t.Fatalf("want rule %d, got %v", tc.rule, err)
			}
		})
	}
	if _, err := spec.InspectIndex([]byte("[")); !errors.Is(err, spec.ErrInvalid) {
		t.Fatal("non-JSON index accepted")
	}
}
