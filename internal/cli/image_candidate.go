package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/weaveplatform/weaveplatform-oci/pkg/channel"
	"github.com/weaveplatform/weaveplatform-oci/pkg/client"
	"github.com/weaveplatform/weaveplatform-oci/pkg/imagecheck"
	"github.com/weaveplatform/weaveplatform-oci/pkg/profile"
)

type candidateVerifier func(context.Context, imagecheck.CandidatePolicy, *client.Client, channel.Image, string) (imagecheck.CandidateEvidence, error)

func newImageCandidate(emit func(any) error) *cobra.Command {
	return newImageCandidateWithVerifier(emit, imagecheck.VerifyCandidatePublished)
}

func newImageCandidateWithVerifier(
	emit func(any) error,
	verifyCandidate candidateVerifier,
) *cobra.Command {
	var policyFile, out string
	cmd := &cobra.Command{
		Use:   "verify-candidate ENTRY",
		Short: "Authenticate a published candidate's build and acceptance evidence",
		Long: "Authenticate a published candidate's exact image, build provenance and schema-3 acceptance. " +
			"This does not authorize promotion, establish channel membership or verify current parents.",
		Args: usageArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if policyFile == "" || out == "" {
				return fmt.Errorf("%w: --policy and --out required", errUsage)
			}
			config, err := loadCandidatePolicy(policyFile)
			if err != nil {
				return err
			}
			policy, err := config.candidate()
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
			result, err := verifyCandidate(cmd.Context(), policy, c, entry, out)
			if err != nil {
				return fmt.Errorf("published candidate verification: %w", err)
			}
			return emit(result)
		},
	}
	cmd.Flags().
		StringVar(&policyFile, "policy", "", "Reviewed candidate policy; channel and parent fields are rejected")
	cmd.Flags().
		StringVar(&out, "out", "", "Audit OCI layout for the exact candidate and its evidence")
	return cmd
}
