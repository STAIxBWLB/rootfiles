package cli

import (
	"fmt"
	"net"
	"os"
	osexec "os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/fleet"
	"github.com/STAIxBWLB/rootfiles/internal/shutdown"
)

// Fleet-wide shutdown: one absolute UTC instant for every selected host, with
// the existing serial rollout contract intact.

func newFleetShutdownCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "shutdown",
		Short: "Schedule, cancel or inspect fleet poweroffs",
		Long: `schedule gives every selected host the same absolute UTC instant, so the hosts
stop together however long the SSH fan-out took. Hosts are pre-flighted before
anything is written: a host whose clock differs by more than 30s, whose rootfiles
predates the shutdown command, or that already has a schedule stops the run with
no host changed. If a host fails while being scheduled, the hosts this run
already scheduled are cancelled again.

now is the same schedule with a short lead time, and it announces the stop
immediately. Powering hosts back on is out of scope.`,
	}

	c.AddCommand(
		newFleetShutdownScheduleCmd("schedule"),
		newFleetShutdownScheduleCmd("now"),
		newFleetShutdownCancelCmd(),
		newFleetShutdownStatusCmd(),
	)
	return c
}

func newFleetShutdownScheduleCmd(mode string) *cobra.Command {
	use, short := "schedule", "Schedule every selected host for one absolute instant"
	if mode == "now" {
		use, short = "now", "Schedule every selected host within --in and announce it now"
	}
	c := &cobra.Command{Use: use, Short: short, Args: cobra.NoArgs, RunE: func(c *cobra.Command, args []string) error {
		timeout, err := fleetMutationTimeout(c)
		if err != nil {
			return err
		}
		now := time.Now()
		at, err := shutdownInstant(c, mode, now)
		if err != nil {
			return err
		}
		raw, _ := c.Flags().GetString("message")
		message, err := shutdown.SanitizeMessage(raw)
		if err != nil {
			return err
		}
		notices, _ := c.Flags().GetString("notice")
		reboot, _ := c.Flags().GetBool("reboot")
		replace, _ := c.Flags().GetBool("replace")
		includeSelf, _ := c.Flags().GetBool("include-self")
		_, hosts, dry, err := authorizeFleetMutation(c)
		if err != nil {
			return err
		}
		if self := controllerHostsIn(hosts); len(self) > 0 && !includeSelf {
			return withNext(
				fmt.Errorf("the selection includes this controller (%s): a poweroff here would leave the run unfinished",
					strings.Join(self, ", ")),
				"re-run with --include-self when you mean to stop this machine too")
		}
		req := fleet.ShutdownRequest{At: at, Message: message, Notices: notices, Replace: replace, NotifyNow: mode == "now"}
		if reboot {
			req.Action = shutdown.ActionReboot
		}
		results := fleet.ShutdownSchedule(c.Context(), fleetMutationRunner(c, timeout), hosts, req, dry)
		reportShutdownRollback(c, results)
		return finishFleetMutation(c, shutdownRunLogCommand(mode, req), args, results, dry)
	}}
	fleetCommonFlags(c)
	addFleetMutationTimeoutFlag(c)
	addFleetSkipUnreachableFlag(c)
	addOutputFlag(c)
	c.Flags().Bool("reboot", false, "Reboot instead of powering off")
	c.Flags().String("message", "", "Reason shown in every notice (printable, at most 300 characters)")
	c.Flags().String("notice", shutdown.DefaultNotices, "Comma-separated lead times before the instant")
	c.Flags().Bool("replace", false, "Replace a schedule a host already has")
	c.Flags().Bool("include-self", false, "Allow scheduling this controller's own host")
	if mode == "schedule" {
		c.Flags().String("at", "", "Absolute instant: RFC 3339 with an explicit offset (2026-11-21T18:00:00+09:00)")
	} else {
		c.Flags().Duration("in", 2*time.Minute, "Lead time before the poweroff (at least 1 minute)")
	}
	return c
}

// shutdownInstant is the one place the fleet's shared instant comes from; the
// same value goes to every host.
func shutdownInstant(c *cobra.Command, mode string, now time.Time) (time.Time, error) {
	if mode == "now" {
		lead, _ := c.Flags().GetDuration("in")
		switch {
		case lead < shutdown.MinLead:
			return time.Time{}, fmt.Errorf("--in %s: must be at least 1 minute so every host is scheduled before the moment", lead)
		case lead > shutdown.MaxLead:
			return time.Time{}, fmt.Errorf("--in %s: must be at most 30 days ahead", lead)
		}
		// The host checks the lead again against its own clock, which may be
		// MaxClockSkew ahead of this one, and the instant loses its sub-second
		// part on the way: the margin is what keeps --in 1m above the host's
		// one-minute minimum.
		return now.Add(lead + fleet.ShutdownMargin).UTC().Truncate(time.Second), nil
	}
	value, _ := c.Flags().GetString("at")
	if strings.TrimSpace(value) == "" {
		return time.Time{}, withNext(
			fmt.Errorf("%s: --at is required: give the instant as RFC 3339 with an explicit offset, such as 2026-11-21T18:00:00+09:00", c.CommandPath()),
			"rootfiles fleet shutdown now --host HOST --in 2m --yes")
	}
	return shutdown.ParseAt(value, now)
}

// controllerHostsIn names the selected hosts that are this controller, so an
// operator machine never powers off in the middle of its own rollout.
func controllerHostsIn(hosts []fleet.NamedHost) []string {
	local := localHostIdentities()
	var out []string
	for _, h := range hosts {
		for _, candidate := range hostCandidates(h) {
			if candidate == "" {
				continue
			}
			if local[hostIdentity(candidate)] || local[hostShort(candidate)] {
				out = append(out, h.Name)
				break
			}
		}
	}
	return out
}

