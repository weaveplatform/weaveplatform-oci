package verify

import (
	"encoding/json"
	"fmt"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func decodeManifest(raw []byte) (ocispec.Manifest, error) {
	var m ocispec.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("decode manifest: %w", err)
	}
	return m, nil
}
