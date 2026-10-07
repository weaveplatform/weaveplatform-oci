package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/weaveplatform/weaveplatform-oci/internal/imagebuild"
)

func newImagePrepare(
	p imagebuild.Packages,
	load func() (imagebuild.Lock, error),
	emit func(any) error,
) *cobra.Command {
	var platform, cache, out string
	cmd := &cobra.Command{
		Use:   "prepare-agent",
		Short: "Verify pinned installers and prepare an offline guest payload",
		Args:  usageArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if platform == "" || cache == "" || out == "" {
				return fmt.Errorf("%w: --platform, --cache and --out required", errUsage)
			}
			l, err := load()
			if err != nil {
				return fmt.Errorf("image: %w", err)
			}
			assets, err := p.Prepare(cmd.Context(), l, platform, cache, out)
			if err != nil {
				return fmt.Errorf("prepare agent: %w", err)
			}
			return emit(assets)
		},
	}
	cmd.Flags().StringVar(&platform, "platform", "", "Guest OS/architecture")
	cmd.Flags().StringVar(&cache, "cache", "", "Verified download cache")
	cmd.Flags().StringVar(&out, "out", "", "New payload directory")
	return cmd
}
