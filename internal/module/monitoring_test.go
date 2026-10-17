package module

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

func TestParseTCPListeners(t *testing.T) {
	got := parseTCPListeners(`LISTEN 0 4096 0.0.0.0:9400 0.0.0.0:* users:(("docker-proxy",pid=1,fd=4))
LISTEN 0 128 [::1]:9100 *:* users:(("prometheus-node-exporter",pid=2,fd=3))
`)
	if len(got[9400]) != 1 || got[9400][0].Process != "docker-proxy" || got[9400][0].PID != 1 {
		t.Fatalf("DCGM listener parse: %+v", got[9400])
	}
	if len(got[9100]) != 1 || got[9100][0].Address != "[::1]:9100" {
		t.Fatalf("IPv6 listener parse: %+v", got[9100])
	}
}

func TestMonitoringPreflightDoesNotTrustUnrelatedDockerProxy(t *testing.T) {
	fakeCommandOutput(t, map[string]string{"ss": `LISTEN 0 128 0.0.0.0:9400 0.0.0.0:* users:(("docker-proxy",pid=44,fd=3))
`})
	fakeMonitoringCommand(t, "systemctl", "#!/bin/sh\necho active\n")
	fakeMonitoringCommand(t, "docker", "#!/bin/sh\nprintf '%s' '{\"9400/tcp\":[{\"HostIp\":\"0.0.0.0\",\"HostPort\":\"9401\"}]}'\n")
	rc := newRealRC(t)
	rc.Config.Modules.Monitoring.DCGMExporter = true
	err := monitoringPreflight(context.Background(), rc)
	if err == nil || !strings.Contains(err.Error(), "9400") || !strings.Contains(err.Error(), "docker-proxy") {
		t.Fatalf("proxy for another container must remain a port conflict: %v", err)
	}
}

func TestNodeExporterDefaultsContentPreservesOtherArgs(t *testing.T) {
	got := nodeExporterDefaultsContent(`# package defaults
ARGS="--collector.textfile.directory=/var/lib/prometheus/node-exporter --web.listen-address=:9200"
`, "10.2.3.4", 9101)
	if !strings.Contains(got, "--collector.textfile.directory=/var/lib/prometheus/node-exporter") || !strings.Contains(got, "--web.listen-address=10.2.3.4:9101") || strings.Contains(got, ":9200") {
		t.Fatalf("unexpected defaults: %s", got)
	}
}

func TestRenderDCGMUnitPinnedImageAndBinding(t *testing.T) {
	unit := renderDCGMUnit(config.MonitoringConfig{ListenAddress: "10.0.0.4", DCGMExporterPort: 9440, DCGMExporterImage: "nvcr.io/nvidia/k8s/dcgm-exporter:3.3.8-3.6.0"})
	for _, want := range []string{"--runtime=nvidia --gpus all", "-p 10.0.0.4:9440:9400", "dcgm-exporter:3.3.8-3.6.0", "rootfiles-dcgm-exporter", "--mount type=bind", "src=" + dcgmCollectorsPath + ",dst=" + dcgmCollectorsContainer + ",readonly", "-f " + dcgmCollectorsContainer} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing %q:\n%s", want, unit)
		}
	}
}

func TestDCGMCollectorsIncludeRequiredHealthFields(t *testing.T) {
	for _, field := range []string{
		"DCGM_FI_DEV_GPU_TEMP", "DCGM_FI_DEV_GPU_UTIL", "DCGM_FI_DEV_FB_USED", "DCGM_FI_DEV_FB_FREE",
		"DCGM_FI_DEV_XID_ERRORS", "DCGM_FI_DEV_ECC_DBE_VOL_TOTAL",
	} {
		row := "\n" + field + ","
		if !strings.Contains(dcgmExporterCollectorsCSV, row) {
			t.Errorf("collector CSV is missing active metric %s", field)
		}
	}
}

