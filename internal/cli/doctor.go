package cli

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/exec"
	"github.com/STAIxBWLB/rootfiles/internal/module"
	"github.com/STAIxBWLB/rootfiles/internal/state"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

func newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose remote access, disks, reboot, time sync, GPU and systemd health",
		Long: "Read-only health checks of the live system (as opposed to `check`, which compares against\n" +
			"the profile). Exit code: 0 = no failures, 2 = at least one failure (or warning with --strict).",
		RunE: runDoctor,
	}
	addOutputFlagWithMetrics(cmd)
	cmd.Flags().Bool("strict", false, "Treat warnings as failures for the exit code")
	return cmd
}

func runDoctor(cmd *cobra.Command, _ []string) error {
	format, err := outputFormat(cmd)
	if err != nil {
		return err
	}
	sys, _ := detectSystem()
	if sys == nil {
		sys = &config.SystemInfo{}
	}
	profile, configPath, applied := resolveTarget(cmd, sys)
	reportProfile, reportPath := reportedTarget(profile, configPath, applied)
	cfg, loadErr := config.LoadWithHomeBase(profile, configPath, sys, homeBaseFlag(cmd))
	if loadErr != nil {
		cfg = config.Fallback(sys, homeBaseFlag(cmd))
	}

	runner := exec.NewRunner(true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rc := &module.RunContext{Config: cfg, Runner: runner, APT: statusPackageManager(runner, sys), DryRun: true, Yes: true}
	findings := module.Doctor(cmd.Context(), rc)
	if loadErr != nil {
		findings = append(findings, module.Finding{Check: "configuration", Level: module.LevelFail, Detail: loadErr.Error()})
	}
	names := []string{}
	for _, m := range module.NewRegistry().Resolve(cfg, nil) {
		names = append(names, m.Name())
	}
	if err := config.ValidateCapabilities(cfg, sys, names); err != nil {
		findings = append(findings, module.Finding{Check: "capabilities", Level: module.LevelSkip, Detail: err.Error()})
	}

	if last, err := state.Last(); err == nil && last != nil && !last.Success {
		findings = append(findings, module.Finding{Check: "last apply", Level: module.LevelWarn,
			Detail: "last apply (" + last.FinishedAt.Local().Format("2006-01-02 15:04") + ") failed",
			Hint:   "rootfiles status"})
	}

	strict, _ := cmd.Flags().GetBool("strict")
	failed := 0
	for _, f := range findings {
		if f.Level == module.LevelFail || (strict && f.Level == module.LevelWarn) {
			failed++
		}
	}

	out := cmd.OutOrStdout()
	if format == "prometheus" {
		counts := map[string]int{module.LevelOK: 0, module.LevelWarn: 0, module.LevelFail: 0, module.LevelSkip: 0}
		for _, f := range findings {
			counts[f.Level]++
		}
		fmt.Fprintln(out, "# HELP rootfiles_doctor_findings Doctor findings by level.")
		fmt.Fprintln(out, "# TYPE rootfiles_doctor_findings gauge")
		for _, l := range []string{module.LevelOK, module.LevelWarn, module.LevelFail, module.LevelSkip} {
			fmt.Fprintf(out, "rootfiles_doctor_findings{level=%q} %d\n", l, counts[l])
		}
		fmt.Fprintln(out, "# HELP rootfiles_doctor_check_ok 1 when a doctor check is ok or skipped.")
		fmt.Fprintln(out, "# TYPE rootfiles_doctor_check_ok gauge")
		for _, f := range findings {
			fmt.Fprintf(out, "rootfiles_doctor_check_ok{check=%q} %d\n", f.Check, boolMetric(f.Level == module.LevelOK || f.Level == module.LevelSkip))
		}
	} else if format == "json" {
		fingerprint, err := appliedFingerprint(cfg, reportProfile, reportPath)
		if err != nil {
			return err
		}
		if err := writeJSON(out, map[string]any{"findings": findings, "failed": failed, "applied_config_sha256": fingerprint, "version": buildVersion}); err != nil {
			return err
		}
	} else {
		ui.WriteHeader(out, "rootfiles doctor")
		for _, f := range findings {
			marker := ui.OKMark()
			switch f.Level {
			case module.LevelWarn:
				marker = ui.WarnMark()
			case module.LevelFail:
				marker = ui.StyleError.Render(ui.MarkFail)
			case module.LevelSkip:
				marker = ui.StyleHint.Render("-")
			}
			ui.WriteBullet(out, marker, fmt.Sprintf("%-16s %s", f.Check, f.Detail))
			if f.Hint != "" && f.Level != module.LevelOK {
				fmt.Fprintf(out, "      %s\n", ui.StyleHint.Render("→ "+f.Hint))
			}
		}
		fmt.Fprintln(out)
	}
	if failed > 0 {
		return &ExitError{Code: exitDrift}
	}
	return nil
}
