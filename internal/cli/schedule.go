package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/exec"
	"github.com/STAIxBWLB/rootfiles/internal/fleet"
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
	return fmt.Sprintf(`# Managed by rootfiles
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
	return fmt.Sprintf(`# Managed by rootfiles
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

// validateOnCalendar shares the unit-file boundary with fleet scheduling.
func validateOnCalendar(expr string) error {
	return fleet.ValidateScheduleCalendar(expr)
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
			if err := validateOnCalendar(onCal); err != nil {
				return err
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
			unitsChanged, err := writeScheduleUnits(rc.Runner, bin, onCal)
			if err != nil {
				return err
			}
			if unitsChanged {
				if _, err := rc.Runner.Run(cmd.Context(), "systemctl", "daemon-reload"); err != nil {
					return fmt.Errorf("units written but systemd not reachable: %w; run systemctl daemon-reload after restoring access to systemd", err)
				}
			}
			if !scheduleTimerReady(cmd.Context(), rc.Runner) {
				// enable itself otherwise implies daemon-reload, even when
				// all installed unit bytes are unchanged.
				if _, err := rc.Runner.Run(cmd.Context(), "systemctl", "enable", "--now", "--no-reload", scheduleName+".timer"); err != nil {
					return fmt.Errorf("cannot enable schedule timer: %w; inspect systemctl status %s.timer before retrying", err, scheduleName)
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s timer enabled (%s); reports in %s/{check,doctor}.json\n",
				ui.MarkOK, onCal, state.Dir())
			startFirstReport(cmd.Context(), rc.Runner, cmd.OutOrStdout(), cmd.ErrOrStderr())
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

	status := &cobra.Command{
		Use:   "status",
		Short: "Show the report timer state and the age of its reports",
		Long: `Reports what systemd has right now: whether the units are installed, the enabled
and active state of the timer, the OnCalendar expression it fires on, and how old
check.json and doctor.json are. A report older than 48 hours is marked stale, the
same horizon the monitoring hub's RootfilesReportStale alert uses. Read-only.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			runner := exec.NewRunner(true, newLogger(cmd, true, slog.LevelInfo))
			reportScheduleStatus(cmd, runner)
			return nil
		},
	}

	cmd.AddCommand(usesConfigTarget(enable), usesConfigTarget(disable), status)
	return cmd
}

func scheduleTimerReady(ctx context.Context, runner *exec.Runner) bool {
	enabled, err := runner.Query(ctx, "systemctl", "is-enabled", scheduleName+".timer")
	if err != nil {
		return false
	}
	state := strings.TrimSpace(enabled.Stdout)
	if state != "enabled" {
		return false
	}
	active, err := runner.Query(ctx, "systemctl", "is-active", scheduleName+".timer")
	return err == nil && strings.TrimSpace(active.Stdout) == "active"
}

// writeScheduleUnits preserves an installed unit when only its legacy marker
// differs. The caller reloads systemd only after a real unit content change.
func writeScheduleUnits(runner *exec.Runner, bin, onCalendar string) (bool, error) {
	changed := false
	for _, unit := range []struct{ suffix, content string }{{".service", scheduleService(bin)}, {".timer", scheduleTimer(onCalendar)}} {
		file := filepath.Join(scheduleUnitDir, scheduleName+unit.suffix)
		current, err := runner.ReadFile(file)
		if err == nil && exec.ManagedContentEqual(string(current), unit.content) {
			continue
		}
		if err != nil && !os.IsNotExist(err) {
			return changed, fmt.Errorf("cannot read schedule unit %s: %w; repair its permissions before enabling the schedule", file, err)
		}
		if err := runner.WriteFile(file, []byte(unit.content), 0644); err != nil {
			return changed, fmt.Errorf("cannot write schedule unit %s: %w; check unit directory and backup permissions before retrying", file, err)
		}
		changed = true
	}
	return changed, nil
}

// reportScheduleStatus prints the timer and report state; it never changes
// anything, so an uninstalled timer is a report, not an error.
func reportScheduleStatus(cmd *cobra.Command, runner *exec.Runner) {
	ctx, out := cmd.Context(), cmd.OutOrStdout()
	ui.WriteSection(out, "Scheduled reports")

	timer := filepath.Join(scheduleUnitDir, scheduleName+".timer")
	if !runner.FileExists(timer) {
		ui.WriteKV(out, "Timer", "not installed")
		ui.WriteHint(out, "run `sudo rootfiles schedule enable` to install the daily report timer")
	} else {
		ui.WriteKV(out, "Timer", "installed")
		ui.WriteKV(out, "Enabled", systemctlWord(ctx, runner, "is-enabled", scheduleName+".timer"))
		ui.WriteKV(out, "Active", systemctlWord(ctx, runner, "is-active", scheduleName+".timer"))
		calendar := "unknown"
		if data, err := runner.ReadFile(timer); err == nil {
			if value := onCalendarLine(string(data)); value != "" {
				calendar = value
			}
		}
		ui.WriteKV(out, "OnCalendar", calendar)
	}
	service := "not installed"
	if runner.FileExists(filepath.Join(scheduleUnitDir, scheduleName+".service")) {
		service = "installed"
	}
	ui.WriteKV(out, "Service", service)
	now := time.Now()
	for _, name := range []string{"check.json", "doctor.json"} {
		ui.WriteKV(out, name, reportAge(filepath.Join(state.Dir(), name), now))
	}
}

// systemctlWord reads a one-word unit state. systemd prints the state on
// stdout even when the exit status is non-zero (a disabled timer exits 1), and
// a host without systemd must not fail the report, so an unreadable state
// shows as unknown.
func systemctlWord(ctx context.Context, runner *exec.Runner, verb, unit string) string {
	res, _ := runner.Query(ctx, "systemctl", verb, unit)
	if res != nil {
		if word := strings.TrimSpace(res.Stdout); word != "" {
			return word
		}
	}
	return "unknown"
}

// onCalendarLine returns the OnCalendar= value of a timer unit.
func onCalendarLine(unit string) string {
	for _, line := range strings.Split(unit, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "OnCalendar="); ok {
			return value
		}
	}
	return ""
}

// staleReportAge is the horizon the monitoring hub's RootfilesReportStale
// alert uses, so the operator sees staleness here before the alert fires.
const staleReportAge = 48 * time.Hour

// reportAge renders a report file's timestamp, its age and, past the alert
// horizon, that it is stale.
func reportAge(path string, now time.Time) string {
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		return "missing"
	}
	if err != nil {
		return "unavailable: " + err.Error() + "; check the report path and permissions"
	}
	age := now.Sub(fi.ModTime())
	text := fmt.Sprintf("%s (%s old)", fi.ModTime().Format("2006-01-02 15:04"), humanAge(age))
	if age > staleReportAge {
		text += " stale"
	}
	return text
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < staleReportAge:
		return fmt.Sprintf("%d h", int(d.Hours()))
	default:
		return fmt.Sprintf("%d d", int(d.Hours()/24))
	}
}

// startFirstReport runs the report service once when no report exists yet,
// so a new host does not look stale until the first timer run. A failure
// only warns: the timer is already enabled.
func startFirstReport(ctx context.Context, runner *exec.Runner, out, errOut io.Writer) {
	if runner.FileExists(filepath.Join(state.Dir(), "check.json")) {
		return
	}
	if _, err := runner.Run(ctx, "systemctl", "start", "--no-block", scheduleName+".service"); err != nil {
		fmt.Fprintf(errOut, "%s could not start the first report: %v\n", ui.MarkWarn, err)
		return
	}
	fmt.Fprintf(out, "%s first report started (no report existed yet)\n", ui.MarkOK)
}
