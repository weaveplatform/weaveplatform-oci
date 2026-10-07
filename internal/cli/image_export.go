package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/weaveplatform/weaveplatform-oci/internal/imagebuild"
)

func newImageExport(tools imagebuild.Tools, emit func(any) error) *cobra.Command {
	var o imagebuild.ExportOptions
	cmd := &cobra.Command{
		Use:   "export LAYOUT",
		Short: "Prepare target disk files from a pinned OCI image; acceptance remains pending",
		Args:  usageArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if o.Out == "" || o.Target == "" || o.Platform == "" || o.ExpectedDigest == "" {
				return fmt.Errorf(
					"%w: --out, --target, --platform and --expected-digest required",
					errUsage,
				)
			}
			o.Layout = args[0]
			result, err := tools.ExportImage(cmd.Context(), o)
			if err != nil {
				return fmt.Errorf("image export: %w", err)
			}
			return emit(result)
		},
	}
	cmd.Flags().StringVar(&o.Out, "out", "", "New output directory")
	cmd.Flags().StringVar(&o.Platform, "platform", "", "Guest OS/architecture")
	cmd.Flags().StringVar(&o.ExpectedDigest, "expected-digest", "", "Pinned source index digest")
	cmd.Flags().StringVar(&o.Target, "target", "", "Target: "+imagebuild.ExportTargets())
	return cmd
}
