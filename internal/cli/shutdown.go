package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/shutdown"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

// A poweroff or reboot at one absolute UTC instant, announced to host
// sessions and tenant containers in advance. Simultaneity across a fleet comes
// from the shared instant, not from parallel SSH.

func newShutdownCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shutdown",
		Short: "Schedule a poweroff or reboot with advance notice",
		Long: `schedule writes rootfiles-shutdown.timer, one notice timer per lead time and a
state file, so the host stops at one absolute UTC instant whether or not anyone
is logged in. cf. systemd's shutdown(8): this keeps the schedule in
/var/lib/rootfiles/shutdown.json, so status reports it and fleet shutdown can
read it.

The instant must carry an explicit offset; a host's own time zone never moves it.`,
	}

	schedule := &cobra.Command{
		Use:   "schedule",
		Short: "Schedule the poweroff or reboot",
		Args:  cobra.NoArgs,
		RunE:  runShutdownSchedule,
	}
	schedule.Flags().String("at", "", "Absolute instant: RFC 3339 with an explicit offset (2026-11-21T18:00:00+09:00)")
	schedule.Flags().Bool("reboot", false, "Reboot instead of powering off")
	schedule.Flags().String("message", "", "Reason shown in every notice (printable, at most 300 characters)")
	schedule.Flags().String("notice", shutdown.DefaultNotices, "Comma-separated lead times before the instant")
	schedule.Flags().Bool("replace", false, "Replace a schedule that already exists")
	schedule.Flags().Bool("notify-now", false, "Send one notice immediately as well (used by `fleet shutdown now`)")

	notify := &cobra.Command{
		Use:    "notify",
		Short:  "Send one notice (run by the notice timers)",
		Args:   cobra.NoArgs,
		Hidden: true,
		RunE:   runShutdownNotify,
	}
	notify.Flags().String("notice", "", "Lead time this notice belongs to, as recorded in the schedule")

	block := &cobra.Command{
		Use:    "block",
		Short:  "Stop new tenant logins (run by the login-block timer)",
		Args:   cobra.NoArgs,
		Hidden: true,
		RunE:   runShutdownBlock,
	}

	cancel := &cobra.Command{
		Use:   "cancel",
		Short: "Remove the schedule, the units and any tenant login block",
		Args:  cobra.NoArgs,
		RunE:  runShutdownCancel,
	}

	status := &cobra.Command{
		Use:   "status",
		Short: "Show the schedule, the next notice and the tenant login block",
		Long: "Read-only: reads the state and units, and inspects running tenant containers\n" +
			"through Docker. An unavailable inspection is reported as unknown.",
		Args: cobra.NoArgs,
		RunE: runShutdownStatus,
	}
	addOutputFlag(status)

	cmd.AddCommand(schedule, notify, block, cancel, status)
	return cmd
}

func runShutdownSchedule(cmd *cobra.Command, _ []string) error {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	atFlag, _ := cmd.Flags().GetString("at")
	if strings.TrimSpace(atFlag) == "" {
		return withNext(fmt.Errorf("%s: --at is required: give the instant as RFC 3339 with an explicit offset, such as 2026-11-21T18:00:00+09:00", cmd.CommandPath()),
			"rootfiles shutdown schedule --at 2026-11-21T18:00:00+09:00")
	}
	now := time.Now()
	at, err := shutdown.ParseAt(atFlag, now)
	if err != nil {
		return err
	}
	message, _ := cmd.Flags().GetString("message")
	notices, _ := cmd.Flags().GetString("notice")
	reboot, _ := cmd.Flags().GetBool("reboot")
	replace, _ := cmd.Flags().GetBool("replace")
	notifyNow, _ := cmd.Flags().GetBool("notify-now")

	bin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot locate this rootfiles binary for the notice units: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(bin); err == nil {
		bin = resolved
	}

	req := shutdown.Request{
		At:        at,
		Message:   message,
		Notices:   notices,
		Replace:   replace,
		NotifyNow: notifyNow,
		Binary:    bin,
	}
	if reboot {
		req.Action = shutdown.ActionReboot
	}
	s, warnings, err := shutdown.ScheduleRun(cmd.Context(), shutdown.Env{Runner: newRunner(cmd, dryRun)}, req)
	reportShutdownWarnings(cmd.ErrOrStderr(), warnings)
	if err != nil {
		if s != nil {
			return withNext(err, "rootfiles shutdown cancel")
		}
		return err
	}
	verb := "scheduled"
	if dryRun {
		verb = "dry-run: would schedule"
	}
	ui.WriteText(cmd.OutOrStdout(), "%s %s %s for %s (%s); notices: %s\n", ui.MarkOK, verb,
		shutdownActionWord(s.Action), s.At.UTC().Format(time.RFC3339),
		s.At.Local().Format("2006-01-02 15:04 MST"), noticeLeads(s.Notices))
	return nil
}

