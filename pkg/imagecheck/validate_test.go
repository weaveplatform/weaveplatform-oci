package imagecheck

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/weaveplatform/weaveplatform-oci/internal/testbundle"
	"github.com/weaveplatform/weaveplatform-oci/pkg/chunk"
	"github.com/weaveplatform/weaveplatform-oci/pkg/conformance"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

func require(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func candidate(t *testing.T, platform string, agent bool) Candidate {
	t.Helper()
	dir := t.TempDir()
	store, err := pack.OpenLayout(t.Context(), filepath.Join(dir, "layout"))
	require(t, err)
	parts := strings.Split(platform, "/")
	var children []ocispec.Descriptor
	for _, arch := range strings.Split(parts[1], ",") {
		bundle := filepath.Join(dir, "bundle-"+arch)
		require(
			t,
			testbundle.Write(bundle, testbundle.Options{OS: parts[0], Arch: arch, Size: 1 << 20}),
		)
		b, err := pack.LoadBundle(bundle)
		require(t, err)
		b.File.Guest.Variant = "base"
		if agent {
			b.File.Guest.Variant = "agent"
			b.File.Provisioning.Agent = &spec.Agent{Name: "weave-agent", Version: "1.0.0"}
			b.File.Build.Base = &spec.BaseImage{
				Name:   "test-base",
				Digest: digest.FromString("parent-" + arch).String(),
			}
		}
		desc, err := pack.Manifest(t.Context(), b, store, chunk.Options{})
		require(t, err)
		children = append(children, desc)
	}
	root, err := pack.Index(t.Context(), store, children, nil)
	require(t, err)
	require(t, store.Tag(t.Context(), root, "candidate"))
	return Candidate{
		Store:   store,
		Root:    root,
		Tag:     "candidate",
		Out:     filepath.Join(dir, "acceptance"),
		Timeout: time.Minute,
	}
}

func observed(c Clone) Boot {
	b := Boot{
		Platform:       c.Config.Guest.OS + "/" + c.Config.Guest.Arch,
		OSVersion:      c.Config.Guest.OSVersion,
		OSBuild:        c.Config.Guest.OSBuild,
		Edition:        c.Config.Guest.Edition,
		Marker:         c.Marker,
		MachineID:      fmt.Sprintf("%032x", c.Number),
		ElapsedSeconds: 1,
		Passed:         true,
		Identities:     map[string]string{},
		Checks:         map[string]bool{},
		Profile:        ValidationProfile(c.Config),
	}
	for _, k := range requiredIdentities(c.Config.Guest.OS) {
		b.Identities[k] = fmt.Sprintf("%s-%d", k, c.Number)
	}
	if c.Config.Provisioning.Agent != nil {
		b.Identities["agentStoreKeyDigest"] = fmt.Sprintf("sha256:%064x", c.Number)
	}
	for _, k := range requiredChecks(c.Config) {
		b.Checks[k] = true
	}
	if c.Config.Provisioning.Agent != nil {
		b.Operations, b.RebootOperations = map[string]Outcome{}, map[string]Outcome{}
		for _, ops := range []map[string]Outcome{b.Operations, b.RebootOperations} {
			for _, name := range []string{"presence", "exec", "time", "metrics", "clipboard", "session", "display", "clipboard-roundtrip", "display-roundtrip", "desktop-session"} {
				ops[name] = Outcome{Status: Passed}
			}
		}
	}
	return b
}

func TestValidateUnpacksIndependentClonesAndBindsReports(t *testing.T) {
	for _, platform := range []string{"linux/amd64,arm64", "windows/amd64,arm64", "darwin/arm64"} {
		for _, agent := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/agent=%t", platform, agent), func(t *testing.T) {
				c := candidate(t, platform, agent)
				var log bytes.Buffer
				c.Log = &log
				var requests []Clone
				report, err := Validate(
					t.Context(),
					c,
					func(ctx context.Context, clone Clone) (Boot, error) {
						if _, ok := ctx.Deadline(); !ok {
							t.Fatal("clone has no deadline")
						}
						b, err := pack.LoadBundle(clone.Bundle)
						require(t, err)
						if b.File.Guest != clone.Config.Guest {
							t.Fatal("not unpacked from candidate")
						}
						// A malicious/broken adapter modifying its input cannot poison the
						// other clone or the immutable candidate's source disk.
						disk := filepath.Join(clone.Bundle, "disk0.img")
						data, err := os.ReadFile(disk)
						require(t, err)
						if string(data) == "modified" {
							t.Fatal("clone shared disk")
						}
						require(t, os.WriteFile(disk, []byte("modified"), 0o600))
						requests = append(requests, clone)
						return observed(clone), nil
					},
				)
				require(t, err)
				if !report.Passed || report.SchemaVersion != 3 ||
					!strings.Contains(log.String(), "clone 2/2") {
					t.Fatal(report, log.String())
				}
				for n, clone := range requests {
					if _, err := os.Stat(clone.Bundle); !os.IsNotExist(err) {
						t.Fatal("disposable disk retained", err)
					}
					if n%2 == 0 {
						other := requests[n+1]
						if bytes.Equal(clone.OwnKey, other.OwnKey) ||
							!bytes.Equal(clone.OwnKey, other.ForeignKey) ||
							clone.Marker == other.Marker {
							t.Fatal("clone challenges or keys not independent")
						}
					}
				}
				data, err := os.ReadFile(filepath.Join(c.Out, "acceptance.json"))
				require(t, err)
				for _, clone := range requests {
					key, err := json.Marshal(clone.OwnKey)
					require(t, err)
					if bytes.Contains(data, key) || len(clone.OwnKey) != ed25519.PrivateKeySize {
						t.Fatal("private key in persisted report")
					}
				}
				inspection, err := conformance.Check(
					t.Context(),
					c.Store,
					c.Root,
					conformance.Options{Deep: true},
				)
				require(t, err)
				require(t, Check(data, inspection, c.Tag))
			})
		}
	}
}

