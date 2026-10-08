package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/weaveplatform/weaveplatform-oci/pkg/channel"
	"github.com/weaveplatform/weaveplatform-oci/pkg/client"
	"github.com/weaveplatform/weaveplatform-oci/pkg/imagecheck"
	"github.com/weaveplatform/weaveplatform-oci/pkg/profile"
)

func newImagePublished(emit func(any) error) *cobra.Command {
	var policyFile, out string
	cmd := &cobra.Command{
		Use:   "verify-published ENTRY",
		Short: "Authenticate a published promotion entry and its registry evidence",
		Args:  usageArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if policyFile == "" || out == "" {
				return fmt.Errorf("%w: --policy and --out required", errUsage)
			}
			config, err := loadAdmissionPolicy(policyFile)
			if err != nil {
				return err
			}
			policy, err := config.admission(cmd.Context())
			if err != nil {
				return err
			}
			entry, err := readPromotionEntry(args[0])
			if err != nil {
				return err
			}
			c := client.New(
				profile.Profile{Registry: profile.Registry{Host: config.Registry}},
				client.Options{},
			)
			result, err := imagecheck.AdmitPublished(cmd.Context(), policy, c, entry, out)
			if err != nil {
				return fmt.Errorf("published image admission: %w", err)
			}
			return emit(result)
		},
	}
	cmd.Flags().
		StringVar(&policyFile, "policy", "", "Reviewed admission policy; never read from dispatch")
	cmd.Flags().StringVar(&out, "out", "", "Audit OCI layout for the exact image and its evidence")
	return cmd
}

func readPromotionEntry(path string) (channel.Image, error) {
	var entry channel.Image
	f, err := os.Open(path)
	if err != nil {
		return entry, fmt.Errorf("promotion entry: %w", err)
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&entry); err != nil {
		return entry, fmt.Errorf("promotion entry: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return entry, fmt.Errorf("%w: promotion must be one JSON object", errUsage)
	}
	return entry, nil
}
