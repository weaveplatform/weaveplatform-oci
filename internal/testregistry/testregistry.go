// Package testregistry runs an in-process OCI registry for tests: the
// go-containerregistry in-memory registry behind a chi router that can
// require basic auth, hide the referrers API, fail requests transiently,
// count requests and refuse tag updates.
package testregistry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
	ggcr "github.com/google/go-containerregistry/pkg/registry"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Options select behaviour.
type Options struct {
	// Username and Password, when set, are required on every /v2/ request.
	Username, Password string
	// NoReferrers makes the referrers API answer 404, like GHCR and
	// distribution v3, so clients use the sha256-<hex> fallback tag.
	NoReferrers bool
	// FailFirst answers 503 to the first N requests (exercising retries).
	FailFirst int64
	// ImmutableTags refuses a manifest PUT to a tag that already exists.
	ImmutableTags bool
}

// Server is a running test registry.
type Server struct {
	*httptest.Server
	// Host is host:port for references.
	Host     string
	requests atomic.Int64
	failed   atomic.Int64
	mu       sync.Mutex
	tags     map[string]bool
	o        Options
}

// New starts a registry; call Close when done.
func New(o Options) *Server {
	s := &Server{o: o, tags: map[string]bool{}}
	inner := ggcr.New(ggcr.WithReferrersSupport(!o.NoReferrers), ggcr.Logger(nopLogger()))
	r := chi.NewRouter()
	r.Use(s.count, s.fail, s.auth)
	// chi parameters cannot span "/", and repository names do, so the two
	// endpoints that need special handling are recognised by path shape.
	r.Handle("/*", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		name, kind, ref := splitV2(req.URL.Path)
		switch {
		case req.Method == http.MethodGet && kind == "referrers":
			s.referrers(inner, name)(w, req)
		case req.Method == http.MethodPut && kind == "manifests":
			s.immutable(inner, name, ref)(w, req)
		default:
			inner.ServeHTTP(w, req)
		}
	}))
	s.Server = httptest.NewServer(r)
	s.Host = strings.TrimPrefix(s.URL, "http://")
	return s
}

// Requests returns how many requests reached the registry.
func (s *Server) Requests() int64 { return s.requests.Load() }

func (s *Server) count(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) fail(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.failed.Add(1) <= s.o.FailFirst {
			http.Error(w, "transient", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.o.Username != "" {
			u, p, ok := r.BasicAuth()
			if !ok || u != s.o.Username || p != s.o.Password {
				w.Header().Set("WWW-Authenticate", `Basic realm="testregistry"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// referrers serves the referrers API spec-correctly: the in-memory registry
// reports a referrer's config media type as its artifactType and ignores the
// artifactType filter, so the descriptors are rewritten from each referrer
// manifest's own artifactType and filtered here.
// splitV2 splits /v2/<name>/<kind>/<ref>.
func splitV2(path string) (name, kind, ref string) {
	rest, ok := strings.CutPrefix(path, "/v2/")
	if !ok {
		return "", "", ""
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 3 {
		return "", "", ""
	}
	n := len(parts)
	return strings.Join(parts[:n-2], "/"), parts[n-2], parts[n-1]
}

func (s *Server) referrers(inner http.Handler, name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.o.NoReferrers {
			http.Error(
				w,
				"404 page not found",
				http.StatusNotFound,
			) // what GHCR and distribution v3 return
			return
		}
		rec := httptest.NewRecorder()
		inner.ServeHTTP(rec, r)
		var idx ocispec.Index
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &idx) != nil {
			copyResponse(w, rec)
			return
		}
		want := r.URL.Query().Get("artifactType")
		kept := []ocispec.Descriptor{}
		for _, d := range idx.Manifests {
			mrec := httptest.NewRecorder()
			mreq := httptest.NewRequestWithContext(r.Context(), http.MethodGet,
				"/v2/"+name+"/manifests/"+d.Digest.String(), nil)
			mreq.Header = r.Header.Clone()
			inner.ServeHTTP(mrec, mreq)
			var m ocispec.Manifest
			if json.Unmarshal(mrec.Body.Bytes(), &m) == nil && m.ArtifactType != "" {
				d.ArtifactType = m.ArtifactType
			}
			if want == "" || d.ArtifactType == want {
				kept = append(kept, d)
			}
		}
		idx.Manifests = kept
		body, _ := json.Marshal(idx)
		w.Header().Set("Content-Type", ocispec.MediaTypeImageIndex)
		if want != "" {
			w.Header().Set("OCI-Filters-Applied", "artifactType")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
}

func copyResponse(w http.ResponseWriter, rec *httptest.ResponseRecorder) {
	for k, v := range rec.Header() {
		w.Header()[k] = v
	}
	w.WriteHeader(rec.Code)
	_, _ = w.Write(rec.Body.Bytes())
}

func (s *Server) immutable(inner http.Handler, name, ref string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := name + ":" + ref
		isTag := !strings.HasPrefix(ref, "sha256:")
		if s.o.ImmutableTags && isTag {
			s.mu.Lock()
			exists := s.tags[key]
			s.tags[key] = true
			s.mu.Unlock()
			if exists {
				http.Error(
					w,
					`{"errors":[{"code":"DENIED","message":"tag is immutable"}]}`,
					http.StatusForbidden,
				)
				return
			}
		}
		inner.ServeHTTP(w, r)
	}
}
