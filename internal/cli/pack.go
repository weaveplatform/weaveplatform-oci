package cli

import (
	"encoding/json"
	"fmt"
	"io"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/spf13/cobra"

	"github.com/weaveplatform/weaveplatform-oci/pkg/chunk"
	"github.com/weaveplatform/weaveplatform-oci/pkg/pack"
)

type packResult struct {
	Index    string       `json:"index"`
	Tags     []string     `json:"tags"`
	Children []childBrief `json:"children"`
}

type childBrief struct {
	Digest     string `json:"digest"`
	Platform   string `json:"platform"`
	OSBuild    string `json:"osBuild"`
	TotalSize  int64  `json:"totalSize"`
	FetchSize  int64  `json:"fetchSize"`
	Chunks     int64  `json:"chunks"`
	ZeroChunks int64  `json:"zeroChunks"`
}

func newPack(stdout io.Writer) *cobra.Command {
	var (
		out    string
		tags   []string
		opts   chunk.Options
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "pack <bundle-dir>... --out <oci-layout>",
		Short: "Chunk one bundle per platform into a contract index in an OCI layout",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("%w: pack needs at least one bundle directory", errUsage)
			}
			if out == "" {
				return fmt.Errorf("%w: --out is required", errUsage)
			}
			ctx := cmd.Context()
			store, err := pack.OpenLayout(ctx, out)
			if err != nil {
				return err //nolint:wrapcheck // pack names the directory
			}
			children := make([]ocispec.Descriptor, 0, len(args))
			res := packResult{Tags: tags}
			for _, dir := range args {
				b, err := pack.LoadBundle(dir)
				if err != nil {
					return err //nolint:wrapcheck // pack errors name the bundle
				}
				m, err := pack.Manifest(ctx, b, store, opts)
				if err != nil {
					return err //nolint:wrapcheck // pack errors name the bundle
				}
				d, err := pack.Describe(ctx, store, m)
				if err != nil {
					return err //nolint:wrapcheck // pack errors name the digest
				}
				children = append(children, m)
				res.Children = append(res.Children, brief(d))
			}
			idx, err := pack.Index(ctx, store, children, nil)
			if err != nil {
				return err //nolint:wrapcheck // spec errors carry the rule list
			}
			res.Index = idx.Digest.String()
			for _, t := range tags {
				if err := store.Tag(ctx, idx, t); err != nil {
					return fmt.Errorf("tag %q: %w", t, err)
				}
			}
			return printPack(stdout, res, asJSON)
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "OCI layout directory to write (created if missing)")
	cmd.Flags().StringArrayVar(&tags, "tag", nil, "tag the index in the layout (repeatable)")
	cmd.Flags().IntVar(&opts.Level, "level", 3, "zstd compression level")
	cmd.Flags().IntVar(&opts.Concurrency, "concurrency", 2, "chunks compressed in parallel")
	cmd.Flags().
		StringVar(&opts.TempDir, "temp-dir", "", "directory for compressed chunks before they are stored")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the result as JSON")
	return cmd
}

func printPack(w io.Writer, r packResult, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(w).Encode(r) //nolint:wrapcheck // terminal write
	}
	if _, err := fmt.Fprintf(w, "index %s\n", r.Index); err != nil {
		return err //nolint:wrapcheck // terminal write
	}
	for _, c := range r.Children {
		if _, err := fmt.Fprintf(
			w,
			"  %s build %s %s chunks=%d zero=%d size=%s fetch=%s\n",
			c.Platform,
			c.OSBuild,
			c.Digest,
			c.Chunks,
			c.ZeroChunks,
			humanBytes(c.TotalSize),
			humanBytes(c.FetchSize),
		); err != nil {
			return err //nolint:wrapcheck // terminal write
		}
	}
	return nil
}