func runShutdownNotify(cmd *cobra.Command, _ []string) error {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	lead, _ := cmd.Flags().GetString("notice")
	s, warnings, err := shutdown.NotifyRun(cmd.Context(), shutdown.Env{Runner: newRunner(cmd, dryRun)}, lead)
	reportShutdownWarnings(cmd.ErrOrStderr(), warnings)
	if err != nil {
		return err
	}
	if s == nil {
		ui.WriteText(cmd.OutOrStdout(), "%s no scheduled shutdown; nothing to announce\n", ui.MarkOK)
		return nil
	}
	where := lead
	if where == "" {
		where = "manual"
	}
	ui.WriteText(cmd.OutOrStdout(), "%s notice sent (%s before %s)\n", ui.MarkOK, where, s.At.UTC().Format(time.RFC3339))
	return nil
}

func runShutdownBlock(cmd *cobra.Command, _ []string) error {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	s, warnings, err := shutdown.BlockRun(cmd.Context(), shutdown.Env{Runner: newRunner(cmd, dryRun)})
	reportShutdownWarnings(cmd.ErrOrStderr(), warnings)
	if err != nil {
		return err
	}
	if s == nil {
		ui.WriteText(cmd.OutOrStdout(), "%s no scheduled shutdown; logins stay open\n", ui.MarkOK)
		return nil
	}
	ui.WriteText(cmd.OutOrStdout(), "%s tenant login block attempted for the %s at %s\n", ui.MarkOK,
		shutdownActionWord(s.Action), s.At.UTC().Format(time.RFC3339))
	return nil
}

func runShutdownCancel(cmd *cobra.Command, _ []string) error {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	s, warnings, err := shutdown.CancelRun(cmd.Context(), shutdown.Env{Runner: newRunner(cmd, dryRun)})
	reportShutdownWarnings(cmd.ErrOrStderr(), warnings)
	if err != nil {
		return err
	}
	if s == nil {
		if len(warnings) > 0 {
			ui.WriteText(cmd.OutOrStdout(), "%s nothing scheduled; leftover units and any login block were removed\n", ui.MarkOK)
			return nil
		}
		ui.WriteText(cmd.OutOrStdout(), "%s nothing scheduled; nothing to cancel\n", ui.MarkOK)
		return nil
	}
	verb := "cancelled"
	if dryRun {
		verb = "dry-run: would cancel"
	}
	ui.WriteText(cmd.OutOrStdout(), "%s %s the %s scheduled for %s; this schedule's tenant login restrictions were removed where possible\n",
		ui.MarkOK, verb, shutdownActionWord(s.Action), s.At.UTC().Format(time.RFC3339))
	return nil
}

type shutdownStatusReport struct {
	Scheduled       *shutdown.Schedule          `json:"scheduled_shutdown,omitempty"`
	NextNotice      *shutdown.Notice            `json:"next_notice,omitempty"`
	LoginsBlocked   *bool                       `json:"logins_blocked"`
	TenantLogins    *shutdown.TenantLoginStatus `json:"tenant_logins,omitempty"`
	LegacyHostBlock bool                        `json:"legacy_host_block"`
	UnitsInstalled  bool                        `json:"units_installed"`
	// ScheduledError reports a state file that cannot be read, so a fleet
	// pre-flight refuses to schedule over a timer it cannot inspect instead of
	// reading the silence as "nothing scheduled".
	ScheduledError string `json:"scheduled_shutdown_error,omitempty"`
}

func runShutdownStatus(cmd *cobra.Command, _ []string) error {
	format, err := outputFormat(cmd)
	if err != nil {
		return err
	}
	report := shutdownStatusReport{}
	s, err := shutdown.LoadForReport(time.Now())
	if err != nil {
		report.ScheduledError = firstLineOf(err)
	}
	if s != nil {
		report.Scheduled = s
		report.NextNotice = nextShutdownNotice(*s, time.Now())
		report.LegacyHostBlock = shutdownLoginsBlocked(s)
		loginStatus := shutdown.InspectTenantLogins(cmd.Context(), newRunner(cmd, true))
		report.TenantLogins = &loginStatus
		if loginStatus.Unknown == 0 {
			blocked := loginStatus.Blocked > 0
			report.LoginsBlocked = &blocked
		}
		report.UnitsInstalled = newRunner(cmd, true).FileExists(filepath.Join(shutdown.UnitDir, shutdown.UnitName+".timer"))
	}
	if format == "json" {
		return writeJSON(cmd.OutOrStdout(), report)
	}
	renderShutdownStatusText(cmd.OutOrStdout(), report)
	return nil
}

