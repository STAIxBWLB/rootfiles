package module

import (
	"context"
	"fmt"
	"sort"
	"strconv"
)

type NetworkModule struct{}

func NewNetworkModule() *NetworkModule { return &NetworkModule{} }
func (m *NetworkModule) Name() string  { return "network" }

// requiredPorts returns the configured allowed ports plus the SSH port(s),
// which are always admitted so enabling the firewall cannot lock the
// operator out.
func (m *NetworkModule) requiredPorts(ctx context.Context, rc *RunContext) []int {
	set := map[int]bool{}
	for _, p := range rc.Config.Modules.Network.AllowedPorts {
		set[p] = true
	}
	for _, p := range sshPorts(ctx, rc) {
		set[p] = true
	}
	if sp := rc.Config.Modules.Tenant.SSHPiper; sp.Enabled {
		set[sp.ListenPort()] = true // tenant SSH through sshpiper
	}
	ports := make([]int, 0, len(set))
	for p := range set {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	return ports
}

func (m *NetworkModule) Check(ctx context.Context, rc *RunContext) (*CheckResult, error) {
	var changes []Change
	cfg := rc.Config.Modules.Network

	if cfg.UFW {
		st, err := networkFirewallState(ctx, rc)
		if err != nil {
			return nil, err
		}
		installed := st.Installed
		if !installed {
			changes = append(changes, Change{
				Description: "Install UFW firewall",
				Command:     "apt-get install ufw",
			})
		}
		for _, port := range m.requiredPorts(ctx, rc) {
			if !st.Allowed[port] {
				changes = append(changes, Change{
					Description: fmt.Sprintf("Allow port %d", port),
					Command:     fmt.Sprintf("ufw allow %d/tcp", port),
				})
			}
		}
		if !st.UFWActive {
			changes = append(changes, Change{
				Description: "Enable UFW firewall",
				Command:     "ufw --force enable",
			})
		}
	}

	return &CheckResult{
		Satisfied: len(changes) == 0,
		Changes:   changes,
	}, nil
}

func (m *NetworkModule) Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	cfg := rc.Config.Modules.Network
	var messages []string
	changed := false

	if !cfg.UFW {
		return &ApplyResult{Changed: false}, nil
	}

	st, err := networkFirewallState(ctx, rc)
	if err != nil {
		return nil, err
	}
	// Ensure UFW is installed only after existing firewall state is known.
	if !st.Installed {
		if err := rc.APT.Update(ctx); err != nil {
			return nil, fmt.Errorf("apt update: %w", err)
		}
		if err := rc.APT.Install(ctx, []string{"ufw"}); err != nil {
			return nil, fmt.Errorf("installing ufw: %w", err)
		}
		messages = append(messages, "ufw installed")
		changed = true
		st, err = networkFirewallState(ctx, rc)
		if err != nil {
			return nil, err
		}
		if !rc.DryRun && !st.Installed {
			return nil, fmt.Errorf("UFW is still unavailable after installation; verify the ufw package and PATH before applying firewall changes")
		}
	}

	// Rules first, then enable: the SSH port must be admitted before the
	// default-deny policy takes effect.
	var added []int
	for _, port := range m.requiredPorts(ctx, rc) {
		if st.Allowed[port] {
			continue
		}
		if _, err := rc.Runner.Run(ctx, "ufw", "allow", strconv.Itoa(port)+"/tcp"); err != nil {
			return nil, fmt.Errorf("allowing port %d: %w", port, err)
		}
		added = append(added, port)
	}
	if len(added) > 0 {
		messages = append(messages, fmt.Sprintf("ports allowed: %v", added))
		changed = true
	}

	if !st.UFWActive {
		if _, err := rc.Runner.Run(ctx, "ufw", "--force", "enable"); err != nil {
			return nil, fmt.Errorf("enabling ufw: %w", err)
		}
		messages = append(messages, "UFW enabled")
		changed = true
	}

	return &ApplyResult{Changed: changed, Messages: messages}, nil
}
