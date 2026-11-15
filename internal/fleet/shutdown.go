package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/STAIxBWLB/rootfiles/internal/shutdown"
)

// Simultaneity across a fleet comes from one absolute UTC instant handed to
// every host, not from parallel SSH: the existing "one host at a time, stop at
// the first failure" rollout contract still governs scheduling, and only the
// timers fire together.

// MaxClockSkew bounds how far a host's clock may differ from the controller's
// before scheduling is refused: the instant is absolute, so a drifting clock
// stops the host at the wrong moment.
const MaxClockSkew = 30 * time.Second

// ShutdownMargin is added to a `fleet shutdown now` lead. The instant is
// truncated to whole seconds, the host's clock may be MaxClockSkew ahead of the
// controller's, and the schedule command takes time to arrive, so a bare
// --in 1m would read as under a minute on the host and be refused there.
const ShutdownMargin = MaxClockSkew + 5*time.Second

// clockNow is this controller's clock, overridable so a test can put the run
// next to an endpoint of the accepted window.
var clockNow = time.Now

// ShutdownRequest is what a fleet run hands to every selected host.
type ShutdownRequest struct {
	At        time.Time // identical for every host
	Action    string    // shutdown.ActionPoweroff or shutdown.ActionReboot
	Message   string
	Notices   string // comma-separated lead times; empty uses the host default
	Replace   bool
	NotifyNow bool // `fleet shutdown now`: announce immediately
}

// ShutdownSchedule schedules every selected host for one instant. All hosts
// are pre-flighted before anything is written, so an unreachable host, a
// drifting clock or an existing schedule stops the run with no host changed.
// If a host then fails mid-run, the hosts this run already scheduled are
// cancelled and reported as cancelled (or cancel-failed).
func ShutdownSchedule(ctx context.Context, runner SSHRunner, hosts []NamedHost, req ShutdownRequest, dry bool) []Result {
	const op = "shutdown schedule"
	verb := shutdownActionWord(req.Action)
	if dry {
		out := make([]Result, 0, len(hosts))
		detail := "dry-run: would schedule " + verb + " for " + req.At.UTC().Format(time.RFC3339)
		if leads := strings.TrimSpace(req.Notices); leads != "" {
			detail += " with notices " + leads
		}
		for _, h := range hosts {
			out = append(out, Result{Host: h.Name, Command: op, State: "ok", Reason: detail})
		}
		return out
	}

	preflight := make([]Result, len(hosts))
	// The host validates the lead again against its own clock, so the whole run
	// is refused here when the instant is already too close for the worst clock
	// the preflight will tolerate: transport and the preflight itself take time
	// that a bare MinLead does not account for.
	if lead := req.At.Sub(clockNow()); lead < shutdown.MinLead+MaxClockSkew {
		reason := fmt.Sprintf("the instant is %s away, and every host needs at least %s after the run reaches it (the %s clock skew plus time to enable the timers): set a later --at or a larger --in",
			lead.Round(time.Second), shutdown.MinLead, MaxClockSkew)
		out := make([]Result, 0, len(hosts))
		for _, h := range hosts {
			out = append(out, Result{Host: h.Name, Command: op, State: "skipped", Reason: reason, ExitCode: -1})
		}
		return out
	}
	failed := ""
	for i, h := range hosts {
		preflight[i] = shutdownPreflight(ctx, runner, h, req)
		if preflight[i].State == "unreachable" && preflight[i].Reason == reasonConnectFailed && runner.SkipUnreachable {
			preflight[i].State = "skipped-unreachable"
			continue
		}
		if preflight[i].State != "ok" && failed == "" {
			failed = h.Name
		}
	}
	if failed != "" {
		out := make([]Result, 0, len(hosts))
		for i, h := range hosts {
			r := preflight[i]
			r.Command = op
			if r.State == "ok" {
				r = Result{Host: h.Name, Command: op, State: "skipped",
					Reason: "not attempted: pre-flight failed on " + failed, ExitCode: -1}
			}
			out = append(out, r)
		}
		return out
	}

	command := shutdownScheduleCommand(req)
	attempted := make([]bool, len(hosts))
	untouched := make([]bool, len(hosts))
	out := make([]Result, 0, len(hosts))
	stopped := -1
	for i, h := range hosts {
		switch {
		case stopped >= 0:
			out = append(out, Result{Host: h.Name, Command: op, State: "skipped", Reason: "an earlier host failed", ExitCode: -1})
			continue
		case preflight[i].State == "skipped-unreachable":
			out = append(out, preflight[i])
			continue
		}
		attempted[i] = true
		r := runner.runMutation(ctx, h, op, privileged(h.Sudo, command), nil)
		if r.State != "ok" {
			stopped = i
			// The host says so when it refused before writing anything, which is
			// what tells a rollback that a schedule already recorded there was
			// never replaced and must not be cancelled.
			untouched[i] = strings.HasPrefix(r.Reason, "rootfiles: "+shutdown.ErrUntouched.Error()+": ")
		}
		out = append(out, r)
	}
	if stopped < 0 {
		return out
	}
	// The failing host counts as uncertain: its schedule can have enabled the
	// poweroff timer before a later notice timer failed, or lost the answer on
	// the way back. Cancelling it is idempotent, so it is cancelled too and
	// only a failed cancel leaves it reported as possibly armed.
	for i, h := range hosts {
		if !attempted[i] {
			continue
		}
		if untouched[i] {
			out[i].Reason = "failed before it changed anything (" + out[i].Reason + "), so any schedule that was already there is still in force: nothing was cancelled"
			out[i].ExitCode = 1
			continue
		}
		cancel := runner.runMutation(ctx, h, "shutdown cancel", privileged(h.Sudo, remoteBinary+" shutdown cancel"), nil)
		switch {
		case cancel.State == "ok" && i == stopped:
			out[i].Reason = "failed while scheduling (" + out[i].Reason + "); its units were cancelled again, so it will not stop on its own"
		case cancel.State == "ok":
			out[i].State = "cancelled"
			out[i].Reason = "scheduled, then cancelled because " + hosts[stopped].Name + " failed"
		case i == stopped:
			out[i].State = "cancel-failed"
			out[i].Reason = "failed while scheduling (" + out[i].Reason + ") and the follow-up cancel also failed (" + cancel.Reason + "): it may still stop at the instant"
		default:
			out[i].State = "cancel-failed"
			out[i].Reason = "cancel failed after " + hosts[stopped].Name + " failed: " + cancel.Reason
		}
		out[i].ExitCode = 1
	}
	return out
}

