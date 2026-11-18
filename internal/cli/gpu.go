package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/module"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

func newGPUCmd() *cobra.Command {
	gpuCmd := &cobra.Command{
		Use:     "gpu",
		Short:   "Manage per-user GPU allocation",
		Example: "  rootfiles gpu list\n  sudo rootfiles gpu assign alice --gpus 0,1",
	}

	// rootfiles gpu assign <USERNAME> --gpus 0,1 [--method env|cgroup|both]
	assignCmd := &cobra.Command{
		Use:   "assign <USERNAME>",
		Short: "Assign GPUs to a user",
		Args:  cobra.ExactArgs(1),
		Example: `  sudo rootfiles gpu assign alice --gpus 0,1
  sudo rootfiles gpu assign alice --gpus 2,3 --method cgroup`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := buildRunContext(cmd, "gpu")
			if err != nil {
				return err
			}
			username := args[0]

			gpuStr, _ := cmd.Flags().GetString("gpus")
			if gpuStr == "" {
				return fmt.Errorf("--gpus is required (e.g., --gpus 0,1)")
			}
			gpus, err := parseGPUList(gpuStr)
			if err != nil {
				return err
			}

			method, _ := cmd.Flags().GetString("method")
			return module.AssignGPUs(cmd.Context(), rc, username, gpus, method)
		},
	}
	assignCmd.Flags().String("gpus", "", "Comma-separated GPU indices (e.g., 0,1,2)")
	assignCmd.Flags().String("method", "", "Isolation: env, cgroup or both (default from profile)")
	gpuCmd.AddCommand(assignCmd)

	// rootfiles gpu revoke USERNAME
	gpuCmd.AddCommand(&cobra.Command{
		Use:     "revoke <USERNAME>",
		Short:   "Revoke GPU allocation from a user",
		Example: "  sudo rootfiles gpu revoke alice",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := buildRunContext(cmd, "gpu")
			if err != nil {
				return err
			}
			return module.RevokeGPUs(cmd.Context(), rc, args[0])
		},
	})

	// rootfiles gpu list
	gpuCmd.AddCommand(&cobra.Command{
		Use:     "list",
		Short:   "List current GPU allocations",
		Example: "  rootfiles gpu list",
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := buildRunContext(cmd, "gpu")
			if err != nil {
				return err
			}
			ui.WriteHeader(cmd.OutOrStdout(), cmd.CommandPath())
			return module.ListGPUAllocations(rc)
		},
	})

	// rootfiles gpu status
	gpuStatusCmd := &cobra.Command{
		Use:     "status",
		Short:   "Show GPU status with allocation info",
		Example: "  rootfiles gpu status\n  rootfiles gpu status --watch",
		RunE: func(cmd *cobra.Command, args []string) error {
			watch, interval, err := watchOptions(cmd, false)
			if err != nil {
				return err
			}
			if watch {
				return watchDashboard(cmd, ui.ModeGPU, interval, func() (ui.RefreshFunc, error) {
					rc, err := buildRunContext(cmd, "gpu")
					if err != nil {
						return nil, err
					}
					return func(ctx context.Context) (ui.DashboardSnapshot, error) {
						return collectGPUDashboardSnapshot(ctx, rc)
					}, nil
				})
			}
			rc, err := buildRunContext(cmd, "gpu")
			if err != nil {
				return err
			}
			report, err := gpuStatusCollector(cmd.Context(), rc)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			ui.WriteHeader(out, cmd.CommandPath())
			return renderGPUStatus(out, report)
		},
	}
	addWatchFlags(gpuStatusCmd, defaultHostWatchInterval)
	gpuCmd.AddCommand(gpuStatusCmd)

	migCmd := &cobra.Command{Use: "mig", Short: "Show Multi-Instance GPU mode and instances",
		Example: "  rootfiles gpu mig status"}
	migCmd.AddCommand(&cobra.Command{
		Use:     "status",
		Short:   "Show MIG mode and instances per GPU",
		Example: "  rootfiles gpu mig status",
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := buildRunContext(cmd, "gpu")
			if err != nil {
				return err
			}
			gpus, err := migStatusCollector(cmd.Context(), rc)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			ui.WriteHeader(out, cmd.CommandPath())
			renderMIGStatus(out, gpus)
			return nil
		},
	})
	gpuCmd.AddCommand(migCmd)

	return usesConfigTarget(gpuCmd)
}

// gpuStatusCollector and migStatusCollector read nvidia-smi and the allocation
// database. A test replaces them with fixture data, so the report layout is
// pinned without a GPU on the machine running it.
var (
	gpuStatusCollector = module.CollectGPUStatus
	migStatusCollector = module.MIGStatus
)

// renderGPUStatus prints the GPU table and says which devices nvidia-smi could
// not see, so an unknown memory or utilization reading is never mistaken for a
// measurement of zero.
func renderGPUStatus(out io.Writer, report *module.GPUStatusReport) error {
	ui.WriteSection(out, "GPU Status")
	table := make([][]string, 0, len(report.Rows))
	restricted := 0
	for _, row := range report.Rows {
		memory, util := "-", "-"
		if row.Restricted {
			restricted++
		} else {
			memory = fmt.Sprintf("%s/%s MB", row.MemUsed, row.MemTotal)
			util = row.Util + "%"
		}
		assigned := row.Assigned
		if assigned == "" {
			assigned = "-"
		}
		table = append(table, []string{strconv.Itoa(row.Index), row.Model, memory, util, assigned})
	}
	if err := ui.WriteTable(out, []string{"GPU", "MODEL", "MEMORY", "UTIL", "ASSIGNED TO"}, table, 1); err != nil {
		return err
	}
	if restricted > 0 {
		ui.WriteHint(out, fmt.Sprintf("%d of %d devices are not visible through nvidia-smi (cgroup restriction); their memory and utilization are unknown", restricted, len(report.Rows)))
	}
	return nil
}

// renderMIGStatus prints the MIG mode and instances per GPU without querying
// nvidia-smi, so a fixture golden pins the layout.
func renderMIGStatus(out io.Writer, gpus []module.MIGGPU) {
	ui.WriteSection(out, "MIG")
	for _, g := range gpus {
		mode := g.Current
		if g.Pending != g.Current {
			mode += " (pending: " + g.Pending + ", needs GPU reset)"
		}
		ui.WriteKV(out, "GPU "+g.Index, g.Name+"  MIG "+mode)
		for _, inst := range g.Instances {
			ui.WriteText(out, "%s\n", ui.StyleHint.Render("    -  "+inst))
		}
	}
}

func parseGPUList(s string) ([]int, error) {
	parts := strings.Split(s, ",")
	var gpus []int
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		idx, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid GPU index %q: %w", p, err)
		}
		if idx < 0 {
			return nil, fmt.Errorf("GPU index must be >= 0, got %d", idx)
		}
		gpus = append(gpus, idx)
	}
	if len(gpus) == 0 {
		return nil, fmt.Errorf("no GPU indices specified")
	}
	return gpus, nil
}
