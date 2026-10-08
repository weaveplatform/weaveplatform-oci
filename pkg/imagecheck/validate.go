package imagecheck

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/oci"

	"github.com/weaveplatform/weaveplatform-oci/pkg/chunk"
	"github.com/weaveplatform/weaveplatform-oci/pkg/conformance"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

// Candidate selects the exact packed index to validate. Out must not exist;
// all disposable disks live below it, so callers can place them on KING.
type Candidate struct {
	Store   *oci.Store
	Root    ocispec.Descriptor
	Tag     string
	Out     string
	Timeout time.Duration
	Log     io.Writer
}

// Clone contains a separately unpacked disk and a host-generated challenge.
// Only the public half of OwnKey may be provisioned in the guest. Keys are
// ephemeral and deliberately excluded from the persisted acceptance report.
type Clone struct {
	Bundle, Report, Marker string
	Config                 spec.Config
	Number                 int
	OwnKey, ForeignKey     ed25519.PrivateKey
}

// BootClone runs the complete first-boot, reboot, identity and shutdown checks.
// Implementations must stop and release their VM before returning, including
// on cancellation. A result must contain observed evidence, never defaults
// copied from Clone.Config. Validate checks it against that expected config.
type BootClone func(context.Context, Clone) (Boot, error)

// Validate deeply verifies an existing OCI index, independently unpacks and
// boots two clones of every platform, and writes digest-bound schema-3 evidence.
// It does not publish, register cloud images, or run a consumer CLI. Native and
// provider adapters supply BootClone; missing or skipped adapters are errors.
func Validate(ctx context.Context, c Candidate, boot BootClone) (result Report, err error) {
	if c.Store == nil || c.Root.MediaType != spec.MediaTypeIndex || c.Tag == "" ||
		c.Out == "" || c.Timeout <= 0 || boot == nil {
		return result, fmt.Errorf(
			"%w: exact index, tag, output, timeout and boot adapter required",
			ErrEvidence,
		)
	}
	resolved, err := c.Store.Resolve(ctx, c.Tag)
	if err != nil || resolved.Digest != c.Root.Digest {
		return result, fmt.Errorf(
			"%w: candidate tag does not resolve to expected index: %v",
			ErrEvidence,
			err,
		)
	}
	inspection, err := conformance.Check(ctx, c.Store, c.Root, conformance.Options{Deep: true})
	if err != nil || !inspection.OK() || len(inspection.Children) == 0 {
		return result, fmt.Errorf(
			"%w: candidate integrity: %v; %v",
			ErrEvidence,
			err,
			inspection.Problems(),
		)
	}
	if err := os.MkdirAll(filepath.Dir(c.Out), 0o750); err != nil {
		return result, fmt.Errorf("create validation parent: %w", err)
	}
	if err := os.Mkdir(c.Out, 0o750); err != nil {
		return result, fmt.Errorf("validation output must be new: %w", err)
	}
	result = Report{
		SchemaVersion: 3, IndexDigest: c.Root.Digest.String(), Tag: c.Tag,
		PlatformDigests: map[string]string{}, Platforms: map[string][]Boot{},
	}
	defer func() {
		if err != nil {
			result.Passed = false
		}
		data, encodeErr := json.MarshalIndent(result, "", "  ")
		if encodeErr != nil {
			err = errors.Join(err, fmt.Errorf("encode acceptance: %w", encodeErr))
			return
		}
		err = errors.Join(
			err,
			os.WriteFile(filepath.Join(c.Out, "acceptance.json"), append(data, '\n'), 0o600),
		)
		if err != nil {
			result.Passed = false
		}
	}()
	for _, child := range inspection.Children {
		cfg := child.Description.Config
		platform := cfg.Guest.OS + "/" + cfg.Guest.Arch
		result.PlatformDigests[platform] = child.Descriptor.Digest.String()
		clones, bootErr := validatePlatform(ctx, c, child, boot)
		result.Platforms[platform] = clones
		if bootErr != nil {
			return result, bootErr
		}
	}
	// The same admission check used during publication must accept the report.
	// In particular, callbacks cannot pass a clone with incomplete assertions.
	result.Passed = true
	data, err := json.Marshal(result)
	if err != nil {
		return result, fmt.Errorf("encode candidate evidence: %w", err)
	}
	if err := Check(data, inspection, c.Tag); err != nil {
		return result, err
	}
	return result, nil
}

func validatePlatform(
	ctx context.Context,
	c Candidate,
	child conformance.Child,
	boot BootClone,
) ([]Boot, error) {
	keys := [2]ed25519.PrivateKey{
		ed25519.NewKeyFromSeed(randomSeed()),
		ed25519.NewKeyFromSeed(randomSeed()),
	}
	cfg := child.Description.Config
	platform := cfg.Guest.OS + "/" + cfg.Guest.Arch
	var results []Boot
	for n := range 2 {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		if c.Log != nil {
			_, _ = fmt.Fprintf(
				c.Log,
				"[%s] validate %s: unpacking and booting clone %d/2; manifest=%s\n",
				time.Now().UTC().Format(time.RFC3339),
				platform,
				n+1,
				child.Descriptor.Digest,
			)
		}
		dir := filepath.Join(c.Out, fmt.Sprintf("%s-%s-%d", cfg.Guest.OS, cfg.Guest.Arch, n+1))
		if err := os.Mkdir(dir, 0o750); err != nil {
			return results, fmt.Errorf("create clone workspace: %w", err)
		}
		request := Clone{
			Bundle: filepath.Join(dir, "bundle"), Report: dir, Config: cfg, Number: n + 1,
			Marker: fmt.Sprintf("WEAVE-BOOT-OK-%x", randomSeed()[:12]),
			OwnKey: keys[n], ForeignKey: keys[1-n],
		}
		result, err := validateClone(ctx, c, child.Descriptor, request, boot)
		results = append(results, result)
		if err != nil {
			return results, fmt.Errorf("validate %s clone %d: %w", platform, n+1, err)
		}
	}
	return results, checkClones(results, cfg, 3)
}

func validateClone(
	ctx context.Context,
	c Candidate,
	desc ocispec.Descriptor,
	request Clone,
	boot BootClone,
) (Boot, error) {
	defer os.RemoveAll(request.Bundle)
	if _, err := pack.Unpack(
		ctx,
		c.Store,
		desc,
		request.Bundle,
		chunk.AssembleOptions{},
	); err != nil {
		return Boot{}, fmt.Errorf("unpack clone: %w", err)
	}
	cloneCtx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	result, err := boot(cloneCtx, request)
	if err == nil {
		err = cloneCtx.Err()
	}
	if err == nil && result.Marker != request.Marker {
		err = fmt.Errorf("%w: clone did not echo this boot's challenge", ErrEvidence)
	}
	if err != nil {
		result.Passed, result.Error = false, err.Error()
	}
	return result, err
}

func randomSeed() []byte {
	b := make([]byte, ed25519.SeedSize)
	_, _ = rand.Read(b)
	return b
}
