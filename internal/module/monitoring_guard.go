package module

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The exporter firewall guard admits exporter traffic only from allow_from.
// The helper keeps the paths, chains and owner marker of the hand-installed
// v1 template, so hosts that already run it are adopted in place.
//
//go:embed assets/exporter-firewall.sh
var exporterGuardHelper []byte

const exporterGuardUnit = "rootfiles-exporter-firewall.service"

// Overridable in tests.
var (
	exporterGuardHelperPath = "/usr/local/sbin/rootfiles-exporter-firewall"
	exporterGuardConfPath   = "/etc/rootfiles/exporter-firewall.conf"
)

// exporterGuardConf renders the helper config. On a hub host the hub's
// Compose bridge is admitted too, so its Prometheus can scrape this host.
func exporterGuardConf(ctx context.Context, rc *RunContext) string {
	cfg := rc.Config.Modules.Monitoring
	s := "# Managed by rootfiles\nBIND_IP=" + cfg.ListenAddress + "\nALLOW_FROM=" + cfg.AllowFrom[0] + "\n"
	if cfg.Hub.Enabled {
		network := filepath.Base(filepath.Dir(monitoringHubCompose)) + "_default"
		if res, err := rc.Runner.Query(ctx, "docker", "network", "inspect", "--format", "{{.Id}}", network); err == nil {
			if id := strings.TrimSpace(res.Stdout); len(id) >= 12 {
				s += "WEB_INTERFACE=br-" + id[:12] + "\n"
			}
		}
	}
	return s
}

// exporterGuardBlock names what keeps the guard from being installed. wait
// is set when the docker module of this run may still provide DOCKER-USER.
func exporterGuardBlock(ctx context.Context, rc *RunContext) (block string, wait bool) {
	if rc.Runner.CommandExists("firewall-cmd") {
		if _, err := rc.Runner.Query(ctx, "firewall-cmd", "--state"); err == nil {
			return "exporter firewall: firewalld is active; the guard supports iptables and UFW only", false
		}
	}
	for _, cmd := range []string{"iptables", "iptables-save", "python3"} {
		if !rc.Runner.CommandExists(cmd) {
			return "exporter firewall: " + cmd + " is missing", false
		}
	}
	if _, err := rc.Runner.Query(ctx, "iptables", "-w", "5", "-S", "DOCKER-USER"); err != nil {
		return "exporter firewall: Docker's DOCKER-USER chain is missing; start Docker first", rc.moduleSucceeding("docker")
	}
	return "", false
}

func checkExporterGuard(ctx context.Context, rc *RunContext) []Change {
	if !rc.Config.Modules.Monitoring.ExporterFirewall {
		return nil
	}
	if block, wait := exporterGuardBlock(ctx, rc); block != "" {
		if wait {
			return []Change{{Description: block + " (waits for the docker module)"}}
		}
		return []Change{{Description: block, State: ChangeBlocked}}
	}
	setup := exporterGuardHelperPath + " setup"
	var changes []Change
	if cur, err := rc.Runner.ReadFile(exporterGuardHelperPath); err != nil || !bytes.Equal(cur, exporterGuardHelper) {
		changes = append(changes, Change{Description: "Install the exporter firewall helper", Command: "write " + exporterGuardHelperPath + " && " + setup})
	}
	if cur, err := rc.Runner.ReadFile(exporterGuardConfPath); err != nil || string(cur) != exporterGuardConf(ctx, rc) {
		changes = append(changes, Change{Description: "Write the exporter firewall config", Command: "write " + exporterGuardConfPath + " && " + setup})
	}
	if len(changes) == 0 {
		if _, err := rc.Runner.Query(ctx, exporterGuardHelperPath, "check"); err != nil {
			changes = append(changes, Change{Description: "Restore exporter firewall rules and units: " + guardError(err), Command: setup})
		}
	}
	return changes
}

func applyExporterGuard(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	out := &ApplyResult{}
	if !rc.Config.Modules.Monitoring.ExporterFirewall {
		return out, nil
	}
	if block, _ := exporterGuardBlock(ctx, rc); block != "" {
		return nil, fmt.Errorf("%s", block)
	}
	write := func(path string, content []byte, mode os.FileMode) (bool, error) {
		if cur, err := rc.Runner.ReadFile(path); err == nil && bytes.Equal(cur, content) {
			return false, nil
		}
		if err := rc.Runner.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false, err
		}
		return true, rc.Runner.WriteFile(path, content, mode)
	}
	helperChanged, err := write(exporterGuardHelperPath, exporterGuardHelper, 0o750)
	if err != nil {
		return nil, err
	}
	confChanged, err := write(exporterGuardConfPath, []byte(exporterGuardConf(ctx, rc)), 0o600)
	if err != nil {
		return nil, err
	}
	if !helperChanged && !confChanged {
		if _, err := rc.Runner.Query(ctx, exporterGuardHelperPath, "check"); err == nil {
			return out, nil
		}
	}
	// setup rebuilds the owned chains behind a DROP guard, so the exporters
	// stay closed while it runs.
	if _, err := rc.Runner.Run(ctx, exporterGuardHelperPath, "setup"); err != nil {
		return nil, fmt.Errorf("exporter firewall setup: %s", guardError(err))
	}
	out.Changed = true
	out.Messages = append(out.Messages, "exporter firewall guard installed and applied")
	// An exporter refused to start while the rules were missing (its
	// ExecStartPre verify failed) starts now that they are back.
	cfg := rc.Config.Modules.Monitoring
	for unit, on := range map[string]bool{nodeExporterUnit: cfg.NodeExporter, dcgmExporterUnit: cfg.DCGMExporter} {
		if on && !serviceActive(ctx, rc, unit) {
			if _, err := rc.Runner.Run(ctx, "systemctl", "start", unit); err != nil {
				out.Warnings = append(out.Warnings, "starting "+unit+": "+firstLine(err.Error()))
			}
		}
	}
	return out, nil
}

// guardError keeps the helper's own message, which it prints to stderr.
func guardError(err error) string {
	msg := err.Error()
	if i := strings.LastIndex(msg, "ERROR: "); i >= 0 {
		return firstLine(msg[i+len("ERROR: "):])
	}
	return firstLine(msg)
}
