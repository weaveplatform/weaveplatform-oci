package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

func newImage(stdout, _ io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:   "image",
		Short: "Verify image build and acceptance evidence",
		Long:  "Verify image build and acceptance evidence. Construction and runtime qualification commands are provided by imageweave image.",
		Args:  usageArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cmd.Help(); err != nil {
				return fmt.Errorf("image help: %w", err)
			}
			return nil
		},
	}
	emit := func(v any) error {
		if err := json.NewEncoder(stdout).Encode(v); err != nil {
			return fmt.Errorf("write result: %w", err)
		}
		return nil
	}
	root.AddCommand(newImageAdmission(emit), newImagePublished(emit))
	return root
}
