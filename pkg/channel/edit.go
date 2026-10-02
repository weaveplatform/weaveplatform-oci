package channel

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/opencontainers/go-digest"
)

// Promote returns manifest bytes with img added (or replacing the entry with
// the same repository and tag), the sequence incremented and generated_at
// refreshed. Every other field, including core and modules, is preserved
// exactly as decoded. The result must be signed again.
func Promote(manifest []byte, img Image, now time.Time) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(manifest, &doc); err != nil {
		return nil, fmt.Errorf("%w: manifest: %w", ErrFormat, err)
	}
	m, err := Parse(manifest)
	if err != nil {
		return nil, err
	}
	if _, err := digest.Parse(img.Digest); err != nil || img.Repository == "" || img.Tag == "" {
		return nil, fmt.Errorf("%w: image needs a repository, a tag and a valid digest", ErrFormat)
	}
	images := m.Images[:0:0]
	for _, existing := range m.Images {
		if existing.Repository != img.Repository || existing.Tag != img.Tag {
			images = append(images, existing)
		}
	}
	images = append(images, img)
	sort.SliceStable(images, func(i, j int) bool {
		if images[i].Repository != images[j].Repository {
			return images[i].Repository < images[j].Repository
		}
		return images[i].Tag < images[j].Tag
	})
	set := func(k string, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("encode %s: %w", k, err)
		}
		doc[k] = b
		return nil
	}
	if err := set("images", images); err != nil {
		return nil, err
	}
	if err := set("sequence", m.Sequence+1); err != nil {
		return nil, err
	}
	if err := set("generated_at", now.UTC().Format(time.RFC3339)); err != nil {
		return nil, err
	}
	return marshalFile(doc)
}

// New returns an empty channel manifest document (no core artifacts or
// modules) for an organisation's own image channel.
func New(name string, now time.Time) ([]byte, error) {
	return marshalFile(map[string]any{
		"schema":       1,
		"channel":      name,
		"generated_at": now.UTC().Format(time.RFC3339),
		"sequence":     0,
		"protocol":     ProtocolWindow{Min: 1, Max: 1},
		"core":         map[string]any{"version": "", "artifacts": []any{}},
		"modules":      []any{},
		"images":       []Image{},
	})
}
