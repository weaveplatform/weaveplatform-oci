package publish_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/weaveplatform/weaveplatform-oci/internal/testbundle"
	"github.com/weaveplatform/weaveplatform-oci/internal/testregistry"
	"github.com/weaveplatform/weaveplatform-oci/pkg/client"
	"github.com/weaveplatform/weaveplatform-oci/pkg/profile"
	"github.com/weaveplatform/weaveplatform-oci/pkg/publish"
	"github.com/weaveplatform/weaveplatform-oci/pkg/sign"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

// sabotage sits in front of a registry and breaks reads that only the
// publish self-check performs.
func sabotage(t *testing.T, mode string) string {
	t.Helper()
	reg := testregistry.New(testregistry.Options{NoReferrers: true})
	t.Cleanup(reg.Close)
	target, _ := url.Parse(reg.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case mode == "blobs" && r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/blobs/"):
			http.Error(w, "gone", http.StatusInternalServerError)
			return
		case mode == "referrers" && r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/manifests/sha256-"):
			// hide the fallback referrers index from readers
			http.Error(w, `{"errors":[{"code":"MANIFEST_UNKNOWN"}]}`, http.StatusNotFound)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func TestSelfCheckFailures(t *testing.T) {
	for _, mode := range []string{"blobs", "referrers"} {
		t.Run(mode, func(t *testing.T) {
			host := sabotage(t, mode)
			p := profile.Profile{
				Name:     "t",
				Kind:     profile.KindPrivate,
				Registry: profile.Registry{Host: host, Namespace: "weave-images", PlainHTTP: true},
				Signing: profile.Signing{
					Provider: profile.SigningCosignKey,
				},
				Verify: profile.Verify{Mode: profile.VerifyNone},
			}
			c := client.New(
				p,
				client.Options{
					Credentials: func(context.Context, string) (auth.Credential, error) { return auth.EmptyCredential, nil },
				},
			)
			d := filepath.Join(t.TempDir(), "b")
			if err := testbundle.Write(
				d,
				testbundle.Options{OS: spec.OSLinux, Arch: spec.ArchAMD64},
			); err != nil {
				t.Fatal(err)
			}
			k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			s, _ := sign.New(k)
			_, err := publish.Run(
				context.Background(),
				publish.Request{
					Client:     c,
					Bundles:    []string{d},
					Repository: "r",
					Tag:        "t",
					Signer:     s,
				},
			)
			if !errors.Is(err, publish.ErrSelfCheck) {
				t.Fatalf("want ErrSelfCheck, got %v", err)
			}
		})
	}
}
