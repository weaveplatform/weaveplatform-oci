package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/weaveplatform/weaveplatform-oci/internal/imagebuild"
)

func newImageRebuild(emit func(any) error) *cobra.Command {
	var completed string
	cmd := &cobra.Command{
		Use:   "rebuild-plan INPUTS",
		Short: "Plan builds from pinned inputs and authenticated completion records",
		Args:  usageArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			var inputs []imagebuild.BuildInputs
			data, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("read build inputs: %w", err)
			}
			if err := json.Unmarshal(data, &inputs); err != nil {
				return fmt.Errorf("decode build inputs: %w", err)
			}
			var accepted []imagebuild.AcceptedBuild
			if completed != "" {
				data, err := os.ReadFile(completed)
				if err != nil {
					return fmt.Errorf("read accepted builds: %w", err)
				}
				if err := json.Unmarshal(data, &accepted); err != nil {
					return fmt.Errorf("decode accepted builds: %w", err)
				}
			}
			result, err := imagebuild.PlanRebuilds(inputs, accepted)
			if err != nil {
				return fmt.Errorf("plan rebuilds: %w", err)
			}
			return emit(result)
		},
	}
	cmd.Flags().
		StringVar(&completed, "accepted", "", "JSON records restored from authenticated admission evidence")
	return cmd
}
