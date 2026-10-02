package spec_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/deploymenttheory/weaveplatform-oci/pkg/spec"
)

// validConfig returns a conformant config for a guest OS with one disk of
// 64 GiB plus 100 MiB (129 chunks, the last short), three of them zero.
func validConfig(osName string) spec.Config {
	const size = 64<<30 + 100<<20
	c := spec.Config{
		SchemaVersion: 1,
		Guest: spec.Guest{
			OS:        osName,
			Arch:      spec.ArchARM64,
			OSVersion: "26.0",
			OSBuild:   "25A354",
			Variant:   "vanilla",
		},
		Firmware: spec.Firmware{
			Type:          "apple",
			TPM:           "none",
			HardwareModel: base64.StdEncoding.EncodeToString([]byte("hw")),
			MinHostOS:     "15.0",
		},
		Disks: []spec.Disk{{
			Name: "disk0", Role: "system", LogicalSize: size, ChunkSize: spec.ChunkSize,
			ChunkCount: spec.ChunkCount(size), Compression: "zstd", ZeroChunks: 3,
		}},
		State: []spec.StateEntry{
			{
				Name:      spec.StateAuxStorage,
				MediaType: spec.MediaTypeAuxStorage,
				Semantics: spec.SemanticsCarry,
				Required:  true,
			},
		},
		Resources: spec.Resources{
			CPU:    spec.MinDefault{Min: 2, Default: 4},
			Memory: spec.MinDefault{Min: 4 << 30, Default: 8 << 30},
		},
		Provisioning: spec.Provisioning{
			DefaultUser:    "admin",
			CredentialHint: "set-at-first-boot",
			Agent:          &spec.Agent{Name: "weave-agent", Version: "0.2.0"},
		},
		Build: spec.Build{
			Template:    "macos-26-vanilla",
			TemplateRef: "deploymenttheory/weaveplatform-oci@0123abc",
			SourceMedia: []spec.SourceMedia{
				{
					Kind:   "ipsw",
					URI:    "https://updates.cdn-apple.com/UniversalMac_26.0_25A354_Restore.ipsw",
					Digest: "sha256:" + fmt.Sprintf("%064x", 1),
				},
			},
			Created: "2026-10-02T08:00:00Z",
		},
	}
	switch osName {
	case spec.OSWindows:
		c.Guest = spec.Guest{
			OS:        spec.OSWindows,
			Arch:      spec.ArchAMD64,
			OSVersion: "10.0.26200.6584",
			OSBuild:   "26200.6584",
			Edition:   "Pro",
			Variant:   "base",
		}
		c.Firmware = spec.Firmware{Type: "uefi", SecureBoot: true, TPM: "required"}
		c.State = []spec.StateEntry{
			{
				Name:      spec.StateUEFIVars,
				MediaType: spec.MediaTypeUEFIVars,
				Semantics: spec.SemanticsCarry,
				Required:  false,
			},
			{
				Name:      spec.StateFirmwarePolicy,
				MediaType: spec.MediaTypeFirmwarePolicy,
				Semantics: spec.SemanticsRegenerate,
				Required:  true,
			},
		}
		c.Build.Template, c.Build.SourceMedia[0].Kind = "windows-11-base", "iso"
		c.Build.SourceMedia[0].URI = "https://software.download.prss.microsoft.com/Win11_25H2_English_x64.iso"
	case spec.OSLinux:
		c.Guest = spec.Guest{
			OS:        spec.OSLinux,
			Arch:      spec.ArchARM64,
			OSVersion: "24.04",
			OSBuild:   "20260915",
			Distro:    "ubuntu",
		}
		c.Firmware = spec.Firmware{Type: "uefi", TPM: "none"}
		c.State = []spec.StateEntry{}
		c.Provisioning = spec.Provisioning{
			CredentialHint: "cloud-init",
			Agent:          &spec.Agent{Name: "weave-agent", Version: "0.2.0"},
		}
		c.Build.Template, c.Build.SourceMedia[0].Kind = "ubuntu-24.04", "cloud-image"
		c.Build.SourceMedia[0].URI = "https://cloud-images.ubuntu.com/noble/20260915/noble-server-cloudimg-arm64.img"
	}
	return c
}

