package module

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

const (
	nodeExporterDefaultsPath = "/etc/default/prometheus-node-exporter"
	dcgmExporterUnit         = "rootfiles-dcgm-exporter.service"
	monitoringSystemdDir     = "/etc/systemd/system"
	dcgmExporterUnitPath     = monitoringSystemdDir + "/" + dcgmExporterUnit
	dcgmExporterCollectors   = "/etc/rootfiles/monitoring/dcgm-exporter/default-counters.csv"
	dcgmCollectorsContainer  = "/etc/dcgm-exporter/default-counters.csv"
)

var (
	nodeExporterDefaults = nodeExporterDefaultsPath
	dcgmUnitPath         = dcgmExporterUnitPath
	dcgmCollectorsPath   = dcgmExporterCollectors
)

func checkMonitoringExporters(ctx context.Context, rc *RunContext) ([]Change, error) {
	cfg := rc.Config.Modules.Monitoring
	var changes []Change
	// Apply cannot fix a missing prerequisite, so it is reported as blocked
	// and the DCGM steps that depend on it are not planned.
	// A prerequisite an earlier module of this run provides (Docker, the
	// NVIDIA runtime) is pending, not blocked; apply checks it again live.
	dcgmBlocked := false
	if cfg.DCGMExporter {
		for _, d := range dcgmDependencies(ctx, rc) {
			if d.provider != "" && rc.moduleSucceeding(d.provider) {
				changes = append(changes, Change{Description: "DCGM exporter waits for the " + d.provider + " module: " + d.missing})
				continue
			}
			changes = append(changes, Change{Description: "DCGM exporter prerequisite missing: " + d.missing, State: ChangeBlocked})
			dcgmBlocked = true
		}
	}
	if cfg.NodeExporter {
		if !rc.APT.IsInstalled(nodeExporterPkg) {
			changes = append(changes, Change{Description: "Install Prometheus node exporter", Command: "apt-get install " + nodeExporterPkg})
		} else {
			want, err := desiredNodeExporterDefaults(rc, cfg)
			if err != nil {
				return nil, err
			}
			got, readErr := rc.Runner.ReadFile(nodeExporterDefaults)
			if readErr != nil || string(got) != want {
				if cfg.ListenAddress != "" || cfg.NodeExporterPort != 0 {
					changes = append(changes, Change{Description: "Configure node exporter listen address and port", Command: "write " + nodeExporterDefaults})
				}
			}
			if !serviceActive(ctx, rc, nodeExporterUnit) || !serviceEnabled(ctx, rc, nodeExporterUnit) {
				changes = append(changes, Change{Description: "Enable and start " + nodeExporterUnit, Command: "systemctl enable --now " + nodeExporterUnit})
			}
		}
	}
	if cfg.DCGMExporter && !dcgmBlocked {
		if got, err := rc.Runner.ReadFile(dcgmCollectorsPath); err != nil || string(got) != dcgmExporterCollectorsCSV {
			changes = append(changes, Change{Description: "Install DCGM exporter collectors including ECC telemetry", Command: "write " + dcgmCollectorsPath})
		}
		want := renderDCGMUnit(cfg)
		got, err := rc.Runner.ReadFile(dcgmUnitPath)
		if err != nil || string(got) != want {
			changes = append(changes, Change{Description: "Install or update DCGM exporter systemd unit", Command: "write " + dcgmUnitPath})
		} else if !rc.Runner.FileExists(dockerWantsLink(dcgmExporterUnit)) {
			changes = append(changes, Change{Description: "Start the DCGM exporter whenever Docker starts", Command: "systemctl reenable " + dcgmExporterUnit})
		}
		if !serviceActive(ctx, rc, dcgmExporterUnit) || !serviceEnabled(ctx, rc, dcgmExporterUnit) {
			changes = append(changes, Change{Description: "Start DCGM exporter", Command: "systemctl enable --now " + dcgmExporterUnit})
		}
	}
	fw, active := queryUFW(ctx, rc)
	if active && fw.Active {
		ports := exporterPorts(cfg)
		for _, p := range ports {
			for _, source := range cfg.AllowFrom {
				if !ufwSourceRuleExists(ctx, rc, source, p) {
					changes = append(changes, Change{Description: fmt.Sprintf("Allow exporter port %d from %s in UFW", p, source), Command: fmt.Sprintf("ufw allow from %s to any port %d proto tcp", source, p)})
				}
			}
		}
	}
	return changes, nil
}

