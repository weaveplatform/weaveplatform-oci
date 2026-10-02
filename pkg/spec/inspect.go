package spec

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ChunkLayer is one disk chunk layer with its parsed annotations.
type ChunkLayer struct {
	Index              int64
	Offset             int64
	Size               int64
	UncompressedDigest digest.Digest
	Zero               bool
	Descriptor         ocispec.Descriptor
}

// DiskLayout is a config disk entry with its chunk layers in index order.
type DiskLayout struct {
	Disk
	Chunks []ChunkLayer
}

// StateLayer is a config state entry with its layer descriptor.
type StateLayer struct {
	StateEntry
	Descriptor ocispec.Descriptor
}

// Description is what a consumer needs to decide whether it can use an image,
// computed from the manifest and config alone (no chunk fetch).
type Description struct {
	Digest      digest.Digest
	Size        int64
	Platform    ocispec.Platform
	Config      Config
	Disks       []DiskLayout
	State       []StateLayer
	TotalSize   int64 // sum of logical disk sizes
	FetchSize   int64 // bytes a cold pull downloads (config, state, non-zero chunks)
	Annotations map[string]string
}

// PlatformFor returns the index platform for a config (contract §9).
func PlatformFor(c Config) ocispec.Platform {
	p := ocispec.Platform{OS: c.Guest.OS, Architecture: c.Guest.Arch}
	if c.Guest.OS != OSLinux {
		p.OSVersion = c.Guest.OSVersion
	}
	return p
}

// GuestAnnotations returns the contract §8.1 weave annotations derived from a
// config. Manifests carry all of them; index children carry the guest set.
func GuestAnnotations(c Config) map[string]string {
	a := map[string]string{
		AnnotationOS:            c.Guest.OS,
		AnnotationArch:          c.Guest.Arch,
		AnnotationOSVersion:     c.Guest.OSVersion,
		AnnotationOSBuild:       c.Guest.OSBuild,
		AnnotationDiskTotalSize: strconv.FormatInt(c.TotalSize(), 10),
	}
	if c.Guest.Distro != "" {
		a[AnnotationDistro] = c.Guest.Distro
	}
	return a
}

// Inspect validates a manifest and its config blob against conformance rules
// 6 to 14 and returns the description. A non-nil error is a
// *ValidationError listing every problem; the description is still returned
// with whatever could be parsed.
func Inspect(manifestRaw, configRaw []byte) (Description, error) {
	var ps problems
	d := Description{Digest: digest.FromBytes(manifestRaw), Size: int64(len(manifestRaw))}

	var m ocispec.Manifest
	if err := json.Unmarshal(manifestRaw, &m); err != nil {
		ps.add(6, "manifest", "not JSON: %v", err)
		return d, ps.err()
	}
	d.Annotations = m.Annotations
	checkManifestHeader(m, &ps)
	checkConfigBlob(m.Config, configRaw, &ps)

	cfg, err := ParseConfig(configRaw)
	ps = append(ps, Problems(err)...)
	if err != nil && cfg.SchemaVersion == 0 {
		return d, ps.err() // schema failure: later rules need a config
	}
	d.Config = cfg
	d.Platform = PlatformFor(cfg)
	d.TotalSize = cfg.TotalSize()

	checkManifestAnnotations(m.Annotations, cfg, &ps)
	chunks, states := classifyLayers(m.Layers, &ps)
	d.Disks = checkDisks(cfg, chunks, &ps)
	d.State = checkState(cfg, states, &ps)
	checkDuplicates(m.Layers, &ps)

	d.FetchSize = m.Config.Size
	for _, dl := range d.Disks {
		for _, c := range dl.Chunks {
			if !c.Zero {
				d.FetchSize += c.Descriptor.Size
			}
		}
	}
	for _, s := range d.State {
		d.FetchSize += s.Descriptor.Size
	}
	return d, ps.err()
}

func checkManifestHeader(m ocispec.Manifest, ps *problems) {
	if m.SchemaVersion != 2 {
		ps.add(6, "schemaVersion", "is %d, want 2", m.SchemaVersion)
	}
	if m.MediaType != MediaTypeManifest {
		ps.add(6, "mediaType", "is %q, want %s", m.MediaType, MediaTypeManifest)
	}
	if m.ArtifactType != ArtifactType {
		ps.add(6, "artifactType", "is %q, want %s", m.ArtifactType, ArtifactType)
	}
	if m.Config.MediaType != MediaTypeConfig {
		ps.add(6, "config.mediaType", "is %q, want %s", m.Config.MediaType, MediaTypeConfig)
	}
}

