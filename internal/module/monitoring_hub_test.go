package module

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"text/template"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"gopkg.in/yaml.v3"
)

func TestRenderMonitoringHubFilesPinsImagesAndLoopbackPorts(t *testing.T) {
	h := (config.MonitoringHubConfig{}).WithDefaults()
	files, err := renderMonitoringHubFiles(h, monitoringHubPassword)
	if err != nil {
		t.Fatal(err)
	}
	var compose struct {
		Services map[string]struct {
			Image string   `yaml:"image"`
			Ports []string `yaml:"ports"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(files[monitoringHubCompose], &compose); err != nil {
		t.Fatalf("parse compose: %v", err)
	}
	wantImages := map[string]string{
		"prometheus":   "prom/prometheus:v3.15.0",
		"alertmanager": "prom/alertmanager:v0.34.1",
		"grafana":      "grafana/grafana:13.2.3",
	}
	for service, image := range wantImages {
		if got := compose.Services[service].Image; got != image {
			t.Errorf("%s image = %q, want %q", service, got, image)
		}
		for _, port := range compose.Services[service].Ports {
			if !strings.HasPrefix(port, "127.0.0.1:") {
				t.Errorf("%s publishes non-loopback port %q", service, port)
			}
		}
	}
	if !strings.Contains(string(files[monitoringHubCompose]), "GF_SECURITY_ADMIN_PASSWORD__FILE") {
		t.Fatal("Grafana compose config does not use a password file")
	}
	composeText := string(files[monitoringHubCompose])
	for _, item := range []string{"user:", "0:0", "cap_drop:", "ALL", "cap_add:", "CHOWN", "no-new-privileges:true"} {
		if !strings.Contains(composeText, item) {
			t.Errorf("compose security settings missing %q", item)
		}
	}
}

func TestMonitoringHubRenderHonorsConfiguredListenAddress(t *testing.T) {
	h := (config.MonitoringHubConfig{}).WithDefaults()
	h.ListenAddress = "10.0.0.25"
	files, err := renderMonitoringHubFiles(h, monitoringHubPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(files[monitoringHubCompose]), "10.0.0.25:9090:9090") {
		t.Fatal("Prometheus is not published at the configured address")
	}
	if strings.Contains(string(files[monitoringHubCompose]), "0.0.0.0") {
		t.Fatal("compose config unexpectedly binds all interfaces")
	}
}

func TestMonitoringHubAlertmanagerRetentionFlagIsOptionalAndValidated(t *testing.T) {
	for _, test := range []struct {
		name              string
		retention         string
		wantRetentionFlag string
	}{
		{name: "unset"},
		{name: "configured", retention: "36h", wantRetentionFlag: "--data.retention=36h"},
		{name: "beyond-image-default", retention: "240h", wantRetentionFlag: "--data.retention=240h"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := (config.MonitoringHubConfig{}).WithDefaults()
			h.AlertmanagerRetention = test.retention
			files, err := renderMonitoringHubFiles(h, monitoringHubPassword)
			if err != nil {
				t.Fatal(err)
			}
			var compose struct {
				Services map[string]struct {
					Command []string `yaml:"command"`
				} `yaml:"services"`
			}
			if err := yaml.Unmarshal(files[monitoringHubCompose], &compose); err != nil {
				t.Fatalf("parse compose: %v", err)
			}
			found := false
			for _, arg := range compose.Services["alertmanager"].Command {
				if strings.HasPrefix(arg, "--data.retention=") {
					if arg != test.wantRetentionFlag {
						t.Errorf("unexpected Alertmanager retention flag %q", arg)
					}
					found = true
				}
			}
			if found != (test.wantRetentionFlag != "") {
				t.Errorf("retention flag present=%t, want=%t", found, test.wantRetentionFlag != "")
			}
		})
	}
	for _, retention := range []string{"0s", "-1h", "invalid", "1y", "999999999999999999999h"} {
		h := (config.MonitoringHubConfig{}).WithDefaults()
		h.AlertmanagerRetention = retention
		if err := validateMonitoringHubConfig(h); err == nil || !strings.Contains(err.Error(), "alertmanager_retention") {
			t.Errorf("invalid retention %q returned %v", retention, err)
		}
	}
}

func TestMonitoringHubRulesAlertOnDoctorFailuresAndGPUHealth(t *testing.T) {
	var parsed struct {
		Groups []struct {
			Rules []struct {
				Alert       string            `yaml:"alert"`
				Expr        string            `yaml:"expr"`
				For         string            `yaml:"for"`
				Labels      map[string]string `yaml:"labels"`
				Annotations map[string]string `yaml:"annotations"`
			} `yaml:"rules"`
		} `yaml:"groups"`
	}
	if err := yaml.Unmarshal([]byte(monitoringHubRules), &parsed); err != nil {
		t.Fatalf("parse alert rules YAML: %v", err)
	}
	type expectedRule struct{ expr, hold, severity string }
	want := map[string]expectedRule{
		"FleetTargetDown":             {`up{job="fleet"} == 0`, "5m", "critical"},
		"FleetFilesystemNearlyFull":   {`(node_filesystem_size_bytes{fstype!~"tmpfs|overlay|squashfs"} - node_filesystem_avail_bytes{fstype!~"tmpfs|overlay|squashfs"}) / node_filesystem_size_bytes{fstype!~"tmpfs|overlay|squashfs"} > 0.99`, "10m", "warning"},
		"RootfilesConfigurationDrift": {"rootfiles_module_satisfied == 0", "5m", "warning"},
		"RootfilesDoctorFailure":      {`rootfiles_doctor_findings{level="fail"} > 0`, "5m", "critical"},
		"RootfilesReportStale":        {`(time() - rootfiles_check_timestamp_seconds > 172800) or ((up{job="fleet",exporter="node"} == 1) unless on(host) rootfiles_check_timestamp_seconds)`, "15m", "warning"},
		"GPUXIDError":                 {"DCGM_FI_DEV_XID_ERRORS > 0", "", "critical"},
		"GPUUncorrectableECCError":    {"increase(DCGM_FI_DEV_ECC_DBE_VOL_TOTAL[5m]) > 0", "", "critical"},
		"GPUHighTemperature":          {"DCGM_FI_DEV_GPU_TEMP > 85", "10m", "warning"},
	}
	seen := make(map[string]bool, len(want))
	for _, group := range parsed.Groups {
		for _, rule := range group.Rules {
			expected, ok := want[rule.Alert]
			if !ok {
				t.Errorf("unexpected alert rule %q", rule.Alert)
				continue
			}
			seen[rule.Alert] = true
			if rule.Expr != expected.expr || rule.For != expected.hold || rule.Labels["severity"] != expected.severity {
				t.Errorf("%s changed its expression/hold/severity: expr=%q for=%q severity=%q", rule.Alert, rule.Expr, rule.For, rule.Labels["severity"])
			}
			for _, key := range []string{"summary", "description"} {
				content := rule.Annotations[key]
				if strings.TrimSpace(content) == "" {
					t.Errorf("%s has no %s annotation", rule.Alert, key)
					continue
				}
				if _, err := template.New(rule.Alert + "." + key).Parse("{{ $labels := .Labels }}{{ $value := .Value }}" + content); err != nil {
					t.Errorf("%s %s annotation template is invalid: %v", rule.Alert, key, err)
				}
			}
		}
	}
	for alert := range want {
		if !seen[alert] {
			t.Errorf("missing alert rule %q", alert)
		}
	}
	if strings.Contains(monitoringHubRules, "rootfiles_doctor_check_ok") {
		t.Fatal("doctor alert uses warning-inclusive check metric")
	}
	for _, alert := range []string{"GPUXIDError", "GPUUncorrectableECCError", "GPUHighTemperature"} {
		var summary string
		for _, group := range parsed.Groups {
			for _, rule := range group.Rules {
				if rule.Alert == alert {
					summary = rule.Annotations["summary"]
				}
			}
		}
		parsedTemplate, err := template.New(alert).Parse("{{ $labels := .Labels }}{{ $value := .Value }}" + summary)
		if err != nil {
			t.Fatal(err)
		}
		var output strings.Builder
		err = parsedTemplate.Execute(&output, map[string]any{"Labels": map[string]string{"host": "gpu-a", "gpu": "0"}, "Value": "42"})
		if err != nil {
			t.Fatalf("render %s summary: %v", alert, err)
		}
		if !strings.Contains(output.String(), "gpu-a") || !strings.Contains(output.String(), "GPU 0") {
			t.Errorf("%s summary omits host/GPU identity: %q", alert, output.String())
		}
		output.Reset()
		err = parsedTemplate.Execute(&output, map[string]any{"Labels": map[string]string{"host": "gpu-a"}, "Value": "42"})
		want := map[string]string{
			"GPUXIDError":              "GPU XID error on gpu-a",
			"GPUUncorrectableECCError": "Uncorrectable GPU ECC error on gpu-a",
			"GPUHighTemperature":       "GPU temperature high on gpu-a",
		}[alert]
		if err != nil || output.String() != want {
			t.Errorf("%s summary should omit a missing GPU label: %q, error: %v", alert, output.String(), err)
		}
		output.Reset()
		err = parsedTemplate.Execute(&output, map[string]any{"Labels": map[string]string{}, "Value": "42"})
		if err != nil || !strings.Contains(output.String(), "unknown host") {
			t.Errorf("%s summary is unsafe for missing labels: %q, error: %v", alert, output.String(), err)
		}
	}
	xidRule := func() (summary, description string) {
		for _, group := range parsed.Groups {
			for _, rule := range group.Rules {
				if rule.Alert == "GPUXIDError" {
					return rule.Annotations["summary"], rule.Annotations["description"]
				}
			}
		}
		return "", ""
	}
	xidSummary, xidDescription := xidRule()
	for _, gpuID := range []string{"0", "1"} {
		labels := map[string]string{"host": "gpu-a", "gpu": gpuID, "Hostname": "dcgm-container", "modelName": "NVIDIA H100"}
		var rendered strings.Builder
		for _, annotation := range []string{xidSummary, xidDescription} {
			parsedTemplate, err := template.New("xid").Parse("{{ $labels := .Labels }}{{ $value := .Value }}" + annotation)
			if err != nil {
				t.Fatal(err)
			}
			if err := parsedTemplate.Execute(&rendered, map[string]any{"Labels": labels, "Value": "79"}); err != nil {
				t.Fatalf("render XID annotation for GPU %s: %v", gpuID, err)
			}
			rendered.WriteByte('\n')
		}
		if !strings.Contains(rendered.String(), "GPU "+gpuID) || !strings.Contains(rendered.String(), "XID code 79") {
			t.Errorf("XID notification omits distinct GPU identity or code for GPU %s: %q", gpuID, rendered.String())
		}
	}
}

func TestMonitoringHubREADMEReceiverTemplateShowsFiringResolvedAndOptionalRunbook(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatalf("read monitoring hub README example: %v", err)
	}
	const start = "route:\n  receiver: operations"
	startAt := strings.Index(string(readme), start)
	if startAt < 0 {
		t.Fatal("README monitoring receiver example is missing")
	}
	endAt := strings.Index(string(readme[startAt:]), "\n```")
	if endAt < 0 {
		t.Fatal("README monitoring receiver example is not closed")
	}
	example := []byte(string(readme[startAt : startAt+endAt]))
	var config struct {
		Receivers []struct {
			Telegram []struct {
				SendResolved bool    `yaml:"send_resolved"`
				ParseMode    *string `yaml:"parse_mode"`
				Message      string  `yaml:"message"`
			} `yaml:"telegram_configs"`
		} `yaml:"receivers"`
	}
	if err := yaml.Unmarshal(example, &config); err != nil {
		t.Fatalf("parse README monitoring receiver example: %v", err)
	}
	if len(config.Receivers) != 1 || len(config.Receivers[0].Telegram) != 1 {
		t.Fatal("README example must define one Telegram receiver")
	}
	receiver := config.Receivers[0].Telegram[0]
	if !receiver.SendResolved {
		t.Fatal("README receiver example does not request resolved notifications")
	}
	if receiver.ParseMode == nil || *receiver.ParseMode != "" {
		t.Fatal("README Telegram receiver must explicitly select plain-text parse_mode")
	}
	if _, err := template.New("telegram-message").Parse(receiver.Message); err != nil {
		t.Fatalf("parse README Telegram message template: %v", err)
	}
	for _, expected := range []string{".Alerts.Firing", ".Alerts.Resolved", ".Annotations.summary", ".Annotations.description", ".Annotations.runbook_url"} {
		if !strings.Contains(receiver.Message, expected) {
			t.Errorf("README Telegram template does not render %q", expected)
		}
	}
	if strings.Contains(receiver.Message, "GeneratorURL") {
		t.Fatal("README Telegram template must not expose the container-local Prometheus generator URL")
	}
	type alert struct {
		Labels       map[string]string
		Annotations  map[string]string
		GeneratorURL string
	}
	type notification struct {
		Alerts struct {
			Firing   []alert
			Resolved []alert
		}
		ExternalURL string
	}
	data := notification{ExternalURL: "https://alerts.example.test"}
	for i := 0; i < 8; i++ {
		data.Alerts.Firing = append(data.Alerts.Firing, alert{
			Labels: map[string]string{"severity": "critical"},
			Annotations: map[string]string{
				"summary":     "GPU " + strconv.Itoa(i) + " XID error",
				"description": "Inspect <device & check \"driver\"> " + strings.Repeat("Inspect current device health. ", 400),
				"runbook_url": "https://runbooks.example.test/xid",
			},
			GeneratorURL: "http://prometheus:9090/graph?g0.expr=fixture",
		})
	}
	for i := 0; i < 7; i++ {
		data.Alerts.Resolved = append(data.Alerts.Resolved, alert{
			Annotations: map[string]string{"summary": "GPU " + strconv.Itoa(i) + " recovered", "description": "Condition cleared."},
		})
	}
	parsedTemplate, err := template.New("telegram-message").Parse(receiver.Message)
	if err != nil {
		t.Fatal(err)
	}
	var rendered strings.Builder
	if err := parsedTemplate.Execute(&rendered, data); err != nil {
		t.Fatalf("render synthetic grouped notification: %v", err)
	}
	message := rendered.String()
	for _, expected := range []string{"GPU 0 XID error", "GPU 1 XID error", "Runbook: https://runbooks.example.test/xid", "4 additional firing alert(s) omitted", "3 additional resolved alert(s) omitted", data.ExternalURL} {
		if !strings.Contains(message, expected) {
			t.Errorf("grouped notification omits %q: %q", expected, message)
		}
	}
	if !strings.Contains(message, "<device & check \"driver\">") || strings.Contains(message, "&lt;") || strings.Contains(message, "&amp;") {
		t.Errorf("plain-text Telegram body escaped HTML-significant annotation text: %q", message)
	}
	if strings.Contains(message, "GPU 4 XID error") {
		t.Fatal("notification rendered an alert beyond its four-alert limit")
	}
	if len(message) > 4096 {
		t.Errorf("grouped notification is %d bytes, exceeding Telegram's 4096-byte limit", len(message))
	}
	if strings.Contains(message, "prometheus:9090") {
		t.Fatal("grouped notification rendered a container-local generator URL")
	}
}

func TestMonitoringHubPromtoolReportsOnlyHostMissingScheduledTimestamp(t *testing.T) {
	promtool, err := exec.LookPath("promtool")
	if err != nil {
		t.Skip("promtool is unavailable; coordinator can run this rule test in integration validation")
	}
	dir := t.TempDir()
	rulesPath := filepath.Join(dir, "alert-rules.yaml")
	if err := os.WriteFile(rulesPath, []byte(monitoringHubRules), 0600); err != nil {
		t.Fatal(err)
	}
	timestamp := func(host, instance string) string {
		return `rootfiles_check_timestamp_seconds{job="fleet",exporter="node",host="` + host + `",instance="` + instance + `"}`
	}
	inputSeries := []any{
		map[string]string{"series": `up{job="fleet",exporter="node",host="gpu01",instance="10.0.0.11:9100"}`, "values": "1+0x20"},
		map[string]string{"series": `up{job="fleet",exporter="node",host="gpu02",instance="10.0.0.12:9100"}`, "values": "1+0x20"},
		map[string]string{"series": timestamp("gpu01", "10.0.0.11:9100"), "values": "4102444800+0x20"},
	}
	alertTests := []any{map[string]any{
		"eval_time": "20m",
		"alertname": "RootfilesReportStale",
		"exp_alerts": []any{map[string]any{
			"exp_labels": map[string]string{
				"exporter": "node", "host": "gpu02", "instance": "10.0.0.12:9100", "job": "fleet", "severity": "warning",
			},
			"exp_annotations": map[string]string{
				"summary":     "Rootfiles report stale on gpu02",
				"description": "The latest scheduled rootfiles check report is older than 48 hours or has no timestamp. Confirm the schedule and inspect the latest check result on the host.",
			},
		}},
	}}
	caseData := map[string]any{
		"rule_files":          []string{rulesPath},
		"evaluation_interval": "1m",
		"tests": []any{map[string]any{
			"interval":        "1m",
			"input_series":    inputSeries,
			"alert_rule_test": alertTests,
		}},
	}
	testData, err := yaml.Marshal(caseData)
	if err != nil {
		t.Fatal(err)
	}
	testPath := filepath.Join(dir, "rules_test.yaml")
	if err := os.WriteFile(testPath, testData, 0600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(promtool, "test", "rules", testPath).CombinedOutput()
	if err != nil {
		t.Fatalf("promtool test rules failed: %v\n%s", err, output)
	}
}

func TestParseMonitoringHubContainerStatesAcceptsArrayAndJSONLines(t *testing.T) {
	for _, output := range []string{
		`[{"Service":"prometheus","State":"running"},{"Service":"grafana","State":"exited"}]`,
		"{\"Service\":\"prometheus\",\"State\":\"running\"}\n{\"Service\":\"grafana\",\"State\":\"exited\"}\n",
	} {
		rows, err := parseMonitoringHubContainerStates(output)
		if err != nil || len(rows) != 2 {
			t.Fatalf("parse %q = %+v, %v", output, rows, err)
		}
		if rows[0].Service != "prometheus" || !monitoringHubContainerRunning(rows[0]) || monitoringHubContainerRunning(rows[1]) {
			t.Fatalf("unexpected service states: %+v", rows)
		}
	}
}

func TestMonitoringHubOwnsOnlyExactRunningComposeListener(t *testing.T) {
	rc := newRealRC(t)
	rc.Config.Modules.Monitoring.Hub.Enabled = true
	valid := `{"Service":"prometheus","State":"running","Publishers":[{"URL":"127.0.0.1","TargetPort":9090,"PublishedPort":9090,"Protocol":"tcp"}]}`
	t.Run("exact mapping", func(t *testing.T) {
		fakeMonitoringCommand(t, "docker", "#!/bin/sh\nprintf '%s\\n' '"+valid+"'\n")
		if !monitoringHubOwnsListener(context.Background(), rc, "Prometheus", 9090, tcpListener{Address: "127.0.0.1:9090", Process: "docker-proxy"}) {
			t.Fatal("exact running Compose binding was not recognized")
		}
	})
	cases := map[string]struct {
		row     string
		service string
		port    int
		addr    string
		process string
	}{
		"wrong port":                {valid, "Prometheus", 9091, "127.0.0.1:9091", "docker-proxy"},
		"wrong service":             {valid, "Grafana", 3000, "127.0.0.1:3000", "docker-proxy"},
		"wrong bind ip":             {valid, "Prometheus", 9090, "0.0.0.0:9090", "docker-proxy"},
		"wrong compose host port":   {strings.Replace(valid, `"PublishedPort":9090`, `"PublishedPort":9091`, 1), "Prometheus", 9090, "127.0.0.1:9090", "docker-proxy"},
		"wrong compose target port": {strings.Replace(valid, `"TargetPort":9090`, `"TargetPort":9091`, 1), "Prometheus", 9090, "127.0.0.1:9090", "docker-proxy"},
		"wrong compose bind ip":     {strings.Replace(valid, `"URL":"127.0.0.1"`, `"URL":"0.0.0.0"`, 1), "Prometheus", 9090, "127.0.0.1:9090", "docker-proxy"},
		"unmanaged process":         {valid, "Prometheus", 9090, "127.0.0.1:9090", "other-process"},
		"stopped service":           {strings.Replace(valid, `"running"`, `"exited"`, 1), "Prometheus", 9090, "127.0.0.1:9090", "docker-proxy"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fakeMonitoringCommand(t, "docker", "#!/bin/sh\nprintf '%s\\n' '"+tc.row+"'\n")
			if monitoringHubOwnsListener(context.Background(), rc, tc.service, tc.port, tcpListener{Address: tc.addr, Process: tc.process}) {
				t.Fatal("listener was exempted without an exact running Compose binding")
			}
		})
	}
}

func TestMonitoringHubReceiverAndPasswordContentsAreNotRendered(t *testing.T) {
	h := (config.MonitoringHubConfig{}).WithDefaults()
	h.AlertReceiverFile = "/etc/rootfiles/monitoring/receiver.yaml"
	files, err := renderMonitoringHubFiles(h, monitoringHubPassword)
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range files {
		text := string(content)
		if strings.Contains(text, "telegram-secret") || strings.Contains(text, "grafana-secret") {
			t.Fatalf("%s contains a secret payload", path)
		}
	}
	if !strings.Contains(string(files[monitoringHubCompose]), h.AlertReceiverFile) {
		t.Fatal("receiver file path is not mounted into Alertmanager")
	}
	h.TelegramBotTokenFile = "/etc/rootfiles/monitoring/telegram-bot-token"
	files, err = renderMonitoringHubFiles(h, monitoringHubPassword)
	if err != nil {
		t.Fatal(err)
	}
	var compose struct {
		Services map[string]struct {
			Volumes []struct {
				Source   string `yaml:"source"`
				Target   string `yaml:"target"`
				ReadOnly bool   `yaml:"read_only"`
			} `yaml:"volumes"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(files[monitoringHubCompose], &compose); err != nil {
		t.Fatalf("parse compose: %v", err)
	}
	containsMount := func(service, source string) bool {
		for _, volume := range compose.Services[service].Volumes {
			if volume.Source == source && volume.ReadOnly {
				return true
			}
		}
		return false
	}
	if !containsMount("alertmanager", h.AlertReceiverFile) || !containsMount("alertmanager", h.TelegramBotTokenFile) {
		t.Fatal("Alertmanager does not receive its configured secret files")
	}
	if !containsMount("grafana", monitoringHubPassword) || containsMount("prometheus", monitoringHubPassword) || containsMount("alertmanager", monitoringHubPassword) {
		t.Fatal("Grafana password file is mounted outside Grafana or missing from Grafana")
	}
}

func TestMonitoringHubRejectsUnsafeSecretFilesWithoutEchoingContents(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, []byte("never-print-this-secret"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := validateRootSecretFile(secret); err == nil || strings.Contains(err.Error(), "never-print-this-secret") {
		t.Fatalf("unsafe mode should fail without echoing content, got %v", err)
	}
	link := filepath.Join(dir, "secret-link")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	if err := validateRootSecretFile(link); err == nil || strings.Contains(err.Error(), "never-print-this-secret") {
		t.Fatalf("symlink should fail without echoing content, got %v", err)
	}
}

func TestMonitoringHubRequiresConfiguredGrafanaPasswordFileBeforeMutation(t *testing.T) {
	fakeMonitoringCommand(t, "docker", "#!/bin/sh\nexit 0\n")
	rc := newRealRC(t)
	tmp := t.TempDir()
	oldLstat := monitoringHubLstat
	monitoringHubLstat = func(path string) (os.FileInfo, error) {
		if path == tmp {
			return fakeRootOwnedInfo{mode: os.ModeDir | 0755}, nil
		}
		if strings.HasPrefix(path, tmp+string(filepath.Separator)) {
			return nil, os.ErrNotExist
		}
		return fakeRootOwnedInfo{mode: os.ModeDir | 0755}, nil
	}
	t.Cleanup(func() { monitoringHubLstat = oldLstat })
	rc.Config.Modules.Monitoring.Hub = config.MonitoringHubConfig{
		Enabled: true, DataDir: filepath.Join(tmp, "data"), TargetsFile: filepath.Join(tmp, "discovery", "targets.json"),
		GrafanaAdminPasswordFile: filepath.Join(tmp, "provided-password"),
	}
	if _, err := checkMonitoringHub(context.Background(), rc); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing configured password file should fail Check, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "data")); !os.IsNotExist(err) {
		t.Fatalf("Check mutated data directory before refusing config: %v", err)
	}
}

func TestMonitoringHubLifecycleRecoveryAndStartupFailureWithFakeCommands(t *testing.T) {
	tmp := t.TempDir()
	oldPaths := []string{monitoringHubConfigDir, monitoringHubCompose, monitoringHubUnitPath, monitoringHubPassword, monitoringHubAlertRules, monitoringHubPromConfig, monitoringHubAlertConfig, monitoringHubDatasource}
	monitoringHubConfigDir = filepath.Join(tmp, "etc", "monitoring")
	monitoringHubCompose = filepath.Join(monitoringHubConfigDir, "compose.yaml")
	monitoringHubUnitPath = filepath.Join(tmp, "systemd", "rootfiles-monitoring-hub.service")
	monitoringHubPassword = filepath.Join(monitoringHubConfigDir, "grafana-admin-password")
	monitoringHubAlertRules = filepath.Join(monitoringHubConfigDir, "alert-rules.yaml")
	monitoringHubPromConfig = filepath.Join(monitoringHubConfigDir, "prometheus.yaml")
	monitoringHubAlertConfig = filepath.Join(monitoringHubConfigDir, "alertmanager.yaml")
	monitoringHubDatasource = filepath.Join(monitoringHubConfigDir, "grafana-datasource.yaml")
	oldSecretCheck := monitoringHubSecretCheck
	oldLstat := monitoringHubLstat
	monitoringHubSecretCheck = func(path string) (bool, error) {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return false, os.ErrPermission
		}
		return true, nil
	}
	monitoringHubLstat = func(path string) (os.FileInfo, error) {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		mode := info.Mode() & os.ModeType
		mode |= info.Mode().Perm() &^ 0022
		if info.IsDir() {
			mode |= 0700
		}
		return fakeRootOwnedInfo{FileInfo: info, mode: mode}, nil
	}
	t.Cleanup(func() {
		monitoringHubConfigDir, monitoringHubCompose, monitoringHubUnitPath = oldPaths[0], oldPaths[1], oldPaths[2]
		monitoringHubPassword, monitoringHubAlertRules, monitoringHubPromConfig = oldPaths[3], oldPaths[4], oldPaths[5]
		monitoringHubAlertConfig, monitoringHubDatasource = oldPaths[6], oldPaths[7]
		monitoringHubSecretCheck = oldSecretCheck
		monitoringHubLstat = oldLstat
	})
	if err := os.MkdirAll(filepath.Dir(monitoringHubUnitPath), 0700); err != nil {
		t.Fatal(err)
	}

	stateFile := filepath.Join(tmp, "unit-active")
	stoppedFile := filepath.Join(tmp, "grafana-stopped")
	failFile := filepath.Join(tmp, "startup-fails")
	calls := filepath.Join(tmp, "calls.log")
	dockerScript := "#!/bin/sh\nprintf 'docker %s\\n' \"$*\" >> '" + calls + "'\ncase \"$4\" in\nps) if test -f '" + failFile + "'; then printf '[]\\n'; elif test -f '" + stoppedFile + "'; then printf '[{\"Service\":\"prometheus\",\"State\":\"running\"},{\"Service\":\"alertmanager\",\"State\":\"running\"}]\\n'; elif test -f '" + stateFile + "'; then printf '[{\"Service\":\"prometheus\",\"State\":\"running\"},{\"Service\":\"alertmanager\",\"State\":\"running\"},{\"Service\":\"grafana\",\"State\":\"running\"}]\\n'; else printf '[]\\n'; fi ;;\nup) rm -f '" + stoppedFile + "' ;;\nesac\nexit 0\n"
	fakeMonitoringCommand(t, "docker", dockerScript)
	fakeMonitoringCommand(t, "systemctl", "#!/bin/sh\nprintf 'systemctl %s\\n' \"$*\" >> '"+calls+"'\ncase \"$1\" in\nis-active) if test -f '"+stateFile+"'; then exit 0; else exit 3; fi ;;\nenable) docker compose -f '"+monitoringHubCompose+"' up -d || exit $?; touch '"+stateFile+"' ;;\nesac\nexit 0\n")

	rc := newRealRC(t)
	rc.Config.Modules.Monitoring.Hub = config.MonitoringHubConfig{
		Enabled: true, DataDir: filepath.Join(tmp, "data"), TargetsFile: filepath.Join(tmp, "discovery", "targets.json"),
		AlertmanagerRetention: "36h",
	}
	if _, err := applyMonitoringHub(context.Background(), rc); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	compose, err := os.ReadFile(monitoringHubCompose)
	if err != nil || !strings.Contains(string(compose), "--data.retention=36h") {
		t.Fatalf("configured Alertmanager retention was not applied: content=%q err=%v", compose, err)
	}
	changes, err := checkMonitoringHub(context.Background(), rc)
	if err != nil || len(changes) != 0 {
		t.Fatalf("check after apply = %+v, %v; want no changes", changes, err)
	}
	result, err := applyMonitoringHub(context.Background(), rc)
	if err != nil || result.Changed {
		t.Fatalf("second apply = %+v, %v; want unchanged", result, err)
	}
	if err := os.WriteFile(stoppedFile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	changes, err = checkMonitoringHub(context.Background(), rc)
	if err != nil || len(changes) != 1 || !strings.Contains(changes[0].Description, "grafana") {
		t.Fatalf("active unit with missing Grafana container should report drift, changes=%+v err=%v", changes, err)
	}
	result, err = applyMonitoringHub(context.Background(), rc)
	if err != nil || !result.Changed {
		t.Fatalf("Apply should recover missing Grafana container, result=%+v err=%v", result, err)
	}
	if err := os.WriteFile(failFile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	result, err = applyMonitoringHub(context.Background(), rc)
	if err == nil || !strings.Contains(err.Error(), "prometheus, alertmanager, grafana") {
		t.Fatalf("Apply should fail when started unit still has no running services, result=%+v err=%v", result, err)
	}
	log, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(log), "compose -f "+monitoringHubCompose+" up -d") != 3 {
		t.Fatalf("compose should start, recover and retry once: %s", log)
	}
}

type fakeRootOwnedInfo struct {
	os.FileInfo
	mode os.FileMode
}

func (fakeRootOwnedInfo) Sys() any { return &syscall.Stat_t{Uid: 0} }

func (info fakeRootOwnedInfo) Mode() os.FileMode {
	if info.mode != 0 {
		return info.mode
	}
	return info.FileInfo.Mode()
}

func (info fakeRootOwnedInfo) IsDir() bool { return info.Mode().IsDir() }

type fakeUserOwnedInfo struct{ os.FileInfo }

func (fakeUserOwnedInfo) Sys() any { return &syscall.Stat_t{Uid: 1001} }

func TestMonitoringHubDataDirectoryRequiresRootWritableDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	oldLstat := monitoringHubLstat
	t.Cleanup(func() { monitoringHubLstat = oldLstat })
	link := filepath.Join(dir, "data-link")
	monitoringHubLstat = func(candidate string) (os.FileInfo, error) {
		if filepath.Clean(candidate) == filepath.Clean(path) || filepath.Clean(candidate) == filepath.Clean(link) {
			info, err := os.Lstat(candidate)
			if err != nil {
				return nil, err
			}
			return fakeRootOwnedInfo{FileInfo: info}, nil
		}
		return fakeRootOwnedInfo{mode: os.ModeDir | 0755}, nil
	}
	exists, err := validateMonitoringDataDir(path)
	if err != nil || !exists {
		t.Fatalf("root-owned writable data directory rejected: exists=%v err=%v", exists, err)
	}
	if err := os.Chmod(path, 0500); err != nil {
		t.Fatal(err)
	}
	if _, err := validateMonitoringDataDir(path); err == nil || !strings.Contains(err.Error(), "read, write and execute") {
		t.Fatalf("non-writable data directory should be refused, got %v", err)
	}
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := validateMonitoringDataDir(link); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("symlink data directory should be refused, got %v", err)
	}
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	monitoringHubLstat = func(candidate string) (os.FileInfo, error) {
		if filepath.Clean(candidate) == filepath.Clean(path) {
			info, err := os.Lstat(candidate)
			if err != nil {
				return nil, err
			}
			return fakeUserOwnedInfo{FileInfo: info}, nil
		}
		return fakeRootOwnedInfo{mode: os.ModeDir | 0755}, nil
	}
	if _, err := validateMonitoringDataDir(path); err == nil || !strings.Contains(err.Error(), "owned by root") {
		t.Fatalf("non-root-owned data directory should be refused, got %v", err)
	}
	monitoringHubLstat = func(candidate string) (os.FileInfo, error) {
		if filepath.Clean(candidate) == filepath.Clean(dir) {
			return fakeRootOwnedInfo{mode: os.ModeDir | 0777}, nil
		}
		return fakeRootOwnedInfo{mode: os.ModeDir | 0755}, nil
	}
	if _, err := validateMonitoringDataDir(path); err == nil || !strings.Contains(err.Error(), "parent is unsafe") {
		t.Fatalf("writable data directory parent should be refused, got %v", err)
	}
}

func TestMonitoringHubDiscoveryFilesystemTrustsOnlyRootOwnedNonsymlinkParents(t *testing.T) {
	oldLstat := monitoringHubLstat
	oldReadDir := monitoringHubReadDir
	t.Cleanup(func() {
		monitoringHubLstat = oldLstat
		monitoringHubReadDir = oldReadDir
	})
	monitoringHubReadDir = func(string) ([]os.DirEntry, error) { return []os.DirEntry{}, nil }

	standardDir := "/etc/rootfiles/monitoring/discovery"
	standardTarget := filepath.Join(standardDir, "targets.json")
	monitoringHubLstat = func(path string) (os.FileInfo, error) {
		if path == standardTarget {
			return nil, os.ErrNotExist
		}
		return fakeRootOwnedInfo{mode: os.ModeDir | 0755}, nil
	}
	if exists, err := validateMonitoringDiscoveryFilesystem(standardTarget); err != nil || exists {
		t.Fatalf("root-owned standard discovery path should allow an absent file: exists=%v err=%v", exists, err)
	}
	monitoringHubLstat = func(path string) (os.FileInfo, error) {
		if path == standardTarget {
			return fakeRootOwnedInfo{mode: 0644}, nil
		}
		return fakeRootOwnedInfo{mode: os.ModeDir | 0755}, nil
	}
	if exists, err := validateMonitoringDiscoveryFilesystem(standardTarget); err != nil || !exists {
		t.Fatalf("root-owned regular target should pass: exists=%v err=%v", exists, err)
	}
	monitoringHubLstat = func(path string) (os.FileInfo, error) {
		if path == standardTarget {
			return fakeRootOwnedInfo{mode: 0666}, nil
		}
		return fakeRootOwnedInfo{mode: os.ModeDir | 0755}, nil
	}
	if _, err := validateMonitoringDiscoveryFilesystem(standardTarget); err == nil || !strings.Contains(err.Error(), "group- or world-writable") {
		t.Fatalf("writable target file should be refused, got %v", err)
	}

	symlinkDir := "/srv/monitoring/discovery"
	symlinkTarget := filepath.Join(symlinkDir, "targets.json")
	monitoringHubLstat = func(path string) (os.FileInfo, error) {
		if path == symlinkDir {
			return fakeRootOwnedInfo{mode: os.ModeSymlink | 0777}, nil
		}
		return fakeRootOwnedInfo{mode: os.ModeDir | 0755}, nil
	}
	if _, err := validateMonitoringDiscoveryFilesystem(symlinkTarget); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink discovery parent should be refused, got %v", err)
	}

	untrustedParent := "/srv/operator-writable"
	untrustedTarget := filepath.Join(untrustedParent, "discovery", "targets.json")
	monitoringHubLstat = func(path string) (os.FileInfo, error) {
		if path == untrustedParent {
			return fakeRootOwnedInfo{mode: os.ModeDir | 0777}, nil
		}
		return fakeRootOwnedInfo{mode: os.ModeDir | 0755}, nil
	}
	if _, err := validateMonitoringDiscoveryFilesystem(untrustedTarget); err == nil || !strings.Contains(err.Error(), "group/world") {
		t.Fatalf("group/world-writable parent should be refused, got %v", err)
	}

	target := "/srv/monitoring/discovery/targets.json"
	monitoringHubLstat = func(path string) (os.FileInfo, error) {
		if path == target {
			return fakeRootOwnedInfo{mode: os.ModeSymlink | 0777}, nil
		}
		return fakeRootOwnedInfo{mode: os.ModeDir | 0755}, nil
	}
	if _, err := validateMonitoringDiscoveryFilesystem(target); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink target file should be refused, got %v", err)
	}
}

func TestMonitoringDiscoveryDirectoryContainsOnlyTargetsFile(t *testing.T) {
	t.Run("configured target only", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "targets.json")
		if err := os.WriteFile(target, []byte("[]\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := validateMonitoringDiscoveryContents(dir, target); err != nil {
			t.Fatalf("target-only directory rejected: %v", err)
		}
	})

	t.Run("credential sibling", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "targets.json")
		if err := os.WriteFile(target, []byte("[]\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "credentials.pem"), []byte("test credential"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := validateMonitoringDiscoveryContents(dir, target); err == nil {
			t.Fatal("credential sibling was accepted for a directory bind mount")
		}
	})

	t.Run("symlink and directory siblings", func(t *testing.T) {
		for _, name := range []string{"secret-link", "subdirectory", "targets.json.tmp.ABC"} {
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				target := filepath.Join(dir, "targets.json")
				if err := os.WriteFile(target, []byte("[]\n"), 0600); err != nil {
					t.Fatal(err)
				}
				entry := filepath.Join(dir, name)
				switch name {
				case "secret-link":
					if err := os.Symlink(filepath.Join(t.TempDir(), "outside-secret"), entry); err != nil {
						t.Fatal(err)
					}
				case "subdirectory":
					if err := os.Mkdir(entry, 0700); err != nil {
						t.Fatal(err)
					}
				default:
					if err := os.WriteFile(entry, []byte("staging"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := validateMonitoringDiscoveryContents(dir, target); err == nil {
					t.Fatalf("unexpected sibling %q was accepted", name)
				}
			})
		}
	})
}

func TestMonitoringHubUsesDiscoveryDirectoryForAtomicTargetReplacement(t *testing.T) {
	h := (config.MonitoringHubConfig{}).WithDefaults()
	discoveryDir := filepath.Join(t.TempDir(), "discovery")
	h.TargetsFile = filepath.Join(discoveryDir, "targets.prod.json")
	files, err := renderMonitoringHubFiles(h, monitoringHubPassword)
	if err != nil {
		t.Fatal(err)
	}
	var compose struct {
		Services map[string]struct {
			Volumes []struct {
				Source string `yaml:"source"`
				Target string `yaml:"target"`
			} `yaml:"volumes"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(files[monitoringHubCompose], &compose); err != nil {
		t.Fatalf("parse compose: %v", err)
	}
	found := false
	for _, volume := range compose.Services["prometheus"].Volumes {
		if volume.Source == discoveryDir && volume.Target == "/etc/prometheus/discovery" {
			found = true
		}
		if volume.Source == h.TargetsFile && volume.Target == "/etc/prometheus/targets.json" {
			t.Fatal("targets file is bind-mounted directly, so atomic replacement may be hidden")
		}
	}
	if !found {
		t.Fatalf("Prometheus does not mount discovery directory %q", discoveryDir)
	}
	if !strings.Contains(string(files[monitoringHubPromConfig]), "/etc/prometheus/discovery/targets.prod.json") {
		t.Fatal("Prometheus file_sd config does not point into the mounted directory")
	}

	if err := os.MkdirAll(discoveryDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.TargetsFile, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	replacement := h.TargetsFile + ".new"
	if err := os.WriteFile(replacement, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, h.TargetsFile); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(h.TargetsFile)
	if err != nil || string(got) != "new" {
		t.Fatalf("directory-mounted targets replacement = %q, %v", got, err)
	}
}

func TestMonitoringHubRejectsBroadDiscoveryMounts(t *testing.T) {
	h := (config.MonitoringHubConfig{}).WithDefaults()
	h.TargetsFile = "/etc/rootfiles/monitoring/targets.json"
	if err := validateMonitoringDiscoveryDir(h, ""); err == nil {
		t.Fatal("config parent directory must not be bind-mounted")
	}
	h.TargetsFile = "/tmp/discovery/targets.json"
	h.AlertReceiverFile = "/tmp/discovery/receiver.yaml"
	if err := validateMonitoringDiscoveryDir(h, ""); err == nil {
		t.Fatal("discovery bind mount must not contain receiver secrets")
	}
}