func applyMonitoringExporters(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	cfg := rc.Config.Modules.Monitoring
	out := &ApplyResult{}
	// A missing DCGM prerequisite must not hold back node exporter: the rest
	// is applied first and the module then fails with the DCGM error.
	var dcgmErr error
	if cfg.DCGMExporter {
		if deps := dcgmDependencies(ctx, rc); len(deps) > 0 {
			missing := make([]string, len(deps))
			for i, d := range deps {
				missing[i] = d.missing
			}
			dcgmErr = fmt.Errorf("DCGM exporter prerequisites are missing: %s", strings.Join(missing, "; "))
		}
	}
	if cfg.NodeExporter {
		if !rc.APT.IsInstalled(nodeExporterPkg) {
			if err := rc.APT.Update(ctx); err != nil {
				return nil, fmt.Errorf("apt update: %w", err)
			}
			if err := rc.APT.Install(ctx, []string{nodeExporterPkg}); err != nil {
				return nil, fmt.Errorf("installing %s: %w", nodeExporterPkg, err)
			}
			out.Changed = true
			out.Messages = append(out.Messages, "node exporter installed")
		}
		if err := rc.Runner.MkdirAll(nodeExporterTextfileDir, 0755); err != nil {
			return nil, err
		}
		if cfg.ListenAddress != "" || cfg.NodeExporterPort != 0 {
			want, err := desiredNodeExporterDefaults(rc, cfg)
			if err != nil {
				return nil, err
			}
			got, readErr := rc.Runner.ReadFile(nodeExporterDefaults)
			if readErr != nil || string(got) != want {
				if err := rc.Runner.WriteFile(nodeExporterDefaults, []byte(want), 0644); err != nil {
					return nil, err
				}
				out.Changed = true
				if _, err := rc.Runner.Run(ctx, "systemctl", "restart", nodeExporterUnit); err != nil {
					out.Warnings = append(out.Warnings, "restarting node exporter: "+firstLine(err.Error()))
				}
			}
		}
		if !serviceActive(ctx, rc, nodeExporterUnit) || !serviceEnabled(ctx, rc, nodeExporterUnit) {
			if _, err := rc.Runner.Run(ctx, "systemctl", "enable", "--now", nodeExporterUnit); err != nil {
				out.Warnings = append(out.Warnings, "enabling node exporter: "+firstLine(err.Error()))
			} else {
				out.Changed = true
			}
		}
		if !serviceActive(ctx, rc, nodeExporterUnit) {
			out.Warnings = append(out.Warnings, "node exporter is not active after enable")
		}
	}
	if cfg.DCGMExporter && dcgmErr == nil {
		collectorsChanged := false
		if got, err := rc.Runner.ReadFile(dcgmCollectorsPath); err != nil || string(got) != dcgmExporterCollectorsCSV {
			if err := rc.Runner.MkdirAll(filepath.Dir(dcgmCollectorsPath), 0755); err != nil {
				return nil, fmt.Errorf("creating DCGM collectors directory: %w", err)
			}
			if err := rc.Runner.WriteFile(dcgmCollectorsPath, []byte(dcgmExporterCollectorsCSV), 0644); err != nil {
				return nil, fmt.Errorf("writing DCGM collectors: %w", err)
			}
			out.Changed = true
			collectorsChanged = true
		}
		want := renderDCGMUnit(cfg)
		got, err := rc.Runner.ReadFile(dcgmUnitPath)
		unitChanged := err != nil || string(got) != want
		if unitChanged {
			if err := rc.Runner.MkdirAll(filepath.Dir(dcgmUnitPath), 0755); err != nil {
				return nil, err
			}
			if err := rc.Runner.WriteFile(dcgmUnitPath, []byte(want), 0644); err != nil {
				return nil, err
			}
			out.Changed = true
			if _, err := rc.Runner.Run(ctx, "systemctl", "daemon-reload"); err != nil {
				return nil, err
			}
		}
		if unitChanged || !rc.Runner.FileExists(dockerWantsLink(dcgmExporterUnit)) {
			if _, err := rc.Runner.Run(ctx, "systemctl", "reenable", dcgmExporterUnit); err != nil {
				return nil, fmt.Errorf("enabling DCGM exporter: %w", err)
			}
			out.Changed = true
		}
		if !serviceActive(ctx, rc, dcgmExporterUnit) || !serviceEnabled(ctx, rc, dcgmExporterUnit) || unitChanged || collectorsChanged {
			args := []string{"enable", "--now", dcgmExporterUnit}
			if serviceActive(ctx, rc, dcgmExporterUnit) && (unitChanged || collectorsChanged) {
				args = []string{"restart", dcgmExporterUnit}
			}
			if _, err := rc.Runner.Run(ctx, "systemctl", args...); err != nil {
				return nil, fmt.Errorf("starting DCGM exporter: %w", err)
			}
			out.Changed = true
		}
		if !serviceActive(ctx, rc, dcgmExporterUnit) {
			out.Warnings = append(out.Warnings, "DCGM exporter is not active after start")
		}
	}
	if fw, present := queryUFW(ctx, rc); present && fw.Active {
		for _, p := range exporterPorts(cfg) {
			for _, source := range cfg.AllowFrom {
				if ufwSourceRuleExists(ctx, rc, source, p) {
					continue
				}
				if _, err := rc.Runner.Run(ctx, "ufw", "allow", "from", source, "to", "any", "port", strconv.Itoa(p), "proto", "tcp"); err != nil {
					out.Warnings = append(out.Warnings, fmt.Sprintf("allowing exporter port %d from %s in UFW: %s", p, source, firstLine(err.Error())))
				} else {
					out.Changed = true
				}
			}
		}
	}
	return out, dcgmErr
}

