package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/weaveplatform/weaveplatform-oci/pkg/handoff"
)

func newImportImageweave(stdout io.Writer) *cobra.Command {
	var o handoff.Options
	cmd := &cobra.Command{
		Use:   "import-imageweave MANIFEST",
		Short: "Snapshot a completed Imageweave/Packer candidate into an unqualified OCI bundle",
		Args:  usageArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if o.Out == "" || o.RecipeCommit == "" || o.Version == "" ||
				(o.SourceURI == "" && (o.ParentLayout == "" || o.ParentRef == "" || o.ParentName == "")) {
				return fmt.Errorf(
					"%w: --out, --recipe-commit, --version and either --source-uri or all parent flags required",
					errUsage,
				)
			}
			o.Manifest = args[0]
			o.Log = cmd.ErrOrStderr()
			r, err := handoff.Import(cmd.Context(), o)
			if err != nil {
				return fmt.Errorf("import Imageweave: %w", err)
			}
			if err := json.NewEncoder(stdout).Encode(r); err != nil {
				return fmt.Errorf("write receipt: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&o.Out, "out", "", "New bundle directory (parent must exist)")
	cmd.Flags().
		StringVar(&o.RecipeCommit, "recipe-commit", "", "Full Imageweave recipe commit used for the build; not an attestation")
	cmd.Flags().
		StringVar(&o.SourceURI, "source-uri", "", "Canonical HTTPS source-media URI, without credentials or query strings")
	cmd.Flags().
		StringVar(&o.Version, "version", "", "Candidate version annotation; does not promote the image")
	cmd.Flags().
		StringVar(&o.ParentLayout, "parent-layout", "", "Verified local OCI layout for a prepared image's base")
	cmd.Flags().
		StringVar(&o.ParentRef, "parent-ref", "", "Base index reference in the parent layout")
	cmd.Flags().
		StringVar(&o.ParentName, "parent-name", "", "Base repository matching the prepared receipt")
	return cmd
}
