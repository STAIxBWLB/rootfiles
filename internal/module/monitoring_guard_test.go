package module

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

// fakeGuardHost points the guard at a temp tree and swaps the embedded
// helper for a stand-in: `check` passes while dir/rules-ok exists and
// `setup` creates it. iptables answers as a host with Docker running.
// active steers `systemctl is-active`. It returns the temp dir and call log.
func fakeGuardHost(t *testing.T, active bool) (string, string) {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls.log")
	oldHelper, oldConf, oldBytes := exporterGuardHelperPath, exporterGuardConfPath, exporterGuardHelper
	t.Cleanup(func() {
		exporterGuardHelperPath, exporterGuardConfPath, exporterGuardHelper = oldHelper, oldConf, oldBytes
	})
	exporterGuardHelperPath = filepath.Join(dir, "sbin", "rootfiles-exporter-firewall")
	exporterGuardConfPath = filepath.Join(dir, "etc", "exporter-firewall.conf")
	exporterGuardHelper = []byte("#!/bin/sh\necho \"helper $*\" >> " + calls + "\ncase $1 in\ncheck) test -f " + dir + "/rules-ok || { echo 'ERROR: required owner-marked firewall rules are missing or drifted' >&2; exit 1; } ;;\nsetup) touch " + dir + "/rules-ok ;;\nesac\n")
	state := "inactive"
	if active {
		state = "active"
	}
	fakeMonitoringCommand(t, "iptables", "#!/bin/sh\nexit 0\n")
	fakeMonitoringCommand(t, "iptables-save", "#!/bin/sh\nexit 0\n")
	fakeMonitoringCommand(t, "python3", "#!/bin/sh\nexit 0\n")
	fakeMonitoringCommand(t, "systemctl", "#!/bin/sh\necho \"systemctl $*\" >> "+calls+"\ncase $1 in is-active) echo "+state+";; esac\n")
	return dir, calls
}

func guardRC(t *testing.T, dryRun bool) *RunContext {
	rc := newRealRC(t)
	rc.Runner.DryRun, rc.DryRun = dryRun, dryRun
	rc.Config.Modules.Monitoring = config.MonitoringConfig{
		NodeExporter: true, ListenAddress: "192.0.2.5", AllowFrom: []string{"198.51.100.200/32"}, ExporterFirewall: true,
	}
	return rc
}

func readCalls(t *testing.T, calls string) string {
	t.Helper()
	b, _ := os.ReadFile(calls)
	return string(b)
}

func TestExporterGuardInstallsAndConverges(t *testing.T) {
	dir, calls := fakeGuardHost(t, true)
	rc := guardRC(t, false)
	ctx := context.Background()

	if changes := checkExporterGuard(ctx, rc); len(changes) != 2 {
		t.Fatalf("fresh host: want helper and config changes, got %+v", changes)
	}
	res, err := applyExporterGuard(ctx, rc)
	if err != nil || !res.Changed {
		t.Fatalf("apply = %+v, %v", res, err)
	}
	helper, err := os.Stat(exporterGuardHelperPath)
	if err != nil || helper.Mode().Perm() != 0o750 {
		t.Fatalf("helper mode: %v %v", helper, err)
	}
	conf, err := os.ReadFile(exporterGuardConfPath)
	if err != nil || string(conf) != "# Managed by rootfiles\nBIND_IP=192.0.2.5\nALLOW_FROM=198.51.100.200/32\n" {
		t.Fatalf("config = %q, %v", conf, err)
	}
	if fi, _ := os.Stat(exporterGuardConfPath); fi.Mode().Perm() != 0o600 {
		t.Fatalf("config mode %v", fi.Mode())
	}
	if !strings.Contains(readCalls(t, calls), "helper setup") {
		t.Fatalf("setup not run: %s", readCalls(t, calls))
	}
	if changes := checkExporterGuard(ctx, rc); len(changes) != 0 {
		t.Fatalf("check after apply: %+v", changes)
	}
	if res, err := applyExporterGuard(ctx, rc); err != nil || res.Changed {
		t.Fatalf("second apply = %+v, %v; want unchanged", res, err)
	}

	// Rules flushed behind rootfiles' back: check names it, apply restores.
	if err := os.Remove(filepath.Join(dir, "rules-ok")); err != nil {
		t.Fatal(err)
	}
	changes := checkExporterGuard(ctx, rc)
	if len(changes) != 1 || !strings.Contains(changes[0].Description, "rules are missing") {
		t.Fatalf("drift: %+v", changes)
	}
	if res, err := applyExporterGuard(ctx, rc); err != nil || !res.Changed {
		t.Fatalf("restore = %+v, %v", res, err)
	}
	if changes := checkExporterGuard(ctx, rc); len(changes) != 0 {
		t.Fatalf("check after restore: %+v", changes)
	}
}

