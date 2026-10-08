// Package handoff imports completed Imageweave/Packer candidates without running builders.
package handoff

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"time"

	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

var ErrInput = errors.New("invalid Imageweave handoff")

var (
	sha    = regexp.MustCompile(`^[a-f0-9]{64}$`)
	commit = regexp.MustCompile(`^[a-f0-9]{40}$`)
)

// Options names completed producer output and explicit publication metadata.
// RecipeCommit is a producer claim, not an authenticated build attestation.
type Options struct {
	Manifest, Out, RecipeCommit, SourceURI, Version string
	ParentLayout, ParentRef, ParentName             string
	Log                                             io.Writer
}

// File binds the exact bytes copied into the independent OCI bundle.
type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

// Receipt is the versioned, unqualified handoff audit record. It grants no admission.
type Receipt struct {
	SchemaVersion  int             `json:"schemaVersion"`
	Producer       string          `json:"producer"`
	RecipeCommit   string          `json:"recipeCommit"`
	ManifestDigest string          `json:"manifestDigest"`
	RunID          string          `json:"runID"`
	Builder        string          `json:"builder"`
	Qualification  string          `json:"qualification"`
	Bundle         pack.BundleFile `json:"bundle"`
	Files          []File          `json:"files"`
}

type manifest struct {
	Builds  []build `json:"builds"`
	LastRun string  `json:"last_run_uuid"`
}

type build struct {
	Name       string            `json:"name"`
	Builder    string            `json:"builder_type"`
	Time       int64             `json:"build_time"`
	Files      []artifact        `json:"files"`
	ArtifactID string            `json:"artifact_id"`
	RunID      string            `json:"packer_run_uuid"`
	Data       map[string]string `json:"custom_data"`
}

type artifact struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

func decode(raw []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return fmt.Errorf("%w: JSON: %w", ErrInput, err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("%w: exactly one JSON document required", ErrInput)
	}
	return nil
}

func selectBuild(raw []byte, o Options) (build, error) {
	var m manifest
	if err := decode(raw, &m); err != nil {
		return build{}, err
	}
	u, err := url.Parse(o.SourceURI)
	parentSource := o.SourceURI == "" && o.ParentLayout != "" && o.ParentRef != "" &&
		o.ParentName != ""
	if !commit.MatchString(o.RecipeCommit) || o.Version == "" || o.Out == "" ||
		(!parentSource && (err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "")) {
		return build{}, fmt.Errorf(
			"%w: recipe commit, version, output and either credential-free HTTPS source URI or complete parent reference required",
			ErrInput,
		)
	}
	var selected []build
	for _, b := range m.Builds {
		if b.RunID == m.LastRun {
			selected = append(selected, b)
		}
	}
	if m.LastRun == "" || len(selected) != 1 {
		return build{}, fmt.Errorf(
			"%w: manifest must identify exactly one build in its last run",
			ErrInput,
		)
	}
	b := selected[0]
	if b.Time <= 0 || b.Data["qualification"] != "unverified" ||
		!sha.MatchString(b.Data["source_sha256"]) ||
		b.Data["source_build"] == "" {
		return build{}, fmt.Errorf(
			"%w: completed unverified build with pinned source required",
			ErrInput,
		)
	}
	if b.Builder != "qemu" && b.Builder != "imageweave-native" &&
		b.Builder != "imageweave-macos-prepared" {
		return build{}, fmt.Errorf("%w: unsupported builder %q", ErrInput, b.Builder)
	}
	return b, nil
}

func baseBundle(b build, o Options) pack.BundleFile {
	return pack.BundleFile{
		SchemaVersion: 1,
		Guest:         spec.Guest{Variant: spec.TierBase, OSBuild: b.Data["source_build"]},
		Firmware:      spec.Firmware{Type: "uefi", TPM: "none"},
		Resources: spec.Resources{
			CPU:    spec.MinDefault{Min: 2, Default: 2},
			Memory: spec.MinDefault{Min: 2 << 30, Default: 4 << 30},
		},
		Provisioning: spec.Provisioning{CredentialHint: "set-at-first-boot"},
		Build: spec.Build{
			TemplateRef: "weaveplatform/imageweave@" + o.RecipeCommit,
			Created:     time.Unix(b.Time, 0).UTC().Format(time.RFC3339),
			SourceMedia: []spec.SourceMedia{
				{URI: o.SourceURI, Digest: "sha256:" + b.Data["source_sha256"]},
			},
		},
		Disks: []pack.BundleDisk{{Name: "disk0", Role: "system", Path: "disk0.img"}},
		Annotations: map[string]string{
			spec.AnnotationVersion:         o.Version,
			spec.AnnotationRevision:        o.RecipeCommit,
			spec.AnnotationSource:          "https://github.com/weaveplatform/imageweave",
			"io.weave.image.qualification": "unverified",
		},
	}
}