// hostCandidates is every name a host can be reached by: the inventory name,
// the address, the ssh destination as written, and the hostname the local ssh
// client resolves that destination to, so an alias pointing back here is not a
// way around --include-self.
func hostCandidates(h fleet.NamedHost) []string {
	destination := sshDestinationHost(h.Host.SSH)
	return []string{h.Name, destination, h.Host.Address, sshEffectiveHost(destination)}
}

// sshEffectiveHost asks the local ssh client what a destination resolves to.
// -G prints the configuration ssh would use and opens no connection.
var sshEffectiveHost = func(destination string) string {
	if destination == "" || strings.HasPrefix(destination, "-") {
		return ""
	}
	out, err := osexec.Command("ssh", "-G", destination).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(line, "hostname "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// localHostIdentities is every name or address that means this machine: its
// hostname and short form, the loopback names, and its own interface addresses.
var localHostIdentities = func() map[string]bool {
	names := []string{"localhost", "localhost.localdomain", "127.0.0.1", "::1", "0.0.0.0", "::"}
	if name, err := os.Hostname(); err == nil && name != "" {
		names = append(names, name)
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok {
				names = append(names, ipnet.IP.String())
			}
		}
	}
	out := make(map[string]bool, len(names)*2)
	for _, name := range names {
		out[hostIdentity(name)] = true
		out[hostShort(name)] = true
	}
	return out
}

// hostIdentity normalises a name for comparison: lowercase, no trailing dot and
// no IPv6 brackets.
func hostIdentity(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.TrimSuffix(name, ".")
	return strings.Trim(name, "[]")
}

// hostShort is the identity without its domain part, and the identity itself
// for an address: splitting 10.0.0.5 on its dots would make every host in that
// subnet look the same.
func hostShort(name string) string {
	id := hostIdentity(name)
	if net.ParseIP(id) != nil {
		return id
	}
	return strings.SplitN(id, ".", 2)[0]
}

func sshDestinationHost(destination string) string {
	_, host, found := strings.Cut(destination, "@")
	if !found {
		return destination
	}
	return host
}

// shutdownRunLogCommand records what the run decided, not just its name: the
// shared instant, the action and the operator's reason.
func shutdownRunLogCommand(mode string, req fleet.ShutdownRequest) string {
	command := "shutdown " + mode + " " + req.At.UTC().Format(time.RFC3339) + " " + shutdownActionWord(req.Action)
	if req.Message != "" {
		command += " --message " + fleet.ShellQuote(req.Message)
	}
	return command
}

// reportShutdownRollback prints the exact cancel command for hosts whose
// rollback failed: the schedule may still be armed there.
func reportShutdownRollback(c *cobra.Command, results []fleet.Result) {
	var failed []string
	for _, r := range results {
		if r.State == "cancel-failed" {
			failed = append(failed, r.Host)
		}
	}
	if len(failed) == 0 {
		return
	}
	fmt.Fprintf(c.ErrOrStderr(), "%d host(s) may still have a schedule armed; cancel them with:\n  %s\n",
		len(failed), fleetCancelRerun(c, failed))
}

// fleetCancelRerun is the command an operator can paste to clean up hosts whose
// rollback failed. It carries --yes because a real cancel is confirmed by
// --yes, so the printed command must not be the one that is refused.
func fleetCancelRerun(c *cobra.Command, hosts []string) string {
	parts := []string{c.Root().Name(), "fleet", "shutdown", "cancel"}
	if c.Flags().Changed("inventory") {
		path, _ := c.Flags().GetString("inventory")
		parts = append(parts, "--inventory", fleet.ShellQuote(path))
	}
	for _, h := range hosts {
		parts = append(parts, "--host", fleet.ShellQuote(h))
	}
	return strings.Join(append(parts, "--yes"), " ")
}

func newFleetShutdownCancelCmd() *cobra.Command {
	c := &cobra.Command{Use: "cancel", Short: "Cancel any scheduled shutdown on every selected host", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			timeout, err := fleetMutationTimeout(c)
			if err != nil {
				return err
			}
			_, hosts, dry, err := authorizeFleetMutation(c)
			if err != nil {
				return err
			}
			results := fleet.ShutdownCancel(c.Context(), fleetMutationRunner(c, timeout), hosts, dry)
			return finishFleetMutation(c, "shutdown cancel", args, results, dry)
		}}
	fleetCommonFlags(c)
	addFleetMutationTimeoutFlag(c)
	addFleetSkipUnreachableFlag(c)
	addOutputFlag(c)
	return c
}

func newFleetShutdownStatusCmd() *cobra.Command {
	c := &cobra.Command{Use: "status", Short: "Show each selected host's scheduled shutdown", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			inv, hosts, err := loadFleetSelection(c, false)
			if err != nil {
				return err
			}
			results, err := fleetReadPool(c, inv, hosts, "status")
			if err != nil {
				return err
			}
			format, _ := outputFormat(c)
			if err := writeFleetStatusResults(c.OutOrStdout(), results, format); err != nil {
				return err
			}
			if !allOK(results) {
				return &ExitError{Code: exitDrift}
			}
			return nil
		}}
	fleetCommonFlags(c)
	c.Flags().Int("parallel", 0, "Maximum concurrent SSH processes (inventory default when omitted)")
	addOutputFlag(c)
	return c
}
