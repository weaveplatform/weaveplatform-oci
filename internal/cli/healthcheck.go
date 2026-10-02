package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/spf13/cobra"
)

var errUnhealthy = errors.New("unhealthy")

// newHealthcheck probes an HTTP endpoint. The weave-zot image uses it as its
// HEALTHCHECK because the upstream zot image is distroless and has no curl.
func newHealthcheck(stdout io.Writer) *cobra.Command {
	var (
		url     string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "healthcheck",
		Short: "Exit 0 when an HTTP endpoint answers 200 (container health checks)",
		Args:  usageArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return fmt.Errorf("%w: %w", errUsage, err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("%w: %w", errUnhealthy, err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("%w: %s returned %s", errUnhealthy, url, resp.Status)
			}
			_, err = fmt.Fprintf(stdout, "ok %s\n", url)
			return err //nolint:wrapcheck // terminal write
		},
	}
	cmd.Flags().StringVar(&url, "url", "http://127.0.0.1:5000/readyz", "endpoint to probe")
	cmd.Flags().DurationVar(&timeout, "timeout", 3*time.Second, "probe timeout")
	return cmd
}
