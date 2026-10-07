package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/weaveplatform/weaveplatform-oci/internal/imagebuild"
)

func newImage(stdout, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:   "image",
		Short: "Build and validate base and agent image candidates",
	}
	var catalogue, lock string
	root.PersistentFlags().
		StringVar(&catalogue, "catalogue", "images/catalogue.json", "Image catalogue")
	root.PersistentFlags().
		StringVar(&lock, "lock", "images/packages.lock.json", "Pinned core and module inputs")
	tools := imagebuild.Tools{Log: stderr}
	packages := imagebuild.Packages{Tools: tools, Downloader: imagebuild.Downloader{Log: stderr}}
	load := func() (imagebuild.Lock, error) {
		c, err := imagebuild.LoadCatalogue(catalogue)
		if err != nil {
			return imagebuild.Lock{}, fmt.Errorf("load image catalogue: %w", err)
		}
		return imagebuild.LoadLock(lock, c)
	}
	emit := func(v any) error {
		if err := json.NewEncoder(stdout).Encode(v); err != nil {
			return fmt.Errorf("write result: %w", err)
		}
		return nil
	}
	root.AddCommand(
		&cobra.Command{
			Use:   "matrix",
			Short: "Expand the requested image matrix",
			Args:  usageArgs(0),
			RunE: func(_ *cobra.Command, _ []string) error {
				c, err := imagebuild.LoadCatalogue(catalogue)
				if err != nil {
					return fmt.Errorf("image: %w", err)
				}
				return emit(c.Matrix())
			},
		},
	)
	var platform string
	check := &cobra.Command{
		Use:   "check-lock",
		Short: "Validate pinned inputs; optionally require native installers",
		Args:  usageArgs(0),
		RunE: func(_ *cobra.Command, _ []string) error {
			l, err := load()
			if err != nil {
				return fmt.Errorf("image: %w", err)
			}
			if platform != "" {
				if _, err := l.RequirePackages(platform); err != nil {
					return fmt.Errorf("image: %w", err)
				}
			}
			return emit(map[string]bool{"valid": true})
		},
	}
	check.Flags().
		StringVar(&platform, "require-packages", "", "Require installers for OS/architecture")
	var out string
	resolve := &cobra.Command{
		Use:   "lock",
		Short: "Resolve stable releases into a new lock file",
		Args:  usageArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if out == "" {
				return fmt.Errorf("%w: --out required", errUsage)
			}
			c, err := imagebuild.LoadCatalogue(catalogue)
			if err != nil {
				return fmt.Errorf("image: %w", err)
			}
			l, err := imagebuild.ResolveLock(cmd.Context(), c, tools)
			if err != nil {
				return fmt.Errorf("image: %w", err)
			}
			return imagebuild.WriteLock(out, l)
		},
	}
	resolve.Flags().StringVar(&out, "out", "", "New dependency lock path")
	root.AddCommand(
		check,
		resolve,
		newImageBoot(tools, emit),
		newImageValidate(tools, emit),
		newImageAgent(packages, load, emit),
		newImageMac(packages, emit),
		newImageIPSW(packages, emit),
		newImageWindowsSource(packages, emit),
		newImageWindows(packages, emit),
	)
	return root
}

func newImageBoot(tools imagebuild.Tools, emit func(any) error) *cobra.Command {
	var o imagebuild.BootOptions
	var timeout int
	cmd := &cobra.Command{
		Use:   "boot-linux BUNDLE",
		Short: "Boot an immutable bundle through disposable QEMU state",
		Args:  usageArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if o.Report == "" || timeout < 1 {
				return fmt.Errorf("%w: --report and positive --timeout required", errUsage)
			}
			o.Bundle = args[0]
			o.Timeout = time.Duration(timeout) * time.Second
			r, err := tools.BootLinux(cmd.Context(), o)
			if err != nil {
				return fmt.Errorf("image: %w", err)
			}
			return emit(r)
		},
	}
	cmd.Flags().StringVar(&o.Report, "report", "", "New report directory")
	cmd.Flags().IntVar(&timeout, "timeout", 900, "Boot timeout in seconds")
	return cmd
}

