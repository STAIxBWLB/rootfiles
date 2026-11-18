package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/fleet"
	"github.com/STAIxBWLB/rootfiles/internal/module"
	"github.com/STAIxBWLB/rootfiles/internal/state"
	"github.com/STAIxBWLB/rootfiles/internal/tenant"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

const (
	defaultHostWatchInterval  = 2 * time.Second
	defaultFleetWatchInterval = 5 * time.Second
	minWatchInterval          = 500 * time.Millisecond
)

// runDashboard is overridable in tests so CLI watch paths can be exercised
// without opening an interactive alt-screen program.
var runDashboard = ui.RunDashboard

func addWatchFlags(cmd *cobra.Command, defaultInterval time.Duration) {
	cmd.Flags().BoolP("watch", "w", false, "Refresh the report continuously in a terminal")
	cmd.Flags().Duration("interval", defaultInterval, "Refresh interval for --watch (at least 500ms)")
}

func watchOptions(cmd *cobra.Command, alwaysWatch bool) (bool, time.Duration, error) {
	watch := alwaysWatch
	if !alwaysWatch {
		watch, _ = cmd.Flags().GetBool("watch")
		if !watch && cmd.Flags().Changed("interval") {
			return false, 0, usageError(fmt.Errorf("--interval requires --watch"))
		}
	}
	if !watch {
		return false, 0, nil
	}
	interval, err := cmd.Flags().GetDuration("interval")
	if err != nil {
		return false, 0, usageError(err)
	}
	if interval < minWatchInterval {
		return false, 0, usageError(fmt.Errorf("--interval must be at least %s", minWatchInterval))
	}
	if jsonOut, _ := cmd.Flags().GetBool("json"); jsonOut {
		return false, 0, usageError(fmt.Errorf("--watch cannot be combined with --json"))
	}
	if f := cmd.Flags().Lookup("output"); f != nil && f.Changed && f.Value.String() != "text" {
		return false, 0, usageError(fmt.Errorf("--watch cannot be combined with --output %s", f.Value.String()))
	}
	return true, interval, nil
}

func dashboardConfigFor(cmd *cobra.Command, mode ui.DashboardMode, interval time.Duration) ui.DashboardConfig {
	colorMode, _ := cmd.Flags().GetString("color")
	if colorMode == "" {
		colorMode, _ = cmd.Root().PersistentFlags().GetString("color")
	}
	ascii, _ := cmd.Flags().GetBool("ascii")
	out := cmd.OutOrStdout()
	width := ui.OutputWidth(out)
	if width <= 0 {
		width = 80
	}
	return ui.DashboardConfig{
		Title:    cmd.CommandPath(),
		Mode:     mode,
		Interval: interval,
		Width:    width,
		Height:   24,
		Color:    ui.ResolveColor(out, colorMode),
		ASCII:    ui.ASCIIMarkers(ascii),
	}
}

func newMonitorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "monitor",
		Aliases: []string{"top"},
		Short:   "Watch live system, GPU, tenant and health status",
		Example: "  rootfiles monitor\n  rootfiles monitor --interval 5s",
		Long: "Interactive live terminal dashboard for host vitals, GPUs, tenant containers\n" +
			"and doctor findings. On a non-Linux controller, monitors the fleet inventory.",
		Args: cobra.NoArgs,
		RunE: runMonitor,
	}
	cmd.Flags().StringSlice("module", nil, "Report specific modules only")
	cmd.Flags().Duration("interval", defaultHostWatchInterval, "Refresh interval (at least 500ms)")
	cmd.Flags().String("inventory", "", "Controller inventory file")
	if nativeHostOS == "linux" {
		_ = cmd.Flags().MarkHidden("inventory")
	}
	cmd = usesConfigTarget(cmd)
	if nativeHostOS != "linux" {
		hidesRootFlags(cmd, "profile", "config", "home-base")
	}
	return cmd
}

func runMonitor(cmd *cobra.Command, _ []string) error {
	_, interval, err := watchOptions(cmd, true)
	if err != nil {
		return err
	}
	if nativeHostOS != "linux" {
		if !cmd.Flags().Changed("interval") {
			interval = defaultFleetWatchInterval
		}
		return runControllerMonitor(cmd, interval)
	}
	if cmd.Flags().Changed("inventory") {
		return fmt.Errorf("--inventory is only used on a controller; omit it on Linux")
	}
	moduleFilter, _ := cmd.Flags().GetStringSlice("module")
	cfg := dashboardConfigFor(cmd, ui.ModeHost, interval)
	return runDashboard(cmd.Context(), cmd.OutOrStdout(), cfg, func(ctx context.Context) (ui.DashboardSnapshot, error) {
		return collectHostDashboardSnapshot(ctx, cmd, moduleFilter)
	})
}

