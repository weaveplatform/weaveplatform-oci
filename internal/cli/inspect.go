package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/weaveplatform/weaveplatform-oci/pkg/conformance"
	"github.com/weaveplatform/weaveplatform-oci/pkg/spec"
)

type inspectResult struct {
	Root     string        `json:"root"`
	OK       bool          `json:"ok"`
	Problems []string      `json:"problems"`
	Children []childDetail `json:"children"`
}

type childDetail struct {
	childBrief
	Disks []diskBrief `json:"disks"`
	State []string    `json:"state"`
}

type diskBrief struct {
	Name        string `json:"name"`
	LogicalSize int64  `json:"logicalSize"`
	Chunks      int64  `json:"chunks"`
	ZeroChunks  int64  `json:"zeroChunks"`
}

func brief(d spec.Description) childBrief {
	b := childBrief{
		Digest:    d.Digest.String(),
		Platform:  platformString(d.Platform),
		OSBuild:   d.Config.Guest.OSBuild,
		TotalSize: d.TotalSize,
		FetchSize: d.FetchSize,
	}
	for _, disk := range d.Disks {
		b.Chunks += int64(len(disk.Chunks))
		for _, c := range disk.Chunks {
			if c.Zero {
				b.ZeroChunks++
			}
		}
	}
	return b
}

func newInspect(stdout, stderr io.Writer) *cobra.Command {
	var (
		ref    string
		strict bool
		deep   bool
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "inspect <oci-layout>",
		Short: "Describe an artifact and run the contract conformance checklist",
		Args:  usageArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			store, root, err := resolveRoot(ctx, args[0], ref)
			if err != nil {
				return err
			}
			rep, err := conformance.Check(ctx, store, root, conformance.Options{Deep: deep})
			if err != nil {
				return err //nolint:wrapcheck // conformance names the digest
			}
			res := inspectResult{Root: root.Digest.String(), OK: rep.OK(), Problems: rep.Problems()}
			if res.Problems == nil {
				res.Problems = []string{}
			}
			for _, c := range rep.Children {
				res.Children = append(res.Children, detail(c))
			}
			if err := printInspect(stdout, res, asJSON); err != nil {
				return err
			}
			if !res.OK {
				if strict {
					return fmt.Errorf("%w: %d problem(s)", errNonConformant, len(res.Problems))
				}
				_, _ = fmt.Fprintf(
					stderr,
					"warning: %d conformance problem(s); rerun with --strict to fail\n",
					len(res.Problems),
				)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&ref, "ref", "", "tag or digest in the layout (default: the only tag)")
	cmd.Flags().
		BoolVar(&strict, "strict", false, "exit non-zero when the conformance checklist finds problems")
	cmd.Flags().BoolVar(&deep, "deep", false, "also fetch and verify every chunk and state blob")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the result as JSON")
	return cmd
}

func detail(c conformance.Child) childDetail {
	d := childDetail{childBrief: brief(c.Description), Disks: []diskBrief{}, State: []string{}}
	d.Digest = c.Descriptor.Digest.String()
	for _, disk := range c.Description.Disks {
		d.Disks = append(
			d.Disks,
			diskBrief{
				Name:        disk.Name,
				LogicalSize: disk.LogicalSize,
				Chunks:      int64(len(disk.Chunks)),
				ZeroChunks:  disk.ZeroChunks,
			},
		)
	}
	for _, s := range c.Description.State {
		d.State = append(d.State, s.Name+":"+s.Semantics)
	}
	return d
}

func printInspect(w io.Writer, r inspectResult, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(w).Encode(r) //nolint:wrapcheck // terminal write
	}
	status := "conformant"
	if !r.OK {
		status = "NOT conformant"
	}
	if _, err := fmt.Fprintf(w, "root %s: %s\n", r.Root, status); err != nil {
		return err //nolint:wrapcheck // terminal write
	}
	for _, c := range r.Children {
		_, _ = fmt.Fprintf(
			w,
			"  %s build %s %s size=%s fetch=%s\n",
			c.Platform,
			c.OSBuild,
			c.Digest,
			humanBytes(c.TotalSize),
			humanBytes(c.FetchSize),
		)
		for _, d := range c.Disks {
			_, _ = fmt.Fprintf(
				w,
				"    disk %s %s chunks=%d zero=%d\n",
				d.Name,
				humanBytes(d.LogicalSize),
				d.Chunks,
				d.ZeroChunks,
			)
		}
		for _, s := range c.State {
			_, _ = fmt.Fprintf(w, "    state %s\n", s)
		}
	}
	for _, p := range r.Problems {
		_, _ = fmt.Fprintf(w, "  problem: %s\n", p)
	}
	return nil
}
