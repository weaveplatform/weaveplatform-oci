package publish_test

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/deploymenttheory/weaveplatform-oci/internal/testbundle"
	"github.com/deploymenttheory/weaveplatform-oci/internal/testregistry"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/chunk"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/profile"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/publish"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/sign"
	"github.com/deploymenttheory/weaveplatform-oci/pkg/spec"
)

type brokenSigner struct{ pub crypto.PublicKey }

func (b brokenSigner) Public() crypto.PublicKey { return b.pub }
func (b brokenSigner) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return nil, errors.New("hsm offline")
}

func TestPublishSignerFailure(t *testing.T) {
	c, dirs, _ := setup(t, profile.SigningCosignKey, testregistry.Options{})
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	s, _ := sign.New(brokenSigner{pub: k.Public()})
	if _, err := publish.Run(
		context.Background(),
		publish.Request{Client: c, Bundles: dirs[:1], Repository: "r", Tag: "t", Signer: s},
	); err == nil {
		t.Fatal("broken signer published")
	}
}

func TestPublishTempDirFailures(t *testing.T) {
	c, dirs, s := setup(t, profile.SigningCosignKey, testregistry.Options{})
	work := t.TempDir()
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv("TMPDIR", missing) // POSIX
	t.Setenv("TMP", missing)    // Windows reads TMP, then TEMP
	t.Setenv("TEMP", missing)
	if _, err := publish.Run(
		context.Background(),
		publish.Request{Client: c, Bundles: dirs[:1], Repository: "r", Tag: "t", Signer: s},
	); err == nil {
		t.Fatal("missing temp dir accepted")
	}
	// with an explicit work dir, the self-check's temp dir fails after push
	if _, err := publish.Run(
		context.Background(),
		publish.Request{
			Client:     c,
			Bundles:    dirs[:1],
			Repository: "r",
			Tag:        "t2",
			Signer:     s,
			WorkDir:    work,
			Chunk:      chunk.Options{TempDir: work},
		},
	); !errors.Is(
		err,
		publish.ErrSelfCheck,
	) {
		t.Fatalf("want ErrSelfCheck, got %v", err)
	}
}

func TestPublishInvalidBundle(t *testing.T) {
	c, _, s := setup(t, profile.SigningCosignKey, testregistry.Options{})
	dir := filepath.Join(t.TempDir(), "darwin")
	if err := testbundle.Write(
		dir,
		testbundle.Options{OS: spec.OSDarwin, Arch: spec.ArchARM64},
	); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "bundle.json")
	raw, _ := os.ReadFile(p)
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	delete(doc["firmware"].(map[string]any), "hardwareModel")
	raw, _ = json.Marshal(doc)
	_ = os.WriteFile(p, raw, 0o600)
	if _, err := publish.Run(
		context.Background(),
		publish.Request{Client: c, Bundles: []string{dir}, Repository: "r", Tag: "t", Signer: s},
	); !errors.Is(
		err,
		spec.ErrInvalid,
	) {
		t.Fatalf("invalid bundle published: %v", err)
	}
}
