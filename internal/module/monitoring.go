package module

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// MonitoringModule manages host exporters and the optional monitoring hub.
type MonitoringModule struct{}

func NewMonitoringModule() *MonitoringModule { return &MonitoringModule{} }
func (m *MonitoringModule) Name() string     { return "monitoring" }

// NodeExporterTextfileDir is where Ubuntu's prometheus-node-exporter reads
// *.prom files; `rootfiles schedule` publishes rootfiles_* metrics there.
const NodeExporterTextfileDir = "/var/lib/prometheus/node-exporter"

var nodeExporterTextfileDir = NodeExporterTextfileDir

const (
	nodeExporterPkg  = "prometheus-node-exporter"
	nodeExporterUnit = "prometheus-node-exporter"
)

func (m *MonitoringModule) Check(ctx context.Context, rc *RunContext) (*CheckResult, error) {
	if err := monitoringPreflight(ctx, rc); err != nil {
		return nil, err
	}
	changes, err := checkMonitoringExporters(ctx, rc)
	if err != nil {
		return nil, err
	}
	hubChanges, err := checkMonitoringHub(ctx, rc)
	if err != nil {
		return nil, err
	}
	changes = append(changes, hubChanges...)
	changes = append(changes, checkExporterGuard(ctx, rc)...)
	return &CheckResult{Satisfied: len(changes) == 0, Changes: changes}, nil
}

func (m *MonitoringModule) Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	// This read-only preflight is deliberately repeated here. Callers may invoke
	// Apply directly, without first calling Check, and no earlier component may
	// mutate the host before every requested endpoint has passed validation.
	if err := monitoringPreflight(ctx, rc); err != nil {
		return nil, err
	}
	if _, err := checkMonitoringHub(ctx, rc); err != nil {
		return nil, err
	}
	// The guard goes first, so no exporter starts before its rules exist.
	guard, err := applyExporterGuard(ctx, rc)
	if err != nil {
		return nil, err
	}
	// exporters carries a result next to a blocked-DCGM error; the hub is
	// still applied before that error fails the module.
	exporters, exportErr := applyMonitoringExporters(ctx, rc)
	if exporters == nil {
		return nil, exportErr
	}
	hub, err := applyMonitoringHub(ctx, rc)
	if err != nil {
		return nil, err
	}
	out := mergeApplyResults(mergeApplyResults(guard, exporters), hub)
	if rc.Config.Modules.Monitoring.Hub.Enabled {
		// Again after the hub, so a first apply admits the bridge it created.
		bridge, err := applyExporterGuard(ctx, rc)
		if err != nil {
			return nil, err
		}
		out = mergeApplyResults(out, bridge)
	}
	return out, exportErr
}

func mergeApplyResults(a, b *ApplyResult) *ApplyResult {
	if a == nil {
		a = &ApplyResult{}
	}
	if b == nil {
		return a
	}
	a.Changed = a.Changed || b.Changed
	a.Messages = append(a.Messages, b.Messages...)
	a.Warnings = append(a.Warnings, b.Warnings...)
	return a
}