func checkConfigBlob(desc ocispec.Descriptor, raw []byte, ps *problems) {
	if desc.Size > MaxConfigSize || int64(len(raw)) > MaxConfigSize {
		ps.add(7, "config", "exceeds %d bytes", MaxConfigSize)
	}
	if desc.Size != int64(len(raw)) {
		ps.add(7, "config.size", "descriptor says %d, blob is %d bytes", desc.Size, len(raw))
	}
	if desc.Digest != digest.FromBytes(raw) {
		ps.add(7, "config.digest", "does not match the blob")
	}
}

func checkManifestAnnotations(a map[string]string, c Config, ps *problems) {
	for _, k := range []string{AnnotationCreated, AnnotationVersion, AnnotationRevision, AnnotationSource} {
		if a[k] == "" {
			ps.add(10, "annotations", "%s is required", k)
		}
	}
	if a[AnnotationCreated] != "" && a[AnnotationCreated] != c.Build.Created {
		ps.add(
			10,
			"annotations",
			"%s %q differs from build.created %q",
			AnnotationCreated,
			a[AnnotationCreated],
			c.Build.Created,
		)
	}
	want := GuestAnnotations(c)
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		switch got, ok := a[k]; {
		case !ok:
			ps.add(10, "annotations", "%s is required", k)
		case got != want[k]:
			ps.add(10, "annotations", "%s is %q but the config says %q", k, got, want[k])
		}
	}
	if s := a[AnnotationDescription]; len(s) > MaxDescriptionLength {
		ps.add(
			10,
			"annotations",
			"%s is longer than %d characters",
			AnnotationDescription,
			MaxDescriptionLength,
		)
	}
}

func classifyLayers(
	layers []ocispec.Descriptor,
	ps *problems,
) (chunks map[string][]ChunkLayer, states map[string][]ocispec.Descriptor) {
	chunks = map[string][]ChunkLayer{}
	states = map[string][]ocispec.Descriptor{}
	for i, l := range layers {
		path := fmt.Sprintf("layers[%d]", i)
		if err := l.Digest.Validate(); err != nil || l.Size <= 0 {
			ps.add(11, path, "invalid descriptor (digest %q, size %d)", l.Digest, l.Size)
			continue
		}
		switch l.MediaType {
		case MediaTypeDiskChunk:
			if c, ok := parseChunk(l, path, ps); ok {
				name := l.Annotations[AnnotationDiskName]
				chunks[name] = append(chunks[name], c)
			}
		case MediaTypeAuxStorage, MediaTypeUEFIVars, MediaTypeFirmwarePolicy:
			name := l.Annotations[AnnotationStateName]
			states[name] = append(states[name], l)
		default:
			ps.add(11, path, "unknown layer media type %q", l.MediaType)
		}
	}
	return chunks, states
}

func parseChunk(l ocispec.Descriptor, path string, ps *problems) (ChunkLayer, bool) {
	a := l.Annotations
	c := ChunkLayer{Descriptor: l}
	ok := true
	if a[AnnotationDiskName] == "" {
		ps.add(12, path, "missing %s", AnnotationDiskName)
		ok = false
	}
	for key, dst := range map[string]*int64{AnnotationChunkIndex: &c.Index, AnnotationChunkOffset: &c.Offset, AnnotationChunkSize: &c.Size} {
		v, err := strconv.ParseInt(a[key], 10, 64)
		if err != nil || v < 0 {
			ps.add(12, path, "%s must be a non-negative decimal integer, got %q", key, a[key])
			ok = false
			continue
		}
		*dst = v
	}
	c.UncompressedDigest = digest.Digest(a[AnnotationChunkDigest])
	if err := c.UncompressedDigest.Validate(); err != nil {
		ps.add(12, path, "%s is not a digest: %q", AnnotationChunkDigest, a[AnnotationChunkDigest])
		ok = false
	}
	switch z, set := a[AnnotationChunkZero]; {
	case !set:
	case z == "true":
		c.Zero = true
	default:
		ps.add(12, path, "%s must be \"true\" or absent, got %q", AnnotationChunkZero, z)
		ok = false
	}
	return c, ok
}

