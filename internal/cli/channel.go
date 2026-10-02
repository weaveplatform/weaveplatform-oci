package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/spf13/cobra"

	"github.com/deploymenttheory/weaveplatform-oci/pkg/channel"
)

// newChannel lets an organisation run its own channel (decision 0011): the
// same files weavemanifest produces, plus image promotion.
func newChannel(stdout io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "channel",
		Short: "Create, promote into, sign and verify channel manifests",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "new <name> <manifest>",
			Short: "Write an empty image channel manifest",
			Args:  usageArgs(2),
			RunE: func(_ *cobra.Command, args []string) error {
				b, err := channel.New(args[0], time.Now())
				if err != nil {
					return err //nolint:wrapcheck // descriptive
				}
				return writeOut(stdout, args[1], b)
			},
		},
		&cobra.Command{
			Use:   "keygen <key-id> <prefix>",
			Short: "Generate <prefix>.key and <prefix>.pub (key id \"root\" for a root key)",
			Args:  usageArgs(2),
			RunE: func(_ *cobra.Command, args []string) error {
				k, p, err := channel.GenerateKey(args[0])
				if err != nil {
					return err //nolint:wrapcheck // descriptive
				}
				if err := os.MkdirAll(filepath.Dir(args[1]), 0o750); err != nil {
					return fmt.Errorf("write key: %w", err)
				}
				if err := os.WriteFile(args[1]+".key", k, 0o600); err != nil {
					return fmt.Errorf("write key: %w", err)
				}
				return writeOut(stdout, args[1]+".pub", p)
			},
		},
		&cobra.Command{
			Use:   "endorse <root.key> <signing.pub>",
			Short: "Write <signing.pub>.sig, the root's endorsement",
			Args:  usageArgs(2),
			RunE: func(_ *cobra.Command, args []string) error {
				rk, pub, err := read2(args[0], args[1])
				if err != nil {
					return err
				}
				sig, err := channel.Endorse(rk, pub)
				if err != nil {
					return err //nolint:wrapcheck // descriptive
				}
				return writeOut(stdout, args[1]+".sig", sig)
			},
		},
		&cobra.Command{
			Use: "sign <signing.key> <manifest>", Short: "Write <manifest>.sig", Args: usageArgs(2),
			RunE: func(_ *cobra.Command, args []string) error {
				k, m, err := read2(args[0], args[1])
				if err != nil {
					return err
				}
				sig, err := channel.Sign(k, channel.ManifestContext, m)
				if err != nil {
					return err //nolint:wrapcheck // descriptive
				}
				return writeOut(stdout, args[1]+".sig", sig)
			},
		},
		&cobra.Command{
			Use:   "promote <manifest> <entry.json>",
			Short: "Add or replace an image entry (from publish --promotion-out); sign again afterwards",
			Args:  usageArgs(2),
			RunE: func(_ *cobra.Command, args []string) error {
				m, e, err := read2(args[0], args[1])
				if err != nil {
					return err
				}
				var img channel.Image
				if err := json.Unmarshal(e, &img); err != nil {
					return fmt.Errorf("%w: entry: %w", errUsage, err)
				}
				out, err := channel.Promote(m, img, time.Now())
				if err != nil {
					return err //nolint:wrapcheck // descriptive
				}
				return writeOut(stdout, args[0], out)
			},
		},
		newChannelVerify(stdout),
	)
	return cmd
}

func newChannelVerify(stdout io.Writer) *cobra.Command {
	var anchors []string
	var repo, dg string
	cmd := &cobra.Command{
		Use:   "verify <manifest-path-or-url> --anchor root.pub",
		Short: "Verify a channel's signature chain, optionally that it lists a digest",
		Args:  usageArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(anchors) == 0 {
				return fmt.Errorf("%w: at least one --anchor is required", errUsage)
			}
			var as []channel.Anchor
			for _, a := range anchors {
				raw, err := os.ReadFile(a) //nolint:gosec // operator-supplied anchor
				if err != nil {
					return fmt.Errorf("anchor: %w", err)
				}
				anchor, err := channel.ParseAnchor(a, raw)
				if err != nil {
					return err //nolint:wrapcheck // descriptive
				}
				as = append(as, anchor)
			}
			b, err := channel.Load(cmd.Context(), args[0], nil)
			if err != nil {
				return err //nolint:wrapcheck // names the file
			}
			m, a, err := channel.Verify(as, b, channel.Options{})
			if err != nil {
				return err //nolint:wrapcheck // descriptive
			}
			_, _ = fmt.Fprintf(
				stdout,
				"channel %s sequence %d verified under %s; %d image(s)\n",
				m.Channel,
				m.Sequence,
				a.Name,
				len(m.Images),
			)
			if dg == "" {
				return nil
			}
			d, err := digest.Parse(dg)
			if err != nil {
				return fmt.Errorf("%w: --digest: %w", errUsage, err)
			}
			img, err := m.Image(repo, d)
			if err != nil {
				return err //nolint:wrapcheck // descriptive
			}
			_, err = fmt.Fprintf(stdout, "lists %s:%s %s\n", img.Repository, img.Tag, img.Digest)
			return err //nolint:wrapcheck // terminal write
		},
	}
	cmd.Flags().StringArrayVar(&anchors, "anchor", nil, "root public key file (repeatable)")
	cmd.Flags().StringVar(&repo, "repository", "", "repository path the entry must name")
	cmd.Flags().StringVar(&dg, "digest", "", "index digest that must be listed")
	return cmd
}

func read2(a, b string) ([]byte, []byte, error) {
	x, err := os.ReadFile(a) //nolint:gosec // operator-supplied path
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", a, err)
	}
	y, err := os.ReadFile(b) //nolint:gosec // operator-supplied path
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", b, err)
	}
	return x, y, nil
}

func writeOut(stdout io.Writer, path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // public channel files
		return fmt.Errorf("write %s: %w", path, err)
	}
	_, err := fmt.Fprintf(stdout, "wrote %s\n", path)
	return err //nolint:wrapcheck // terminal write
}
