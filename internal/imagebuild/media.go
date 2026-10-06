package imagebuild

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Media pins a download, including the metadata needed to resume partial files.
type Media struct {
	URI    string `json:"uri"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Downloader fetches bounded ranges and verifies cached bytes before reuse.
type Downloader struct {
	Client *http.Client
	Log    io.Writer
}

func verifiedFile(file string, m Media) error {
	f, err := os.Open(file)
	if err != nil {
		return fmt.Errorf("open media: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat media: %w", err)
	}
	if info.Size() != m.Size {
		return fmt.Errorf("%w: media size differs from lock", ErrInput)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash media: %w", err)
	}
	if hex.EncodeToString(h.Sum(nil)) != m.SHA256 {
		return fmt.Errorf("%w: media checksum differs from lock", ErrInput)
	}
	return nil
}

// Download retains incomplete downloads and promotes only fully verified files.
func (d Downloader) Download(ctx context.Context, m Media, destination string) (string, error) {
	if m.Size <= 0 || !shaPattern.MatchString("sha256:"+m.SHA256) {
		return "", fmt.Errorf("%w: media requires size and SHA256", ErrInput)
	}
	if _, err := os.Stat(destination); err == nil {
		if d.Log != nil {
			fmt.Fprintf(d.Log, "verifying cached media (%d bytes): %s\n", m.Size, destination)
		}
		return destination, verifiedFile(destination, m)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat cache: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return "", fmt.Errorf("create cache: %w", err)
	}
	part := destination + ".part"
	if _, err := os.Stat(part); err == nil {
		var previous Media
		if err := readJSON(part+".json", &previous); err != nil {
			return "", err
		}
		if previous != m {
			return "", fmt.Errorf("%w: partial download belongs to different media", ErrInput)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat partial: %w", err)
	}
	if err := writeJSON(part+".json", m); err != nil {
		return "", err
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", fmt.Errorf("open partial: %w", err)
	}
	defer f.Close()
	if err := d.transfer(ctx, f, m); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close download: %w", err)
	}
	if d.Log != nil {
		fmt.Fprintf(d.Log, "verifying downloaded media (%d bytes): %s\n", m.Size, part)
	}
	if err := verifiedFile(part, m); err != nil {
		return "", err
	}
	if err := os.Rename(part, destination); err != nil {
		return "", fmt.Errorf("promote download: %w", err)
	}
	if err := os.Remove(part + ".json"); err != nil {
		return "", fmt.Errorf("remove partial metadata: %w", err)
	}
	return destination, nil
}

func (d Downloader) transfer(ctx context.Context, f *os.File, m Media) error {
	client := d.Client
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	failures := 0
	for {
		info, err := f.Stat()
		if err != nil {
			return fmt.Errorf("stat partial: %w", err)
		}
		offset := info.Size()
		if offset > m.Size {
			return fmt.Errorf("%w: partial exceeds locked size", ErrInput)
		}
		if offset == m.Size {
			return nil
		}
		err = d.downloadRange(ctx, client, f, m, offset)
		if err == nil {
			failures = 0
			continue
		}
		if errors.Is(err, ErrInput) || ctx.Err() != nil {
			return err
		}
		failures++
		if failures >= 8 {
			return err
		}
		if d.Log != nil {
			_, _ = fmt.Fprintf(
				d.Log,
				"Download interrupted at %d bytes: %v; retrying\n",
				offset,
				err,
			)
		}
		timer := time.NewTimer(time.Duration(min(failures, 15)) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("download cancelled: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func (d Downloader) downloadRange(
	ctx context.Context,
	client *http.Client,
	f *os.File,
	m Media,
	offset int64,
) error {
	end := min(offset+64*1024*1024-1, m.Size-1)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.URI, nil)
	if err != nil {
		return fmt.Errorf("download request: %w", err)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, end))
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	expected := end - offset + 1
	switch resp.StatusCode {
	case http.StatusPartialContent:
		want := fmt.Sprintf("bytes %d-%d/%d", offset, end, m.Size)
		if resp.Header.Get("Content-Range") != want {
			return fmt.Errorf(
				"%w: unexpected Content-Range %q",
				ErrInput,
				resp.Header.Get("Content-Range"),
			)
		}
	case http.StatusOK:
		offset = 0
		expected = m.Size
		if err := f.Truncate(0); err != nil {
			return fmt.Errorf("restart download: %w", err)
		}
	default:
		return fmt.Errorf("download HTTP %d: %w", resp.StatusCode, ErrInput)
	}
	if resp.ContentLength >= 0 && resp.ContentLength != expected {
		return fmt.Errorf(
			"%w: unexpected Content-Length %s",
			ErrInput,
			strconv.FormatInt(resp.ContentLength, 10),
		)
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return fmt.Errorf("seek partial: %w", err)
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, expected+1))
	if n > expected {
		return fmt.Errorf("%w: response exceeds locked size", ErrInput)
	}
	if err != nil {
		return fmt.Errorf("read download: %w", err)
	}
	if n != expected {
		return fmt.Errorf("short download: %w", io.ErrUnexpectedEOF)
	}
	if d.Log != nil {
		_, _ = fmt.Fprintf(
			d.Log,
			"Downloaded %.1f%% (%d/%d bytes)\n",
			float64(offset+n)*100/float64(m.Size),
			offset+n,
			m.Size,
		)
	}
	return nil
}

// Asset downloads a locked release asset into a content-addressed cache.
func (d Downloader) Asset(ctx context.Context, a Asset, cache string) (string, error) {
	if err := a.Validate(); err != nil {
		return "", err
	}
	sha := strings.TrimPrefix(a.Digest, "sha256:")
	return d.Download(
		ctx,
		Media{URI: a.URI, Size: a.Size, SHA256: sha},
		filepath.Join(cache, sha, a.Name),
	)
}