func nextShutdownNotice(s shutdown.Schedule, now time.Time) *shutdown.Notice {
	for i := range s.Notices {
		if s.Notices[i].At.After(now) {
			return &s.Notices[i]
		}
	}
	return nil
}

// shutdownLoginsBlocked reports a host /run/nologin carrying this schedule's
// marker. This build blocks logins inside tenant containers only, so the file
// can only be a leftover from an earlier build: reading the host path keeps that
// leftover visible without claiming anything about the tenants.
func shutdownLoginsBlocked(s *shutdown.Schedule) bool {
	data, err := os.ReadFile(shutdown.NologinPath)
	return err == nil && shutdown.WroteNologin(string(data), s.ID)
}

func renderShutdownStatusText(out io.Writer, report shutdownStatusReport) {
	s := report.Scheduled
	if s == nil {
		ui.WriteSection(out, "Scheduled shutdown")
		ui.WriteHint(out, "nothing scheduled")
		if report.ScheduledError != "" {
			ui.WriteHint(out, fmt.Sprintf("%s %s", ui.WarnMark(), report.ScheduledError))
			ui.WriteHint(out, "run 'rootfiles shutdown cancel' to remove the units it cannot name")
		}
		return
	}
	ui.WriteSection(out, "Scheduled shutdown")
	ui.WriteKV(out, "Action", shutdownActionWord(s.Action))
	ui.WriteKV(out, "At", fmt.Sprintf("%s (%s)", s.At.UTC().Format(time.RFC3339), s.At.Local().Format("2006-01-02 15:04 MST")))
	ui.WriteKV(out, "Time left", timeLeftWords(time.Until(s.At)))
	if s.Message != "" {
		ui.WriteKV(out, "Message", s.Message)
	}
	ui.WriteKV(out, "Scheduled by", fmt.Sprintf("%s at %s", s.CreatedBy, s.CreatedAt.Local().Format("2006-01-02 15:04")))
	ui.WriteKV(out, "Units", installedWord(report.UnitsInstalled))

	ui.WriteSection(out, "Notices")
	for _, n := range s.Notices {
		ui.WriteKV(out, n.Lead+" before", n.At.UTC().Format(time.RFC3339))
	}
	if report.NextNotice == nil {
		ui.WriteHint(out, "no notice left; one was sent when the schedule was written")
	} else {
		ui.WriteKV(out, "Next", fmt.Sprintf("%s at %s", report.NextNotice.Lead, report.NextNotice.At.UTC().Format(time.RFC3339)))
	}

	ui.WriteSection(out, "Logins")
	if report.LegacyHostBlock {
		ui.WriteKV(out, "Legacy host block", "yes: "+shutdown.NologinPath+" carries this schedule's marker")
		ui.WriteHint(out, "run 'rootfiles shutdown cancel' to remove the legacy block")
	}
	if report.TenantLogins != nil {
		state := fmt.Sprintf("%d of %d checked containers", report.TenantLogins.Blocked, report.TenantLogins.Checked)
		if report.TenantLogins.Unknown > 0 {
			state += fmt.Sprintf("; %d unknown", report.TenantLogins.Unknown)
		}
		ui.WriteKV(out, "Blocked", state)
	}
	ui.WriteHint(out, fmt.Sprintf("tenant login blocks start %s before the instant; host SSH stays open", shutdown.NologinLead))
}

func installedWord(installed bool) string {
	if installed {
		return "installed"
	}
	return "missing: run 'rootfiles shutdown schedule' again or 'rootfiles shutdown cancel'"
}

func timeLeftWords(d time.Duration) string {
	if d <= 0 {
		return "the instant has passed"
	}
	return shutdown.TimeLeft(d)
}

func shutdownActionWord(action string) string {
	if action == shutdown.ActionReboot {
		return "reboot"
	}
	return "poweroff"
}

func noticeLeads(notices []shutdown.Notice) string {
	if len(notices) == 0 {
		return "one sent now"
	}
	leads := make([]string, 0, len(notices))
	for _, n := range notices {
		leads = append(leads, n.Lead)
	}
	return strings.Join(leads, ", ")
}

// firstLineOf keeps a JSON report field to the line that explains the failure.
func firstLineOf(err error) string {
	line := strings.TrimSpace(strings.SplitN(err.Error(), "\n", 2)[0])
	if line == "" {
		return "failed"
	}
	return line
}

// reportShutdownWarnings keeps a delivery problem visible without failing a
// schedule or a cancel that already did its work.
func reportShutdownWarnings(w io.Writer, warnings []string) {
	for _, warning := range warnings {
		fmt.Fprintf(w, "%s %s\n", ui.MarkWarn, warning)
	}
}
