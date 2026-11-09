package module

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// firewallInspectionError distinguishes unknown external firewall state from
// invalid configuration and known policy conflicts in read-only reports.
type firewallInspectionError struct{ cause error }

func (e *firewallInspectionError) Error() string { return e.cause.Error() }
func (e *firewallInspectionError) Unwrap() error { return e.cause }
func firewallInspectionErrorf(format string, args ...any) error {
	return &firewallInspectionError{cause: fmt.Errorf(format, args...)}
}
func isFirewallInspectionError(err error) bool {
	var inspection *firewallInspectionError
	return errors.As(err, &inspection)
}
func blockedFirewallCheck(err error) *CheckResult {
	return &CheckResult{Satisfied: false, Changes: []Change{{Description: fmt.Sprintf("Firewall verification (blocked: %v)", err), State: ChangeBlocked}}}
}

// Rule is one parsed UFW row. Consumers apply their own reachability/source
// policy to the same target, direction and address-family representation.
type Rule struct {
	Target, Source, Action, Direction string
	IPv6                              bool
}

// FirewallState keeps missing/inactive firewalls distinct from failed probes.
// Allowed indexes numeric inbound TCP UFW allowances; Rules preserves sources.
type FirewallState struct {
	Known, Active        bool
	Allowed              map[int]bool
	Rules                []Rule
	DefaultInbound       string
	Installed, UFWActive bool
	firewalldActive      bool
}

// parseUFWStatus is the single parser for normal and verbose UFW output.
func parseUFWStatus(out string) FirewallState {
	st := FirewallState{Allowed: map[int]bool{}, Installed: true}
	statusSeen, conflicting := false, false
	for _, line := range strings.Split(out, "\n") {
		line, _, _ = strings.Cut(line, "#")
		trim := strings.TrimSpace(line)
		lower := strings.ToLower(trim)
		if strings.HasPrefix(lower, "status:") {
			status := strings.TrimSpace(strings.TrimPrefix(lower, "status:"))
			known := status == "active" || status == "inactive"
			active := status == "active"
			if statusSeen && (!known || !st.Known || st.Active != active) {
				conflicting = true
			}
			st.Known, st.Active = known, active
			statusSeen = true
			continue
		}
		if i := strings.Index(lower, "(incoming)"); i >= 0 && strings.HasPrefix(lower, "default:") {
			fields := strings.Fields(trim[:i])
			if len(fields) > 0 {
				st.DefaultInbound = strings.ToLower(strings.Trim(fields[len(fields)-1], ":"))
			}
			continue
		}
		fields := strings.Fields(trim)
		at := -1
		for i, f := range fields {
			if f == "ALLOW" || f == "DENY" || f == "REJECT" || f == "LIMIT" {
				at = i
				break
			}
		}
		if at < 1 || at+1 >= len(fields) {
			continue
		}
		target := strings.Join(fields[:at], " ")
		ipv6 := strings.HasSuffix(target, " (v6)")
		target = strings.TrimSuffix(target, " (v6)")
		sourceAt := at + 1
		direction := "IN"
		if fields[sourceAt] == "IN" || fields[sourceAt] == "OUT" || fields[sourceAt] == "FWD" {
			direction = fields[sourceAt]
			sourceAt++
		}
		if sourceAt >= len(fields) {
			continue
		}
		rule := Rule{Target: target, Source: strings.Join(fields[sourceAt:], " "), Action: fields[at], Direction: direction, IPv6: ipv6}
		st.Rules = append(st.Rules, rule)
		if rule.Action != "ALLOW" || direction != "IN" {
			continue
		}
		spec := target
		if name, proto, ok := strings.Cut(spec, "/"); ok {
			if proto != "tcp" {
				continue
			}
			spec = name
		}
		for _, value := range strings.Split(spec, ",") {
			if port, err := strconv.Atoi(value); err == nil && port > 0 && port <= 65535 {
				st.Allowed[port] = true
			}
		}
	}
	st.Known = st.Known && statusSeen && !conflicting
	st.UFWActive = st.Active
	return st
}