func TestMonitoringPreflightRejectsUnmanagedListener(t *testing.T) {
	fakeCommandOutput(t, map[string]string{"ss": `LISTEN 0 128 0.0.0.0:9400 0.0.0.0:* users:(("dcgm-exporter",pid=44,fd=3))
`})
	rc := newRealRC(t)
	rc.Config.Modules.Monitoring.DCGMExporter = true
	err := monitoringPreflight(context.Background(), rc)
	if err == nil || !strings.Contains(err.Error(), "9400") || !strings.Contains(err.Error(), "dcgm-exporter") {
		t.Fatalf("expected named port conflict, got %v", err)
	}
}

func TestMonitoringPreflightCatchesDefaultHubPortCollision(t *testing.T) {
	rc := newRealRC(t)
	rc.Config.Modules.Monitoring.NodeExporter = true
	rc.Config.Modules.Monitoring.NodeExporterPort = 9090
	rc.Config.Modules.Monitoring.Hub.Enabled = true
	if err := monitoringPreflight(context.Background(), rc); err == nil || !strings.Contains(err.Error(), "both node exporter and Prometheus") {
		t.Fatalf("expected default hub port collision before querying or applying, got %v", err)
	}
}

func TestExporterFirewallPreflightRejectsBroadRuleBeforeApplyChanges(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "apt-called")
	fakeMonitoringCommand(t, "ss", "#!/bin/sh\nexit 0\n")
	fakeMonitoringCommand(t, "ufw", "#!/bin/sh\ncat <<'EOF'\nStatus: active\nDefault: deny (incoming), allow (outgoing), disabled (routed)\n\nTo                         Action      From\n--                         ------      ----\n9100/tcp                   ALLOW IN    10.0.0.5\n9100/tcp                   ALLOW IN    Anywhere\nEOF\n")
	fakeMonitoringCommand(t, "dpkg-query", "#!/bin/sh\nexit 1\n")
	fakeMonitoringCommand(t, "apt-get", "#!/bin/sh\necho called > '"+logPath+"'\n")
	oldTextfile := nodeExporterTextfileDir
	nodeExporterTextfileDir = filepath.Join(t.TempDir(), "textfile")
	t.Cleanup(func() { nodeExporterTextfileDir = oldTextfile })
	rc := newRealRC(t)
	rc.Config.Modules.Monitoring = config.MonitoringConfig{NodeExporter: true, AllowFrom: []string{"10.0.0.5/32"}}
	if _, err := NewMonitoringModule().Apply(context.Background(), rc); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("broad UFW rule should block Apply, got %v", err)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("apt ran before firewall preflight: %v", err)
	}
	if _, err := os.Stat(nodeExporterTextfileDir); !os.IsNotExist(err) {
		t.Fatalf("exporter files changed before firewall preflight: %v", err)
	}
}

func TestExporterFirewallPreflightRejectsAllowDefault(t *testing.T) {
	fakeMonitoringCommand(t, "ufw", "#!/bin/sh\ncat <<'EOF'\nStatus: active\nDefault: allow (incoming), allow (outgoing), disabled (routed)\n9100/tcp ALLOW IN 10.0.0.5\nEOF\n")
	rc := newRealRC(t)
	rc.Config.Modules.Monitoring = config.MonitoringConfig{NodeExporter: true, AllowFrom: []string{"10.0.0.5/32"}}
	if err := preflightExporterFirewall(context.Background(), rc); err == nil || !strings.Contains(err.Error(), "default incoming policy") {
		t.Fatalf("allow-by-default UFW policy should fail closed, got %v", err)
	}
}

