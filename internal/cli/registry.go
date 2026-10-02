package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/weaveplatform/weaveplatform-oci/pkg/chunk"
	"github.com/weaveplatform/weaveplatform-oci/pkg/client"
	"github.com/weaveplatform/weaveplatform-oci/pkg/fetch"
	"github.com/weaveplatform/weaveplatform-oci/pkg/profile"
	"github.com/weaveplatform/weaveplatform-oci/pkg/publish"
	"github.com/weaveplatform/weaveplatform-oci/pkg/sign"
	"github.com/weaveplatform/weaveplatform-oci/pkg/verify"
)

func registryCommands(stdout io.Writer, g *globals) []*cobra.Command {
	return []*cobra.Command{
		newProfile(stdout, g), newPush(stdout, g), newPull(stdout, g), newVerify(stdout, g),
		newSign(stdout, g), newKeygen(stdout), newPublish(stdout, g),
		newExportLayout(stdout, g), newImportLayout(stdout, g), newGC(stdout, g),
	}
}

func newProfile(stdout io.Writer, g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "profile", Short: "Show or validate the deployment profile"}
	cmd.AddCommand(&cobra.Command{
		Use: "show", Short: "Print the selected profile", Args: usageArgs(0),
		RunE: func(*cobra.Command, []string) error {
			p, err := g.selected()
			if err != nil {
				return err
			}
			return yaml.NewEncoder(stdout).Encode(p) //nolint:wrapcheck // terminal write
		},
	}, &cobra.Command{
		Use: "validate", Short: "Validate the profile file", Args: usageArgs(0),
		RunE: func(*cobra.Command, []string) error {
			path, err := profile.Path(g.profiles)
			if err != nil {
				return err //nolint:wrapcheck // descriptive
			}
			f, err := profile.Load(path)
			if err != nil {
				return err //nolint:wrapcheck // names the file
			}
			_, err = fmt.Fprintf(stdout, "%s: %d profile(s) valid\n", path, len(f.Profiles))
			return err //nolint:wrapcheck // terminal write
		},
	})
	return cmd
}

func newPush(stdout io.Writer, g *globals) *cobra.Command {
	var srcRef string
	var allow bool
	cmd := &cobra.Command{
		Use:   "push <oci-layout> <ref>",
		Short: "Push an artifact from a layout; build tags that exist are refused",
		Args:  usageArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			store, root, err := resolveRoot(ctx, args[0], srcRef)
			if err != nil {
				return err
			}
			c, _, err := g.client()
			if err != nil {
				return err
			}
			ref, err := c.Parse(args[1])
			if err != nil {
				return fmt.Errorf("%w: %w", errUsage, err)
			}
			d, err := c.Push(
				ctx,
				store,
				root.Digest.String(),
				ref,
				client.PushOptions{AllowExisting: allow},
			)
			if err != nil {
				return err //nolint:wrapcheck // names the reference
			}
			_, err = fmt.Fprintf(stdout, "pushed %s %s\n", ref, d.Digest)
			return err //nolint:wrapcheck // terminal write
		},
	}
	cmd.Flags().StringVar(&srcRef, "ref", "", "tag or digest in the layout (default: the only tag)")
	cmd.Flags().
		BoolVar(&allow, "allow-existing", false, "move a tag that already exists (promotion of channel tags)")
	return cmd
}

