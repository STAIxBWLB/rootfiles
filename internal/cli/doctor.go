package cli

import (
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/module"
	"github.com/STAIxBWLB/rootfiles/internal/state"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

// doctorChecks keeps command-format fixtures independent of host files.
var doctorChecks = module.Doctor

func newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "doctor",
		Short:   "Check disks, reboot, time sync, GPU, systemd and access",
		Example: "  rootfiles doctor\n  rootfiles doctor --json --strict",
		Long: "Read-only health checks of the live system, as opposed to `check`,\n" +
			"which compares against the profile.\n" +
			"Exit code: 0 = no failures, 2 = at least one failure (or warning with --strict).",
		RunE: runDoctor,
	}
	cmd.Flags().String("inventory", "", "Controller inventory file")
	if nativeHostOS == "linux" {
		// Only the controller report reads the inventory. The flag stays
		// registered so a Linux invocation is refused with the reason rather
		// than an unknown-flag error; it just is not advertised here.
		_ = cmd.Flags().MarkHidden("inventory")
	}
	addOutputFlagWithMetrics(cmd)
	cmd.Flags().Bool("strict", false, "Treat warnings as failures for the exit code")
	return usesConfigTarget(cmd)
}

func runDoctor(cmd *cobra.Command, _ []string) error {
	if nativeHostOS != "linux" {
		return runControllerDoctor(cmd)
	}
	if cmd.Flags().Changed("inventory") {
		return fmt.Errorf("--inventory is only used on a controller; omit it on Linux")
	}
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
	// Doctor reports on the config it is given; it does not read
	// --vlan-address, so it stays a config-target reader and keeps refusing
	// inputs it does not use.
	cfg, loadErr := config.LoadWithHomeBase(profile, configPath, sys, homeBaseFlag(cmd))
	if loadErr != nil {
		cfg = config.Fallback(sys, homeBaseFlag(cmd))
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if diagnosticVerbose(cmd) {
		logger = newLogger(cmd, true, slog.LevelInfo)
	}
	rc := readOnlyRunContext(cfg, sys, logger)
	rc.Out = cmd.OutOrStdout()
	rc.Err = cmd.ErrOrStderr()
	findings := doctorChecks(cmd.Context(), rc)
	if loadErr != nil {
		findings = append(findings, module.Finding{Check: "configuration", Level: module.LevelFail, Detail: loadErr.Error()})
	}
	if err := validateModules(cfg, sys, module.NewRegistry().Resolve(cfg, nil)); err != nil {
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
			detail := f.Detail
			switch f.Level {
			case module.LevelWarn:
				marker = ui.WarnMark()
			case module.LevelFail:
				marker = ui.StyleError.Render(ui.MarkFail)
			case module.LevelSkip:
				marker = ui.StyleHint.Render(ui.MarkAbsent)
				detail = "skipped: " + detail
			}
			ui.WriteBullet(out, marker, fmt.Sprintf("%-16s %s", f.Check, detail))
			if f.Hint != "" && f.Level != module.LevelOK {
				fmt.Fprintf(out, "      %s\n", ui.StyleAccent.Render(ui.MarkNext+" "+f.Hint))
			}
		}
		counts := map[string]int{}
		for _, f := range findings {
			counts[f.Level]++
		}
		ascii, _ := cmd.Flags().GetBool("ascii")
		ascii = ui.ASCIIMarkers(ascii)
		parts := []string{}
		for _, entry := range []struct{ level, marker, word string }{
			{module.LevelFail, ui.FailMark(), "failed"}, {module.LevelWarn, ui.WarnMark(), "warnings"}, {module.LevelOK, ui.OKMark(), "ok"}, {module.LevelSkip, ui.StyleHint.Render(ui.MarkAbsent), "skipped"},
		} {
			if count := counts[entry.level]; count > 0 {
				parts = append(parts, fmt.Sprintf("%s %d %s", ui.MarkerCell(entry.marker, ascii), count, entry.word))
			}
		}
		if len(parts) > 0 {
			ui.WriteLine(out, strings.Join(parts, "  "))
		}
	}
	if failed > 0 {
		return &ExitError{Code: exitDrift}
	}
	return nil
}