// dcgmRuntimeFix registers the runtime; a reload keeps running containers.
const dcgmRuntimeFix = "sudo nvidia-ctk runtime configure --runtime=docker && sudo systemctl reload docker"

type dcgmDependency struct {
	missing  string
	provider string // module that installs it, "" when none does
}

// dcgmDependencies lists the missing DCGM exporter prerequisites in the
// wording check, dry-run and apply share.
func dcgmDependencies(ctx context.Context, rc *RunContext) []dcgmDependency {
	var deps []dcgmDependency
	if !rc.Runner.CommandExists("docker") {
		deps = append(deps, dcgmDependency{"docker command is missing", "docker"})
	} else if res, err := rc.Runner.Query(ctx, "docker", "info", "--format", "{{json .Runtimes}}"); err != nil || !strings.Contains(res.Stdout, "nvidia") {
		deps = append(deps, dcgmDependency{"Docker NVIDIA runtime is unavailable (fix: " + dcgmRuntimeFix + ")", "nvidia"})
	}
	if !rc.Runner.CommandExists("nvidia-smi") {
		deps = append(deps, dcgmDependency{"nvidia-smi is missing", ""})
	} else if res, err := rc.Runner.Query(ctx, "nvidia-smi", "-L"); err != nil || !strings.Contains(res.Stdout, "GPU ") {
		deps = append(deps, dcgmDependency{"no NVIDIA GPU is visible", ""})
	}
	return deps
}

func desiredNodeExporterDefaults(rc *RunContext, cfg config.MonitoringConfig) (string, error) {
	data, err := rc.Runner.ReadFile(nodeExporterDefaults)
	if err != nil && !rc.Runner.FileExists(nodeExporterDefaults) {
		data = nil
	}
	return nodeExporterDefaultsContent(string(data), cfg.ListenAddress, cfg.NodePort()), nil
}

func nodeExporterDefaultsContent(current string, address string, port int) string {
	if port == 0 {
		port = 9100
	}
	wantArg := "--web.listen-address=" + net.JoinHostPort(address, strconv.Itoa(port))
	if address == "" {
		wantArg = "--web.listen-address=0.0.0.0:" + strconv.Itoa(port)
	}
	var lines []string
	if current != "" {
		lines = strings.Split(strings.TrimSuffix(current, "\n"), "\n")
	}
	found := false
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if !strings.HasPrefix(trim, "ARGS=") {
			continue
		}
		value := strings.Trim(strings.TrimPrefix(trim, "ARGS="), "\"'")
		args := strings.Fields(value)
		filtered := args[:0]
		for _, arg := range args {
			if !strings.HasPrefix(arg, "--web.listen-address=") {
				filtered = append(filtered, arg)
			}
		}
		filtered = append(filtered, wantArg)
		lines[i] = "ARGS=\"" + strings.Join(filtered, " ") + "\""
		found = true
		break
	}
	if !found {
		lines = append(lines, "ARGS=\""+wantArg+"\"")
	}
	return strings.Join(lines, "\n") + "\n"
}