func newImageValidate(tools imagebuild.Tools, emit func(any) error) *cobra.Command {
	var o imagebuild.ValidateOptions
	var timeout int
	cmd := &cobra.Command{
		Use:   "validate-linux BUNDLE...",
		Short: "Pack, deeply verify and boot two fresh clones per platform",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("%w: bundle paths required", errUsage)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if o.Out == "" || timeout < 1 {
				return fmt.Errorf("%w: --out and positive --timeout required", errUsage)
			}
			o.Bundles = args
			o.Timeout = time.Duration(timeout) * time.Second
			r, err := tools.ValidateLinux(cmd.Context(), o)
			if err != nil {
				return fmt.Errorf("image: %w", err)
			}
			return emit(r)
		},
	}
	cmd.Flags().StringVar(&o.Out, "out", "", "New candidate directory")
	cmd.Flags().
		StringSliceVar(&o.Arches, "arches", []string{"amd64", "arm64"}, "Required architectures (comma separated)")
	cmd.Flags().IntVar(&timeout, "timeout", 1200, "Boot timeout per clone in seconds")
	return cmd
}

func newImageAgent(
	p imagebuild.Packages,
	load func() (imagebuild.Lock, error),
	emit func(any) error,
) *cobra.Command {
	var o imagebuild.AgentOptions
	var timeout int
	cmd := &cobra.Command{
		Use:   "build-linux-agent",
		Short: "Install verified native packages into a sealed base clone",
		Args:  usageArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if o.Base == "" || o.BaseName == "" || o.Cache == "" || o.Out == "" || o.Revision < 1 ||
				timeout < 1 ||
				(o.Arch != "amd64" && o.Arch != "arm64") {
				return fmt.Errorf(
					"%w: --base, --base-name, --arch, --cache, --out and positive revision/timeout required",
					errUsage,
				)
			}
			l, err := load()
			if err != nil {
				return fmt.Errorf("image: %w", err)
			}
			o.Lock = l
			o.Timeout = time.Duration(timeout) * time.Second
			path, err := p.BuildLinuxAgent(cmd.Context(), o)
			if err != nil {
				return fmt.Errorf("image: %w", err)
			}
			return emit(map[string]string{"bundle": path})
		},
	}
	cmd.Flags().StringVar(&o.Base, "base", "", "Verified base OCI layout")
	cmd.Flags().StringVar(&o.BaseName, "base-name", "", "Base repository without tag or digest")
	cmd.Flags().StringVar(&o.Arch, "arch", "", "Guest architecture")
	cmd.Flags().StringVar(&o.Cache, "cache", "", "Package cache directory")
	cmd.Flags().StringVar(&o.Out, "out", "", "New candidate directory")
	cmd.Flags().IntVar(&o.Revision, "revision", 1, "Positive build revision")
	cmd.Flags().IntVar(&timeout, "timeout", 1200, "Provisioning timeout in seconds")
	return cmd
}

func newImageMac(p imagebuild.Packages, emit func(any) error) *cobra.Command {
	var o imagebuild.MacOptions
	cmd := &cobra.Command{
		Use:   "build-macos",
		Short: "Restore a pinned Apple IPSW to an agent-free base bundle",
		Args:  usageArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if imagebuild.ValidateMacVersion(o.Version) != nil || o.Revision < 1 {
				return fmt.Errorf(
					"%w: --version must be a macOS major or exact version and --revision positive",
					errUsage,
				)
			}
			path, err := p.BuildMacOS(cmd.Context(), o)
			if err != nil {
				return fmt.Errorf("image: %w", err)
			}
			return emit(map[string]string{"bundle": path})
		},
	}
	cmd.Flags().
		Int64Var(&o.DiskSize, "disk-size", 80<<30, "Raw system disk size in bytes (minimum 64 GiB)")
	cmd.Flags().
		StringVar(&o.Version, "version", "", "macOS major or exact version (e.g. 26, 27, 26.6.2)")
	cmd.Flags().
		StringVar(&o.SourceLock, "source-lock", "", "Previously resolved Apple restore metadata")
	cmd.Flags().IntVar(&o.Revision, "revision", 1, "Positive build revision")
	cmd.Flags().
		BoolVar(&o.Resume, "resume", false, "Resume an interrupted media download with identical source metadata")
	return cmd
}

func newImageIPSW(p imagebuild.Packages, emit func(any) error) *cobra.Command {
	var version, out string
	var list bool
	cmd := &cobra.Command{
		Use:   "ipsw",
		Short: "Resolve Apple restore media using guestweave-compatible version selectors",
		Args:  usageArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := imagebuild.ValidateMacVersion(version); err != nil {
				return fmt.Errorf("%w: %w", errUsage, err)
			}
			sources, err := imagebuild.MacSources(cmd.Context(), p.Downloader.Client, version)
			if err != nil {
				return fmt.Errorf("resolve Apple media: %w", err)
			}
			if out != "" {
				if err := imagebuild.WriteSourceLock(out, sources[0]); err != nil {
					return fmt.Errorf("write source lock: %w", err)
				}
			}
			if list {
				return emit(sources)
			}
			return emit(sources[0])
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "Major or exact macOS version")
	cmd.Flags().BoolVar(&list, "list", false, "List all matching production versions, newest first")
	cmd.Flags().StringVar(&out, "out", "", "Write the selected exact source to a new lock file")
	return cmd
}