func fakeDigest(s string) digest.Digest { return digest.FromString(s) }

// manifestFor builds the manifest a conformant producer would emit for cfg.
// Zero chunks are the last ZeroChunks full-size chunks before the final one.
func manifestFor(t testing.TB, cfg spec.Config) (ocispec.Manifest, []byte) {
	t.Helper()
	raw, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	m := ocispec.Manifest{
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    spec.MediaTypeManifest,
		ArtifactType: spec.ArtifactType,
		Config: ocispec.Descriptor{
			MediaType: spec.MediaTypeConfig,
			Digest:    digest.FromBytes(raw),
			Size:      int64(len(raw)),
		},
		Annotations: map[string]string{
			spec.AnnotationVersion:  "26.0-25A354-r1",
			spec.AnnotationRevision: "0123abc",
			spec.AnnotationSource:   "https://github.com/deploymenttheory/weaveplatform-oci",
			spec.AnnotationTitle:    cfg.Build.Template,
		},
	}
	for k, v := range spec.GuestAnnotations(cfg) {
		m.Annotations[k] = v
	}
	m.Annotations[spec.AnnotationCreated] = cfg.Build.Created
	for _, d := range cfg.Disks {
		firstZero := d.ChunkCount - 1 - d.ZeroChunks
		for i := range d.ChunkCount {
			n := spec.ChunkLength(d.LogicalSize, i)
			zero := i >= firstZero && i < d.ChunkCount-1
			comp, unc := fakeDigest(
				fmt.Sprintf("%s-c-%d", d.Name, i),
			), fakeDigest(
				fmt.Sprintf("%s-u-%d", d.Name, i),
			)
			size := int64(1000 + i)
			if zero {
				comp, unc = spec.ZeroChunkDigest(n)
				size = int64(len(spec.ZeroChunk(n)))
			}
			a := map[string]string{
				spec.AnnotationTitle:       fmt.Sprintf("%s.chunk.%06d", d.Name, i),
				spec.AnnotationDiskName:    d.Name,
				spec.AnnotationChunkIndex:  strconv.FormatInt(i, 10),
				spec.AnnotationChunkOffset: strconv.FormatInt(i*spec.ChunkSize, 10),
				spec.AnnotationChunkSize:   strconv.FormatInt(n, 10),
				spec.AnnotationChunkDigest: unc.String(),
			}
			if zero {
				a[spec.AnnotationChunkZero] = "true"
			}
			m.Layers = append(
				m.Layers,
				ocispec.Descriptor{
					MediaType:   spec.MediaTypeDiskChunk,
					Digest:      comp,
					Size:        size,
					Annotations: a,
				},
			)
		}
	}
	for _, s := range cfg.State {
		m.Layers = append(m.Layers, ocispec.Descriptor{
			MediaType: s.MediaType, Digest: fakeDigest("state-" + s.Name), Size: 4096,
			Annotations: map[string]string{
				spec.AnnotationTitle:          s.Name + ".bin",
				spec.AnnotationStateName:      s.Name,
				spec.AnnotationStateSemantics: s.Semantics,
			},
		})
	}
	return m, raw
}

func mustJSON(t testing.TB, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func indexFor(t testing.TB, children ...spec.Config) (ocispec.Index, []byte) {
	t.Helper()
	idx := ocispec.Index{
		Versioned: specs.Versioned{
			SchemaVersion: 2,
		},
		MediaType:    spec.MediaTypeIndex,
		ArtifactType: spec.ArtifactType,
		Annotations:  map[string]string{spec.AnnotationVersion: "26.0-25A354-r1"},
	}
	for _, c := range children {
		m, _ := manifestFor(t, c)
		raw := mustJSON(t, m)
		p := spec.PlatformFor(c)
		a := spec.GuestAnnotations(c)
		delete(a, spec.AnnotationDiskTotalSize)
		idx.Manifests = append(idx.Manifests, ocispec.Descriptor{
			MediaType: spec.MediaTypeManifest, ArtifactType: spec.ArtifactType,
			Digest: digest.FromBytes(raw), Size: int64(len(raw)), Platform: &p, Annotations: a,
		})
	}
	return idx, mustJSON(t, idx)
}