func inspectUFW(ctx context.Context, rc *RunContext, verbose bool) (FirewallState, error) {
	if !rc.Runner.CommandExists("ufw") {
		return FirewallState{Known: true, Allowed: map[int]bool{}}, nil
	}
	args := []string{"status"}
	if verbose {
		args = append(args, "verbose")
	}
	res, err := rc.Runner.Query(ctx, "ufw", args...)
	if err != nil {
		return FirewallState{Installed: true, Allowed: map[int]bool{}}, firewallInspectionErrorf("cannot inspect installed UFW status: %w; run sudo ufw status and repair the firewall query before applying", err)
	}
	if res == nil {
		return FirewallState{Installed: true, Allowed: map[int]bool{}}, firewallInspectionErrorf("installed UFW returned no status; run sudo ufw status and repair the firewall query before applying")
	}
	st := parseUFWStatus(res.Stdout)
	if !st.Known {
		return st, firewallInspectionErrorf("installed UFW returned an unrecognized status; run sudo ufw status and repair its status output before applying")
	}
	return st, nil
}

// inspectFirewall checks installed UFW and firewalld state without mutation.
// Per-port firewalld policy is verified separately against every active zone.
func inspectFirewall(ctx context.Context, rc *RunContext) (FirewallState, error) {
	st, err := inspectUFW(ctx, rc, false)
	if err != nil {
		return st, err
	}
	if !rc.Runner.CommandExists("firewall-cmd") {
		return st, nil
	}
	result, err := rc.Runner.Query(ctx, "firewall-cmd", "--state")
	if result != nil && strings.TrimSpace(result.Stdout+"\n"+result.Stderr) == "not running" && err != nil && result.ExitCode == 252 {
		return st, nil
	}
	if err != nil {
		st.Known = false
		return st, firewallInspectionErrorf("cannot determine installed firewalld state: %w; run sudo firewall-cmd --state and repair the query before applying", err)
	}
	if result == nil || strings.TrimSpace(result.Stdout) != "running" {
		st.Known = false
		return st, firewallInspectionErrorf("installed firewalld returned an unrecognized state; run sudo firewall-cmd --state and verify its policy before applying")
	}
	st.firewalldActive = true
	st.Active = true
	return st, nil
}

// queryUFW retains the read-only consumer API: present is true even if the
// installed firewall is unknown, and callers must inspect Known before Active.
func queryUFW(ctx context.Context, rc *RunContext) (FirewallState, bool) {
	st, _ := inspectUFW(ctx, rc, false)
	return st, st.Installed
}

func inspectUFWVerbose(ctx context.Context, rc *RunContext) (FirewallState, error) {
	return inspectUFW(ctx, rc, true)
}

func (st FirewallState) hasDenyOrRejectRule() bool {
	for _, rule := range st.Rules {
		if rule.Action == "DENY" || rule.Action == "REJECT" {
			return true
		}
	}
	return false
}

func (st FirewallState) allowsUnrestrictedTCPPort(port int) bool {
	ipv4, ipv6 := false, false
	for _, rule := range st.Rules {
		if rule.Action != "ALLOW" || rule.Direction != "IN" || rule.Target != strconv.Itoa(port)+"/tcp" {
			continue
		}
		if !rule.IPv6 && rule.Source == "Anywhere" {
			ipv4 = true
		}
		if rule.IPv6 && rule.Source == "Anywhere (v6)" {
			ipv6 = true
		}
	}
	return ipv4 && ipv6
}

func rockyFirewallAllowsSSH(ctx context.Context, rc *RunContext, port int) (bool, error) {
	st, err := inspectFirewall(ctx, rc)
	if err != nil {
		return false, err
	}
	if st.UFWActive && (st.hasDenyOrRejectRule() || !st.allowsUnrestrictedTCPPort(port)) {
		return false, nil
	}
	allowed, err := firewalldAllowsSSH(ctx, rc, st, port)
	if err != nil {
		return false, fmt.Errorf("%w; inspect the installed firewall policy for SSH port %d or restore ssh.port before retrying", err, port)
	}
	return allowed, nil
}