func TestValidateRefusesSkippedFailedAndReplayedEvidence(t *testing.T) {
	for _, failure := range []string{"skip", "error", "challenge", "version", "identity", "check", "timeout", "nan", "write"} {
		t.Run(failure, func(t *testing.T) {
			c := candidate(t, "windows/amd64", true)
			boot := func(ctx context.Context, clone Clone) (Boot, error) {
				b := observed(clone)
				switch failure {
				case "skip":
					b = Boot{}
				case "error":
					return b, errors.New("VM failed")
				case "challenge":
					b.Marker = "WEAVE-BOOT-OK-" + strings.Repeat("a", 24)
				case "version":
					b.OSVersion = "old"
				case "identity":
					b.MachineID = "same"
				case "check":
					delete(b.Checks, "trust-after-reboot")
				case "timeout":
					<-ctx.Done()
				case "nan":
					b.ElapsedSeconds = math.NaN()
				case "write":
					require(t, os.MkdirAll(filepath.Join(c.Out, "acceptance.json"), 0o750))
				}
				return b, nil
			}
			if failure == "timeout" {
				c.Timeout = time.Millisecond
			}
			r, err := Validate(t.Context(), c, boot)
			if err == nil {
				t.Fatal("invalid evidence accepted", r)
			}
			if failure != "write" && r.Passed {
				t.Fatal("failed result passed", r)
			}
			if failure != "nan" && failure != "write" {
				var saved Report
				data, err := os.ReadFile(filepath.Join(c.Out, "acceptance.json"))
				require(t, err)
				require(t, json.Unmarshal(data, &saved))
				if saved.Passed {
					t.Fatal("persisted failure passed")
				}
			}
		})
	}
}

func TestValidatePreflightAndFilesystemFailures(t *testing.T) {
	for _, failure := range []string{"nil-store", "manifest", "empty-tag", "tag", "root", "out", "timeout", "nil-adapter", "exists", "parent", "integrity", "cancel", "clone-directory", "unpack"} {
		t.Run(failure, func(t *testing.T) {
			c := candidate(t, "linux/amd64", false)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var boot BootClone = func(_ context.Context, clone Clone) (Boot, error) {
				if failure == "clone-directory" {
					require(
						t,
						os.WriteFile(
							filepath.Join(c.Out, "linux-amd64-2"),
							[]byte("obstacle"),
							0o600,
						),
					)
				}
				if failure == "unpack" {
					require(t, os.RemoveAll(filepath.Join(filepath.Dir(c.Out), "layout", "blobs")))
				}
				return observed(clone), nil
			}
			switch failure {
			case "nil-store":
				c.Store = nil
			case "manifest":
				c.Root.MediaType = spec.MediaTypeManifest
			case "empty-tag":
				c.Tag = ""
			case "tag":
				c.Tag = "absent"
			case "root":
				c.Root.Digest = digest.Digest("sha256:" + strings.Repeat("0", 64))
			case "out":
				c.Out = ""
			case "timeout":
				c.Timeout = 0
			case "nil-adapter":
				boot = nil
			case "exists":
				require(t, os.Mkdir(c.Out, 0o750))
			case "parent":
				require(t, os.WriteFile(c.Out, []byte("obstacle"), 0o600))
				c.Out = filepath.Join(c.Out, "child")
			case "integrity":
				require(t, os.RemoveAll(filepath.Join(filepath.Dir(c.Out), "layout", "blobs")))
			case "cancel":
				cancel()
			}
			if r, err := Validate(ctx, c, boot); err == nil || r.Passed {
				t.Fatal("failure passed", r, err)
			}
		})
	}
}

func TestValidateCancellationBetweenClones(t *testing.T) {
	c := candidate(t, "linux/arm64", false)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err := Validate(ctx, c, func(_ context.Context, clone Clone) (Boot, error) {
		cancel()
		return observed(clone), nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
