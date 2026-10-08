package handoff

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/opencontainers/go-digest"

	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

const maxMetadata = 1 << 20

// Import snapshots a completed Packer run into a new independent bundle. It
// never restores, boots, downloads media, publishes, or grants qualification.
// Artifact paths must stay under the manifest directory, including symlinks.
func Import(ctx context.Context, o Options) (Receipt, error) {
	var receipt Receipt
	abs, err := filepath.Abs(o.Manifest)
	if err != nil {
		return receipt, fmt.Errorf("manifest path: %w", err)
	}
	root, err := os.OpenRoot(filepath.Dir(abs))
	if err != nil {
		return receipt, fmt.Errorf("manifest directory: %w", err)
	}
	defer root.Close()
	raw, err := readMetadata(root, filepath.Base(abs))
	if err != nil {
		return receipt, err
	}
	b, err := selectBuild(raw, o)
	if err != nil {
		return receipt, err
	}
	files, err := inventory(root, filepath.Dir(abs), b)
	if err != nil {
		return receipt, err
	}
	f := baseBundle(b, o)
	var selected []string
	if b.Builder == "qemu" {
		err = linuxMetadata(b, &f)
		selected = []string{"disk.raw"}
	} else {
		var data []byte
		data, err = readMetadata(root, files["build-result.json"].Name)
		if err == nil {
			err = nativeMetadata(data, b, &f)
		}
		selected = []string{"disk0.img"}
		if f.Guest.OS == "darwin" {
			selected = append(selected, "auxstorage.bin")
		}
	}
	if err != nil {
		return receipt, err
	}
	if err := checkPreparedParent(ctx, o, f); err != nil {
		return receipt, err
	}
	if err := validateBundle(f, files[selected[0]].Size); err != nil {
		return receipt, err
	}
	if f.Guest.OS == "darwin" && files["auxstorage.bin"].Size > pack.MaxStateSize {
		return receipt, fmt.Errorf("%w: auxiliary storage exceeds limit", ErrInput)
	}
	if err := os.Mkdir(o.Out, 0o700); err != nil {
		return receipt, fmt.Errorf("new bundle: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(o.Out)
		}
	}()
	receipt = Receipt{
		SchemaVersion:  1,
		Producer:       "imageweave",
		RecipeCommit:   o.RecipeCommit,
		ManifestDigest: digest.FromBytes(raw).String(),
		RunID:          b.RunID,
		Builder:        b.Builder,
		Qualification:  "unverified",
	}
	for n, name := range selected {
		dst := "disk0.img"
		if n > 0 {
			dst = name
		}
		file, chunks, err := snapshotFile(
			ctx,
			root,
			files[name],
			filepath.Join(o.Out, dst),
			copyProgress(o.Log, name, files[name].Size),
		)
		if err != nil {
			return Receipt{}, err
		}
		file.Path = dst
		receipt.Files = append(receipt.Files, file)
		if n == 0 {
			f.Disks[0].ChunkDigests = chunks
		} else {
			f.State[0].Digest = file.Digest
		}
	}
	if f.Guest.OS == "windows" {
		policy := []byte(
			"{\"schemaVersion\":1,\"secureBoot\":true,\"tpm\":\"required\",\"generation\":2}\n",
		)
		if err := os.WriteFile(
			filepath.Join(o.Out, "firmware-policy.json"),
			policy,
			0o600,
		); err != nil {
			return Receipt{}, fmt.Errorf("firmware policy: %w", err)
		}
		d := digest.FromBytes(policy).String()
		f.State = []pack.BundleState{
			{
				Name:      spec.StateFirmwarePolicy,
				Path:      "firmware-policy.json",
				Semantics: spec.SemanticsRegenerate,
				Required:  true,
				Digest:    d,
			},
		}
		receipt.Files = append(
			receipt.Files,
			File{Path: "firmware-policy.json", Size: int64(len(policy)), Digest: d},
		)
	}
	receipt.Bundle = f
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return Receipt{}, fmt.Errorf("handoff receipt: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(
		filepath.Join(o.Out, "imageweave-import.json"),
		data,
		0o600,
	); err != nil {
		return Receipt{}, fmt.Errorf("write receipt: %w", err)
	}
	// Bind the audit record into the final platform manifest without a circular hash.
	f.Annotations = cloneAnnotations(f.Annotations)
	f.Annotations["io.weave.imageweave.handoff.digest"] = digest.FromBytes(data).String()
	if err := pack.WriteBundleFile(o.Out, f); err != nil {
		return Receipt{}, fmt.Errorf("import bundle: %w", err)
	}
	complete = true
	return receipt, nil
}