func firewalldAllowsSSH(ctx context.Context, rc *RunContext, st FirewallState, port int) (bool, error) {
	if !st.firewalldActive {
		return true, nil
	}
	zones, err := rc.Runner.Query(ctx, "firewall-cmd", "--get-active-zones")
	if err != nil {
		return false, firewallInspectionErrorf("cannot inspect active firewalld zones before changing SSH port: %w", err)
	}
	if zones == nil {
		return false, firewallInspectionErrorf("firewalld returned no active-zone status before changing SSH port")
	}
	activeZones := parseFirewalldZones(zones.Stdout)
	if len(activeZones) == 0 {
		defaultZone, err := rc.Runner.Query(ctx, "firewall-cmd", "--get-default-zone")
		if err != nil {
			return false, firewallInspectionErrorf("cannot inspect firewalld default zone before changing SSH port: %w", err)
		}
		if defaultZone == nil {
			return false, firewallInspectionErrorf("firewalld returned no default-zone status before changing SSH port")
		}
		zone := strings.TrimSpace(defaultZone.Stdout)
		if zone == "" {
			return false, firewallInspectionErrorf("firewalld returned an empty default zone before changing SSH port")
		}
		activeZones = []string{zone}
	}
	allZonesAllow := true
	for _, zone := range activeZones {
		richRules, err := rc.Runner.Query(ctx, "firewall-cmd", "--zone", zone, "--list-rich-rules")
		if err != nil {
			return false, firewallInspectionErrorf("cannot inspect rich firewalld rules in zone %s before changing SSH port: %w", zone, err)
		}
		if richRules == nil {
			return false, firewallInspectionErrorf("firewalld returned no rich-rule status for zone %s before changing SSH port", zone)
		}
		if strings.TrimSpace(richRules.Stdout) != "" {
			return false, nil
		}
		allowed, err := queryFirewalldBoolean(ctx, rc, "--zone", zone, "--query-port", strconv.Itoa(port)+"/tcp")
		if err != nil {
			return false, firewallInspectionErrorf("cannot verify firewalld allowance for SSH port %d in zone %s: %w", port, zone, err)
		}
		if !allowed && port == 22 {
			serviceAllowed, serviceErr := queryFirewalldBoolean(ctx, rc, "--zone", zone, "--query-service", "ssh")
			if serviceErr != nil {
				return false, firewallInspectionErrorf("cannot verify firewalld SSH service in zone %s: %w", zone, serviceErr)
			}
			allowed = serviceAllowed
		}
		if !allowed {
			allZonesAllow = false
		}
	}
	return allZonesAllow, nil
}

func ufwRuleAppliesToPort(ctx context.Context, rc *RunContext, target string, port int) (bool, error) {
	value := strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(target, "/tcp"), "/udp"))
	if value == "anywhere" || value == "any" {
		return true, nil
	}
	matched, known := numericUFWPortTarget(value, port)
	if known {
		return matched, nil
	}
	// UFW application profiles can map a name to a port or range. Resolve them
	// read-only so a named broad rule cannot bypass the port preflight.
	res, err := rc.Runner.Query(ctx, "ufw", "app", "info", target)
	if err != nil {
		return false, firewallInspectionErrorf("cannot resolve UFW application rule %q while checking exporter port %d: %w", target, port, err)
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		trim := strings.TrimSpace(line)
		if i := strings.Index(trim, "/"); i > 0 {
			if applies, known := numericUFWPortTarget(strings.ToLower(trim[:i]), port); known && applies {
				return true, nil
			}
		}
	}
	return false, nil
}

func numericUFWPortTarget(target string, port int) (applies, known bool) {
	parts := strings.Split(target, ",")
	for _, part := range parts {
		bounds := strings.SplitN(part, ":", 2)
		start, err := strconv.Atoi(bounds[0])
		if err != nil {
			return false, false
		}
		end := start
		if len(bounds) == 2 {
			end, err = strconv.Atoi(bounds[1])
			if err != nil {
				return false, false
			}
		}
		if port >= start && port <= end {
			applies = true
		}
	}
	return applies, true
}

func ufwSourceInAllowFrom(source string, allowFrom []string) bool {
	source = strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(source), "(v6)"), "/32")
	source = strings.TrimSpace(strings.TrimSuffix(source, "/128"))
	if addr, err := netip.ParseAddr(source); err == nil {
		source = addr.String()
	}
	for _, allowed := range allowFrom {
		prefix, err := netip.ParsePrefix(allowed)
		if err != nil {
			continue
		}
		if prefix.Bits() == prefix.Addr().BitLen() {
			candidate := prefix.Addr().String()
			if source == candidate || source == candidate+"/"+strconv.Itoa(prefix.Bits()) {
				return true
			}
			continue
		}
		if source == prefix.String() {
			return true
		}
	}
	return false
}

