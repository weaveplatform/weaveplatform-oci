package spec

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// Config is the weave guest config document (contract §4). Field order is
// fixed so that encoding a given value always yields the same bytes.
type Config struct {
	SchemaVersion int          `json:"schemaVersion"`
	Guest         Guest        `json:"guest"`
	Firmware      Firmware     `json:"firmware"`
	Disks         []Disk       `json:"disks"`
	State         []StateEntry `json:"state"`
	Resources     Resources    `json:"resources"`
	Provisioning  Provisioning `json:"provisioning"`
	Build         Build        `json:"build"`
}

// Guest identifies the installed operating system.
type Guest struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	OSVersion string `json:"osVersion"`
	OSBuild   string `json:"osBuild"`
	Edition   string `json:"edition,omitempty"`
	Variant   string `json:"variant,omitempty"`
	Distro    string `json:"distro,omitempty"`
}

// Firmware describes what the guest needs from the virtual platform.
type Firmware struct {
	Type          string `json:"type"`
	SecureBoot    bool   `json:"secureBoot"`
	TPM           string `json:"tpm"`
	HardwareModel string `json:"hardwareModel,omitempty"`
	MinHostOS     string `json:"minHostOS,omitempty"`
}

// Disk describes one guest disk published as chunk layers.
type Disk struct {
	Name        string `json:"name"`
	Role        string `json:"role"`
	LogicalSize int64  `json:"logicalSize"`
	ChunkSize   int64  `json:"chunkSize"`
	ChunkCount  int64  `json:"chunkCount"`
	Compression string `json:"compression"`
	ZeroChunks  int64  `json:"zeroChunks"`
}

// StateEntry describes one state layer and how consumers treat it.
type StateEntry struct {
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	Semantics string `json:"semantics"`
	Required  bool   `json:"required"`
}

// Resources are the minimum and default vCPU and memory.
type Resources struct {
	CPU    MinDefault `json:"cpu"`
	Memory MinDefault `json:"memory"`
}

// MinDefault is a minimum and a default value.
type MinDefault struct {
	Min     int64 `json:"min"`
	Default int64 `json:"default"`
}

// Provisioning tells consumers how to gain access to the guest.
type Provisioning struct {
	DefaultUser    string `json:"defaultUser,omitempty"`
	CredentialHint string `json:"credentialHint"`
	Agent          *Agent `json:"agent,omitempty"`
}

// Agent is the in-guest agent baked into the image.
type Agent struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Build records how the image was produced.
type Build struct {
	Template    string        `json:"template"`
	TemplateRef string        `json:"templateRef"`
	SourceMedia []SourceMedia `json:"sourceMedia"`
	Created     string        `json:"created"`
}

// SourceMedia is one input (IPSW, ISO, ESD, cloud image or bootc image).
type SourceMedia struct {
	Kind   string `json:"kind"`
	URI    string `json:"uri"`
	Digest string `json:"digest"`
}

// Marshal encodes the config deterministically. Nil slices are written as
// empty arrays because the schema requires arrays.
func (c Config) Marshal() ([]byte, error) {
	if c.State == nil {
		c.State = []StateEntry{}
	}
	if c.Disks == nil {
		c.Disks = []Disk{}
	}
	if c.Build.SourceMedia == nil {
		c.Build.SourceMedia = []SourceMedia{}
	}
	b, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	return b, nil
}

// TotalSize is the sum of the logical sizes of every disk.
func (c Config) TotalSize() int64 {
	var n int64
	for _, d := range c.Disks {
		n += d.LogicalSize
	}
	return n
}

// ParseConfig validates raw against the JSON Schema and the semantic rules
// and returns the decoded config. All problems are reported as rule 8
// (schema) or rule 9 (semantics).
func ParseConfig(raw []byte) (Config, error) {
	var ps problems
	if err := validateSchema(raw); err != nil {
		ps.add(8, "config", "%v", err)
		return Config{}, ps.err()
	}
	var c Config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		ps.add(8, "config", "decode: %v", err)
		return Config{}, ps.err()
	}
	if err := Validate(c); err != nil {
		return c, err
	}
	return c, nil
}

