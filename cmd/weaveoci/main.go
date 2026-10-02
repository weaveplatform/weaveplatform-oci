// Command weaveoci packs, inspects and unpacks weave guest artifacts.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/deploymenttheory/weaveplatform-oci/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli.Main(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
