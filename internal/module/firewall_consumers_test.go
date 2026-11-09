package module

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

func firewallConsumerFixture(t *testing.T, status string, fails bool) (dir, mutations string) {
	t.Helper()
	dir = t.TempDir()
	mutations = filepath.Join(dir, "mutations")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	write := func(name, script string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script+"\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	statusScript := "printf '%s' " + quote(status)
	if fails {
		statusScript += "\nexit 1"
	}
	write("ufw", statusScript)
	write("ss", "exit 0")
	for _, name := range []string{"apt-get", "dpkg-query", "systemctl", "docker"} {
		write(name, "printf called >> "+quote(mutations)+"\nexit 0")
	}
	t.Setenv("PATH", dir)
	return dir, mutations
}

func TestFirewallConsumersRejectUnknownUFWState(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		fails        bool
	}{
		{"failed query", "", true},
		{"empty output", "", false},
		{"unrecognized status", "Status: maybe\n", false},
		{"contradictory status", "Status: active\nStatus: inactive\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, mutations := firewallConsumerFixture(t, tc.status, tc.fails)
			oldDefaults, oldTextfile := nodeExporterDefaults, nodeExporterTextfileDir
			nodeExporterDefaults = filepath.Join(dir, "node-defaults")
			nodeExporterTextfileDir = filepath.Join(dir, "textfile")
			t.Cleanup(func() { nodeExporterDefaults, nodeExporterTextfileDir = oldDefaults, oldTextfile })
			rc := newRealRC(t)
			// Even without allow_from, an unknown firewall must not be skipped
			// before starting an exporter or changing its access rules.
			rc.Config.Modules.Monitoring = config.MonitoringConfig{NodeExporter: true}
			if _, err := NewMonitoringModule().Apply(context.Background(), rc); err == nil || !strings.Contains(err.Error(), "UFW") {
				t.Fatalf("monitoring Apply accepted unknown status: %v", err)
			}
			if _, err := checkMonitoringExporters(context.Background(), rc); err == nil {
				t.Fatal("exporter Check accepted unknown status")
			}
			if _, err := applyMonitoringExporters(context.Background(), rc); err == nil {
				t.Fatal("exporter Apply accepted unknown status")
			}
			for _, file := range []string{mutations, nodeExporterDefaults, nodeExporterTextfileDir} {
				if _, err := os.Stat(file); !os.IsNotExist(err) {
					t.Fatalf("changed state before rejecting unknown firewall: %s (%v)", file, err)
				}
			}
			finding := doctorFirewall(context.Background(), rc)
			if finding.Level != LevelFail || !strings.Contains(finding.Detail, "cannot determine") {
				t.Fatalf("doctor misclassified unknown status: %+v", finding)
			}
			exposure := doctorExporterExposure(context.Background(), rc)
			if len(exposure) != 1 || exposure[0].Level != LevelWarn || !strings.Contains(exposure[0].Detail, "cannot determine") {
				t.Fatalf("doctor claimed exporter protection for unknown status: %+v", exposure)
			}
			// A network module expected to open the port cannot make an
			// unreadable firewall safe to treat as already admitted.
			rc.Config.Modules.Network = config.NetworkConfig{Enabled: true, UFW: true}
			if reason := piperBlock(context.Background(), rc, 2222); !strings.Contains(reason, "cannot read the UFW state") {
				t.Fatalf("piper admitted unknown status: %q", reason)
			}
		})
	}
}

func TestExporterFirewallOnlyConsidersInboundAllowRules(t *testing.T) {
	firewallConsumerFixture(t, "Status: active\nDefault: deny (incoming), allow (outgoing), disabled (routed)\n9100/tcp ALLOW IN 10.0.0.5\n9100/tcp DENY IN Anywhere\n9100/tcp ALLOW OUT Anywhere\n", false)
	rc := newDryRunRC(t)
	rc.Config.Modules.Monitoring = config.MonitoringConfig{NodeExporter: true, AllowFrom: []string{"10.0.0.5/32"}}
	if err := preflightExporterFirewall(context.Background(), rc); err != nil {
		t.Fatalf("non-allow or outbound rule was treated as a broad inbound allow: %v", err)
	}
}

func TestDoctorFirewallDistinguishesAbsentAndInactive(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if got := doctorFirewall(context.Background(), newDryRunRC(t)); got.Level != LevelSkip {
			t.Fatalf("absent firewall: %+v", got)
		}
	})
	t.Run("inactive", func(t *testing.T) {
		firewallConsumerFixture(t, "Status: inactive\n", false)
		if got := doctorFirewall(context.Background(), newDryRunRC(t)); got.Level != LevelWarn || !strings.Contains(got.Detail, "inactive") {
			t.Fatalf("known inactive firewall: %+v", got)
		}
	})
}