func monitoringPreflight(ctx context.Context, rc *RunContext) error {
	cfg := rc.Config.Modules.Monitoring
	ports := map[int]string{}
	addPort := func(port int, service string) error {
		if old, exists := ports[port]; exists {
			return fmt.Errorf("monitoring port %d is configured for both %s and %s", port, old, service)
		}
		ports[port] = service
		return nil
	}
	if cfg.NodeExporter {
		if err := addPort(cfg.NodePort(), "node exporter"); err != nil {
			return err
		}
	}
	if cfg.DCGMExporter {
		if err := addPort(cfg.DCGMPort(), "DCGM exporter"); err != nil {
			return err
		}
	}
	if cfg.Hub.Enabled {
		h := cfg.Hub.WithDefaults()
		for port, name := range map[int]string{
			h.GrafanaPort: "Grafana", h.PrometheusPort: "Prometheus", h.AlertmanagerPort: "Alertmanager",
		} {
			if err := addPort(port, name); err != nil {
				return err
			}
		}
	}
	if len(ports) == 0 {
		return nil
	}
	if cfg.ListenAddress != "" {
		if ip := net.ParseIP(cfg.ListenAddress); ip == nil {
			return fmt.Errorf("invalid monitoring listen_address %q", cfg.ListenAddress)
		}
	}
	if err := preflightExporterFirewall(ctx, rc); err != nil {
		return err
	}
	res, err := rc.Runner.Query(ctx, "ss", "-H", "-ltnp")
	if err != nil {
		return fmt.Errorf("checking monitoring port conflicts with ss: %w", err)
	}
	listeners := parseTCPListeners(res.Stdout)
	for port, service := range ports {
		for _, listener := range listeners[port] {
			if monitoringListenerManaged(ctx, rc, port, listener, service) {
				continue
			}
			holder := listener.Process
			if holder == "" {
				holder = "unknown process"
			}
			return fmt.Errorf("monitoring %s port %d is already held by %s (%s)", service, port, holder, listener.Address)
		}
	}
	return nil
}

type tcpListener struct {
	Address, Process string
	PID              int
}

func parseTCPListeners(output string) map[int][]tcpListener {
	out := map[int][]tcpListener{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		addr := fields[3]
		_, portText, err := net.SplitHostPort(addr)
		if err != nil {
			continue
		}
		port, err := strconv.Atoi(portText)
		if err != nil {
			continue
		}
		proc := ""
		pid := 0
		if i := strings.Index(line, "users:(("); i >= 0 {
			rest := line[i+8:]
			if j := strings.IndexAny(rest, ",)"); j >= 0 {
				proc = strings.Trim(rest[:j], "\"")
			}
			if p := strings.Index(rest, "pid="); p >= 0 {
				pidText := rest[p+4:]
				if j := strings.IndexByte(pidText, ','); j >= 0 {
					pid, _ = strconv.Atoi(pidText[:j])
				}
			}
		}
		out[port] = append(out[port], tcpListener{Address: addr, Process: proc, PID: pid})
	}
	return out
}

func monitoringListenerManaged(ctx context.Context, rc *RunContext, port int, l tcpListener, service string) bool {
	if service == "node exporter" && rc.Config.Modules.Monitoring.NodeExporter {
		if res, err := rc.Runner.Query(ctx, "systemctl", "is-active", nodeExporterUnit); err == nil && strings.TrimSpace(res.Stdout) == "active" {
			return l.PID > 0 && serviceMainPID(ctx, rc, nodeExporterUnit) == l.PID
		}
	}
	if service == "DCGM exporter" && rc.Config.Modules.Monitoring.DCGMExporter {
		if res, err := rc.Runner.Query(ctx, "systemctl", "is-active", dcgmExporterUnit); err == nil && strings.TrimSpace(res.Stdout) == "active" {
			return strings.Contains(l.Process, "docker-proxy") && dcgmContainerOwnsPort(ctx, rc, port)
		}
	}
	if service == "Prometheus" || service == "Grafana" || service == "Alertmanager" {
		return monitoringHubOwnsListener(ctx, rc, service, port, l)
	}
	return false
}

func serviceMainPID(ctx context.Context, rc *RunContext, unit string) int {
	res, err := rc.Runner.Query(ctx, "systemctl", "show", "-p", "MainPID", "--value", unit)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(res.Stdout))
	return pid
}

func dcgmContainerOwnsPort(ctx context.Context, rc *RunContext, port int) bool {
	res, err := rc.Runner.Query(ctx, "docker", "inspect", "--format", "{{json .NetworkSettings.Ports}}", "rootfiles-dcgm-exporter")
	if err != nil || res == nil {
		return false
	}
	var bindings map[string][]struct {
		HostIP   string `json:"HostIp"`
		HostPort string `json:"HostPort"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &bindings) != nil {
		return false
	}
	for _, binding := range bindings["9400/tcp"] {
		if binding.HostPort == strconv.Itoa(port) {
			return true
		}
	}
	return false
}