func runControllerMonitor(cmd *cobra.Command, interval time.Duration) error {
	invPath := fleetInventoryPath(cmd)
	inv, err := fleet.LoadInventory(invPath)
	if err != nil {
		return withNext(err, "rootfiles fleet init --inventory "+fleet.ShellQuote(invPath))
	}
	hosts, err := inv.Select(nil, nil, true)
	if err != nil {
		return err
	}
	cfg := dashboardConfigFor(cmd, ui.ModeFleet, interval)
	return runDashboard(cmd.Context(), cmd.OutOrStdout(), cfg, func(ctx context.Context) (ui.DashboardSnapshot, error) {
		return collectFleetDashboardSnapshot(ctx, inv, hosts, inv.Defaults.Parallel, "")
	})
}

func collectHostDashboardSnapshot(ctx context.Context, cmd *cobra.Command, moduleFilter []string) (ui.DashboardSnapshot, error) {
	sysInfo, _ := detectSystem()
	if sysInfo == nil {
		sysInfo = &config.SystemInfo{}
	}
	last, _ := state.Last()
	active, configPath, applied := resolveTarget(cmd, sysInfo)
	reportProfile, reportPath := reportedTarget(active, configPath, applied)

	cfg, cfgErr := config.LoadWithHomeBase(active, configPath, sysInfo, homeBaseFlag(cmd))
	if cfgErr != nil {
		cfg = config.Fallback(sysInfo, homeBaseFlag(cmd))
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	rc := readOnlyRunContext(cfg, sysInfo, logger)
	rc.Out = io.Discard
	rc.Err = io.Discard

	report := collectStatus(ctx, rc, sysInfo, reportProfile, reportPath, cfgErr, last, moduleFilter...)
	vitals := module.CollectHostVitals(rc)

	osLine := ui.FirstNonEmpty(sysInfo.OS, "unknown")
	if sysInfo.Version != "" {
		osLine += " " + sysInfo.Version
	}
	shutdownText := ""
	if report.ScheduledShutdown != nil {
		shutdownText = fmt.Sprintf("%s at %s (%s left)",
			shutdownActionWord(report.ScheduledShutdown.Action),
			report.ScheduledShutdown.At.Local().Format("2006-01-02 15:04"),
			timeLeftWords(time.Until(report.ScheduledShutdown.At)))
	}

	snap := ui.DashboardSnapshot{
		UpdatedAt: time.Now(),
		Hostname:  report.Hostname,
		OS:        osLine,
		Profile:   ui.FirstNonEmpty(report.Profile, report.ConfigPath, "(auto)"),
		Vitals: ui.VitalSummary{
			CPUCores:    sysInfo.CPUCores,
			Load1:       vitals.Load1,
			Load5:       vitals.Load5,
			Load15:      vitals.Load15,
			LoadKnown:   vitals.LoadKnown,
			MemUsedMiB:  vitals.MemUsedMiB,
			MemTotalMiB: vitals.MemTotalMiB,
			MemUsedPct:  vitals.MemUsedPct,
			Uptime:      formatDurationShort(vitals.Uptime),
			Shutdown:    shutdownText,
		},
		ModulesTotal: len(report.Modules),
	}
	if report.ConfigErr != "" {
		snap.Err = report.ConfigErr
	} else if report.ModuleCheckError != "" {
		snap.Err = report.ModuleCheckError
	}

	for _, m := range report.Modules {
		if m.Satisfied {
			snap.ModulesSatisfied++
		} else {
			snap.PendingModules = append(snap.PendingModules, m.Name)
		}
	}
	for _, mt := range vitals.Mounts {
		snap.Mounts = append(snap.Mounts, ui.MountDashRow{
			Path:    mt.Path,
			UsedPct: mt.UsedPct,
			FreeGiB: mt.FreeGiB,
			Level:   mt.Level,
		})
	}

	if statusModuleSelected(moduleFilter, "gpu") {
		if gpuRep, err := gpuStatusCollector(ctx, rc); err == nil && gpuRep != nil {
			snap.GPUs = toGPUDashRows(gpuRep.Rows)
		}
	}
	if statusModuleSelected(moduleFilter, "tenant") && rc.Config.Users.HomeBase != "" && !config.IsRocky(rc.Config.System) {
		m := module.NewTenantManager(rc, io.Discard)
		if names, err := tenantSpecLister(m.SpecDir); err == nil && len(names) > 0 {
			if tRep, err := tenantStatusCollector(m, ctx, names, false); err == nil && tRep != nil {
				snap.Tenants = toTenantDashRows(tRep.Rows)
				snap.Commitment = tRep.Commitment
				snap.Overcommitted = tRep.Overcommitted
			}
		}
	}
	for _, f := range doctorChecks(ctx, rc) {
		snap.Findings = append(snap.Findings, ui.FindingDashRow{
			Level:  f.Level,
			Check:  f.Check,
			Detail: f.Detail,
		})
	}
	return snap, nil
}

func collectGPUDashboardSnapshot(ctx context.Context, rc *module.RunContext) (ui.DashboardSnapshot, error) {
	report, err := gpuStatusCollector(ctx, rc)
	if err != nil {
		return ui.DashboardSnapshot{}, err
	}
	snap := ui.DashboardSnapshot{
		UpdatedAt: time.Now(),
		GPUs:      toGPUDashRows(report.Rows),
	}
	if migs, err := migStatusCollector(ctx, rc); err == nil {
		for _, g := range migs {
			mode := g.Current
			if g.Pending != "" && g.Pending != g.Current {
				mode += " (pending: " + g.Pending + ")"
			}
			snap.MIG = append(snap.MIG, ui.MIGDashRow{
				Index:     g.Index,
				Name:      g.Name,
				Mode:      mode,
				Instances: g.Instances,
			})
		}
	}
	return snap, nil
}

func collectTenantDashboardSnapshot(ctx context.Context, m *tenant.Manager, names []string, single bool) (ui.DashboardSnapshot, error) {
	var err error
	if len(names) == 0 {
		names, err = tenantSpecLister(m.SpecDir)
		if err != nil {
			return ui.DashboardSnapshot{}, err
		}
	}
	report, err := tenantStatusCollector(m, ctx, names, single)
	if err != nil {
		return ui.DashboardSnapshot{}, err
	}
	return ui.DashboardSnapshot{
		UpdatedAt:     time.Now(),
		Tenants:       toTenantDashRows(report.Rows),
		Commitment:    report.Commitment,
		Overcommitted: report.Overcommitted,
	}, nil
}

func collectFleetDashboardSnapshot(ctx context.Context, _ *fleet.Inventory, hosts []fleet.NamedHost, parallel int, view string) (ui.DashboardSnapshot, error) {
	if parallel <= 0 {
		parallel = 2
	}
	if parallel > len(hosts) && len(hosts) > 0 {
		parallel = len(hosts)
	}
	results := runReadPool(ctx, fleet.SSHRunner{Binary: "ssh"}, hosts, "status", parallel)
	rows := make([]ui.FleetDashRow, 0, len(results))
	for _, r := range results {
		row := ui.FleetDashRow{
			Host:     r.Host,
			State:    r.State,
			Reason:   strings.ReplaceAll(r.Reason, "\n", " "),
			Shutdown: shutdownCell(r.Remote),
		}
		if payload, ok := decodeGPUView(r); ok {
			gpuText := strconv.Itoa(payload.TotalGPUs)
			if payload.GPUModel != "" {
				gpuText += "x " + payload.GPUModel
			}
			if allocs := allocationsCell(payload); allocs != "" {
				gpuText += " (" + allocs + ")"
			}
			row.GPUs = gpuText
		}
		if payload, ok := decodeUsersView(r); ok {
			row.Users = fmt.Sprintf("managed=%d system=%d (%s)", payload.Managed, payload.System, payload.HomeBase)
		}
		rows = append(rows, row)
	}
	return ui.DashboardSnapshot{
		UpdatedAt: time.Now(),
		Fleet:     rows,
		FleetView: view,
	}, nil
}

func toGPUDashRows(rows []module.GPUStatusRow) []ui.GPUDashRow {
	out := make([]ui.GPUDashRow, 0, len(rows))
	for _, r := range rows {
		used, _ := strconv.ParseFloat(r.MemUsed, 64)
		total, _ := strconv.ParseFloat(r.MemTotal, 64)
		util, _ := strconv.ParseFloat(r.Util, 64)
		memPct := 0.0
		if total > 0 {
			memPct = (used / total) * 100
		}
		out = append(out, ui.GPUDashRow{
			Index:      r.Index,
			Model:      r.Model,
			MemUsed:    r.MemUsed,
			MemTotal:   r.MemTotal,
			MemPct:     memPct,
			UtilPct:    util,
			TempC:      r.TempC,
			PowerW:     r.PowerW,
			Assigned:   r.Assigned,
			Restricted: r.Restricted,
		})
	}
	return out
}

func toTenantDashRows(rows []tenant.StatusRow) []ui.TenantDashRow {
	out := make([]ui.TenantDashRow, 0, len(rows))
	for _, r := range rows {
		stateText := r.State
		if r.Error != "" {
			stateText = "error: " + r.Error
		}
		out = append(out, ui.TenantDashRow{
			Name:    r.Tenant,
			State:   stateText,
			SSHPort: r.SSHPort,
			GPUs:    r.GPUs,
			Spec:    r.Spec,
			Volume:  r.Volume,
		})
	}
	return out
}

func formatDurationShort(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", max(1, mins))
}
