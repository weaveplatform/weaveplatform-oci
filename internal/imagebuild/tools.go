// Package imagebuild builds and tests VM image candidates using the shared OCI contract.
package imagebuild

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrInput reports an incomplete or inconsistent build input.
var ErrInput = errors.New("image build input")

// RunFunc invokes a native tool without shell interpolation.
type RunFunc func(context.Context, io.Writer, io.Writer, string, ...string) error

// Tools supplies native tools and output streams. A nil Run uses os/exec.
type Tools struct {
	Run RunFunc
	Log io.Writer
}

func (t Tools) run(ctx context.Context, stdout io.Writer, name string, args ...string) error {
	stderr := t.Log
	if stderr == nil {
		stderr = io.Discard
	}
	if stdout == nil {
		stdout = stderr
	}
	if t.Run != nil {
		return t.Run(ctx, stdout, stderr, name, args...)
	}
	cmd := exec.CommandContext( //nolint:gosec // Native executable and separate arguments; never interpreted by a shell.
		ctx,
		name,
		args...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func (t Tools) output(ctx context.Context, name string, args ...string) ([]byte, error) {
	var b bytes.Buffer
	if err := t.run(ctx, &b, name, args...); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func readJSON(path string, target any) error {
	raw, err := os.ReadFile(path) //nolint:gosec // caller-selected build input
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func writeJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	//nolint:gosec // Writes a caller-selected build output.
	if err := os.WriteFile(
		path,
		append(raw, '\n'),
		0o600,
	); err != nil { //nolint:gosec // Caller-selected output path.
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func newDirectory(path string) error {
	//nolint:gosec // Creates the caller-selected candidate directory.
	if err := os.MkdirAll(
		filepath.Dir(path),
		0o750,
	); err != nil { //nolint:gosec // Caller-selected candidate directory.
		return fmt.Errorf("create parent: %w", err)
	}
	//nolint:gosec // Creates the caller-selected candidate directory.
	if err := os.Mkdir(
		path,
		0o750,
	); err != nil { //nolint:gosec // Caller-selected candidate directory.
		return fmt.Errorf("candidate must be new: %w", err)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // validated build input
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()
	//nolint:gosec // Creates the caller-selected output without replacing existing files.
	out, err := os.OpenFile(
		dst,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o600,
	) //nolint:gosec // candidate output
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	_, copyErr := io.Copy(out, in)
	if err := errors.Join(copyErr, out.Close()); err != nil {
		return fmt.Errorf("copy %s: %w", src, err)
	}
	return nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func writeNewJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode JSON: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create JSON: %w", err)
	}
	_, writeErr := f.Write(append(raw, '\n'))
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return fmt.Errorf("write JSON: %w", err)
	}
	return nil
}
