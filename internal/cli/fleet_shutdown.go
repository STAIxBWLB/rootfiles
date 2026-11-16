package cli

import (
	"errors"
	"fmt"
	"net"
	"os"
	osexec "os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/fleet"
	"github.com/STAIxBWLB/rootfiles/internal/shutdown"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

// Fleet-wide shutdown: one absolute UTC instant for every selected host, with
// the existing serial rollout contract intact.

func newFleetShutdownCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "shutdown",
		Short:   "Schedule, cancel or inspect fleet poweroffs",
		Example: "  rootfiles fleet shutdown status --all\n  rootfiles fleet shutdown now --host gpu01 --yes",
		Long: `schedule gives every selected host the same absolute UTC instant, so the hosts
stop together however long the SSH fan-out took. Hosts are pre-flighted before
anything is written: a host whose clock differs by more than 30s, whose
rootfiles predates the shutdown command, or that already has a schedule stops
the run with no host changed. If a host fails while being scheduled, the hosts
this run already scheduled are cancelled again.

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
	example := "  rootfiles fleet shutdown schedule --all --at 2026-11-21T09:00:00Z --yes"
	if mode == "now" {
		use, short = "now", "Schedule every selected host within --in and announce it now"
		example = "  rootfiles fleet shutdown now --host gpu01 --in 90s --yes"
	}
	c := &cobra.Command{Use: use, Short: short, Args: cobra.NoArgs, Example: example, RunE: func(c *cobra.Command, args []string) error {
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
		if self, err := controllerHostsIn(hosts); err != nil {
			return withNext(
				fmt.Errorf("cannot tell whether the selection includes this controller: %w", err),
				"make sure ssh is installed and this machine's hostname and addresses can be read, then re-run; selecting hosts one at a time with --host also narrows the check")
		} else if len(self) > 0 && !includeSelf {
			return withNext(
				fmt.Errorf("the selection includes this controller (%s): a poweroff here would leave the run unfinished",
					strings.Join(self, ", ")),
				"re-run with --include-self when you mean to stop this machine too")
		}
		// The notice leads are validated here as well as on the host, so a bad
		// list is one refusal instead of one failed host per host.
		if _, _, err := shutdown.ParseNotices(notices, at, time.Now()); err != nil {
			return err
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
		case lead > shutdown.MaxLead-fleet.ShutdownMargin:
			// The margin is added to the instant, so `--in 30d` would hand the
			// host a lead past its own maximum and be refused there.
			return time.Time{}, fmt.Errorf("--in %s: must be at most %s, which is 30 days minus the %s added for clock skew and delivery",
				lead, shutdown.MaxLead-fleet.ShutdownMargin, fleet.ShutdownMargin)
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
// operator machine never powers off in the middle of its own rollout. An error
// means the selection could not be judged, which is a refusal rather than a
// pass: a host that was not proven to be elsewhere may be this machine.
func controllerHostsIn(hosts []fleet.NamedHost) ([]string, error) {
	local, err := localHostIdentities()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, h := range hosts {
		mine, err := isThisController(h, local)
		if err != nil {
			return nil, err
		}
		if mine {
			out = append(out, h.Name)
		}
	}
	return out, nil
}

// isThisController decides one host. A name or address that is this machine is
// this machine on any port: its hostname and its own non-loopback interface
// addresses are not shared with a separate host. A loopback name is this
// controller only on its own ssh port, because a forwarded port on loopback is a
// different machine - the same distinction ssh itself makes when it keys
// known_hosts by [127.0.0.1]:2222 rather than by 127.0.0.1. A name that resolves
// to one of this machine's addresses is this machine too, which is how a DNS
// alias or a CNAME to the controller is caught.
func isThisController(h fleet.NamedHost, local localIdentities) (bool, error) {
	destination := sshDestinationHost(h.Host.SSH)
	candidates := []string{h.Name, destination, h.Host.Address}
	// This machine's own hostname and its interface addresses are its identity on
	// any port: no other host can answer to them.
	for _, candidate := range candidates {
		if local.isDefinite(candidate) {
			return true, nil
		}
	}
	host, port, err := sshEffectiveTarget(destination)
	if err != nil {
		return false, fmt.Errorf("%s: cannot read what ssh would connect to: %w", h.Name, err)
	}
	// A loopback name reaches this controller only on its own ssh port: a
	// forwarded port on loopback is a different machine, the distinction ssh
	// itself draws when it keys known_hosts by [127.0.0.1]:2222.
	forwarded := port != "" && port != local.sshPort
	if !forwarded {
		for _, candidate := range candidates {
			if local.isLoopback(candidate) {
				return true, nil
			}
		}
	}
	if host == "" {
		return false, fmt.Errorf("%s: ssh named no host to connect to", h.Name)
	}
	if local.isDefinite(host) || (!forwarded && local.isLoopback(host)) {
		return true, nil
	}
	// A name that does not resolve here is not this machine: nothing local
	// answers to it either, and ssh would fail on it before any timer was set.
	addrs, err := resolveHost(host)
	if err != nil {
		return false, nil
	}
	for _, addr := range addrs {
		if local.isLocalAddress(addr) || (!forwarded && local.isLoopback(addr)) {
			return true, nil
		}
	}
	return false, nil
}

// sshEffectiveTarget asks the local ssh client what a destination resolves to.
// -G prints the configuration ssh would use and opens no connection.
var sshEffectiveTarget = func(destination string) (host, port string, err error) {
	if destination == "" || strings.HasPrefix(destination, "-") {
		return "", "", nil
	}
	out, err := osexec.Command("ssh", "-G", destination).Output()
	if err != nil {
		return "", "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(line, "hostname "); ok {
			host = strings.TrimSpace(rest)
		}
		if rest, ok := strings.CutPrefix(line, "port "); ok {
			port = strings.TrimSpace(rest)
		}
	}
	return host, port, nil
}

// resolveHost is the DNS lookup the self-check uses, overridable in tests.
var resolveHost = func(host string) ([]string, error) { return net.LookupHost(host) }

// localIdentities is this controller's own identity, split by how strongly a
// match proves it: a name in names is this machine whatever port is used, while
// a name in loopback only reaches this machine on its own ssh port.
type localIdentities struct {
	names    map[string]bool
	addrs    map[string]bool
	loopback map[string]bool
	sshPort  string
}

func (l localIdentities) isDefinite(name string) bool {
	for _, id := range identityForms(name) {
		if l.names[id] || l.addrs[id] {
			return true
		}
	}
	return false
}

func (l localIdentities) isLoopback(name string) bool {
	for _, id := range identityForms(name) {
		if l.loopback[id] {
			return true
		}
	}
	return false
}

func (l localIdentities) isLocalAddress(addr string) bool {
	return l.addrs[hostIdentity(addr)]
}

// localHostIdentities is every name or address that means this machine: its
// hostname and short form, its own interface addresses, and the loopback names.
// Both reads must succeed: without them nothing can be proven to be elsewhere.
var localHostIdentities = func() (localIdentities, error) {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return localIdentities{}, fmt.Errorf("cannot read this machine's hostname: %w", orUnknown(err))
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return localIdentities{}, fmt.Errorf("cannot read this machine's addresses: %w", err)
	}
	return identitiesFrom(name, addrs), nil
}

// identitiesFrom sorts this machine's hostname and interface addresses into the
// ones that prove it on any port and the loopback ones that prove it only on its
// own ssh port. A loopback address is an interface address of this machine too,
// but on any other port the same address is a forward to a different host, so
// taking it as definite would refuse every loopback-reached fleet.
func identitiesFrom(name string, addrs []net.Addr) localIdentities {
	names := identitySet([]string{name})
	loopback := identitySet([]string{"localhost", "localhost.localdomain", "127.0.0.1", "::1", "0.0.0.0", "::"})
	ipSet := map[string]bool{}
	for _, addr := range addrs {
		ipnet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := hostIdentity(ipnet.IP.String())
		if ipnet.IP.IsLoopback() {
			loopback[ip] = true
			continue
		}
		ipSet[ip] = true
		names[ip] = true
	}
	return localIdentities{
		names:    names,
		addrs:    ipSet,
		loopback: loopback,
		sshPort:  controllerSSHPort,
	}
}

func orUnknown(err error) error {
	if err != nil {
		return err
	}
	return errors.New("the hostname is empty")
}

// controllerSSHPort is the port this controller's own sshd answers on, so a
// loopback destination on any other port is treated as a forwarded host rather
// than as this machine. An inventory that reaches the controller's own sshd on a
// non-standard port must name it by hostname or address instead.
const controllerSSHPort = "22"

func identitySet(names []string) map[string]bool {
	out := make(map[string]bool, len(names)*2)
	for _, name := range names {
		for _, id := range identityForms(name) {
			out[id] = true
		}
	}
	return out
}

// identityForms is the two spellings a name is compared under: its normalised
// identity and, for a hostname, the short form.
func identityForms(name string) []string {
	if name == "" {
		return nil
	}
	return []string{hostIdentity(name), hostShort(name)}
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
	ui.WriteWarning(c.ErrOrStderr(),
		fmt.Sprintf("%d host(s) may still have a schedule armed", len(failed)),
		fleetCancelRerun(c, failed))
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
		Example: `  rootfiles fleet shutdown cancel --all --yes`,
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
		Example: `  rootfiles fleet shutdown status --all`,
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
