package config

const MonitoringDiscoveryDir = "/etc/rootfiles/monitoring/discovery"

func (m MonitoringConfig) NodePort() int {
	if m.NodeExporterPort == 0 {
		return 9100
	}
	return m.NodeExporterPort
}

func (m MonitoringConfig) DCGMPort() int {
	if m.DCGMExporterPort == 0 {
		return 9400
	}
	return m.DCGMExporterPort
}

func (m MonitoringConfig) ExporterImage() string {
	if m.DCGMExporterImage == "" {
		return "nvcr.io/nvidia/k8s/dcgm-exporter:4.8.4"
	}
	return m.DCGMExporterImage
}

func (h MonitoringHubConfig) WithDefaults() MonitoringHubConfig {
	if h.DataDir == "" {
		h.DataDir = "/data/monitoring"
	}
	if h.Retention == "" {
		h.Retention = "30d"
	}
	if h.TargetsFile == "" {
		h.TargetsFile = MonitoringDiscoveryDir + "/targets.json"
	}
	if h.ListenAddress == "" {
		h.ListenAddress = "127.0.0.1"
	}
	if h.GrafanaPort == 0 {
		h.GrafanaPort = 3000
	}
	if h.PrometheusPort == 0 {
		h.PrometheusPort = 9090
	}
	if h.AlertmanagerPort == 0 {
		h.AlertmanagerPort = 9093
	}
	if h.PrometheusImage == "" {
		h.PrometheusImage = "prom/prometheus:v3.15.0"
	}
	if h.AlertmanagerImage == "" {
		h.AlertmanagerImage = "prom/alertmanager:v0.34.1"
	}
	if h.GrafanaImage == "" {
		h.GrafanaImage = "grafana/grafana:13.2.3"
	}
	if h.GrafanaAdminPasswordFile == "" {
		h.GrafanaAdminPasswordFile = "/etc/rootfiles/monitoring/grafana-admin-password"
	}
	return h
}
