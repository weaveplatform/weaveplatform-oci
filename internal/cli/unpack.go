package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/spf13/cobra"
	"oras.land/oras-go/v2/content"

	"github.com/weaveplatform/weaveplatform-oci/pkg/chunk"
	"github.com/weaveplatform/weaveplatform-oci/pkg/fetch"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
)

type unpackResult struct {
	Manifest string `json:"manifest"`
	Platform string `json:"platform"`
	Dir      string `json:"dir"`
	Fetched  int64  `json:"fetched"`
	Zero     int64  `json:"zero"`
	Resumed  int64  `json:"resumed"`
	Retried  int64  `json:"retried"`
}

func newUnpack(stdout io.Writer) *cobra.Command {
	var (
		ref      string
		platform string
		opts     chunk.AssembleOptions
		asJSON   bool
	)
	cmd := &cobra.Command{
		Use:   "unpack <oci-layout> <dir>",
		Short: "Materialise one platform of an artifact as a bundle directory with sparse raw disks",
		Args:  usageArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			store, root, err := resolveRoot(ctx, args[0], ref)
			if err != nil {
				return err
			}
			m, err := selectManifest(cmd, store, root, platform)
			if err != nil {
				return err
			}
			r, err := pack.Unpack(ctx, store, m, args[1], opts)
			if err != nil {
				return err //nolint:wrapcheck // pack errors name the disk
			}
			res := unpackResult{
				Manifest: m.Digest.String(),
				Platform: platformString(r.Description.Platform),
				Dir:      args[1],
				Fetched:  r.Stats.Fetched,
				Zero:     r.Stats.Zero,
				Resumed:  r.Stats.Resumed,
				Retried:  r.Stats.Retried,
			}
			if asJSON {
				return json.NewEncoder(stdout).Encode(res) //nolint:wrapcheck // terminal write
			}
			_, err = fmt.Fprintf(
				stdout,
				"unpacked %s (%s) into %s: fetched=%d zero=%d resumed=%d retried=%d\n",
				res.Manifest,
				res.Platform,
				res.Dir,
				res.Fetched,
				res.Zero,
				res.Resumed,
				res.Retried,
			)
			return err //nolint:wrapcheck // terminal write
		},
	}
	cmd.Flags().StringVar(&ref, "ref", "", "tag or digest in the layout (default: the only tag)")
	cmd.Flags().
		StringVar(&platform, "platform", "", "os/arch to select from an index (default: the only child)")
	cmd.Flags().
		BoolVar(&opts.Resume, "resume", false, "keep ranges already present in the destination disks when they verify")
	cmd.Flags().IntVar(&opts.Concurrency, "concurrency", 2, "chunks written in parallel")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the result as JSON")
	return cmd
}

func selectManifest(
	cmd *cobra.Command,
	f content.Fetcher,
	root ocispec.Descriptor,
	platform string,
) (ocispec.Descriptor, error) {
	d, err := fetch.SelectPlatform(cmd.Context(), f, root, platform)
	if errors.Is(err, fetch.ErrPlatform) {
		return d, fmt.Errorf("%w: %w (use --platform os/arch)", errUsage, err)
	}
	return d, err //nolint:wrapcheck // fetch names the index problem
}