// ShutdownCancel cancels every selected host, continuing past failures:
// cancelling has to reach every host it can, so this is the one mutating fleet
// command that does not stop at the first error.
func ShutdownCancel(ctx context.Context, runner SSHRunner, hosts []NamedHost, dry bool) []Result {
	const op = "shutdown cancel"
	out := make([]Result, 0, len(hosts))
	for _, h := range hosts {
		if dry {
			out = append(out, Result{Host: h.Name, Command: op, State: "ok", Reason: "dry-run: would cancel any scheduled shutdown"})
			continue
		}
		r := runner.runMutation(ctx, h, op, privileged(h.Sudo, remoteBinary+" shutdown cancel"), nil)
		if r.State == "unreachable" && r.Reason == reasonConnectFailed && runner.SkipUnreachable {
			r.State = "skipped-unreachable"
		}
		out = append(out, r)
	}
	return out
}

// shutdownPreflight proves a host can take the schedule before any host is
// written: its clock is close enough, rootfiles there has the shutdown command,
// and (without --replace) nothing is scheduled yet.
func shutdownPreflight(ctx context.Context, runner SSHRunner, h NamedHost, req ShutdownRequest) Result {
	const op = "shutdown schedule"
	skew, probe := clockSkew(ctx, runner, h)
	if probe.State != "ok" {
		probe.Command = op
		return probe
	}
	if skew > MaxClockSkew || skew < -MaxClockSkew {
		return Result{Host: h.Name, Command: op, State: "error", ExitCode: 1,
			Reason: fmt.Sprintf("%s: clock skew is %s (the host clock is %s the controller); the limit is %s, so fix time sync before scheduling",
				h.Name, skew.Round(time.Second).Abs(), skewDirection(skew), MaxClockSkew)}
	}
	remote := runner.runMutation(ctx, h, "shutdown status", privileged(h.Sudo, remoteBinary+" shutdown status -o json"), nil)
	if remote.State != "ok" {
		remote.Command = op
		if remote.State == "error" {
			remote.Reason = "cannot read the host's schedule: " + remote.Reason + "; update rootfiles there if it predates `rootfiles shutdown`"
		}
		return remote
	}
	var report struct {
		Scheduled *struct {
			At     time.Time `json:"at"`
			Action string    `json:"action"`
		} `json:"scheduled_shutdown"`
		Error string `json:"scheduled_shutdown_error"`
	}
	if json.Unmarshal(remote.Remote, &report) != nil {
		return Result{Host: h.Name, Command: op, State: "error", ExitCode: 1,
			Reason: "cannot read the host's schedule: rootfiles there answered without a status document; update it if it predates `rootfiles shutdown`"}
	}
	if report.Error != "" {
		return Result{Host: h.Name, Command: op, State: "error", ExitCode: 1,
			Reason: fmt.Sprintf("%s: the stored schedule cannot be read (%s), so a timer there may still be armed: run 'rootfiles fleet shutdown cancel --host %s --yes' to clear it before scheduling",
				h.Name, report.Error, h.Name)}
	}
	if s := report.Scheduled; s != nil && !req.Replace {
		return Result{Host: h.Name, Command: op, State: "error", ExitCode: 1,
			Reason: fmt.Sprintf("a %s is already scheduled for %s: pass --replace to replace it, or run 'rootfiles fleet shutdown cancel --host %s --yes' first",
				shutdownActionWord(s.Action), s.At.UTC().Format(time.RFC3339), h.Name)}
	}
	return Result{Host: h.Name, Command: op, State: "ok"}
}

