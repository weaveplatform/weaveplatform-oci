package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// upstreamAlias is the name the mirror uses for the private registry on
// the shared Docker network.
const upstreamAlias = "weave-upstream"

func (w *world) request(path, user, password string) (int, error) {
	req, err := http.NewRequestWithContext(
		context.Background(), http.MethodGet, "http://"+w.reg.host+path, nil,
	)
	if err != nil {
		return 0, err
	}
	if user != "" {
		req.SetBasicAuth(user, password)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

func (w *world) anonymousRefused(path string) error {
	code, err := w.request(path, "", "")
	if err != nil {
		return err
	}
	if code != http.StatusUnauthorized {
		return fmt.Errorf("anonymous %s returned %d, want 401", path, code)
	}
	return nil
}

func (w *world) wrongPasswordRefused(path, user string) error {
	code, err := w.request(path, user, "not-the-password")
	if err != nil {
		return err
	}
	if code != http.StatusUnauthorized {
		return fmt.Errorf("%s with a wrong password returned %d, want 401", user, code)
	}
	return nil
}

func (w *world) authenticatedSucceeds(path, user string) error {
	code, err := w.request(path, user, passwords[user])
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("%s %s returned %d, want 200", user, path, code)
	}
	return nil
}

// mirrorConfig derives the test mirror from the shipped mirror role, so
// the settings under test are the ones operators get. Only the upstream
// changes: the private registry on the shared network, over plain HTTP,
// with the publisher's credentials and every repository in scope.
func mirrorConfig(dir string) (string, error) {
	raw, err := os.ReadFile("../../deploy/zot/config/mirror.json")
	if err != nil {
		return "", err
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", err
	}
	sync := cfg["extensions"].(map[string]any)["sync"].(map[string]any)
	reg := sync["registries"].([]any)[0].(map[string]any)
	reg["urls"] = []string{"http://" + upstreamAlias + ":5000"}
	reg["tlsVerify"] = false
	reg["content"] = []map[string]any{{"prefix": "**"}}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "mirror.json")
	return path, os.WriteFile(path, out, 0o644) //nolint:gosec // read by the container user
}

func (w *world) mirrorRunning() error {
	ctx := context.Background()
	m, err := registries.mirror(ctx)
	w.mirror = m
	return err
}

// mirror starts (once) a weave-zot in the mirror role whose upstream is
// the private weave-zot, both attached to one Docker network.
func (s *registrySet) mirror(ctx context.Context) (*registry, error) {
	up, err := s.get(ctx, "weave-zot")
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.running["weave-zot-mirror"]; ok {
		return r, nil
	}
	netName := fmt.Sprintf("weave-accept-%d", time.Now().UnixNano())
	if out, err := exec.CommandContext(ctx, "docker", "network", "create", netName).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("docker network create: %w\n%s", err, out)
	}
	s.networks = append(s.networks, netName)
	if out, err := exec.CommandContext(
		ctx, "docker", "network", "connect", "--alias", upstreamAlias, netName, up.container.GetContainerID(),
	).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("docker network connect: %w\n%s", err, out)
	}
	dir, err := os.MkdirTemp("", "weave-zot-mirror-")
	if err != nil {
		return nil, err
	}
	cfg, err := mirrorConfig(dir)
	if err != nil {
		return nil, err
	}
	creds := filepath.Join(dir, "sync-credentials.json")
	b, _ := json.Marshal(map[string]map[string]string{
		upstreamAlias + ":5000": {"username": "publisher", "password": publisherPassword},
	})
	if err := os.WriteFile(creds, b, 0o644); err != nil { //nolint:gosec // read by the container user
		return nil, err
	}
	c, err := testcontainers.Run(ctx, s.zotImage,
		testcontainers.WithExposedPorts("5000/tcp"),
		testcontainers.WithFiles(
			testcontainers.ContainerFile{HostFilePath: cfg, ContainerFilePath: "/etc/zot/roles/mirror-test.json", FileMode: 0o644},
			testcontainers.ContainerFile{HostFilePath: creds, ContainerFilePath: "/etc/zot/sync-credentials.json", FileMode: 0o644},
		),
		testcontainers.WithCmd("serve", "/etc/zot/roles/mirror-test.json"),
		network.WithNetworkName(nil, netName),
		testcontainers.WithWaitStrategy(
			wait.ForHTTP("/readyz").WithPort("5000/tcp").WithStartupTimeout(90*time.Second),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("start mirror: %w", err)
	}
	host, err := c.PortEndpoint(ctx, "5000/tcp", "")
	if err != nil {
		return nil, err
	}
	r := &registry{kind: "weave-zot", host: host, container: c}
	s.running["weave-zot-mirror"] = r
	return r, nil
}

// onMirror runs a registry step against the mirror instead of the
// registry the scenario started.
func (w *world) onMirror(step func() error) error {
	if w.mirror == nil {
		return fmt.Errorf("no mirror is running")
	}
	saved := w.reg
	w.reg = w.mirror
	defer func() { w.reg = saved }()
	return step()
}

func (w *world) copyDownMirror(ref, user, layout string) error {
	return w.onMirror(func() error { return w.copyDown(ref, user, layout) })
}

func (w *world) referrerOnMirror(ref string) error {
	// On-demand sync fetches referrers with the image; give it a moment
	// when the first discovery races the background copy.
	var err error
	for i := 0; i < 10; i++ {
		if err = w.onMirror(func() error { return w.referrerDiscoverable(ref) }); err == nil ||
			!strings.Contains(err.Error(), "found 0") {
			return err
		}
		if serr := sleep(context.Background()); serr != nil {
			return serr
		}
	}
	return err
}