func newPull(stdout io.Writer, g *globals) *cobra.Command {
	var to, platform, mode string
	var o chunk.AssembleOptions
	cmd := &cobra.Command{
		Use:   "pull <ref>",
		Short: "Pull into the cache, verify per the profile, and optionally unpack a platform",
		Args:  usageArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, _, err := g.client()
			if err != nil {
				return err
			}
			store, err := g.openCache(cmd)
			if err != nil {
				return err
			}
			res, err := fetch.Pull(ctx, fetch.Request{
				Client: c, Cache: store, Ref: args[0], Mode: profile.VerifyMode(mode),
				Platform: platform, To: to, Assemble: o,
			})
			if errors.Is(err, fetch.ErrPlatform) {
				return fmt.Errorf("%w: %w (use --platform os/arch)", errUsage, err)
			}
			if errors.Is(err, fetch.ErrRequest) {
				return fmt.Errorf("%w: %w", errUsage, err)
			}
			if err != nil {
				return err //nolint:wrapcheck // each stage names what failed
			}
			_, _ = fmt.Fprintf(
				stdout,
				"pulled %s %s (verify=%s)\n",
				res.Reference,
				res.Root.Digest,
				res.Policy.Mode,
			)
			printEvidence(stdout, res.Evidence)
			if res.Unpacked == nil {
				return nil
			}
			_, err = fmt.Fprintf(
				stdout,
				"unpacked %s into %s: fetched=%d zero=%d resumed=%d\n",
				res.Manifest.Digest,
				to,
				res.Unpacked.Stats.Fetched,
				res.Unpacked.Stats.Zero,
				res.Unpacked.Stats.Resumed,
			)
			return err //nolint:wrapcheck // terminal write
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "unpack the selected platform into this bundle directory")
	cmd.Flags().
		StringVar(&platform, "platform", "", "os/arch to unpack from a multi-platform index")
	cmd.Flags().
		StringVar(&mode, "verify", "", "channel, signature, both or none (default: the profile's mode)")
	cmd.Flags().
		BoolVar(&o.Resume, "resume", false, "keep verified ranges already in the destination")
	return cmd
}

func printEvidence(w io.Writer, e verify.Evidence) {
	if e.Signature != nil {
		_, _ = fmt.Fprintf(
			w,
			"  signature: %s key=%s identity=%s\n",
			e.Signature.Provider,
			e.Signature.KeyID,
			e.Signature.Identity,
		)
	}
	if e.Channel != nil {
		_, _ = fmt.Fprintf(
			w,
			"  channel: %s (anchor %s, sequence %d) %s:%s\n",
			e.Channel.Channel,
			e.Channel.Anchor,
			e.Channel.Sequence,
			e.Channel.Image.Repository,
			e.Channel.Image.Tag,
		)
	}
}

func newVerify(stdout io.Writer, g *globals) *cobra.Command {
	var mode string
	var cached bool
	cmd := &cobra.Command{
		Use: "verify <ref>", Short: "Verify a remote artifact's signature and channel membership",
		Args: usageArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, p, err := g.client()
			if err != nil {
				return err
			}
			ref, err := c.Parse(args[0])
			if err != nil {
				return fmt.Errorf("%w: %w", errUsage, err)
			}
			pol, err := verify.FromProfile(ctx, p, ref.Repository, profile.VerifyMode(mode), nil)
			if err != nil {
				return err //nolint:wrapcheck // descriptive
			}
			var (
				root ocispec.Descriptor
				src  verify.Source = c.Bind(ref)
			)
			if cached {
				// air-gapped: verify what import-layout or pull put in the cache
				store, err := g.openCache(cmd)
				if err != nil {
					return err
				}
				if root, err = store.Resolve(ctx, ref.String()); err != nil {
					return err //nolint:wrapcheck // names the reference
				}
				src = verify.StoreSource{Store: store.Target()}
			} else if root, _, err = c.Resolve(ctx, ref); err != nil {
				return err //nolint:wrapcheck // names the reference
			}
			ev, err := verify.Verify(ctx, pol, src, root)
			if err != nil {
				return err //nolint:wrapcheck // names the digest
			}
			_, _ = fmt.Fprintf(stdout, "verified %s %s (verify=%s)\n", ref, root.Digest, pol.Mode)
			printEvidence(stdout, ev)
			return nil
		},
	}
	cmd.Flags().
		StringVar(&mode, "verify", "", "channel, signature, both or none (default: the profile's mode)")
	cmd.Flags().
		BoolVar(&cached, "cached", false, "verify the cached copy (after pull or import-layout) without contacting a registry")
	return cmd
}

