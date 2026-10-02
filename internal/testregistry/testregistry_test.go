package testregistry_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/opencontainers/go-digest"

	"github.com/weaveplatform/weaveplatform-oci/internal/testregistry"
)

func do(t *testing.T, method, url string, body []byte, user, pass string) int {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), method, url, bytes.NewReader(body))
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

const manifest = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.empty.v1+json","digest":"sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a","size":2},"layers":[]}`

func TestFaults(t *testing.T) {
	s := testregistry.New(
		testregistry.Options{Username: "u", Password: "p", FailFirst: 1, ImmutableTags: true},
	)
	defer s.Close()
	if code := do(
		t,
		http.MethodGet,
		s.URL+"/v2/",
		nil,
		"u",
		"p",
	); code != http.StatusServiceUnavailable {
		t.Fatalf("first request %d", code)
	}
	if code := do(
		t,
		http.MethodGet,
		s.URL+"/v2/",
		nil,
		"u",
		"wrong",
	); code != http.StatusUnauthorized {
		t.Fatalf("bad credentials %d", code)
	}
	// referrers on an unknown repository: the inner error passes through
	if code := do(
		t,
		http.MethodGet,
		s.URL+"/v2/none/x/referrers/"+digest.FromString("x").String(),
		nil,
		"u",
		"p",
	); code == http.StatusOK {
		t.Fatal("referrers of an unknown repository answered 200")
	}
	url := s.URL + "/v2/repo/manifests/v1"
	if code := do(t, http.MethodPut, url, []byte(manifest), "u", "p"); code >= 300 {
		t.Fatalf("first tag push %d", code)
	}
	if code := do(
		t,
		http.MethodPut,
		url,
		[]byte(manifest),
		"u",
		"p",
	); code != http.StatusForbidden {
		t.Fatalf("tag update %d, want 403", code)
	}
	// digest pushes are never refused
	if code := do(
		t,
		http.MethodPut,
		s.URL+"/v2/repo/manifests/"+digest.FromString(manifest).String(),
		[]byte(manifest),
		"u",
		"p",
	); code == http.StatusForbidden {
		t.Fatal("digest push refused")
	}
	if code := do(t, http.MethodGet, s.URL+"/other", nil, "u", "p"); code == 0 {
		t.Fatal("no response")
	}
	if s.Requests() < 6 {
		t.Fatalf("requests %d", s.Requests())
	}
}