func TestApplyDCGMWithoutPrerequisitesDoesNotWriteUnit(t *testing.T) {
	dir := fakeCommandOutput(t, map[string]string{
		"ss": "", "docker": `{}`, "nvidia-smi": `No devices were found`,
	})
	oldUnit, oldDefaults, oldTextfile := dcgmUnitPath, nodeExporterDefaults, nodeExporterTextfileDir
	dcgmUnitPath = filepath.Join(t.TempDir(), "rootfiles-dcgm-exporter.service")
	nodeExporterDefaults = filepath.Join(t.TempDir(), "defaults")
	nodeExporterTextfileDir = filepath.Join(t.TempDir(), "textfile")
	t.Cleanup(func() {
		dcgmUnitPath, nodeExporterDefaults, nodeExporterTextfileDir = oldUnit, oldDefaults, oldTextfile
	})
	rc := newRealRC(t)
	rc.Config.Modules.Docker.Enabled = true
	rc.Config.Modules.Nvidia.Enabled = true
	rc.Config.Modules.Monitoring.DCGMExporter = true
	if _, err := NewMonitoringModule().Apply(context.Background(), rc); err == nil || !strings.Contains(err.Error(), "no NVIDIA GPU") {
		t.Fatalf("Apply should stop on missing GPU before changes, got %v", err)
	}
	if _, err := os.Stat(dcgmUnitPath); !os.IsNotExist(err) {
		t.Fatalf("unit was written despite missing prerequisite: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "calls.log")); err == nil {
		t.Fatal("unexpected side-effect log")
	}
}

func TestDCGMInstalledPrerequisitesDoNotRequireEnabledModules(t *testing.T) {
	oldUnit, oldCollectors := dcgmUnitPath, dcgmCollectorsPath
	dcgmUnitPath = filepath.Join(t.TempDir(), "rootfiles-dcgm-exporter.service")
	dcgmCollectorsPath = filepath.Join(t.TempDir(), "default-counters.csv")
	t.Cleanup(func() { dcgmUnitPath, dcgmCollectorsPath = oldUnit, oldCollectors })
	cfg := config.MonitoringConfig{DCGMExporter: true}
	if err := os.WriteFile(dcgmUnitPath, []byte(renderDCGMUnit(cfg)), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dcgmCollectorsPath, []byte(dcgmExporterCollectorsCSV), 0644); err != nil {
		t.Fatal(err)
	}
	fakeMonitoringCommand(t, "docker", "#!/bin/sh\necho '{\"nvidia\":{}}'\n")
	fakeMonitoringCommand(t, "nvidia-smi", "#!/bin/sh\necho 'GPU 0: Test GPU'\n")
	fakeMonitoringCommand(t, "systemctl", "#!/bin/sh\ncase \"$1\" in is-active) echo active;; is-enabled) echo enabled;; esac\n")
	fakeMonitoringCommand(t, "ufw", "#!/bin/sh\necho 'Status: inactive'\n")
	rc := newRealRC(t)
	// Both lifecycle modules are disabled so their existing host configuration
	// remains untouched, while the installed runtime and GPU are usable.
	rc.Config.Modules.Monitoring = cfg
	changes, err := checkMonitoringExporters(context.Background(), rc)
	if err != nil || len(changes) != 0 {
		t.Fatalf("installed dependencies should satisfy DCGM checks, got %+v, %v", changes, err)
	}
}

func TestNodeExporterCheckAndApplyAreIdempotentWhenActive(t *testing.T) {
	oldDefaults, oldTextfile := nodeExporterDefaults, nodeExporterTextfileDir
	dir := t.TempDir()
	nodeExporterDefaults = filepath.Join(dir, "defaults")
	nodeExporterTextfileDir = filepath.Join(dir, "textfile")
	t.Cleanup(func() { nodeExporterDefaults, nodeExporterTextfileDir = oldDefaults, oldTextfile })
	if err := os.MkdirAll(nodeExporterTextfileDir, 0755); err != nil {
		t.Fatal(err)
	}
	config := config.MonitoringConfig{NodeExporter: true, ListenAddress: "10.0.0.8"}
	defaults := nodeExporterDefaultsContent(`ARGS=""
`, config.ListenAddress, config.NodePort())
	if err := os.WriteFile(nodeExporterDefaults, []byte(defaults), 0644); err != nil {
		t.Fatal(err)
	}
	fakeMonitoringCommand(t, "ss", "#!/bin/sh\nexit 0\n")
	fakeMonitoringCommand(t, "dpkg-query", "#!/bin/sh\nprintf 'prometheus-node-exporter install ok installed\\n'\n")
	fakeMonitoringCommand(t, "systemctl", "#!/bin/sh\ncase \"$1:$2\" in\nis-active:prometheus-node-exporter) echo active;;\nis-enabled:prometheus-node-exporter) echo enabled;;\nesac\n")
	fakeMonitoringCommand(t, "ufw", "#!/bin/sh\necho 'Status: inactive'\n")
	rc := newRealRC(t)
	rc.Config.Modules.Monitoring = config
	m := NewMonitoringModule()
	check, err := m.Check(context.Background(), rc)
	if err != nil || !check.Satisfied {
		t.Fatalf("Check = %+v, %v; want satisfied", check, err)
	}
	result, err := m.Apply(context.Background(), rc)
	if err != nil || result.Changed {
		t.Fatalf("idempotent Apply = %+v, %v; want unchanged", result, err)
	}
}

func fakeMonitoringCommand(t *testing.T, name, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

func TestDoctorExporterExposure(t *testing.T) {
	t.Run("private address", func(t *testing.T) {
		rc := newDryRunRC(t)
		rc.Config.Modules.Monitoring = config.MonitoringConfig{NodeExporter: true, ListenAddress: "10.0.0.5"}
		got := doctorExporterExposure(context.Background(), rc)
		if len(got) != 1 || got[0].Level != LevelOK {
			t.Fatalf("private listener should be covered: %+v", got)
		}
	})
	t.Run("perimeter firewall", func(t *testing.T) {
		rc := newDryRunRC(t)
		rc.Config.Modules.Monitoring = config.MonitoringConfig{NodeExporter: true, PerimeterFirewall: true}
		got := doctorExporterExposure(context.Background(), rc)
		if len(got) != 1 || got[0].Level != LevelOK {
			t.Fatalf("perimeter should cover listener: %+v", got)
		}
	})
	t.Run("wildcard warns", func(t *testing.T) {
		rc := newDryRunRC(t)
		rc.Config.Modules.Monitoring = config.MonitoringConfig{NodeExporter: true}
		fakeCommandOutput(t, map[string]string{"ufw": `Status: inactive
`})
		got := doctorExporterExposure(context.Background(), rc)
		if len(got) != 1 || got[0].Level != LevelWarn {
			t.Fatalf("uncovered wildcard should warn: %+v", got)
		}
	})
	t.Run("unrestricted UFW allow still warns", func(t *testing.T) {
		rc := newDryRunRC(t)
		rc.Config.Modules.Monitoring = config.MonitoringConfig{NodeExporter: true}
		fakeCommandOutput(t, map[string]string{"ufw": `Status: active
Default: deny (incoming), allow (outgoing), disabled (routed)
9100/tcp ALLOW IN Anywhere
`})
		got := doctorExporterExposure(context.Background(), rc)
		if len(got) != 1 || got[0].Level != LevelWarn {
			t.Fatalf("public allow rule must not count as protection: %+v", got)
		}
	})
	t.Run("restricted UFW allow covers listener", func(t *testing.T) {
		rc := newDryRunRC(t)
		rc.Config.Modules.Monitoring = config.MonitoringConfig{NodeExporter: true, AllowFrom: []string{"10.0.0.5/32"}}
		fakeCommandOutput(t, map[string]string{"ufw": `Status: active
Default: deny (incoming), allow (outgoing), disabled (routed)
9100/tcp ALLOW IN 10.0.0.5
`})
		got := doctorExporterExposure(context.Background(), rc)
		if len(got) != 1 || got[0].Level != LevelOK {
			t.Fatalf("restricted UFW rule should cover listener: %+v", got)
		}
	})
}

func fakeCommandOutput(t *testing.T, outputs map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, output := range outputs {
		script := "#!/bin/sh\nprintf '%s' '" + strings.ReplaceAll(output, "'", "'\\''") + "'\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return dir
}

// fakeGPUHostWithoutRuntime stubs a GPU host whose Docker lacks the NVIDIA
// runtime, with node exporter installed but stopped. It returns the call log.
func fakeGPUHostWithoutRuntime(t *testing.T) string {
	t.Helper()
	oldUnit, oldCollectors, oldDefaults, oldTextfile := dcgmUnitPath, dcgmCollectorsPath, nodeExporterDefaults, nodeExporterTextfileDir
	dir := t.TempDir()
	dcgmUnitPath = filepath.Join(dir, "rootfiles-dcgm-exporter.service")
	dcgmCollectorsPath = filepath.Join(dir, "default-counters.csv")
	nodeExporterDefaults = filepath.Join(dir, "defaults")
	nodeExporterTextfileDir = filepath.Join(dir, "textfile")
	t.Cleanup(func() {
		dcgmUnitPath, dcgmCollectorsPath, nodeExporterDefaults, nodeExporterTextfileDir = oldUnit, oldCollectors, oldDefaults, oldTextfile
	})
	calls := filepath.Join(dir, "calls.log")
	fakeMonitoringCommand(t, "ss", "#!/bin/sh\nexit 0\n")
	fakeMonitoringCommand(t, "ufw", "#!/bin/sh\necho 'Status: inactive'\n")
	fakeMonitoringCommand(t, "docker", "#!/bin/sh\necho '{\"runc\":{}}'\n")
	fakeMonitoringCommand(t, "nvidia-smi", "#!/bin/sh\necho 'GPU 0: Test GPU'\n")
	fakeMonitoringCommand(t, "dpkg-query", "#!/bin/sh\nprintf 'prometheus-node-exporter install ok installed\\n'\n")
	fakeMonitoringCommand(t, "systemctl", "#!/bin/sh\necho \"systemctl $*\" >> '"+calls+"'\nexit 3\n")
	return calls
}

func TestDCGMMissingRuntimeIsBlockedAndDoesNotHoldBackNodeExporter(t *testing.T) {
	calls := fakeGPUHostWithoutRuntime(t)
	rc := newRealRC(t)
	rc.Config.Modules.Monitoring = config.MonitoringConfig{NodeExporter: true, DCGMExporter: true}
	m := NewMonitoringModule()

	check, err := m.Check(context.Background(), rc)
	if err != nil || check.Satisfied {
		t.Fatalf("Check = %+v, %v; want unsatisfied", check, err)
	}
	blocked := 0
	for _, c := range check.Changes {
		if c.State == ChangeBlocked {
			blocked++
			if !strings.Contains(c.Description, dcgmRuntimeFix) {
				t.Fatalf("blocked item should name the fix: %q", c.Description)
			}
		}
		if strings.Contains(c.Description, "DCGM exporter systemd unit") || c.Description == "Start DCGM exporter" {
			t.Fatalf("blocked DCGM must not plan steps apply will skip: %+v", check.Changes)
		}
	}
	if blocked != 1 {
		t.Fatalf("want one blocked item, got %+v", check.Changes)
	}

	_, err = m.Apply(context.Background(), rc)
	if err == nil || !strings.Contains(err.Error(), "Docker NVIDIA runtime is unavailable") || !strings.Contains(err.Error(), dcgmRuntimeFix) {
		t.Fatalf("Apply should fail with the DCGM error and fix, got %v", err)
	}
	log, _ := os.ReadFile(calls)
	if !strings.Contains(string(log), "systemctl enable --now prometheus-node-exporter") {
		t.Fatalf("node exporter should still be started: %s", log)
	}
	if _, err := os.Stat(dcgmUnitPath); !os.IsNotExist(err) {
		t.Fatalf("DCGM unit written despite missing runtime: %v", err)
	}
}

func TestDryRunFailsOnBlockedChange(t *testing.T) {
	fakeGPUHostWithoutRuntime(t)
	rc := newDryRunRC(t)
	rc.Config.Modules.Monitoring = config.MonitoringConfig{NodeExporter: true, DCGMExporter: true}
	outcomes, err := RunAll(context.Background(), []Module{NewMonitoringModule()}, rc)
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("dry run with a blocked change should fail, got %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Status != "failed" {
		t.Fatalf("outcome = %+v; want failed", outcomes)
	}
}
