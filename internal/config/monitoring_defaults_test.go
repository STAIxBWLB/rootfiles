package config

import "testing"

func TestExporterImageDefaultAndExplicitPin(t *testing.T) {
	if got := (MonitoringConfig{}).ExporterImage(); got != "nvcr.io/nvidia/k8s/dcgm-exporter:4.8.4" {
		t.Fatalf("default image %q", got)
	}
	for _, image := range []string{"nvcr.io/nvidia/k8s/dcgm-exporter:3.3.8-3.6.0-ubuntu22.04", "registry.example/exporter@sha256:0123456789abcdef"} {
		if got := (MonitoringConfig{DCGMExporterImage: image}).ExporterImage(); got != image {
			t.Fatalf("explicit pin changed: %q -> %q", image, got)
		}
	}
}