func ufwRestrictedPortRuleExists(ctx context.Context, rc *RunContext, port int) bool {
	cfg := rc.Config.Modules.Monitoring
	if len(cfg.AllowFrom) == 0 {
		return false
	}
	state, err := inspectUFWVerbose(ctx, rc)
	if err != nil || !state.Known || !state.Active || (state.DefaultInbound != "deny" && state.DefaultInbound != "reject") {
		return false
	}
	found := false
	for _, rule := range state.Rules {
		if rule.Action != "ALLOW" || rule.Direction != "IN" {
			continue
		}
		applies, err := ufwRuleAppliesToPort(ctx, rc, rule.Target, port)
		if err != nil {
			return false
		}
		if !applies {
			continue
		}
		if !ufwSourceInAllowFrom(rule.Source, cfg.AllowFrom) {
			return false
		}
		found = true
	}
	return found
}

func ufwSourceRuleExists(ctx context.Context, rc *RunContext, source string, port int) bool {
	st, err := inspectUFW(ctx, rc, false)
	if err != nil || !st.Known || !st.Active {
		return false
	}
	for _, rule := range st.Rules {
		if rule.Action != "ALLOW" || rule.Direction != "IN" || (rule.Target != strconv.Itoa(port) && rule.Target != strconv.Itoa(port)+"/tcp") {
			continue
		}
		if ufwSourceInAllowFrom(rule.Source, []string{source}) {
			return true
		}
	}
	return false
}

// sshFirewallPlan is shared by Ubuntu Check and Apply. UFW can add a missing
// unrestricted rule; other installed firewall policies must already allow it.
func sshFirewallPlan(ctx context.Context, rc *RunContext, port int) (bool, error) {
	st, err := inspectFirewall(ctx, rc)
	if err != nil {
		return false, fmt.Errorf("refusing SSH port %d change: %w; verify the port allowance or restore ssh.port before retrying", port, err)
	}
	allowed, err := firewalldAllowsSSH(ctx, rc, st, port)
	if err != nil {
		return false, fmt.Errorf("refusing SSH port %d change: %w; inspect the installed firewall policy or restore ssh.port before retrying", port, err)
	}
	if !allowed {
		return false, fmt.Errorf("firewall policy does not allow SSH port %d; open it through the host firewall change process or restore ssh.port before retrying", port)
	}
	if st.UFWActive && st.hasDenyOrRejectRule() {
		return false, fmt.Errorf("UFW has a DENY or REJECT rule, so SSH port %d reachability cannot be verified; review sudo ufw status and adjust the rules or restore ssh.port before retrying", port)
	}
	if st.UFWActive && !st.allowsUnrestrictedTCPPort(port) {
		for _, rule := range st.Rules {
			if rule.Direction != "IN" || strings.HasSuffix(rule.Target, "/udp") {
				continue
			}
			applies, err := ufwRuleAppliesToPort(ctx, rc, rule.Target, port)
			if err != nil {
				return false, fmt.Errorf("cannot verify UFW policy for SSH port %d: %w; inspect sudo ufw status and its application profiles, or restore ssh.port before retrying", port, err)
			}
			if applies {
				return false, fmt.Errorf("existing UFW rules for SSH port %d do not verify unrestricted TCP access in both address families; review sudo ufw status and adjust the rule explicitly or restore ssh.port before retrying", port)
			}
		}
		return true, nil
	}
	return false, nil
}

func networkFirewallState(ctx context.Context, rc *RunContext) (FirewallState, error) {
	st, err := inspectFirewall(ctx, rc)
	if err != nil {
		return st, fmt.Errorf("refusing UFW changes while firewall state is unknown: %w", err)
	}
	if st.firewalldActive {
		return st, fmt.Errorf("firewalld is active; choose one firewall manager and disable modules.network.ufw or stop firewalld through the host firewall change process before applying")
	}
	return st, nil
}