// clockSkew measures the host's clock against the controller's. It runs plain
// ssh without sudo: reading the clock needs no privilege, and the read-only
// sudoers rule must stay exactly as it is.
func clockSkew(ctx context.Context, runner SSHRunner, h NamedHost) (time.Duration, Result) {
	before := time.Now()
	res := runner.run(ctx, h, "date", "date -u +%s", nil)
	after := time.Now()
	if res.State != "ok" {
		return 0, res
	}
	remote, ok := parseEpoch(res.Output)
	if !ok {
		return 0, Result{Host: h.Name, Command: "date", State: "error", ExitCode: 1,
			Reason: "cannot measure clock skew: `date -u +%s` did not print an epoch second"}
	}
	// The midpoint halves the error SSH latency adds to one wall clock.
	mid := before.Add(after.Sub(before) / 2)
	return time.Duration(remote-mid.Unix()) * time.Second, res
}

func parseEpoch(output string) (int64, bool) {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if v, err := strconv.ParseInt(strings.TrimSpace(lines[i]), 10, 64); err == nil {
			return v, true
		}
	}
	return 0, false
}

func skewDirection(skew time.Duration) string {
	if skew > 0 {
		return "ahead of"
	}
	return "behind"
}

func shutdownScheduleCommand(req ShutdownRequest) string {
	parts := []string{remoteBinary, "shutdown", "schedule", "--at", ShellQuote(req.At.UTC().Format(time.RFC3339))}
	if req.Action == shutdown.ActionReboot {
		parts = append(parts, "--reboot")
	}
	if req.Message != "" {
		parts = append(parts, "--message", ShellQuote(req.Message))
	}
	if leads := strings.TrimSpace(req.Notices); leads != "" {
		parts = append(parts, "--notice", ShellQuote(leads))
	}
	if req.Replace {
		parts = append(parts, "--replace")
	}
	if req.NotifyNow {
		parts = append(parts, "--notify-now")
	}
	return strings.Join(parts, " ")
}

func shutdownActionWord(action string) string {
	if action == shutdown.ActionReboot {
		return "reboot"
	}
	return "poweroff"
}