func cloneAnnotations(m map[string]string) map[string]string {
	out := make(map[string]string, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func readMetadata(root *os.Root, path string) ([]byte, error) {
	f, err := root.Open(path)
	if err != nil {
		return nil, fmt.Errorf("metadata: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("metadata stat: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxMetadata {
		return nil, fmt.Errorf("%w: metadata must be a regular file of at most 1 MiB", ErrInput)
	}
	return readBounded(f)
}

func readBounded(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxMetadata+1))
	if err != nil {
		return nil, fmt.Errorf("read metadata: %w", err)
	}
	if len(data) > maxMetadata {
		return nil, fmt.Errorf("%w: metadata grew beyond limit", ErrInput)
	}
	return data, nil
}

func inventory(root *os.Root, dir string, b build) (map[string]artifact, error) {
	files := make(map[string]artifact, len(b.Files))
	for _, f := range b.Files {
		name := filepath.Base(f.Name)
		allowed := name == "disk.raw" || name == "efivars.fd"
		if b.Builder == "imageweave-native" || b.Builder == "imageweave-macos-prepared" {
			allowed = name == "disk0.img" || name == "auxstorage.bin" || name == "build-result.json"
		}
		if !allowed || files[name].Name != "" || f.Size <= 0 {
			return nil, fmt.Errorf("%w: unexpected/duplicate artifact %q", ErrInput, f.Name)
		}
		if filepath.IsAbs(f.Name) {
			rel, err := filepath.Rel(dir, f.Name)
			if err != nil {
				return nil, fmt.Errorf("artifact path: %w", err)
			}
			f.Name = rel
		}
		if !filepath.IsLocal(f.Name) || strings.Contains(f.Name, "\\") {
			return nil, fmt.Errorf("%w: artifact path escapes manifest directory", ErrInput)
		}
		info, err := root.Stat(f.Name)
		if err != nil {
			return nil, fmt.Errorf("artifact %s: %w", name, err)
		}
		if !info.Mode().IsRegular() || info.Size() != f.Size {
			return nil, fmt.Errorf("%w: artifact %s size/type changed", ErrInput, name)
		}
		files[name] = f
	}
	return files, nil
}

func validateBundle(f pack.BundleFile, size int64) error {
	if size <= 0 || size%512 != 0 {
		return fmt.Errorf("%w: aligned nonempty raw system disk required", ErrInput)
	}
	c := spec.Config{
		SchemaVersion: 1,
		Guest:         f.Guest,
		Firmware:      f.Firmware,
		Resources:     f.Resources,
		Provisioning:  f.Provisioning,
		Build:         f.Build,
		Disks: []spec.Disk{
			{
				Name:        "disk0",
				Role:        "system",
				LogicalSize: size,
				ChunkSize:   spec.ChunkSize,
				ChunkCount:  spec.ChunkCount(size),
				Compression: "zstd",
			},
		},
	}
	for _, s := range f.State {
		mt, _ := spec.StateMediaType(s.Name)
		c.State = append(
			c.State,
			spec.StateEntry{
				Name:      s.Name,
				MediaType: mt,
				Semantics: s.Semantics,
				Required:  s.Required,
			},
		)
	}
	raw, err := c.Marshal()
	if err != nil {
		return fmt.Errorf("bundle config: %w", err)
	}
	if _, err := spec.ParseConfig(raw); err != nil {
		return fmt.Errorf("imported config: %w", err)
	}
	return nil
}

// snapshot hashes the bytes being copied and records raw chunk hashes so pack
// can reject subsequent modification without a racy preflight-only hash check.
func snapshotFile(
	ctx context.Context,
	root *os.Root,
	src artifact,
	dst string,
	report func(int64),
) (File, []string, error) {
	in, err := root.Open(src.Name)
	if err != nil {
		return File{}, nil, fmt.Errorf("open artifact: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return File{}, nil, fmt.Errorf("snapshot: %w", err)
	}
	defer out.Close()
	full := sha256.New()
	var chunks []string
	buf := make([]byte, 1<<20)
	zero := make([]byte, len(buf))
	for off := int64(0); off < src.Size; {
		h := sha256.New()
		end := min(off+spec.ChunkSize, src.Size)
		for off < end {
			if err := ctx.Err(); err != nil {
				return File{}, nil, fmt.Errorf("snapshot: %w", err)
			}
			data := buf[:min(int64(len(buf)), end-off)]
			if _, err := io.ReadFull(in, data); err != nil {
				return File{}, nil, fmt.Errorf("read artifact: %w", err)
			}
			_, _ = full.Write(data)
			_, _ = h.Write(data)
			if bytes.Equal(data, zero[:len(data)]) {
				_, err = out.Seek(int64(len(data)), io.SeekCurrent)
			} else {
				_, err = out.Write(data)
			}
			if err != nil {
				return File{}, nil, fmt.Errorf("copy artifact: %w", err)
			}
			off += int64(len(data))
			if report != nil {
				report(off)
			}
		}
		chunks = append(chunks, digest.NewDigest(digest.SHA256, h).String())
	}
	var extra [1]byte
	if n, err := in.Read(extra[:]); n != 0 || !errors.Is(err, io.EOF) {
		return File{}, nil, fmt.Errorf("%w: artifact grew during import", ErrInput)
	}
	if err := errors.Join(out.Truncate(src.Size), out.Close()); err != nil {
		return File{}, nil, fmt.Errorf("finish snapshot: %w", err)
	}
	return File{Size: src.Size, Digest: digest.NewDigest(digest.SHA256, full).String()}, chunks, nil
}

func copyProgress(log io.Writer, name string, size int64) func(int64) {
	if log == nil {
		log = io.Discard
	}
	start, last := time.Now(), time.Now()
	_, _ = fmt.Fprintf(log, "Importing %s: hashing and copying %d bytes\n", name, size)
	return func(copied int64) {
		if copied == size || time.Since(last) >= 30*time.Second {
			_, _ = fmt.Fprintf(
				log,
				"Importing %s: %d/%d bytes copied; elapsed=%s\n",
				name,
				copied,
				size,
				time.Since(start).Round(time.Second),
			)
			last = time.Now()
		}
	}
}