func checkDisks(c Config, chunks map[string][]ChunkLayer, ps *problems) []DiskLayout {
	out := make([]DiskLayout, 0, len(c.Disks))
	known := map[string]bool{}
	for _, disk := range c.Disks {
		known[disk.Name] = true
		got := chunks[disk.Name]
		path := "disk " + disk.Name
		if int64(len(got)) != disk.ChunkCount {
			ps.add(12, path, "has %d chunk layers, config says %d", len(got), disk.ChunkCount)
		}
		var zeros int64
		for i, ch := range got {
			checkChunk(disk, int64(i), ch, path, ps)
			if ch.Zero {
				zeros++
			}
		}
		if zeros != disk.ZeroChunks {
			ps.add(12, path, "has %d zero chunks, config says %d", zeros, disk.ZeroChunks)
		}
		out = append(out, DiskLayout{Disk: disk, Chunks: got})
	}
	names := make([]string, 0, len(chunks))
	for name := range chunks {
		if !known[name] {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		ps.add(12, "disk "+name, "chunk layers reference a disk absent from the config")
	}
	return out
}

func checkChunk(disk Disk, pos int64, ch ChunkLayer, path string, ps *problems) {
	if ch.Index != pos {
		ps.add(
			12,
			path,
			"chunk at position %d has index %d; indexes must ascend from 0 without gaps",
			pos,
			ch.Index,
		)
		return
	}
	if want := ch.Index * ChunkSize; ch.Offset != want {
		ps.add(12, path, "chunk %d offset is %d, want %d", ch.Index, ch.Offset, want)
	}
	if want := ChunkLength(disk.LogicalSize, ch.Index); ch.Size != want {
		ps.add(12, path, "chunk %d size is %d, want %d", ch.Index, ch.Size, want)
	}
	if ch.Zero {
		comp, unc := ZeroChunkDigest(ch.Size)
		if ch.Descriptor.Digest != comp {
			ps.add(12, path, "zero chunk %d is not the canonical zero chunk", ch.Index)
		}
		if ch.UncompressedDigest != unc {
			ps.add(12, path, "zero chunk %d has the wrong uncompressed digest", ch.Index)
		}
	}
}

func checkState(c Config, states map[string][]ocispec.Descriptor, ps *problems) []StateLayer {
	out := make([]StateLayer, 0, len(c.State))
	known := map[string]bool{}
	for _, e := range c.State {
		known[e.Name] = true
		got := states[e.Name]
		path := "state " + e.Name
		switch {
		case len(got) == 0 && e.Required:
			ps.add(13, path, "required state layer is missing")
			continue
		case len(got) == 0:
			ps.add(13, path, "config entry has no layer")
			continue
		case len(got) > 1:
			ps.add(13, path, "has %d layers, want exactly one", len(got))
		}
		l := got[0]
		if l.MediaType != e.MediaType {
			ps.add(13, path, "layer media type %q differs from config %q", l.MediaType, e.MediaType)
		}
		if s := l.Annotations[AnnotationStateSemantics]; s != e.Semantics {
			ps.add(13, path, "layer semantics %q differs from config %q", s, e.Semantics)
		}
		out = append(out, StateLayer{StateEntry: e, Descriptor: l})
	}
	names := make([]string, 0, len(states))
	for name := range states {
		if !known[name] {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		ps.add(13, "state "+name, "state layer has no config entry")
	}
	return out
}

// checkDuplicates enforces rule 14: a blob may repeat (identical chunks, zero
// chunks) but every occurrence must describe the same content.
func checkDuplicates(layers []ocispec.Descriptor, ps *problems) {
	type content struct{ mediaType, size, digest, zero string }
	seen := map[digest.Digest]content{}
	for i, l := range layers {
		c := content{
			mediaType: l.MediaType,
			size:      l.Annotations[AnnotationChunkSize],
			digest:    l.Annotations[AnnotationChunkDigest],
			zero:      l.Annotations[AnnotationChunkZero],
		}
		if prev, ok := seen[l.Digest]; ok && prev != c {
			ps.add(
				14,
				fmt.Sprintf("layers[%d]", i),
				"blob %s appears with conflicting content annotations",
				l.Digest,
			)
		}
		seen[l.Digest] = c
	}
}
