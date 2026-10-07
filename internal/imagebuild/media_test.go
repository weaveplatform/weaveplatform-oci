package imagebuild

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func mediaFor(uri string, data []byte) Media {
	sum := sha256.Sum256(data)
	return Media{URI: uri, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
}

func TestDownloadResumeAndCacheVerification(t *testing.T) {
	data := []byte("complete verified image bytes")
	for _, ignoreRange := range []bool{false, true} {
		t.Run(fmt.Sprint(ignoreRange), func(t *testing.T) {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Range") != "bytes=8-28" {
						t.Errorf("range %s", r.Header.Get("Range"))
					}
					if ignoreRange {
						w.Write(data)
						return
					}
					w.Header().Set("Content-Range", "bytes 8-28/29")
					w.WriteHeader(206)
					w.Write(data[8:])
				}),
			)
			defer server.Close()
			m := mediaFor(server.URL, data)
			out := filepath.Join(t.TempDir(), "media")
			must(t, os.WriteFile(out+".part", data[:8], 0o600))
			must(t, writeJSON(out+".part.json", m))
			d := Downloader{Client: server.Client(), Log: io.Discard}
			got, err := d.Download(t.Context(), m, out)
			must(t, err)
			if got != out {
				t.Fatal(got)
			}
			_, err = d.Download(t.Context(), m, out)
			must(t, err)
			must(t, os.WriteFile(out, bytes.Repeat([]byte("x"), len(data)), 0o600))
			if _, err := d.Download(t.Context(), m, out); err == nil {
				t.Fatal("tampered cache accepted")
			}
		})
	}
}

func TestDownloadRejectsBadResponse(t *testing.T) {
	for _, mode := range []string{"wrong-range", "wrong-size", "wrong-hash", "too-large", "http-error"} {
		t.Run(mode, func(t *testing.T) {
			data := []byte("bytes")
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					switch mode {
					case "wrong-range":
						w.Header().Set("Content-Range", "bytes 1-5/6")
						w.WriteHeader(206)
						w.Write(data)
					case "wrong-size":
						w.Write([]byte("extra bytes"))
					case "wrong-hash":
						w.Write([]byte("other"))
					case "too-large":
						w.WriteHeader(200)
						w.(http.Flusher).Flush()
						w.Write([]byte("extra bytes"))
					case "http-error":
						w.WriteHeader(404)
					}
				}),
			)
			defer server.Close()
			out := filepath.Join(t.TempDir(), "media")
			_, err := (Downloader{Client: server.Client()}).Download(
				t.Context(),
				mediaFor(server.URL, data),
				out,
			)
			if err == nil {
				t.Fatal("bad download accepted")
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatal("promoted corrupt media")
			}
		})
	}
}

func TestDownloadPartialMetadata(t *testing.T) {
	m := mediaFor("https://example.test/image", []byte("bytes"))
	out := filepath.Join(t.TempDir(), "image")
	d := Downloader{}
	invalid := m
	invalid.Size = 0
	if _, err := d.Download(t.Context(), invalid, out); err == nil {
		t.Fatal("invalid pin")
	}
	must(t, os.WriteFile(out+".part", []byte("too many bytes"), 0o600))
	if _, err := d.Download(t.Context(), m, out); err == nil {
		t.Fatal("partial without pin")
	}
	must(t, writeJSON(out+".part.json", invalid))
	if _, err := d.Download(t.Context(), m, out); err == nil {
		t.Fatal("different pin")
	}
	must(t, writeJSON(out+".part.json", m))
	if _, err := d.Download(t.Context(), m, out); err == nil {
		t.Fatal("oversize partial")
	}
	if _, err := d.Asset(t.Context(), Asset{}, t.TempDir()); err == nil {
		t.Fatal("invalid asset")
	}
	if err := verifiedFile("missing", m); err == nil {
		t.Fatal("missing media")
	}
	if err := verifiedFile(out+".part", m); err == nil {
		t.Fatal("size mismatch")
	}
	f, err := os.Create(filepath.Join(t.TempDir(), "partial"))
	must(t, err)
	defer f.Close()
	m.URI = ":"
	if err := d.downloadRange(t.Context(), http.DefaultClient, f, m, 0); err == nil {
		t.Fatal("invalid request")
	}
}

func TestDownloadRetriesShortRead(t *testing.T) {
	data := []byte("abcdefgh")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Content-Length", "8")
			w.WriteHeader(200)
			w.Write(data[:3])
			return
		}
		if !strings.HasPrefix(r.Header.Get("Range"), "bytes=3-") {
			t.Error(r.Header)
		}
		w.Header().Set("Content-Range", "bytes 3-7/8")
		w.WriteHeader(206)
		w.Write(data[3:])
	}))
	defer server.Close()
	_, err := (Downloader{Client: server.Client(), Log: io.Discard}).Download(
		t.Context(),
		mediaFor(server.URL, data),
		filepath.Join(t.TempDir(), "media"),
	)
	must(t, err)
	if calls != 2 {
		t.Fatal(calls)
	}
}

func TestDownloadCancellationPreservesPartial(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	data := []byte("expected bytes")
	m := mediaFor("https://example.test/image", data)
	out := filepath.Join(t.TempDir(), "image")
	if _, err := (Downloader{}).Download(ctx, m, out); err == nil {
		t.Fatal("cancelled download succeeded")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("promoted cancelled download")
	}
	var saved Media
	must(t, readJSON(out+".part.json", &saved))
	if saved != m {
		t.Fatal("lost resume metadata")
	}
}

func TestDownloadCancellationDuringRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "8")
		w.WriteHeader(200)
		w.Write([]byte("abc"))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	out := filepath.Join(t.TempDir(), "image")
	if _, err := (Downloader{Client: server.Client(), Log: io.Discard}).Download(
		ctx,
		mediaFor(server.URL, []byte("abcdefgh")),
		out,
	); err == nil {
		t.Fatal("cancelled retry succeeded")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("promoted partial transfer")
	}
}

func TestDownloadRetryBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		client := &http.Client{
			Transport: roundTripFunc(
				func(*http.Request) (*http.Response, error) { calls++; return nil, io.ErrUnexpectedEOF },
			),
		}
		out := filepath.Join(t.TempDir(), "image")
		var log bytes.Buffer
		_, err := (Downloader{Client: client, Log: &log}).Download(
			t.Context(),
			mediaFor("https://example.test/image", []byte("x")),
			out,
		)
		if err == nil || calls != 8 {
			t.Fatalf("retry budget: calls=%d error=%v", calls, err)
		}
		if !strings.Contains(log.String(), "retrying") {
			t.Fatal("missing retry diagnostic")
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Fatal("promoted failed transfer")
		}
	})
}

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (failingBody) Close() error             { return nil }

func TestSourceBodyReadFailure(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    200,
			Header:        http.Header{},
			Request:       r,
			Body:          failingBody{},
			ContentLength: 1,
		}, nil
	})}
	if _, err := MacSources(t.Context(), client, "26"); err == nil {
		t.Fatal("truncated catalogue accepted")
	}
	f, err := os.Create(filepath.Join(t.TempDir(), "partial"))
	must(t, err)
	defer f.Close()
	if err := (Downloader{}).downloadRange(
		t.Context(),
		client,
		f,
		mediaFor("https://example.test/image", []byte("x")),
		0,
	); err == nil {
		t.Fatal("truncated transfer accepted")
	}
}
