// Package cli implements the weaveoci command line.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/weaveplatform/weaveplatform-oci/internal/buildinfo"
)

// Exit codes.
const (
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
)

// errUsage marks argument errors so Main can return ExitUsage.
var errUsage = errors.New("usage")

// errNonConformant is returned by inspect --strict when problems were found.
var errNonConformant = errors.New("artifact is not conformant")

// Main runs weaveoci with args and returns the process exit code.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	// cobra carries ctx through ExecuteContext and cmd.Context().
	root := newRoot(stdout, stderr) //nolint:contextcheck // see above
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.ExecuteContext(ctx)
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, errUsage):
		_, _ = fmt.Fprintln(stderr, "weaveoci:", err)
		return ExitUsage
	default:
		_, _ = fmt.Fprintln(stderr, "weaveoci:", err)
		return ExitFailure
	}
}

func newRoot(stdout, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:           "weaveoci",
		Short:         "Pack, publish, pull, sign and verify weave guest artifacts (contract v1)",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return fmt.Errorf("%w: %w", errUsage, err)
	})
	root.AddCommand(
		newPack(stdout),
		newInspect(stdout, stderr),
		newUnpack(stdout),
		newHealthcheck(stdout),
		newVersion(stdout),
		newSource(stdout),
		newBundle(stdout),
	)
	g := &globals{}
	g.register(root)
	root.AddCommand(registryCommands(stdout, g)...)
	root.AddCommand(newChannel(stdout))
	return root
}

func usageArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return fmt.Errorf(
				"%w: %s takes %d argument(s), got %d",
				errUsage,
				cmd.Name(),
				n,
				len(args),
			)
		}
		return nil
	}
}

func newVersion(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  usageArgs(0),
		RunE: func(*cobra.Command, []string) error {
			_, err := fmt.Fprintf(
				stdout,
				"weaveoci %s commit %s built %s\n",
				buildinfo.Version(),
				buildinfo.Commit(),
				buildinfo.BuildDate(),
			)
			return err //nolint:wrapcheck // terminal write
		},
	}
}
