package testregistry_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/weaveplatform/weaveplatform-oci/internal/testregistry"
)

func getIndex(t *testing.T, url string) (int, ocispec.Index, string) {
	t.Helper()
	resp, err := http.Get(url) //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var idx ocispec.Index
	_ = json.Unmarshal(b, &idx)
	return resp.StatusCode, idx, resp.Header.Get("OCI-Filters-Applied")
}

func TestReferrersAreSpecCorrect(t *testing.T) {
	s := testregistry.New(testregistry.Options{})
	defer s.Close()
	subj := digest.FromString(manifest)
	if code := do(
		t,
		http.MethodPut,
		s.URL+"/v2/a/b/manifests/"+subj.String(),
		[]byte(manifest),
		"",
		"",
	); code >= 300 {
		t.Fatalf("subject push %d", code)
	}
	ref := fmt.Sprintf(
		`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","artifactType":"application/vnd.test.sig","config":{"mediaType":"application/vnd.oci.empty.v1+json","digest":"sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a","size":2},"layers":[],"subject":{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"%s","size":%d}}`,
		subj,
		len(manifest),
	)
	if code := do(
		t,
		http.MethodPut,
		s.URL+"/v2/a/b/manifests/"+digest.FromString(ref).String(),
		[]byte(ref),
		"",
		"",
	); code >= 300 {
		t.Fatalf("referrer push %d", code)
	}
	code, idx, _ := getIndex(t, s.URL+"/v2/a/b/referrers/"+subj.String())
	if code != 200 || len(idx.Manifests) != 1 ||
		idx.Manifests[0].ArtifactType != "application/vnd.test.sig" {
		t.Fatalf("%d %+v", code, idx)
	}
	code, idx, filters := getIndex(
		t,
		s.URL+"/v2/a/b/referrers/"+subj.String()+"?artifactType=application/vnd.other",
	)
	if code != 200 || len(idx.Manifests) != 0 || filters != "artifactType" {
		t.Fatalf("filter %d %+v %q", code, idx, filters)
	}
	none := testregistry.New(testregistry.Options{NoReferrers: true})
	defer none.Close()
	if code, _, _ := getIndex(
		t,
		none.URL+"/v2/a/b/referrers/"+subj.String(),
	); code != http.StatusNotFound {
		t.Fatalf("hidden referrers API answered %d", code)
	}
}