func windowsSelectionFlags(cmd *cobra.Command, s *imagebuild.WindowsSelection) {
	cmd.Flags().
		StringVar(&s.FromWindows, "from-windows", "", "Edition-release or build selector (pro-26h2, enterprise-25h2, enterprise-26200)")
	cmd.Flags().StringVar(&s.Arch, "arch", "amd64", "Guest architecture: amd64 or arm64")
	cmd.Flags().StringVar(&s.Language, "language", "en-US", "Windows media language")
}

func newImageWindowsSource(p imagebuild.Packages, emit func(any) error) *cobra.Command {
	var s imagebuild.WindowsSelection
	var out, cache string
	var list bool
	cmd := &cobra.Command{
		Use:   "windows",
		Short: "Resolve Microsoft media with an explicit edition, release and architecture",
		Args:  usageArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, _, err := imagebuild.ParseWindowsSelection(s); err != nil {
				return fmt.Errorf("%w: %w", errUsage, err)
			}
			if list {
				if out != "" || cache != "" {
					return fmt.Errorf(
						"%w: --list cannot acquire or write a single source lock",
						errUsage,
					)
				}
				sources, err := p.WindowsESDSources(cmd.Context(), s)
				if err != nil {
					return fmt.Errorf("list Windows media: %w", err)
				}
				return emit(sources)
			}
			if out != "" && cache == "" {
				return fmt.Errorf(
					"%w: --out requires --cache to acquire and hash the exact media",
					errUsage,
				)
			}
			source, err := p.ResolveWindows(cmd.Context(), s)
			if err != nil {
				return fmt.Errorf("resolve Windows media: %w", err)
			}
			if cache != "" {
				source, _, err = p.AcquireWindows(cmd.Context(), source, s, cache)
				if err != nil {
					return fmt.Errorf("acquire Windows media: %w", err)
				}
			}
			if out != "" {
				if err := imagebuild.WriteSourceLock(out, source); err != nil {
					return fmt.Errorf("write source lock: %w", err)
				}
			}
			return emit(source)
		},
	}
	windowsSelectionFlags(cmd, &s)
	cmd.Flags().
		BoolVar(&list, "list", false, "List matching Media Foundry ESD versions, newest build first")
	cmd.Flags().
		StringVar(&cache, "cache", "", "Acquire media into this cache and calculate its SHA256")
	cmd.Flags().
		StringVar(&out, "out", "", "Write acquired source and digest to a new lock file (requires --cache)")
	return cmd
}

func newImageWindows(p imagebuild.Packages, emit func(any) error) *cobra.Command {
	var o imagebuild.WindowsOptions
	cmd := &cobra.Command{
		Use:   "build-windows",
		Short: "Install and generalize a Windows base through native HCS APIs",
		Args:  usageArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, _, err := imagebuild.ParseWindowsSelection(o.Selection); err != nil {
				return fmt.Errorf("%w: %w", errUsage, err)
			}
			if o.Out == "" || o.Cache == "" || o.Revision < 1 || o.Timeout <= 0 {
				return fmt.Errorf(
					"%w: --out, --cache and positive revision/timeout required",
					errUsage,
				)
			}
			bundle, err := p.BuildWindows(cmd.Context(), o)
			if err != nil {
				return fmt.Errorf("build Windows image: %w", err)
			}
			return emit(map[string]string{"bundle": bundle})
		},
	}
	windowsSelectionFlags(cmd, &o.Selection)
	cmd.Flags().StringVar(&o.SourceLock, "source-lock", "", "Acquired exact Microsoft source lock")
	cmd.Flags().StringVar(&o.Out, "out", "", "New candidate directory on NTFS/ReFS")
	cmd.Flags().StringVar(&o.Cache, "cache", "", "Microsoft ISO/ESD cache directory")
	cmd.Flags().IntVar(&o.Revision, "revision", 1, "Positive build revision")
	cmd.Flags().
		DurationVar(&o.Timeout, "timeout", 2*time.Hour, "Installation and generalization timeout")
	return cmd
}