func newSign(stdout io.Writer, g *globals) *cobra.Command {
	var key string
	cmd := &cobra.Command{
		Use:   "sign <ref>",
		Short: "Sign a remote artifact with a cosign-compatible key (private profile)",
		Args:  usageArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, p, err := g.client()
			if err != nil {
				return err
			}
			if key == "" {
				key = p.Signing.Key
			}
			if key == "" {
				return fmt.Errorf("%w: --key or signing.key is required", errUsage)
			}
			s, err := sign.LoadKey(ctx, key, nil)
			if err != nil {
				return err //nolint:wrapcheck // descriptive
			}
			ref, err := c.Parse(args[0])
			if err != nil {
				return fmt.Errorf("%w: %w", errUsage, err)
			}
			root, _, err := c.Resolve(ctx, ref)
			if err != nil {
				return err //nolint:wrapcheck // names the reference
			}
			repo, err := c.Repository(ref.Registry, ref.Repository)
			if err != nil {
				return err //nolint:wrapcheck // names the reference
			}
			d, err := s.Sign(
				ctx,
				repo,
				ocispec.Descriptor{MediaType: root.MediaType, Digest: root.Digest, Size: root.Size},
			)
			if err != nil {
				return err //nolint:wrapcheck // names the subject
			}
			_, err = fmt.Fprintf(
				stdout,
				"signed %s@%s key=%s signature=%s\n",
				ref.Repository,
				root.Digest,
				s.KeyID(),
				d.Digest,
			)
			return err //nolint:wrapcheck // terminal write
		},
	}
	cmd.Flags().
		StringVar(&key, "key", "", "key file, env://NAME or KMS URI (default: the profile's signing.key); password from $COSIGN_PASSWORD")
	return cmd
}

func newKeygen(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "keygen <prefix>",
		Short: "Generate a cosign-format key pair: <prefix>.key (encrypted with $COSIGN_PASSWORD) and <prefix>.pub",
		Args:  usageArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			priv, pub, err := sign.GenerateKeyPair([]byte(os.Getenv(sign.EnvPassword)))
			if err != nil {
				return err //nolint:wrapcheck // descriptive
			}
			if err := writeKeyPair(args[0], priv, pub); err != nil {
				return err
			}
			_, err = fmt.Fprintf(stdout, "wrote %s.key and %s.pub\n", args[0], args[0])
			return err //nolint:wrapcheck // terminal write
		},
	}
}

func newPublish(stdout io.Writer, g *globals) *cobra.Command {
	var repo, tag, key, out string
	var o chunk.Options
	cmd := &cobra.Command{
		Use:   "publish <bundle-dir>... --repository <repo> --tag <build-tag>",
		Short: "Pack, push, sign, self-verify and emit the channel promotion entry",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 || repo == "" || tag == "" {
				return fmt.Errorf(
					"%w: publish needs bundle directories, --repository and --tag",
					errUsage,
				)
			}
			ctx := cmd.Context()
			c, p, err := g.client()
			if err != nil {
				return err
			}
			req := publish.Request{Client: c, Bundles: args, Repository: repo, Tag: tag, Chunk: o}
			if p.Signing.Provider == profile.SigningCosignKey {
				if key == "" {
					key = p.Signing.Key
				}
				if req.Signer, err = sign.LoadKey(ctx, key, nil); err != nil {
					return err //nolint:wrapcheck // descriptive
				}
			}
			res, err := publish.Run(ctx, req)
			if err != nil {
				return err //nolint:wrapcheck // names the stage
			}
			if out != "" {
				if err := publish.WritePromotion(out, res.Promotion); err != nil {
					return err //nolint:wrapcheck // names the file
				}
			}
			_, _ = fmt.Fprintf(stdout, "published %s %s\n", res.Reference, res.Index.Digest)
			if res.Signature != nil {
				_, _ = fmt.Fprintf(
					stdout,
					"  signature %s key=%s\n",
					res.Signature.Digest,
					res.Promotion.Signature.KeyID,
				)
			}
			for _, d := range res.Children {
				_, _ = fmt.Fprintf(stdout, "  %s %s\n", platformString(d.Platform), d.Digest)
			}
			return nil
		},
	}
	cmd.Flags().
		StringVar(&repo, "repository", "", "repository, relative to the profile namespace or fully qualified")
	cmd.Flags().StringVar(&tag, "tag", "", "immutable build tag, e.g. 24.04-20260915-r1")
	cmd.Flags().
		StringVar(&key, "key", "", "cosign key reference (default: the profile's signing.key)")
	cmd.Flags().
		StringVar(&out, "promotion-out", "", "write the channel promotion entry (JSON) here")
	cmd.Flags().IntVar(&o.Concurrency, "concurrency", 2, "chunks compressed in parallel")
	return cmd
}

