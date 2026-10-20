package config

import (
	"net/netip"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var monitoringImage = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9./:_@-]*$`)
var prometheusRetention = regexp.MustCompile(`^([1-9][0-9]*)(ms|s|m|h|d|w|y)$`)
var alertmanagerRetentionPart = regexp.MustCompile(`^([0-9]+)(ns|us|µs|μs|ms|s|m|h|d|w)`)

// IsValidPrometheusDuration reports whether value is a positive Prometheus duration
// that fits in time.Duration.
func IsValidPrometheusDuration(value string) bool {
	parts := prometheusRetention.FindStringSubmatch(value)
	if len(parts) != 3 {
		return false
	}
	amount, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return false
	}
	unit := map[string]time.Duration{
		"ms": time.Millisecond,
		"s":  time.Second,
		"m":  time.Minute,
		"h":  time.Hour,
		"d":  24 * time.Hour,
		"w":  7 * 24 * time.Hour,
		"y":  365 * 24 * time.Hour,
	}[parts[2]]
	const maxDuration = int64(1<<63 - 1)
	return unit > 0 && amount <= uint64(maxDuration/int64(unit))
}

// IsValidAlertmanagerDuration accepts positive integer duration components
// supported by the pinned Alertmanager Kingpin parser.
func IsValidAlertmanagerDuration(value string) bool {
	units := map[string]uint64{
		"ns": 1,
		"us": 1_000,
		"µs": 1_000,
		"μs": 1_000,
		"ms": 1_000_000,
		"s":  1_000_000_000,
		"m":  60 * 1_000_000_000,
		"h":  60 * 60 * 1_000_000_000,
		"d":  24 * 60 * 60 * 1_000_000_000,
		"w":  7 * 24 * 60 * 60 * 1_000_000_000,
	}
	const maxDuration = uint64(1<<63 - 1)
	var total uint64
	for value != "" {
		parts := alertmanagerRetentionPart.FindStringSubmatch(value)
		if len(parts) != 3 {
			return false
		}
		amount, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			return false
		}
		unit := units[parts[2]]
		if amount > maxDuration/unit {
			return false
		}
		component := amount * unit
		if total > maxDuration-component {
			return false
		}
		total += component
		value = value[len(parts[0]):]
	}
	return total > 0
}

func (c *Config) validateMonitoring(add func(string, ...any)) {
	m := c.Modules.Monitoring
	port := func(name string, p int) {
		if p < 1 || p > 65535 {
			add("modules.monitoring.%s: port out of range", name)
		}
	}
	ip := func(name, value string) {
		if value != "" {
			if _, err := netip.ParseAddr(value); err != nil {
				add("modules.monitoring.%s: invalid IP address", name)
			}
		}
	}
	path := func(name, value string) {
		if value != "" && (!filepath.IsAbs(value) || strings.ContainsAny(value, "\r\n\x00")) {
			add("modules.monitoring.%s: invalid absolute path", name)
		}
	}
	image := func(name, value string) {
		if !monitoringImage.MatchString(value) || (!strings.Contains(value, "@sha256:") && !strings.Contains(filepath.Base(value), ":")) || strings.HasSuffix(value, ":latest") {
			add("modules.monitoring.%s: pinned image reference required", name)
		}
	}
	port("node_exporter_port", m.NodePort())
	port("dcgm_exporter_port", m.DCGMPort())
	if m.NodeExporter && m.DCGMExporter && m.NodePort() == m.DCGMPort() {
		add("modules.monitoring: exporter ports must differ")
	}
	ip("listen_address", m.ListenAddress)
	for _, cidr := range m.AllowFrom {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			add("modules.monitoring.allow_from: invalid CIDR")
		}
	}
	if m.DCGMExporter {
		image("dcgm_exporter_image", m.ExporterImage())
	}
	if m.ExporterFirewall {
		addr, err := netip.ParseAddr(m.ListenAddress)
		switch {
		case err != nil || !addr.Is4():
			add("modules.monitoring.exporter_firewall: listen_address must be an IPv4 address")
		case !m.NodeExporter && !m.DCGMExporter:
			add("modules.monitoring.exporter_firewall: enable node_exporter or dcgm_exporter")
		case m.NodePort() != 9100 || m.DCGMPort() != 9400:
			add("modules.monitoring.exporter_firewall: requires the default exporter ports 9100 and 9400")
		}
		if len(m.AllowFrom) != 1 {
			add("modules.monitoring.exporter_firewall: allow_from must list exactly one address")
		} else if p, err := netip.ParsePrefix(m.AllowFrom[0]); err != nil || !p.Addr().Is4() || p.Bits() != 32 || p.String() != m.AllowFrom[0] {
			add("modules.monitoring.exporter_firewall: allow_from must be one IPv4 /32 such as 192.0.2.10/32")
		}
	}
	if m.Hub.Enabled {
		h := m.Hub.WithDefaults()
		ip("hub.listen_address", h.ListenAddress)
		port("hub.grafana_port", h.GrafanaPort)
		port("hub.prometheus_port", h.PrometheusPort)
		port("hub.alertmanager_port", h.AlertmanagerPort)
		ports := map[int]bool{}
		for _, p := range []int{h.GrafanaPort, h.PrometheusPort, h.AlertmanagerPort} {
			if ports[p] {
				add("modules.monitoring.hub: ports must differ")
			}
			ports[p] = true
		}
		if m.NodeExporter && ports[m.NodePort()] || m.DCGMExporter && ports[m.DCGMPort()] {
			add("modules.monitoring: hub and exporter ports must differ")
		}
		path("hub.data_dir", h.DataDir)
		if err := ValidateMonitoringDiscoveryPath(h.TargetsFile, h.DataDir, MonitoringDiscoveryDir,
			h.AlertReceiverFile, h.TelegramBotTokenFile, h.GrafanaAdminPasswordFile, c.Modules.Cloudflared.TunnelTokenFile); err != nil {
			add("modules.monitoring.hub: %v", err)
		}
		path("hub.alert_receiver_file", h.AlertReceiverFile)
		path("hub.grafana_admin_password_file", h.GrafanaAdminPasswordFile)
		path("hub.telegram_bot_token_file", h.TelegramBotTokenFile)
		if filepath.Clean(h.DataDir) == "/" {
			add("modules.monitoring.hub.data_dir: refusing filesystem root")
		}
		if !IsValidPrometheusDuration(h.Retention) {
			add("modules.monitoring.hub.retention: invalid duration")
		}
		if h.AlertmanagerRetention != "" && !IsValidAlertmanagerDuration(h.AlertmanagerRetention) {
			add("modules.monitoring.hub.alertmanager_retention: invalid positive duration")
		}
		image("hub.prometheus_image", h.PrometheusImage)
		image("hub.alertmanager_image", h.AlertmanagerImage)
		image("hub.grafana_image", h.GrafanaImage)
	}
	seen := map[string]bool{}
	if len(c.Users.FleetSudoUsers) > 0 && !c.Modules.Users.Enabled {
		add("users.fleet_sudo_users: users module must be enabled")
	}
	for _, user := range c.Users.FleetSudoUsers {
		if !validAccountName.MatchString(user) || len(user) > 32 || seen[user] {
			add("users.fleet_sudo_users: invalid or duplicate account")
		}
		seen[user] = true
	}
}