func TestExporterGuardStartsExporterRefusedWithoutRules(t *testing.T) {
	_, calls := fakeGuardHost(t, false)
	rc := guardRC(t, false)
	if _, err := applyExporterGuard(context.Background(), rc); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readCalls(t, calls), "systemctl start "+nodeExporterUnit) {
		t.Fatalf("inactive exporter not started after setup: %s", readCalls(t, calls))
	}
	if strings.Contains(readCalls(t, calls), dcgmExporterUnit) {
		t.Fatalf("disabled DCGM exporter must not be started: %s", readCalls(t, calls))
	}
}

func TestExporterGuardDryRunChangesNothing(t *testing.T) {
	_, calls := fakeGuardHost(t, true)
	rc := guardRC(t, true)
	if _, err := applyExporterGuard(context.Background(), rc); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(exporterGuardHelperPath); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote the helper: %v", err)
	}
	if _, err := os.Stat(exporterGuardConfPath); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote the config: %v", err)
	}
	if strings.Contains(readCalls(t, calls), "helper") {
		t.Fatalf("dry run ran the helper: %s", readCalls(t, calls))
	}
}

func TestExporterGuardOffLeavesExistingGuardAlone(t *testing.T) {
	_, calls := fakeGuardHost(t, true)
	if err := os.MkdirAll(filepath.Dir(exporterGuardHelperPath), 0o755); err != nil {
		t.Fatal(err)
	}
	old := []byte("#!/bin/sh\n# rootfiles-exporter-firewall:v1 hand-installed\n")
	if err := os.WriteFile(exporterGuardHelperPath, old, 0o750); err != nil {
		t.Fatal(err)
	}
	rc := guardRC(t, false)
	rc.Config.Modules.Monitoring.ExporterFirewall = false
	if changes := checkExporterGuard(context.Background(), rc); len(changes) != 0 {
		t.Fatalf("off: want no changes, got %+v", changes)
	}
	if res, err := applyExporterGuard(context.Background(), rc); err != nil || res.Changed {
		t.Fatalf("off: apply = %+v, %v", res, err)
	}
	if got, _ := os.ReadFile(exporterGuardHelperPath); string(got) != string(old) {
		t.Fatal("off: helper was rewritten")
	}
	if strings.Contains(readCalls(t, calls), "helper") {
		t.Fatalf("off: helper was run: %s", readCalls(t, calls))
	}
	found := false
	for _, f := range doctorExporterExposure(context.Background(), rc) {
		if f.Check == "exporter firewall" && f.Level == LevelWarn && strings.Contains(f.Detail, "not managed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("doctor must name the unmanaged guard: %+v", doctorExporterExposure(context.Background(), rc))
	}
}

func TestExporterGuardBlocked(t *testing.T) {
	t.Run("firewalld active", func(t *testing.T) {
		fakeGuardHost(t, true)
		fakeMonitoringCommand(t, "firewall-cmd", "#!/bin/sh\necho running\n")
		rc := guardRC(t, false)
		changes := checkExporterGuard(context.Background(), rc)
		if len(changes) != 1 || changes[0].State != ChangeBlocked || !strings.Contains(changes[0].Description, "firewalld") {
			t.Fatalf("want blocked by firewalld, got %+v", changes)
		}
		if _, err := applyExporterGuard(context.Background(), rc); err == nil {
			t.Fatal("apply must refuse while firewalld is active")
		}
		if _, err := os.Stat(exporterGuardHelperPath); !os.IsNotExist(err) {
			t.Fatal("blocked apply wrote the helper")
		}
	})
	t.Run("docker not running", func(t *testing.T) {
		fakeGuardHost(t, true)
		fakeMonitoringCommand(t, "iptables", "#!/bin/sh\necho 'No chain/target/match by that name.' >&2; exit 1\n")
		rc := guardRC(t, false)
		changes := checkExporterGuard(context.Background(), rc)
		if len(changes) != 1 || changes[0].State != ChangeBlocked || !strings.Contains(changes[0].Description, "DOCKER-USER") {
			t.Fatalf("want blocked on DOCKER-USER, got %+v", changes)
		}
		rc.Config.Modules.Docker.Enabled = true
		changes = checkExporterGuard(context.Background(), rc)
		if len(changes) != 1 || changes[0].State == ChangeBlocked {
			t.Fatalf("docker module in this run: want pending, got %+v", changes)
		}
	})
}

func TestExporterGuardAdmitsHubBridge(t *testing.T) {
	fakeGuardHost(t, true)
	fakeMonitoringCommand(t, "docker", "#!/bin/sh\n[ \"$1 $2 $5\" = 'network inspect monitoring_default' ] && echo 8209a3f1a15e44f4f9d1f6a517e78995130119f37f60b93f4d39baacf6f0fe61\n")
	rc := guardRC(t, false)
	rc.Config.Modules.Monitoring.Hub.Enabled = true
	if got := exporterGuardConf(context.Background(), rc); !strings.HasSuffix(got, "\nWEB_INTERFACE=br-8209a3f1a15e\n") {
		t.Fatalf("hub bridge missing from config: %q", got)
	}
}

func TestDoctorExporterGuardCoverage(t *testing.T) {
	dir, _ := fakeGuardHost(t, true)
	rc := guardRC(t, false)
	if _, err := applyExporterGuard(context.Background(), rc); err != nil {
		t.Fatal(err)
	}
	got := doctorExporterExposure(context.Background(), rc)
	if len(got) != 1 || got[0].Level != LevelOK || !strings.Contains(got[0].Detail, "198.51.100.200/32") {
		t.Fatalf("passing guard should cover the exporters: %+v", got)
	}
	if err := os.Remove(filepath.Join(dir, "rules-ok")); err != nil {
		t.Fatal(err)
	}
	got = doctorExporterExposure(context.Background(), rc)
	if len(got) != 1 || got[0].Level != LevelWarn || !strings.Contains(got[0].Detail, "rules are missing") {
		t.Fatalf("failing guard check should warn: %+v", got)
	}
}

// The embedded helper must keep the v1 names so hand-installed guards are
// adopted in place, and must not tie exporters to the guard unit's state.
func TestEmbeddedExporterGuardHelper(t *testing.T) {
	s := string(exporterGuardHelper)
	for _, want := range []string{
		`readonly OWNER="rootfiles-exporter-firewall:v1"`, `readonly INPUT_CHAIN="RFILES_EXPORTER_IN"`, `readonly DOCKER_CHAIN="RFILES_DCGM_DU"`,
		`readonly CONFIG="/etc/rootfiles/exporter-firewall.conf"`, `readonly HELPER="/usr/local/sbin/rootfiles-exporter-firewall"`,
		"Wants=network-online.target docker.service", "ExecStartPre=+$HELPER $verify",
		`"$NODE_DROPIN exporter_content verify-host"`, `"$DCGM_DROPIN exporter_content verify-docker"`,
		"verify-host)", "verify-docker)",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("helper missing %q", want)
		}
	}
	if strings.Contains(s, "Requires=") {
		t.Error("helper must not write Requires= dependencies")
	}
	if exporterGuardHelperPath != "/usr/local/sbin/rootfiles-exporter-firewall" || exporterGuardConfPath != "/etc/rootfiles/exporter-firewall.conf" {
		t.Error("Go paths must match the helper's")
	}
}

func TestDockerDependentUnitsStartWithDocker(t *testing.T) {
	for name, unit := range map[string]string{
		"dcgm": renderDCGMUnit(config.MonitoringConfig{DCGMExporter: true}),
		"hub":  monitoringHubSystemdUnit("/etc/rootfiles/monitoring/compose.yaml"),
	} {
		if !strings.Contains(unit, "\nWantedBy=multi-user.target docker.service\n") {
			t.Errorf("%s unit must be wanted by docker.service:\n%s", name, unit)
		}
	}
}