func newExportLayout(stdout io.Writer, g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "export-layout <ref> <dir>",
		Short: "Write a cached artifact and its signatures to an OCI layout for air-gapped transfer",
		Args:  usageArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := g.client()
			if err != nil {
				return err
			}
			ref, err := c.Parse(args[0])
			if err != nil {
				return fmt.Errorf("%w: %w", errUsage, err)
			}
			store, err := g.openCache(cmd)
			if err != nil {
				return err
			}
			d, err := store.ExportLayout(cmd.Context(), ref.String(), args[1])
			if err != nil {
				return err //nolint:wrapcheck // names the reference
			}
			_, err = fmt.Fprintf(stdout, "exported %s %s to %s\n", ref, d.Digest, args[1])
			return err //nolint:wrapcheck // terminal write
		},
	}
}

func newImportLayout(stdout io.Writer, g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "import-layout <dir>",
		Short: "Load every tagged artifact (with signatures) from an OCI layout into the cache",
		Args:  usageArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := g.openCache(cmd)
			if err != nil {
				return err
			}
			roots, err := store.ImportLayout(cmd.Context(), args[0])
			if err != nil {
				return err //nolint:wrapcheck // names the layout
			}
			for _, r := range roots {
				_, _ = fmt.Fprintf(stdout, "imported %s\n", r.Digest)
			}
			return nil
		},
	}
}

func newGC(stdout io.Writer, g *globals) *cobra.Command {
	var keep string
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Evict unpinned cached images, least recently used first",
		Args:  usageArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			n, err := strconv.ParseInt(keep, 10, 64)
			if err != nil {
				return fmt.Errorf("%w: --keep must be bytes: %w", errUsage, err)
			}
			store, err := g.openCache(cmd)
			if err != nil {
				return err
			}
			freed, err := store.GC(cmd.Context(), n)
			if err != nil {
				return err //nolint:wrapcheck // descriptive
			}
			_, err = fmt.Fprintf(stdout, "freed %s\n", humanBytes(freed))
			return err //nolint:wrapcheck // terminal write
		},
	}
	cmd.Flags().StringVar(&keep, "keep", "0", "bytes to keep (0 means the quota)")
	return cmd
}

// writeKeyPair writes <prefix>.key (owner-only) and <prefix>.pub; the
// operator chooses the prefix.
func writeKeyPair(prefix string, priv, pub []byte) error {
	if err := os.MkdirAll(filepath.Dir(prefix), 0o750); err != nil {
		return fmt.Errorf("write key: %w", err)
	}
	keyPath, pubPath := prefix+".key", prefix+".pub"
	//nolint:gosec // operator-chosen path
	if err := os.WriteFile(keyPath, priv, 0o600); err != nil {
		return fmt.Errorf("write key: %w", err)
	}
	//nolint:gosec // public key at an operator-chosen path
	if err := os.WriteFile(pubPath, pub, 0o644); err != nil {
		return fmt.Errorf("write key: %w", err)
	}
	return nil
}
