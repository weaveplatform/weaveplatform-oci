package acceptance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/go-chi/chi/v5"
)

// cloudServer stands in for a distribution's cloud image mirror: an image,
// a SHA256SUMS naming it, and a detached signature over SHA256SUMS, served
// over HTTP the way cloud-images.ubuntu.com serves them.
type cloudServer struct {
	mu    sync.Mutex
	files map[string][]byte
	srv   *httptest.Server
}

func (c *cloudServer) handler() http.Handler {
	r := chi.NewRouter()
	r.Get("/{file}", func(w http.ResponseWriter, req *http.Request) {
		c.mu.Lock()
		b, ok := c.files[chi.URLParam(req, "file")]
		c.mu.Unlock()
		if !ok {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write(b)
	})
	return r
}

func newSigningKey(name string) (*openpgp.Entity, []byte, string, error) {
	e, err := openpgp.NewEntity(
		name,
		"",
		name+"@example.invalid",
		&packet.Config{Algorithm: packet.PubKeyAlgoEdDSA},
	)
	if err != nil {
		return nil, nil, "", err
	}
	var ring bytes.Buffer
	w, err := armor.Encode(&ring, openpgp.PublicKeyType, nil)
	if err != nil {
		return nil, nil, "", err
	}
	if err := e.Serialize(w); err != nil {
		return nil, nil, "", err
	}
	_ = w.Close()
	return e, ring.Bytes(), strings.ToUpper(hex.EncodeToString(e.PrimaryKey.Fingerprint)), nil
}

// signedCloudImage serves name, signed by a fresh key whose armored public
// key is written to {<key>.asc} and whose fingerprint is {<key>.fpr}. The
// image (a small raw disk with data in two places) is also kept locally as
// {upstream.img} so the pulled disk can be compared with it.
func (w *world) signedCloudImage(name, key string) error {
	e, ring, fpr, err := newSigningKey(key)
	if err != nil {
		return err
	}
	if err := os.WriteFile(w.path(key+".asc"), ring, 0o600); err != nil {
		return err
	}
	w.vars[key+".fpr"] = fpr
	img := make([]byte, 6<<20)
	rng := rand.New(rand.NewPCG(7, 7)) //nolint:gosec // test data
	for _, off := range []int{0, 4 << 20} {
		for i := off; i < off+(1<<20); i++ {
			img[i] = byte(rng.UintN(256))
		}
	}
	if err := os.WriteFile(w.path("upstream.img"), img, 0o600); err != nil {
		return err
	}
	h := sha256.Sum256(img)
	sums := []byte(hex.EncodeToString(h[:]) + " *" + name + "\n")
	var sig bytes.Buffer
	if err := openpgp.DetachSign(&sig, e, bytes.NewReader(sums), nil); err != nil {
		return err
	}
	c := &cloudServer{
		files: map[string][]byte{name: img, "SHA256SUMS": sums, "SHA256SUMS.gpg": sig.Bytes()},
	}
	c.srv = httptest.NewServer(c.handler())
	w.cloud = c
	w.vars["server"] = c.srv.URL + "/"
	return nil
}

func (w *world) anotherKey(key string) error {
	_, ring, fpr, err := newSigningKey(key)
	if err != nil {
		return err
	}
	w.vars[key+".fpr"] = fpr
	return os.WriteFile(w.path(key+".asc"), ring, 0o600)
}

// alterServedImage flips a byte of the served image after it was signed: a
// compromised mirror serving different bytes under a valid signature.
func (w *world) alterServedImage(name string) error {
	if w.cloud == nil {
		return errors.New("no cloud image server is running")
	}
	w.cloud.mu.Lock()
	defer w.cloud.mu.Unlock()
	b := append([]byte{}, w.cloud.files[name]...)
	b[len(b)/2] ^= 0xff
	w.cloud.files[name] = b
	return nil
}
