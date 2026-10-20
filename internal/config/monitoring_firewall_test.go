package config

import (
	"strings"
	"testing"
)

func TestValidateExporterFirewall(t *testing.T) {
	valid := MonitoringConfig{Enabled: true, NodeExporter: true, ListenAddress: "192.0.2.5", AllowFrom: []string{"198.51.100.200/32"}, ExporterFirewall: true}
	errFor := func(m MonitoringConfig) string {
		var msgs []string
		c := &Config{}
		c.Modules.Monitoring = m
		c.validateMonitoring(func(f string, a ...any) { msgs = append(msgs, f) })
		return strings.Join(msgs, "; ")
	}
	if got := errFor(valid); got != "" {
		t.Fatalf("valid config rejected: %s", got)
	}
	for name, mutate := range map[string]func(*MonitoringConfig){
		"no listen_address":   func(m *MonitoringConfig) { m.ListenAddress = "" },
		"IPv6 listen_address": func(m *MonitoringConfig) { m.ListenAddress = "2001:db8::5" },
		"two sources":         func(m *MonitoringConfig) { m.AllowFrom = append(m.AllowFrom, "198.51.100.201/32") },
		"no source":           func(m *MonitoringConfig) { m.AllowFrom = nil },
		"subnet source":       func(m *MonitoringConfig) { m.AllowFrom = []string{"198.51.100.0/24"} },
		"IPv6 source":         func(m *MonitoringConfig) { m.AllowFrom = []string{"2001:db8::1/128"} },
		"no exporter":         func(m *MonitoringConfig) { m.NodeExporter = false },
		"custom port":         func(m *MonitoringConfig) { m.NodeExporterPort = 9101 },
	} {
		m := valid
		m.AllowFrom = append([]string(nil), valid.AllowFrom...)
		mutate(&m)
		if got := errFor(m); !strings.Contains(got, "exporter_firewall") {
			t.Errorf("%s: want an exporter_firewall error, got %q", name, got)
		}
	}
	off := valid
	off.ExporterFirewall, off.AllowFrom = false, []string{"198.51.100.0/24", "198.51.100.201/32"}
	if got := errFor(off); got != "" {
		t.Fatalf("exporter_firewall off must not restrict allow_from: %s", got)
	}
}
