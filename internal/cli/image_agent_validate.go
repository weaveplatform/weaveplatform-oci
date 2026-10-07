package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/weaveplatform/weaveplatform-oci/internal/imagebuild"
	"github.com/weaveplatform/weaveplatform-oci/pkg/imagecheck"
)

func newImageAgentValidate(
	tools imagebuild.Tools,
	load func() (imagebuild.Lock, error),
	emit func(any) error,
	log io.Writer,
) *cobra.Command {
	var ref, tag, out, consoleUser string
	var timeout time.Duration
	var sequence uint64
	cmd := &cobra.Command{
		Use:   "validate-agent-linux LAYOUT",
		Short: "Unpack and exercise two independent agent clones through QEMU",
		Args:  usageArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if tag == "" || out == "" || timeout <= 0 {
				return fmt.Errorf("%w: --tag, --out and positive --timeout required", errUsage)
			}
			lock, err := load()
			if err != nil {
				return fmt.Errorf("acceptance lock: %w", err)
			}
			store, root, err := resolveRoot(cmd.Context(), args[0], ref)
			if err != nil {
				return err
			}
			report, err := imagecheck.Validate(
				cmd.Context(),
				imagecheck.Candidate{
					Store:   store,
					Root:    root,
					Tag:     tag,
					Out:     out,
					Timeout: timeout,
					Log:     log,
				},
				tools.LinuxAgentBoot(lock, consoleUser, sequence),
			)
			if err != nil {
				return fmt.Errorf("linux agent acceptance: %w", err)
			}
			return emit(report)
		},
	}
	cmd.Flags().StringVar(&ref, "ref", "", "Candidate index tag or digest")
	cmd.Flags().StringVar(&tag, "tag", "", "Immutable candidate tag")
	cmd.Flags().StringVar(&out, "out", "", "New directory for clone disks and reports")
	cmd.Flags().
		StringVar(&consoleUser, "console-user", "", "Required active console user for desktop acceptance")
	cmd.Flags().
		DurationVar(&timeout, "timeout", 20*time.Minute, "Deadline for each clone, including reboot")
	cmd.Flags().
		Uint64Var(&sequence, "minimum-sequence", 0, "Sealed channel manifest high-water mark")
	return cmd
}
