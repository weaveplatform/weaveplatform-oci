package spec

// Media types defined by contract §2. The artifact type and the config media
// type are deliberately the same string.
const (
	MediaTypeConfig         = "application/vnd.weave.guest.config.v1+json"
	MediaTypeDiskChunk      = "application/vnd.weave.guest.disk.v1.raw+zstd"
	MediaTypeAuxStorage     = "application/vnd.weave.guest.state.auxstorage.v1"
	MediaTypeUEFIVars       = "application/vnd.weave.guest.state.uefivars.v1"
	MediaTypeFirmwarePolicy = "application/vnd.weave.guest.state.firmware-policy.v1+json"
	// MediaTypeSeed is reserved by the contract and never produced in v1.
	MediaTypeSeed = "application/vnd.weave.guest.state.seed.v1+tar"

	ArtifactType = MediaTypeConfig

	MediaTypeManifest = "application/vnd.oci.image.manifest.v1+json"
	MediaTypeIndex    = "application/vnd.oci.image.index.v1+json"
)

// AnnotationPrefix is the reverse-DNS namespace for weave annotation keys.
const AnnotationPrefix = "run.weaveplatform.guest."

// Layer annotations (contract §5 and §6).
const (
	AnnotationDiskName       = AnnotationPrefix + "disk.name"
	AnnotationChunkIndex     = AnnotationPrefix + "disk.chunk.index"
	AnnotationChunkOffset    = AnnotationPrefix + "disk.chunk.offset"
	AnnotationChunkSize      = AnnotationPrefix + "disk.chunk.size"
	AnnotationChunkDigest    = AnnotationPrefix + "disk.chunk.digest"
	AnnotationChunkZero      = AnnotationPrefix + "disk.chunk.zero"
	AnnotationStateName      = AnnotationPrefix + "state.name"
	AnnotationStateSemantics = AnnotationPrefix + "state.semantics"
)

// Manifest and index annotations (contract §8).
const (
	AnnotationOS            = AnnotationPrefix + "os"
	AnnotationArch          = AnnotationPrefix + "arch"
	AnnotationOSVersion     = AnnotationPrefix + "osVersion"
	AnnotationOSBuild       = AnnotationPrefix + "osBuild"
	AnnotationDistro        = AnnotationPrefix + "distro"
	AnnotationDiskTotalSize = AnnotationPrefix + "disk.totalSize"
	AnnotationHypervisors   = AnnotationPrefix + "hypervisors"
)

// Standard OCI annotation keys used by the contract.
const (
	AnnotationCreated     = "org.opencontainers.image.created"
	AnnotationVersion     = "org.opencontainers.image.version"
	AnnotationRevision    = "org.opencontainers.image.revision"
	AnnotationSource      = "org.opencontainers.image.source"
	AnnotationTitle       = "org.opencontainers.image.title"
	AnnotationDescription = "org.opencontainers.image.description"
	AnnotationVendor      = "org.opencontainers.image.vendor"
	AnnotationLicenses    = "org.opencontainers.image.licenses"
)

// Fixed sizes and limits.
const (
	// ChunkSize is the uncompressed guest-LBA span of every chunk except the last.
	ChunkSize int64 = 536870912
	// MaxConfigSize caps the config blob (contract §12 rule 7).
	MaxConfigSize int64 = 4 << 20
	// MaxDescriptionLength is GHCR's limit for org.opencontainers.image.description.
	MaxDescriptionLength = 512
)

// State names and semantics (contract §6 and §7).
const (
	StateAuxStorage     = "auxstorage"
	StateUEFIVars       = "uefivars"
	StateFirmwarePolicy = "firmware-policy"

	SemanticsCarry      = "carry"
	SemanticsRegenerate = "regenerate"
)

// Guest operating systems and architectures (GOOS and GOARCH values).
const (
	OSDarwin  = "darwin"
	OSWindows = "windows"
	OSLinux   = "linux"
	ArchARM64 = "arm64"
	ArchAMD64 = "amd64"
)

// StateMediaType returns the media type a state layer with the given name must
// carry, and whether the name is known.
func StateMediaType(name string) (string, bool) {
	switch name {
	case StateAuxStorage:
		return MediaTypeAuxStorage, true
	case StateUEFIVars:
		return MediaTypeUEFIVars, true
	case StateFirmwarePolicy:
		return MediaTypeFirmwarePolicy, true
	default:
		return "", false
	}
}

// ChunkCount returns ceil(logicalSize / ChunkSize).
func ChunkCount(logicalSize int64) int64 {
	if logicalSize <= 0 {
		return 0
	}
	return (logicalSize + ChunkSize - 1) / ChunkSize
}

// ChunkLength returns the uncompressed length of chunk index i of a disk.
func ChunkLength(logicalSize, i int64) int64 {
	rest := logicalSize - i*ChunkSize
	if rest > ChunkSize {
		return ChunkSize
	}
	return rest
}
