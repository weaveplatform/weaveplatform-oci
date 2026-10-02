// Package conformance runs the contract §12 checklist over an index or a
// manifest held in any OCI content source. hostweave, both guestweave CLIs
// and weaveoci run the same checks through this package.
package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"

	"github.com/deploymenttheory/weaveplatform-oci/pkg/chunk"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/spec"
)

// Options select the optional checks.
type Options struct {
	// Deep fetches every chunk and state blob and verifies content (rules 15 and 16).
	Deep bool
}

// Child is the result for one manifest.
type Child struct {
	Descriptor  ocispec.Descriptor
	Description spec.Description
	Problems    []spec.Problem
}

// Report is the result of Check.
type Report struct {
	Root          ocispec.Descriptor
	IndexProblems []spec.Problem
	Children      []Child
}

// OK reports whether no problem was found.
func (r Report) OK() bool {
	if len(r.IndexProblems) > 0 {
		return false
	}
	for _, c := range r.Children {
		if len(c.Problems) > 0 {
			return false
		}
	}
	return true
}

// Problems lists every problem as text, index problems first.
func (r Report) Problems() []string {
	var out []string
	for _, p := range r.IndexProblems {
		out = append(out, "index: "+p.String())
	}
	for _, c := range r.Children {
		for _, p := range c.Problems {
			out = append(out, fmt.Sprintf("%s: %s", c.Descriptor.Digest, p))
		}
	}
	return out
}

// Check validates root, which is either a contract index or a single contract
// manifest. The returned error reports a failure to read content, not a
// conformance failure; those are in the report.
func Check(
	ctx context.Context,
	f content.Fetcher,
	root ocispec.Descriptor,
	o Options,
) (Report, error) {
	r := Report{Root: root}
	raw, err := content.FetchAll(ctx, f, root)
	if err != nil {
		return r, fmt.Errorf("fetch %s: %w", root.Digest, err)
	}
	var head struct {
		MediaType string `json:"mediaType"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		r.IndexProblems = append(
			r.IndexProblems,
			spec.Problem{Rule: 1, Path: "root", Message: "not JSON"},
		)
		return r, nil
	}
	children := []ocispec.Descriptor{root}
	if head.MediaType == spec.MediaTypeIndex {
		kids, err := spec.InspectIndex(raw)
		r.IndexProblems = spec.Problems(err)
		children = kids
	}
	for _, c := range children {
		child, err := checkManifest(ctx, f, c, o)
		if err != nil {
			return r, err
		}
		r.Children = append(r.Children, child)
	}
	return r, nil
}

func checkManifest(
	ctx context.Context,
	f content.Fetcher,
	m ocispec.Descriptor,
	o Options,
) (Child, error) {
	ch := Child{Descriptor: m}
	mraw, err := content.FetchAll(ctx, f, m)
	if err != nil {
		return ch, fmt.Errorf("fetch manifest %s: %w", m.Digest, err)
	}
	var man ocispec.Manifest
	if err := json.Unmarshal(mraw, &man); err != nil {
		ch.Problems = append(
			ch.Problems,
			spec.Problem{Rule: 6, Path: "manifest", Message: "not JSON"},
		)
		return ch, nil
	}
	if man.Config.Size > spec.MaxConfigSize || man.Config.Size < 0 {
		ch.Problems = append(
			ch.Problems,
			spec.Problem{Rule: 7, Path: "config", Message: "size outside 0..4 MiB"},
		)
		return ch, nil
	}
	craw, err := content.FetchAll(ctx, f, man.Config)
	if err != nil {
		ch.Problems = append(
			ch.Problems,
			spec.Problem{Rule: 7, Path: "config", Message: err.Error()},
		)
		return ch, nil
	}
	d, err := spec.Inspect(mraw, craw)
	ch.Description = d
	ch.Problems = append(ch.Problems, spec.Problems(err)...)
	if o.Deep && err == nil {
		ch.Problems = append(ch.Problems, deep(ctx, f, d)...)
	}
	return ch, nil
}

func deep(ctx context.Context, f content.Fetcher, d spec.Description) []spec.Problem {
	var out []spec.Problem
	src := sourceOf(f)
	for _, disk := range d.Disks {
		for _, c := range disk.Chunks {
			if err := verifyChunk(ctx, src, c); err != nil {
				out = append(
					out,
					spec.Problem{Rule: 15, Path: "disk " + disk.Name, Message: err.Error()},
				)
			}
		}
	}
	for _, s := range d.State {
		if _, err := content.FetchAll(ctx, f, s.Descriptor); err != nil {
			out = append(out, spec.Problem{Rule: 16, Path: "state " + s.Name, Message: err.Error()})
		}
	}
	return out
}

func verifyChunk(ctx context.Context, src chunk.Source, c spec.ChunkLayer) error {
	rc, err := src.Fetch(ctx, c.Descriptor)
	if err != nil {
		return err //nolint:wrapcheck // source errors name the digest
	}
	defer func() { _ = rc.Close() }()
	return chunk.Decode(rc, c, io.Discard) //nolint:wrapcheck // chunk errors carry the index
}

type fetcherSource struct{ f content.Fetcher }

func (s fetcherSource) Fetch(ctx context.Context, d ocispec.Descriptor) (io.ReadCloser, error) {
	rc, err := s.f.Fetch(ctx, d)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", d.Digest, err)
	}
	return rc, nil
}

func sourceOf(f content.Fetcher) chunk.Source { return fetcherSource{f: f} }
