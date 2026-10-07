package imagebuild

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func appleFixture() AppleSource {
	return AppleSource{
		URL:     "https://updates.cdn-apple.com/path/restore.ipsw",
		Version: "26.6.2",
		Build:   "25G83",
		SHA256:  strings.Repeat("a", 64),
		Size:    100,
	}
}

func TestAppleVersionSelection(t *testing.T) {
	latest := appleFixture()
	older := latest
	older.Version = "26.6.1"
	older.Build = "25G80"
	other := latest
	other.Version = "27.0.1"
	other.Build = "26A434"
	beta := latest
	beta.Build = "25G83a"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).
			Encode(map[string]any{"identifier": "VirtualMac2,1", "firmwares": []AppleSource{older, beta, other, latest}})
	}))
	defer server.Close()
	for _, version := range []string{"26", "26.6.2", "27"} {
		sources, e := macSources(t.Context(), server.Client(), server.URL, version)
		must(t, e)
		if !macVersionMatches(sources[0].Version, version) {
			t.Fatal(sources)
		}
	}
	sources, e := macSources(t.Context(), server.Client(), server.URL, "26")
	must(t, e)
	if len(sources) != 2 || sources[0].Version != "26.6.2" {
		t.Fatal(sources)
	}
	for _, version := range []string{"latest", "26.7", "26.6.2.1", "../../26"} {
		if _, e := macSources(t.Context(), server.Client(), server.URL, version); e == nil {
			t.Fatal("accepted " + version)
		}
	}
	for _, mutate := range []func(*AppleSource){func(s *AppleSource) { s.URL = "http://example.com/a.ipsw" }, func(s *AppleSource) { s.Version = "27.0" }, func(s *AppleSource) { s.Build = "../bad" }, func(s *AppleSource) { s.SHA256 = "bad" }} {
		s := latest
		mutate(&s)
		if _, e := s.validate("26", 1); e == nil {
			t.Fatal("invalid source accepted")
		}
	}
	if _, e := latest.validate("26.6.1", 1); e == nil {
		t.Fatal("exact version silently changed")
	}
}

func TestAppleCatalogueRefusalsAndOrdering(t *testing.T) {
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{`, 200}, {`{"identifier":"iPhone1,1"}`, 200}, {`{"identifier":"VirtualMac2,1","firmwares":[]}`, 200}, {`{}`, 503}, {strings.Repeat("x", 8*1024*1024+1), 200},
	} {
		client := &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: tc.status,
					Body:       io.NopCloser(strings.NewReader(tc.body)),
					Header:     http.Header{},
					Request:    r,
				}, nil
			}),
		}
		if _, err := MacSources(t.Context(), client, "26"); err == nil {
			t.Fatal("invalid catalogue accepted")
		}
	}
	if _, err := macSources(t.Context(), nil, "://invalid", "26"); err == nil {
		t.Fatal("bad URL")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := MacSources(ctx, nil, "26"); err == nil {
		t.Fatal("cancelled request")
	}
	base := appleFixture()
	versions := make([]AppleSource, 5)
	for i := range versions {
		versions[i] = base
	}
	versions[0].Build = "25G99"
	versions[1].Build = "25G100"
	versions[2].Signed = true
	versions[3].Build = "25H1"
	versions[4].ReleaseDate = "2026-10-01"
	raw, err := json.Marshal(map[string]any{"identifier": "VirtualMac2,1", "firmwares": versions})
	must(t, err)
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(bytes.NewReader(raw)),
			Header:     http.Header{},
			Request:    r,
		}, nil
	})}
	sources, err := MacSources(t.Context(), client, "26")
	must(t, err)
	if !sources[0].Signed || sources[1].ReleaseDate == "" || sources[2].Build != "25H1" ||
		sources[3].Build != "25G100" {
		t.Fatal(sources)
	}
	bad := base
	bad.URL = "://bad"
	if _, err := bad.validate("26", 1); err == nil {
		t.Fatal("bad URL")
	}
	if _, err := base.validate("bad", 1); err == nil {
		t.Fatal("bad selector")
	}

}

func TestAppleBuildOrderingIsIndependentOfCatalogueOrder(t *testing.T) {
	a := appleFixture()
	a.Build = "25G99"
	b := a
	b.Build = "25G100"
	c := a
	c.URL = "https://updates.cdn-apple.com/another.ipsw"
	for _, entries := range [][]AppleSource{{b, a, c}, {c, b, a}, {a, c, b}} {
		raw, err := json.Marshal(
			map[string]any{"identifier": "VirtualMac2,1", "firmwares": entries},
		)
		must(t, err)
		client := &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: 200,
					Header:     http.Header{},
					Request:    r,
					Body:       io.NopCloser(bytes.NewReader(raw)),
				}, nil
			}),
		}
		sources, err := MacSources(t.Context(), client, "26")
		must(t, err)
		if sources[0] != b || sources[1] != c || sources[2] != a {
			t.Fatal("unstable source ordering", sources)
		}
	}
}
