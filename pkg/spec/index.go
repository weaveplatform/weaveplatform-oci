package spec

import (
	"encoding/json"
	"fmt"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// InspectIndex validates an index against conformance rules 1 to 5 and
// returns its children.
func InspectIndex(raw []byte) ([]ocispec.Descriptor, error) {
	var ps problems
	var idx ocispec.Index
	if err := json.Unmarshal(raw, &idx); err != nil {
		ps.add(1, "index", "not JSON: %v", err)
		return nil, ps.err()
	}
	if idx.SchemaVersion != 2 || idx.MediaType != MediaTypeIndex {
		ps.add(
			1,
			"index",
			"schemaVersion %d and mediaType %q, want 2 and %s",
			idx.SchemaVersion,
			idx.MediaType,
			MediaTypeIndex,
		)
	}
	if idx.ArtifactType != ArtifactType {
		ps.add(1, "artifactType", "is %q, want %s", idx.ArtifactType, ArtifactType)
	}
	if len(idx.Manifests) == 0 {
		ps.add(2, "manifests", "index has no children")
	}
	type key struct{ os, arch, version string }
	seen := map[key]bool{}
	family := ""
	for i, c := range idx.Manifests {
		path := fmt.Sprintf("manifests[%d]", i)
		if c.MediaType != MediaTypeManifest || c.ArtifactType != ArtifactType {
			ps.add(
				2,
				path,
				"mediaType %q and artifactType %q, want %s and %s",
				c.MediaType,
				c.ArtifactType,
				MediaTypeManifest,
				ArtifactType,
			)
		}
		if c.Platform == nil {
			ps.add(2, path, "platform is required")
			continue
		}
		if !validOS(c.Platform.OS) || !validArch(c.Platform.Architecture) {
			ps.add(
				2,
				path,
				"platform %s/%s is outside darwin|windows|linux and arm64|amd64",
				c.Platform.OS,
				c.Platform.Architecture,
			)
		}
		k := key{c.Platform.OS, c.Platform.Architecture, c.Platform.OSVersion}
		if seen[k] {
			ps.add(3, path, "duplicate platform %s/%s %s", k.os, k.arch, k.version)
		}
		seen[k] = true
		if c.Annotations[AnnotationOS] != c.Platform.OS ||
			c.Annotations[AnnotationArch] != c.Platform.Architecture {
			ps.add(
				4,
				path,
				"annotations %s=%q %s=%q disagree with platform %s/%s",
				AnnotationOS,
				c.Annotations[AnnotationOS],
				AnnotationArch,
				c.Annotations[AnnotationArch],
				c.Platform.OS,
				c.Platform.Architecture,
			)
		}
		if family == "" {
			family = c.Platform.OS
		} else if c.Platform.OS != family {
			ps.add(5, path, "mixes guest OS %s into a %s index", c.Platform.OS, family)
		}
	}
	return idx.Manifests, ps.err()
}

// SelectChild returns the first child whose os and architecture equal p's,
// and whose os.version equals p's when p sets one.
func SelectChild(idx ocispec.Index, p ocispec.Platform) (ocispec.Descriptor, error) {
	for _, c := range idx.Manifests {
		if c.Platform == nil || c.Platform.OS != p.OS || c.Platform.Architecture != p.Architecture {
			continue
		}
		if p.OSVersion != "" && c.Platform.OSVersion != p.OSVersion {
			continue
		}
		return c, nil
	}
	return ocispec.Descriptor{}, fmt.Errorf(
		"%w: %s/%s %s",
		ErrNoMatchingPlatform,
		p.OS,
		p.Architecture,
		p.OSVersion,
	)
}

func validOS(os string) bool {
	return os == OSDarwin || os == OSWindows || os == OSLinux
}

func validArch(a string) bool {
	return a == ArchARM64 || a == ArchAMD64
}
