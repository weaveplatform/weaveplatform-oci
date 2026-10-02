// Package acceptance runs the Gherkin features in features/ against the real
// weaveoci binary and real registries (decision 0013). The binary is built
// once with -cover; when WEAVEOCI_GOCOVERDIR is set (make accept) every run
// writes coverage there so acceptance counts toward the merged gate.
//
// Scenarios tagged @docker start weave-zot (built from deploy/zot, or the
// image named by WEAVEOCI_ZOT_IMAGE) and registry:3.1.2 with testcontainers.
// They are skipped, with a message, when no Docker daemon is reachable;
// CI always has one, so the gate always includes them.
package acceptance

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/cucumber/godog"
	"github.com/testcontainers/testcontainers-go"
)

var (
	binary   string
	coverDir = os.Getenv("WEAVEOCI_GOCOVERDIR")
)

func TestMain(m *testing.M) {
	flag.Parse()
	dir, err := os.MkdirTemp("", "weaveoci-accept-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := func() int {
		defer func() { _ = os.RemoveAll(dir) }()
		defer registries.terminate()
		binary = filepath.Join(dir, "weaveoci")
		if runtime.GOOS == "windows" {
			binary += ".exe"
		}
		args := []string{"build", "-o", binary}
		if coverDir != "" {
			args = append(
				args,
				"-cover",
				"-covermode=atomic",
				"-coverpkg=github.com/weaveplatform/weaveplatform-oci/...",
			)
		}
		cmd := exec.Command("go", append(args, "../../cmd/weaveoci")...)
		cmd.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "build weaveoci: %v\n%s", err, out)
			return 1
		}
		return m.Run()
	}()
	os.Exit(code)
}

func dockerAvailable() bool {
	p, err := testcontainers.NewDockerProvider()
	if err != nil {
		return false
	}
	defer func() { _ = p.Close() }()
	return p.Health(context.Background()) == nil
}

func TestFeatures(t *testing.T) {
	tags := os.Getenv("WEAVEOCI_ACCEPT_TAGS")
	if !dockerAvailable() {
		t.Log("no Docker daemon: skipping @docker scenarios")
		if tags == "" {
			tags = "~@docker"
		} else {
			tags += " && ~@docker"
		}
	}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) { newWorld(t).register(sc) },
		Options: &godog.Options{
			Format: "pretty", Paths: []string{"features"}, TestingT: t, Strict: true,
			Tags: tags, Concurrency: 1,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("acceptance features failed")
	}
}
