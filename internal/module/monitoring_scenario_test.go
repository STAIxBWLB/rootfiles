package module

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

// TestWriteMonitoringScenarioFixtures writes the real renderer output for the
// Docker acceptance scenario. It is inert during ordinary unit runs; CI opts
// into fixture generation by setting ROOTFILES_MONITORING_SCENARIO_DIR.
func TestWriteMonitoringScenarioFixtures(t *testing.T) {
	root := os.Getenv("ROOTFILES_MONITORING_SCENARIO_DIR")
	if root == "" {
		t.Skip("set ROOTFILES_MONITORING_SCENARIO_DIR to generate Docker scenario fixtures")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("resolve fixture directory: %v", err)
	}
	root = filepath.Clean(root)
	if root == string(filepath.Separator) {
		t.Fatal("refusing to write monitoring fixtures at filesystem root")
	}

	configDir := filepath.Join(root, "etc", "rootfiles", "monitoring")
	dataDir := filepath.Join(root, "var", "lib", "rootfiles", "monitoring")
	passwordFile := filepath.Join(configDir, "grafana-admin-password")
	telegramTokenFile := filepath.Join(configDir, "telegram-bot-token")
	targetsFile := filepath.Join(configDir, "discovery", "targets.json")

	old := struct {
		configDir, compose, unit, password, rules, prom, alert, datasource string
	}{
		configDir:  monitoringHubConfigDir,
		compose:    monitoringHubCompose,
		unit:       monitoringHubUnitPath,
		password:   monitoringHubPassword,
		rules:      monitoringHubAlertRules,
		prom:       monitoringHubPromConfig,
		alert:      monitoringHubAlertConfig,
		datasource: monitoringHubDatasource,
	}
	monitoringHubConfigDir = configDir
	monitoringHubCompose = filepath.Join(configDir, "compose.yaml")
	monitoringHubUnitPath = filepath.Join(configDir, "rootfiles-monitoring-hub.service")
	monitoringHubPassword = passwordFile
	monitoringHubAlertRules = filepath.Join(configDir, "alert-rules.yaml")
	monitoringHubPromConfig = filepath.Join(configDir, "prometheus.yaml")
	monitoringHubAlertConfig = filepath.Join(configDir, "alertmanager.yaml")
	monitoringHubDatasource = filepath.Join(configDir, "grafana-datasource.yaml")
	t.Cleanup(func() {
		monitoringHubConfigDir = old.configDir
		monitoringHubCompose = old.compose
		monitoringHubUnitPath = old.unit
		monitoringHubPassword = old.password
		monitoringHubAlertRules = old.rules
		monitoringHubPromConfig = old.prom
		monitoringHubAlertConfig = old.alert
		monitoringHubDatasource = old.datasource
	})

	for _, dir := range append([]string{configDir, filepath.Dir(targetsFile), dataDir}, monitoringHubDataDirs(dataDir)...) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("create fixture directory %s: %v", dir, err)
		}
	}

	h := (config.MonitoringHubConfig{
		Enabled:              true,
		DataDir:              dataDir,
		Retention:            "30d",
		TargetsFile:          targetsFile,
		AlertReceiverFile:    filepath.Join(configDir, "receiver.yaml"),
		TelegramBotTokenFile: telegramTokenFile,
		ListenAddress:        "127.0.0.1",
	}).WithDefaults()
	files, err := renderMonitoringHubFiles(h, passwordFile)
	if err != nil {
		t.Fatalf("render monitoring hub fixture: %v", err)
	}
	for path, content := range files {
		path = filepath.Clean(path)
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("rendered path escapes fixture directory: %s", path)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("create rendered file directory for %s: %v", path, err)
		}
		if err := os.WriteFile(path, content, 0644); err != nil {
			t.Fatalf("write rendered fixture %s: %v", path, err)
		}
	}

	// The receiver and credentials are scenario-only inert fixtures. No real
	// Telegram token or Grafana password is read or written.
	for path, content := range map[string][]byte{
		passwordFile:        []byte("scenario-only-grafana-password\n"),
		telegramTokenFile:   []byte("scenario-only-not-a-telegram-token\n"),
		h.AlertReceiverFile: []byte("route:\n  receiver: fixture-noop\nreceivers:\n  - name: fixture-noop\n"),
	} {
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatalf("write scenario-only fixture %s: %v", path, err)
		}
	}
	targets, err := json.MarshalIndent([]any{map[string]any{
		"targets": []string{"prometheus:9090"},
		"labels":  map[string]string{"host": "monitoring-scenario-prometheus", "exporter": "scenario"},
	}}, "", "  ")
	if err != nil {
		t.Fatalf("encode file-discovery fixture: %v", err)
	}
	if err := os.WriteFile(targetsFile, append(targets, '\n'), 0644); err != nil {
		t.Fatalf("write file-discovery fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "fixture-root.txt"), []byte(root+"\n"), 0644); err != nil {
		t.Fatalf("write fixture root marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "dcgm-image.txt"), []byte((config.MonitoringConfig{}).ExporterImage()+"\n"), 0644); err != nil {
		t.Fatalf("write production default image selector: %v", err)
	}
}
