// Package testbundle writes small, sparse, contract-shaped bundle directories
// for unit and acceptance tests.
package testbundle

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"

	"github.com/deploymenttheory/weaveplatform-oci/pkg/pack"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/spec"
)

// ContractSize is 1.25 GiB: two full chunks (the second all zero with the
// default DataAt) and a short third chunk.
const ContractSize = 2*spec.ChunkSize + spec.ChunkSize/2

// ErrOptions reports unusable options.
var ErrOptions = errors.New("testbundle: invalid options")

// Options describe the bundle to write.
type Options struct {
	OS   string // darwin, windows or linux
	Arch string // arm64 or amd64
	// Size is the disk0 logical size; default 8 MiB (one short chunk). Use
	// ContractSize for a disk with full 512 MiB chunks and a zero chunk.
	Size int64
	// DataAt lists offsets where 64 KiB of pseudo-random data is written;
	// default puts data in the first and last chunk so the middle chunk is zero.
	DataAt []int64
	// ExtraDisk adds a 1 MiB all-zero data disk named disk1 (one zero chunk).
	ExtraDisk bool
	// Seed makes the data deterministic.
	Seed uint64
	// Version is the org.opencontainers.image.version annotation.
	Version string
}

// Write creates dir and writes a bundle into it.
func Write(dir string, o Options) error {
	if o.OS == "" || o.Arch == "" {
		return fmt.Errorf("%w: os and arch are required", ErrOptions)
	}
	if o.Size == 0 {
		o.Size = 8 << 20
	}
	if o.DataAt == nil {
		o.DataAt = []int64{0, o.Size - 64<<10}
	}
	if o.Version == "" {
		o.Version = "1.0-test-r1"
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("testbundle: %w", err)
	}
	rng := rand.New(rand.NewPCG(o.Seed, 0x5eed)) //nolint:gosec // test data, not security
	if err := writeDisk(filepath.Join(dir, "disk0.img"), o.Size, o.DataAt, rng); err != nil {
		return err
	}
	f := pack.BundleFile{
		SchemaVersion: 1,
		Guest:         spec.Guest{OS: o.OS, Arch: o.Arch, OSVersion: "1.0", OSBuild: "1A1"},
		Firmware:      spec.Firmware{Type: "uefi", TPM: "none"},
		Resources: spec.Resources{
			CPU:    spec.MinDefault{Min: 1, Default: 2},
			Memory: spec.MinDefault{Min: 1 << 30, Default: 2 << 30},
		},
		Provisioning: spec.Provisioning{CredentialHint: "none"},
		Build: spec.Build{
			Template: "test", TemplateRef: "deploymenttheory/weaveplatform-oci@test",
			SourceMedia: []spec.SourceMedia{}, Created: "2026-10-02T00:00:00Z",
		},
		Disks: []pack.BundleDisk{{Name: "disk0", Role: "system", Path: "disk0.img"}},
		Annotations: map[string]string{
			spec.AnnotationVersion:  o.Version,
			spec.AnnotationRevision: "test",
			spec.AnnotationSource:   "https://github.com/deploymenttheory/weaveplatform-oci",
		},
	}
	switch o.OS {
	case spec.OSDarwin:
		f.Guest.OSVersion, f.Guest.OSBuild = "26.0", "25A354"
		f.Firmware = spec.Firmware{
			Type:          "apple",
			HardwareModel: base64.StdEncoding.EncodeToString([]byte("test-hardware-model")),
			TPM:           "none",
		}
		if err := writeRandom(filepath.Join(dir, "nvram.bin"), 32<<10, rng); err != nil {
			return err
		}
		f.State = []pack.BundleState{
			{
				Name:      spec.StateAuxStorage,
				Path:      "nvram.bin",
				Semantics: spec.SemanticsCarry,
				Required:  true,
			},
		}
	case spec.OSWindows:
		f.Guest.OSVersion, f.Guest.OSBuild, f.Guest.Edition = "10.0.26200.6584", "26200.6584", "Pro"
		f.Firmware = spec.Firmware{Type: "uefi", SecureBoot: true, TPM: "required"}
		if err := os.WriteFile(
			filepath.Join(dir, "firmware-policy.json"),
			[]byte(
				`{"schemaVersion":1,"secureBoot":true,"tpm":"required","generation":2}`,
			),
			0o600,
		); err != nil {
			return fmt.Errorf("testbundle: %w", err)
		}
		f.State = []pack.BundleState{
			{
				Name:      spec.StateFirmwarePolicy,
				Path:      "firmware-policy.json",
				Semantics: spec.SemanticsRegenerate,
				Required:  true,
			},
		}
	case spec.OSLinux:
		f.Guest.OSVersion, f.Guest.OSBuild, f.Guest.Distro = "24.04", "20260915", "ubuntu"
		f.Provisioning.CredentialHint = "cloud-init"
	default:
		return fmt.Errorf("%w: unknown os %q", ErrOptions, o.OS)
	}
	if o.ExtraDisk {
		if err := writeDisk(filepath.Join(dir, "disk1.img"), 1<<20, []int64{}, rng); err != nil {
			return err
		}
		f.Disks = append(f.Disks, pack.BundleDisk{Name: "disk1", Role: "data", Path: "disk1.img"})
	}
	return pack.WriteBundleFile(dir, f) //nolint:wrapcheck // pack names the file
}

func writeDisk(path string, size int64, dataAt []int64, rng *rand.Rand) error {
	fh, err := os.Create(path) //nolint:gosec // test helper writes where the caller asks
	if err != nil {
		return fmt.Errorf("testbundle: %w", err)
	}
	defer func() { _ = fh.Close() }()
	if err := fh.Truncate(size); err != nil {
		return fmt.Errorf("testbundle: %w", err)
	}
	buf := make([]byte, 64<<10)
	for _, off := range dataAt {
		fill(buf, rng)
		for i := range buf {
			if buf[i] == 0 {
				buf[i] = 1 // data chunks must never look like zero chunks
			}
		}
		n := min(int64(len(buf)), size-off)
		if n <= 0 || off < 0 {
			return fmt.Errorf("%w: data offset %d outside disk of %d bytes", ErrOptions, off, size)
		}
		if _, err := fh.WriteAt(buf[:n], off); err != nil {
			return fmt.Errorf("testbundle: %w", err)
		}
	}
	return nil
}

// fill writes pseudo-random bytes without integer narrowing.
func fill(b []byte, rng *rand.Rand) {
	var word [8]byte
	for i := 0; i < len(b); i += len(word) {
		binary.LittleEndian.PutUint64(word[:], rng.Uint64())
		copy(b[i:], word[:])
	}
}

func writeRandom(path string, n int, rng *rand.Rand) error {
	b := make([]byte, n)
	fill(b, rng)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("testbundle: %w", err)
	}
	return nil
}
