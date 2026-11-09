package module

import (
	"context"
	"fmt"
)

func preflightExporterFirewall(ctx context.Context, rc *RunContext) error {
	cfg := rc.Config.Modules.Monitoring
	ports := exporterPorts(cfg)
	if len(ports) == 0 {
		return nil
	}
	state, err := inspectUFWVerbose(ctx, rc)
	if err != nil {
		return fmt.Errorf("checking UFW exporter access rules: %w", err)
	}
	if !state.Known {
		return fmt.Errorf("cannot determine UFW state before restricting exporter access; inspect sudo ufw status verbose and repair the status query before retrying")
	}
	if !state.Active || len(cfg.AllowFrom) == 0 {
		return nil
	}
	if state.DefaultInbound == "allow" {
		return fmt.Errorf("UFW default incoming policy allows traffic; review sudo ufw status verbose and configure a restrictive incoming policy through the host firewall change process before applying allow_from")
	}
	if state.DefaultInbound != "deny" && state.DefaultInbound != "reject" {
		return fmt.Errorf("cannot determine a restrictive UFW incoming policy (got %q); inspect sudo ufw status verbose and configure an explicit deny/reject incoming policy before retrying", state.DefaultInbound)
	}
	for _, rule := range state.Rules {
		if rule.Action != "ALLOW" || rule.Direction != "IN" {
			continue
		}
		for _, port := range ports {
			applies, err := ufwRuleAppliesToPort(ctx, rc, rule.Target, port)
			if err != nil {
				return err
			}
			if !applies {
				continue
			}
			if !ufwSourceInAllowFrom(rule.Source, cfg.AllowFrom) {
				return fmt.Errorf("UFW rule %q ALLOW IN %q conflicts with modules.monitoring.allow_from for exporter port %d; adjust it explicitly before applying", rule.Target, rule.Source, port)
			}
		}
	}
	return nil
}

// monitoringExporterFirewallState runs before exporter package, file or service
// changes so an installed firewall with unknown status cannot be skipped.
func monitoringExporterFirewallState(ctx context.Context, rc *RunContext) (FirewallState, error) {
	if len(exporterPorts(rc.Config.Modules.Monitoring)) == 0 {
		return FirewallState{Known: true}, nil
	}
	state, err := inspectUFW(ctx, rc, false)
	if err != nil {
		return state, fmt.Errorf("checking UFW before configuring exporters: %w", err)
	}
	if !state.Known {
		return state, fmt.Errorf("cannot determine UFW state before configuring exporters; run sudo ufw status and repair the status query")
	}
	return state, nil
}
