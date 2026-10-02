package acceptance

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/crypto/bcrypt"
)

// Test credentials for weave-zot's private role.
const (
	publisherPassword = "publisher-test-password"
	adminPassword     = "admin-test-password"
)

type registry struct {
	kind      string // weave-zot or distribution
	host      string // host:port
	container testcontainers.Container
}

type registrySet struct {
	mu      sync.Mutex
	running map[string]*registry
}

var registries = &registrySet{running: map[string]*registry{}}

func (s *registrySet) get(ctx context.Context, kind string) (*registry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.running[kind]; ok {
		return r, nil
	}
	var (
		c   testcontainers.Container
		err error
	)
	switch kind {
	case "weave-zot":
		c, err = startZot(ctx)
	case "distribution":
		c, err = testcontainers.Run(
			ctx,
			"registry:3.1.2",
			testcontainers.WithExposedPorts("5000/tcp"),
			testcontainers.WithWaitStrategy(
				wait.ForHTTP("/v2/").WithPort("5000/tcp").WithStartupTimeout(60*time.Second),
			),
		)
	default:
		return nil, fmt.Errorf("unknown registry %q", kind)
	}
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", kind, err)
	}
	host, err := c.PortEndpoint(ctx, "5000/tcp", "")
	if err != nil {
		return nil, err
	}
	r := &registry{kind: kind, host: host, container: c}
	s.running[kind] = r
	return r, nil
}

func (s *registrySet) terminate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.running {
		_ = testcontainers.TerminateContainer(r.container)
	}
}

func startZot(ctx context.Context) (testcontainers.Container, error) {
	dir, err := os.MkdirTemp("", "weave-zot-")
	if err != nil {
		return nil, err
	}
	htpasswd := filepath.Join(dir, "htpasswd")
	var lines []byte
	for user, pw := range map[string]string{"publisher": publisherPassword, "admin": adminPassword} {
		h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
		if err != nil {
			return nil, err
		}
		lines = append(lines, fmt.Sprintf("%s:%s\n", user, h)...)
	}
	if err := os.WriteFile(
		htpasswd,
		lines,
		0o644,
	); err != nil { //nolint:gosec // read by the container user
		return nil, err
	}
	opts := []testcontainers.ContainerCustomizer{
		testcontainers.WithExposedPorts("5000/tcp"),
		testcontainers.WithFiles(
			testcontainers.ContainerFile{
				HostFilePath:      htpasswd,
				ContainerFilePath: "/etc/zot/htpasswd",
				FileMode:          0o644,
			},
		),
		testcontainers.WithWaitStrategy(
			wait.ForHTTP("/readyz").WithPort("5000/tcp").WithStartupTimeout(90 * time.Second),
		),
	}
	image := os.Getenv("WEAVEOCI_ZOT_IMAGE")
	if image == "" {
		// BuildKit (the docker CLI) provides BUILDPLATFORM for the
		// cross-compiling stage; the testcontainers builder does not.
		image = "weave-zot:accept"
		cmd := exec.CommandContext(
			ctx,
			"docker",
			"build",
			"-q",
			"-t",
			image,
			"-f",
			"deploy/zot/Dockerfile",
			".",
		)
		cmd.Dir = "../.."
		cmd.Env = append(os.Environ(), "DOCKER_BUILDKIT=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("docker build weave-zot: %w\n%s", err, out)
		}
	}
	return testcontainers.Run(ctx, image, opts...)
}