// Validate checks the semantic rules the JSON Schema cannot express
// (contract §4.3 and §12 rule 9).
func Validate(c Config) error {
	var ps problems
	validateFirmware(c, &ps)
	validateDisks(c, &ps)
	validateState(c, &ps)
	if c.Resources.CPU.Min > c.Resources.CPU.Default {
		ps.add(
			9,
			"resources.cpu",
			"min %d exceeds default %d",
			c.Resources.CPU.Min,
			c.Resources.CPU.Default,
		)
	}
	if c.Resources.Memory.Min > c.Resources.Memory.Default {
		ps.add(
			9,
			"resources.memory",
			"min %d exceeds default %d",
			c.Resources.Memory.Min,
			c.Resources.Memory.Default,
		)
	}
	if _, err := time.Parse(time.RFC3339, c.Build.Created); err != nil {
		ps.add(9, "build.created", "not RFC 3339: %q", c.Build.Created)
	}
	if c.Guest.OS == OSLinux && c.Guest.Distro == "" {
		ps.add(9, "guest.distro", "required for linux guests")
	}
	return ps.err()
}

func validateFirmware(c Config, ps *problems) {
	darwin := c.Guest.OS == OSDarwin
	switch {
	case darwin && c.Firmware.Type != "apple":
		ps.add(9, "firmware.type", "darwin guests require apple firmware, got %q", c.Firmware.Type)
	case !darwin && c.Firmware.Type == "apple":
		ps.add(9, "firmware.type", "apple firmware is only valid for darwin guests")
	}
	switch {
	case darwin && c.Firmware.HardwareModel == "":
		ps.add(9, "firmware.hardwareModel", "required for darwin guests")
	case !darwin && c.Firmware.HardwareModel != "":
		ps.add(9, "firmware.hardwareModel", "must be absent for %s guests", c.Guest.OS)
	case darwin:
		if _, err := base64.StdEncoding.DecodeString(c.Firmware.HardwareModel); err != nil {
			ps.add(9, "firmware.hardwareModel", "not valid base64")
		}
	}
}

func validateDisks(c Config, ps *problems) {
	if len(c.Disks) == 0 {
		ps.add(9, "disks", "at least one disk is required")
		return
	}
	if c.Disks[0].Role != "system" {
		ps.add(9, "disks[0].role", "the first disk is the boot disk and must have role system")
	}
	seen := map[string]bool{}
	for i, d := range c.Disks {
		path := fmt.Sprintf("disks[%d]", i)
		if seen[d.Name] {
			ps.add(9, path+".name", "duplicate disk name %q", d.Name)
		}
		seen[d.Name] = true
		if d.ChunkSize != ChunkSize {
			ps.add(9, path+".chunkSize", "must be %d", ChunkSize)
		}
		if want := ChunkCount(d.LogicalSize); d.ChunkCount != want {
			ps.add(
				9,
				path+".chunkCount",
				"is %d, want ceil(%d/%d) = %d",
				d.ChunkCount,
				d.LogicalSize,
				ChunkSize,
				want,
			)
		}
		if d.ZeroChunks < 0 || d.ZeroChunks > d.ChunkCount {
			ps.add(9, path+".zeroChunks", "%d is outside 0..%d", d.ZeroChunks, d.ChunkCount)
		}
	}
}

func validateState(c Config, ps *problems) {
	seen := map[string]bool{}
	for i, s := range c.State {
		path := fmt.Sprintf("state[%d]", i)
		if seen[s.Name] {
			ps.add(9, path+".name", "duplicate state entry %q", s.Name)
		}
		seen[s.Name] = true
		if want, ok := StateMediaType(s.Name); ok && s.MediaType != want {
			ps.add(9, path+".mediaType", "%q requires media type %s", s.Name, want)
		}
		if s.Name == StateFirmwarePolicy && s.Semantics != SemanticsRegenerate {
			ps.add(9, path+".semantics", "firmware-policy is always regenerate")
		}
		if s.Name == StateAuxStorage && s.Semantics != SemanticsCarry {
			ps.add(9, path+".semantics", "auxstorage is always carry")
		}
	}
	if c.Guest.OS == OSDarwin {
		ok := false
		for _, s := range c.State {
			if s.Name == StateAuxStorage && s.Required {
				ok = true
			}
		}
		if !ok {
			ps.add(9, "state", "darwin guests require a required auxstorage entry")
		}
	}
}
