package cli

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/exec"
	"github.com/STAIxBWLB/rootfiles/internal/module"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

func newCheckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check current system state against profile",
		Long: "Check which modules are satisfied and which need changes.\n" +
			"Exit code: 0 = all satisfied, 2 = pending changes, 1 = error. Without --profile/--config,\n" +
			"the last applied profile (or the detected suggestion) is used.",
		RunE: runCheck,
	}
	cmd.Flags().BoolP("verbose", "v", false, "Show commands that will be executed")
	addOutputFlagWithMetrics(cmd)
	return cmd
}

func runCheck(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	moduleFilter, _ := cmd.Flags().GetStringSlice("module")
	format, err := outputFormat(cmd)
	if err != nil {
		return err
	}

	sysInfo, err := detectSystem()
	if err != nil {
		return fmt.Errorf("detecting system: %w", err)
	}
	profileName, configPath, applied := resolveTarget(cmd, sysInfo)

	cfg, err := config.LoadWithHomeBase(profileName, configPath, sysInfo, homeBaseFlag(cmd))
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	applyFlagOverrides(cmd, cfg)
	applyAccountFlags(cmd, cfg)
	if err := cfg.Validate(); err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))
	runner := exec.NewRunner(true, logger) // always dry-run for check
	apt, err := exec.NewPackageManager(runner, sysInfo)
	if err != nil {
		return err
	}

	registry := module.NewRegistry()
	modules := registry.Resolve(cfg, moduleFilter)
	names := make([]string, 0, len(modules))
	for _, m := range modules {
		names = append(names, m.Name())
	}
	if err := config.ValidateCapabilities(cfg, sysInfo, names); err != nil {
		return err
	}

	rc := &module.RunContext{
		Config: cfg,
		Runner: runner,
		APT:    apt,
		DryRun: true,
		Yes:    true,
	}

	results, err := module.CheckAll(ctx, modules, rc)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	verbose, _ := cmd.Flags().GetBool("verbose")
	// Reports name the recorded target when the kept applied copy was
	// resolved, so the copy path never reaches them.
	reportProfile, reportPath := reportedTarget(profileName, configPath, applied)

	satisfied := 0
	for _, m := range modules {
		if r := results[m.Name()]; r != nil && r.Satisfied {
			satisfied++
		}
	}
	allOK := satisfied == len(modules)

	if format == "prometheus" {
		fmt.Fprintln(out, "# HELP rootfiles_module_satisfied 1 when the module has no pending changes.")
		fmt.Fprintln(out, "# TYPE rootfiles_module_satisfied gauge")
		for _, m := range modules {
			r := results[m.Name()]
			fmt.Fprintf(out, "rootfiles_module_satisfied{module=%q} %d\n", m.Name(), boolMetric(r != nil && r.Satisfied))
		}
		fmt.Fprintln(out, "# HELP rootfiles_module_pending_changes Number of pending changes per module.")
		fmt.Fprintln(out, "# TYPE rootfiles_module_pending_changes gauge")
		for _, m := range modules {
			n := 0
			if r := results[m.Name()]; r != nil {
				n = len(r.Changes)
			}
			fmt.Fprintf(out, "rootfiles_module_pending_changes{module=%q} %d\n", m.Name(), n)
		}
		fmt.Fprintln(out, "# HELP rootfiles_check_timestamp_seconds When the check ran.")
		fmt.Fprintln(out, "# TYPE rootfiles_check_timestamp_seconds gauge")
		fmt.Fprintf(out, "rootfiles_check_timestamp_seconds %d\n", time.Now().Unix())
	} else if format == "json" {
		report := checkReport{Profile: reportProfile, ConfigPath: reportPath, Satisfied: allOK}
		report.Version = buildVersion
		report.AppliedConfigSHA256, err = appliedFingerprint(cfg, reportProfile, reportPath)
		if err != nil {
			return err
		}
		for _, m := range modules {
			mr := checkModule{Name: m.Name(), Satisfied: true, Changes: []module.Change{}}
			if r := results[m.Name()]; r != nil {
				mr.Satisfied = r.Satisfied
				if r.Changes != nil {
					mr.Changes = r.Changes
				}
			}
			report.Modules = append(report.Modules, mr)
		}
		if err := writeJSON(out, report); err != nil {
			return err
		}
	} else {
		renderCheckText(out, reportProfile, reportPath, modules, results, satisfied, verbose)
	}

	if !allOK {
		return &ExitError{Code: exitDrift}
	}
	return nil
}

type checkModule struct {
	Name      string          `json:"name"`
	Satisfied bool            `json:"satisfied"`
	Changes   []module.Change `json:"changes"`
}

type checkReport struct {
	Version             string        `json:"version,omitempty"`
	Profile             string        `json:"profile,omitempty"`
	ConfigPath          string        `json:"config_path,omitempty"`
	AppliedConfigSHA256 string        `json:"applied_config_sha256,omitempty"`
	Satisfied           bool          `json:"satisfied"`
	Modules             []checkModule `json:"modules"`
}

func renderCheckText(out io.Writer, profileName, configPath string, modules []module.Module, results map[string]*module.CheckResult, satisfied int, verbose bool) {
	ui.WriteHeader(out, "rootfiles check")
	if configPath != "" {
		ui.WriteKV(out, "Config", configPath)
	} else {
		ui.WriteKV(out, "Profile", profileName)
	}
	ui.WriteSection(out, fmt.Sprintf("Modules (%d/%d satisfied)", satisfied, len(modules)))

	for _, m := range modules {
		r := results[m.Name()]
		marker := ui.OKMark()
		statusLabel := ui.StyleSuccess.Render("OK")
		changeCount := 0
		if r != nil {
			changeCount = len(r.Changes)
		}
		if r == nil || !r.Satisfied {
			marker = ui.PendingMark()
			statusLabel = ui.StyleWarning.Render("PENDING")
		}
		ui.WriteBullet(out, marker, fmt.Sprintf("%-15s %s  %d change(s)", m.Name(), statusLabel, changeCount))
		if r == nil {
			continue
		}
		for _, c := range r.Changes {
			fmt.Fprintf(out, "        %s %s\n", ui.PendingMark(), c.Description)
			if verbose && c.Command != "" {
				fmt.Fprintf(out, "          %s\n", ui.StyleHint.Render("$ "+c.Command))
			}
		}
	}

	fmt.Fprintln(out)
	if satisfied == len(modules) {
		fmt.Fprintln(out, "  "+ui.StyleSuccess.Render(ui.MarkOK+" all modules satisfied."))
	} else {
		ui.WriteHint(out, "run 'rootfiles apply' to apply pending changes (exit code 2 = pending).")
	}
}
