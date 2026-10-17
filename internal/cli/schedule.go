package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/module"
	"github.com/STAIxBWLB/rootfiles/internal/state"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

// Periodic drift/health reporting via a systemd timer. Results land in the
// state dir as JSON so monitoring (node_exporter textfile, cron mail,
// scripts) can pick them up; nothing is changed on the system.

var (
	scheduleUnitDir = "/etc/systemd/system"
	scheduleName    = "rootfiles-check"
)

func scheduleService(bin string) string {
	dir := state.Dir()
	return fmt.Sprintf(`# Managed by rootfiles-v2
[Unit]
Description=rootfiles drift and health report
After=network-online.target

[Service]
Type=oneshot
# Exit code 2 means "drift/problems found", which is a report, not a failure.
SuccessExitStatus=2
ExecStart=/bin/sh -c '%[1]s check -o json > %[2]s/check.json.tmp; rc=$?; mv %[2]s/check.json.tmp %[2]s/check.json; exit $rc'
ExecStart=/bin/sh -c '%[1]s doctor -o json > %[2]s/doctor.json.tmp; rc=$?; mv %[2]s/doctor.json.tmp %[2]s/doctor.json; exit $rc'
# Publish metrics when node_exporter's textfile collector is present.
ExecStart=/bin/sh -c 'd=%[3]s; [ -d "$d" ] || exit 0; { %[1]s check -o prometheus; %[1]s doctor -o prometheus; } > "$d/rootfiles.prom.tmp"; mv "$d/rootfiles.prom.tmp" "$d/rootfiles.prom"; exit 0'
`, bin, dir, module.NodeExporterTextfileDir)
}

func scheduleTimer(onCalendar string) string {
	return fmt.Sprintf(`# Managed by rootfiles-v2
[Unit]
Description=Run rootfiles drift and health report %s

[Timer]
OnCalendar=%s
RandomizedDelaySec=15m
Persistent=true

[Install]
WantedBy=timers.target
`, onCalendar, onCalendar)
}

func newScheduleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Periodic check/doctor reports via a systemd timer",
		Long: "enable installs rootfiles-check.timer, which writes check.json and doctor.json to\n" +
			state.Dir() + " (read-only: no changes are applied).",
	}

	enable := &cobra.Command{
		Use:   "enable",
		Short: "Install and start the report timer",
		RunE: func(cmd *cobra.Command, _ []string) error {
			onCal, _ := cmd.Flags().GetString("on-calendar")
			if strings.ContainsAny(onCal, "\n'") {
				return fmt.Errorf("invalid --on-calendar %q", onCal)
			}
			bin, err := os.Executable()
			if err != nil {
				return err
			}
			if resolved, err := filepath.EvalSymlinks(bin); err == nil {
				bin = resolved
			}
			rc, err := buildConfigFreeRunContext(cmd)
			if err != nil {
				return err
			}
			if err := rc.Runner.MkdirAll(state.Dir(), 0755); err != nil {
				return err
			}
			svc := filepath.Join(scheduleUnitDir, scheduleName+".service")
			tmr := filepath.Join(scheduleUnitDir, scheduleName+".timer")
			if err := rc.Runner.WriteFile(svc, []byte(scheduleService(bin)), 0644); err != nil {
				return err
			}
			if err := rc.Runner.WriteFile(tmr, []byte(scheduleTimer(onCal)), 0644); err != nil {
				return err
			}
			if _, err := rc.Runner.Run(cmd.Context(), "systemctl", "daemon-reload"); err != nil {
				return fmt.Errorf("units written but systemd not reachable: %w", err)
			}
			if _, err := rc.Runner.Run(cmd.Context(), "systemctl", "enable", "--now", scheduleName+".timer"); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s timer enabled (%s); reports in %s/{check,doctor}.json\n",
				ui.MarkOK, onCal, state.Dir())
			return nil
		},
	}
	enable.Flags().String("on-calendar", "daily", "systemd OnCalendar expression (e.g. hourly, daily, *-*-* 06:00)")

	disable := &cobra.Command{
		Use:   "disable",
		Short: "Stop and remove the report timer",
		RunE: func(cmd *cobra.Command, _ []string) error {
			rc, err := buildConfigFreeRunContext(cmd)
			if err != nil {
				return err
			}
			_, _ = rc.Runner.Run(cmd.Context(), "systemctl", "disable", "--now", scheduleName+".timer")
			for _, ext := range []string{".timer", ".service"} {
				if err := rc.Runner.Remove(filepath.Join(scheduleUnitDir, scheduleName+ext)); err != nil && !os.IsNotExist(err) {
					return err
				}
			}
			_, _ = rc.Runner.Run(cmd.Context(), "systemctl", "daemon-reload")
			fmt.Fprintln(cmd.OutOrStdout(), ui.MarkOK+" report timer removed")
			return nil
		},
	}

	cmd.AddCommand(enable, disable)
	return cmd
}