func renderDCGMUnit(cfg config.MonitoringConfig) string {
	address := cfg.ListenAddress
	if address == "" {
		address = "0.0.0.0"
	}
	publish := net.JoinHostPort(address, strconv.Itoa(cfg.DCGMPort())) + ":9400"
	return fmt.Sprintf("[Unit]\nDescription=rootfiles NVIDIA DCGM exporter\nAfter=docker.service\nRequires=docker.service\n\n[Service]\nRestart=always\nRestartSec=5\nExecStartPre=-/usr/bin/docker rm -f rootfiles-dcgm-exporter\nExecStart=/usr/bin/docker run --rm --name rootfiles-dcgm-exporter --runtime=nvidia --gpus all -p %s --mount type=bind,src=%s,dst=%s,readonly %s -f %s\nExecStop=/usr/bin/docker stop rootfiles-dcgm-exporter\n\n[Install]\nWantedBy=multi-user.target docker.service\n", publish, dcgmCollectorsPath, dcgmCollectorsContainer, cfg.ExporterImage(), dcgmCollectorsContainer)
}

// DCGM's collectors file replaces image defaults. Keep only fields shared by
// the legacy R535 image and the newer image override. ECC can be absent on
// hardware or configurations that do not expose counters; the XID gauge is the
// last observed ID and can remain stale after recovery.
const dcgmExporterCollectorsCSV = `# DCGM FIELD, Prometheus metric type, help message
DCGM_FI_DEV_GPU_TEMP, gauge, GPU temperature (in C).
DCGM_FI_DEV_GPU_UTIL, gauge, GPU utilization (in %).
DCGM_FI_DEV_FB_FREE, gauge, Framebuffer memory free (in MiB).
DCGM_FI_DEV_FB_USED, gauge, Framebuffer memory used (in MiB).
DCGM_FI_DEV_ECC_DBE_VOL_TOTAL, counter, Total number of double-bit volatile ECC errors.
DCGM_FI_DEV_XID_ERRORS, gauge, Value of the last XID error encountered.
`

// dockerWantsDir holds the links `systemctl enable` makes for units with
// WantedBy=docker.service, which start again whenever Docker starts.
var dockerWantsDir = monitoringSystemdDir + "/docker.service.wants"

func dockerWantsLink(unit string) string { return filepath.Join(dockerWantsDir, unit) }

func exporterPorts(cfg config.MonitoringConfig) []int {
	var ports []int
	if cfg.NodeExporter {
		ports = append(ports, cfg.NodePort())
	}
	if cfg.DCGMExporter {
		ports = append(ports, cfg.DCGMPort())
	}
	return ports
}
func serviceEnabled(ctx context.Context, rc *RunContext, unit string) bool {
	res, err := rc.Runner.Query(ctx, "systemctl", "is-enabled", unit)
	return err == nil && strings.TrimSpace(res.Stdout) == "enabled"
}
func ufwSourceRuleExists(ctx context.Context, rc *RunContext, source string, port int) bool {
	res, err := rc.Runner.Query(ctx, "ufw", "status")
	if err != nil {
		return false
	}
	wantPort := strconv.Itoa(port)
	wantSource := strings.ToLower(source)
	wantSourceHost := strings.TrimSuffix(strings.TrimSuffix(wantSource, "/32"), "/128")
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := strings.Fields(strings.ToLower(line))
		if len(fields) < 4 || (fields[0] != wantPort && fields[0] != wantPort+"/tcp") {
			continue
		}
		for i, field := range fields {
			if field == "allow" && i+2 < len(fields) && fields[i+1] == "in" {
				gotSource := strings.TrimSuffix(strings.TrimSuffix(fields[i+2], "/32"), "/128")
				if fields[i+2] == wantSource || gotSource == wantSourceHost {
					return true
				}
			}
		}
	}
	return false
}
