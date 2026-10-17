package module

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

const (
	monitoringHubUnit                  = "rootfiles-monitoring-hub.service"
	monitoringHubBotTokenContainerPath = "/run/secrets/telegram-bot-token"
)

var (
	monitoringHubConfigDir   = "/etc/rootfiles/monitoring"
	monitoringHubCompose     = "/etc/rootfiles/monitoring/compose.yaml"
	monitoringHubUnitPath    = "/etc/systemd/system/rootfiles-monitoring-hub.service"
	monitoringHubPassword    = "/etc/rootfiles/monitoring/grafana-admin-password"
	monitoringHubAlertRules  = "/etc/rootfiles/monitoring/alert-rules.yaml"
	monitoringHubPromConfig  = "/etc/rootfiles/monitoring/prometheus.yaml"
	monitoringHubAlertConfig = "/etc/rootfiles/monitoring/alertmanager.yaml"
	monitoringHubDatasource  = "/etc/rootfiles/monitoring/grafana-datasource.yaml"
	monitoringHubSecretCheck = validateOptionalRootSecretFile
	monitoringHubLstat       = os.Lstat
	monitoringHubReadDir     = os.ReadDir
)

// checkMonitoringHub is read-only and is also used by monitoring's aggregate
// preflight before any exporter or hub mutation occurs.
func checkMonitoringHub(ctx context.Context, rc *RunContext) ([]Change, error) {
	rawHub := rc.Config.Modules.Monitoring.Hub
	h := rawHub.WithDefaults()
	if !h.Enabled {
		return nil, nil
	}
	if err := validateMonitoringHubConfig(h); err != nil {
		return nil, err
	}
	if err := validateMonitoringDiscoveryDir(h, rc.Config.Modules.Cloudflared.TunnelTokenFile); err != nil {
		return nil, err
	}
	targetsExist, err := validateMonitoringDiscoveryFilesystem(h.TargetsFile)
	if err != nil {
		return nil, err
	}
	if !rc.Runner.CommandExists("docker") {
		return nil, errors.New("monitoring hub requires Docker; enable the docker module first")
	}
	if _, err := rc.Runner.Query(ctx, "docker", "compose", "version"); err != nil {
		return nil, fmt.Errorf("monitoring hub requires the Docker Compose plugin: %w", err)
	}
	if h.AlertReceiverFile != "" {
		if err := validateRootSecretFile(h.AlertReceiverFile); err != nil {
			return nil, fmt.Errorf("monitoring hub alert receiver file: %w", err)
		}
	}
	if h.TelegramBotTokenFile != "" {
		if err := validateRootSecretFile(h.TelegramBotTokenFile); err != nil {
			return nil, errors.New("monitoring hub Telegram bot token file must be root-owned with mode 0600 and cannot be a symlink")
		}
	}
	passwordFile := h.GrafanaAdminPasswordFile
	if rawHub.GrafanaAdminPasswordFile == "" {
		passwordFile = monitoringHubPassword
	}
	passwordExists, err := monitoringHubSecretCheck(passwordFile)
	if err != nil {
		return nil, fmt.Errorf("monitoring hub Grafana password file: %w", err)
	}
	if rawHub.GrafanaAdminPasswordFile != "" && !passwordExists {
		return nil, errors.New("configured Grafana password file does not exist")
	}
	var changes []Change
	files, err := renderMonitoringHubFiles(h, passwordFile)
	if err != nil {
		return nil, err
	}
	configDrift := false
	for path, expected := range files {
		current, err := rc.Runner.ReadFile(path)
		if err != nil || string(current) != string(expected) {
			configDrift = true
			break
		}
	}
	unit, err := rc.Runner.ReadFile(monitoringHubUnitPath)
	if err != nil || string(unit) != monitoringHubSystemdUnit(monitoringHubCompose) {
		configDrift = true
	}
	if configDrift {
		changes = append(changes, Change{Description: "Configure Prometheus, Alertmanager, Grafana and the managed monitoring hub unit"})
	}
	if !passwordExists {
		changes = append(changes, Change{Description: "Create a root-only Grafana administrator password file"})
	}
	for _, dir := range append([]string{h.DataDir}, monitoringHubDataDirs(h.DataDir)...) {
		exists, err := validateMonitoringDataDir(dir)
		if err != nil {
			return nil, err
		}
		if !exists {
			changes = append(changes, Change{Description: "Create monitoring data directory " + dir})
		}
	}
	if !targetsExist {
		changes = append(changes, Change{Description: "Create empty Prometheus file discovery targets"})
	}
	serviceChanges, err := monitoringHubServiceChanges(ctx, rc)
	if err != nil {
		return nil, err
	}
	changes = append(changes, serviceChanges...)
	if !unitActive(ctx, rc) {
		changes = append(changes, Change{Description: "Start the monitoring hub stack", Command: "systemctl enable --now " + monitoringHubUnit})
	}
	return changes, nil
}

func applyMonitoringHub(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	rawHub := rc.Config.Modules.Monitoring.Hub
	h := rawHub.WithDefaults()
	if !h.Enabled {
		return &ApplyResult{}, nil
	}
	initialChanges, err := checkMonitoringHub(ctx, rc)
	if err != nil {
		return nil, err
	}
	changed := len(initialChanges) > 0
	passwordFile := h.GrafanaAdminPasswordFile
	if rawHub.GrafanaAdminPasswordFile == "" {
		passwordFile = monitoringHubPassword
	}
	if err := rc.Runner.MkdirAll(monitoringHubConfigDir, 0700); err != nil {
		return nil, fmt.Errorf("creating monitoring configuration directory: %w", err)
	}
	if err := rc.Runner.MkdirAll(h.DataDir, 0750); err != nil {
		return nil, fmt.Errorf("creating monitoring data directory: %w", err)
	}
	for _, dir := range monitoringHubDataDirs(h.DataDir) {
		if err := rc.Runner.MkdirAll(dir, 0750); err != nil {
			return nil, fmt.Errorf("creating monitoring data directory: %w", err)
		}
	}
	targetsExist, err := validateMonitoringDiscoveryFilesystem(h.TargetsFile)
	if err != nil {
		return nil, err
	}
	if !targetsExist {
		if err := rc.Runner.MkdirAll(filepath.Dir(h.TargetsFile), 0755); err != nil {
			return nil, fmt.Errorf("creating targets directory: %w", err)
		}
		if err := rc.Runner.WriteFile(h.TargetsFile, []byte("[]\n"), 0644); err != nil {
			return nil, fmt.Errorf("creating empty targets file: %w", err)
		}
	}
	if _, err := monitoringHubSecretCheck(passwordFile); err != nil {
		return nil, fmt.Errorf("monitoring hub Grafana password file: %w", err)
	}
	if rawHub.GrafanaAdminPasswordFile == "" && !rc.Runner.FileExists(passwordFile) {
		secret, err := newMonitoringSecret()
		if err != nil {
			return nil, fmt.Errorf("generating Grafana administrator password: %w", err)
		}
		if err := rc.Runner.MkdirAll(filepath.Dir(passwordFile), 0700); err != nil {
			return nil, fmt.Errorf("creating Grafana password directory: %w", err)
		}
		if err := rc.Runner.WriteFile(passwordFile, []byte(secret+"\n"), 0600); err != nil {
			return nil, fmt.Errorf("writing Grafana administrator password file: %w", err)
		}
	}

	files, err := renderMonitoringHubFiles(h, passwordFile)
	if err != nil {
		return nil, err
	}
	written := map[string]bool{}
	for path, content := range files {
		current, readErr := rc.Runner.ReadFile(path)
		if readErr == nil && string(current) == string(content) {
			continue
		}
		if err := rc.Runner.WriteFile(path, content, 0644); err != nil {
			return nil, fmt.Errorf("writing monitoring hub configuration %s: %w", path, err)
		}
		written[path] = true
		changed = true
	}
	unit := monitoringHubSystemdUnit(monitoringHubCompose)
	currentUnit, unitErr := rc.Runner.ReadFile(monitoringHubUnitPath)
	unitChanged := unitErr != nil || string(currentUnit) != unit
	if unitChanged {
		if err := rc.Runner.WriteFile(monitoringHubUnitPath, []byte(unit), 0644); err != nil {
			return nil, fmt.Errorf("writing monitoring hub systemd unit: %w", err)
		}
		changed = true
	}
	if rc.DryRun {
		return &ApplyResult{Changed: changed, Messages: []string{"monitoring hub configuration rendered (dry run)"}}, nil
	}
	if unitChanged {
		if _, err := rc.Runner.Run(ctx, "systemctl", "daemon-reload"); err != nil {
			return nil, fmt.Errorf("reloading systemd: %w", err)
		}
	}
	active := unitActive(ctx, rc)
	if changed && active {
		if _, err := rc.Runner.Run(ctx, "docker", "compose", "-f", monitoringHubCompose, "up", "-d"); err != nil {
			return nil, fmt.Errorf("starting monitoring hub containers: %w", err)
		}
		// compose up leaves running containers alone when only their
		// bind-mounted files changed, so they must reread them.
		if written[monitoringHubPromConfig] || written[monitoringHubAlertRules] {
			if err := reloadMonitoringHubService(ctx, h, "prometheus"); err != nil {
				return nil, err
			}
		}
		if written[monitoringHubAlertConfig] {
			if err := reloadMonitoringHubService(ctx, h, "alertmanager"); err != nil {
				return nil, err
			}
		}
	}
	if !active {
		if _, err := rc.Runner.Run(ctx, "systemctl", "enable", "--now", monitoringHubUnit); err != nil {
			return nil, fmt.Errorf("enabling monitoring hub unit: %w", err)
		}
		changed = true
	}
	if err := verifyMonitoringHubServices(ctx, rc); err != nil {
		return nil, err
	}
	var messages []string
	if changed {
		messages = append(messages, "monitoring hub configured and started")
	}
	return &ApplyResult{Changed: changed, Messages: messages}, nil
}

func validateMonitoringDataDir(path string) (bool, error) {
	if _, err := validateTrustedMonitoringDirectoryPath(filepath.Dir(path)); err != nil {
		return false, fmt.Errorf("monitoring hub data directory parent is unsafe: %w", err)
	}
	info, err := monitoringHubLstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cannot inspect monitoring hub data directory %s", path)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, fmt.Errorf("monitoring hub data path %s must be a real directory (symlinks are forbidden)", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return false, fmt.Errorf("monitoring hub data directory %s must be owned by root", path)
	}
	if info.Mode().Perm()&0700 != 0700 {
		return false, fmt.Errorf("monitoring hub data directory %s must grant root read, write and execute access", path)
	}
	return true, nil
}

func validateMonitoringDiscoveryFilesystem(target string) (bool, error) {
	dir := filepath.Dir(target)
	exists, err := validateTrustedMonitoringDirectoryPath(dir)
	if err != nil {
		return false, fmt.Errorf("monitoring discovery directory is unsafe: %w", err)
	}
	if exists {
		if err := validateMonitoringDiscoveryContents(dir, target); err != nil {
			return false, err
		}
	}
	info, err := monitoringHubLstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("cannot inspect monitoring targets file safely")
	}
	if !exists {
		return false, errors.New("monitoring targets file exists without an existing parent directory")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, errors.New("monitoring targets file must be a regular file, not a symlink")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return false, errors.New("monitoring targets file must be owned by root")
	}
	if info.Mode().Perm()&0022 != 0 {
		return false, errors.New("monitoring targets file must not be group- or world-writable")
	}
	if info.Mode().Perm()&0400 == 0 {
		return false, errors.New("monitoring targets file must be readable by root")
	}
	return true, nil
}

func validateMonitoringDiscoveryContents(dir, target string) error {
	entries, err := monitoringHubReadDir(dir)
	if err != nil {
		return errors.New("cannot inspect monitoring discovery directory contents safely")
	}
	allowedName := filepath.Base(target)
	for _, entry := range entries {
		if entry.Name() != allowedName {
			return errors.New("monitoring discovery directory contains unexpected entries; only the configured targets file is permitted")
		}
	}
	return nil
}

func validateTrustedMonitoringDirectoryPath(path string) (bool, error) {
	path = filepath.Clean(path)
	components := []string{}
	for current := path; ; current = filepath.Dir(current) {
		components = append(components, current)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	exists := true
	var previous os.FileInfo
	for i := len(components) - 1; i >= 0; i-- {
		component := components[i]
		info, err := monitoringHubLstat(component)
		if errors.Is(err, os.ErrNotExist) {
			if previous != nil && previous.Mode().Perm()&0200 == 0 {
				return false, fmt.Errorf("existing parent %s must grant root write access to create missing components", components[i+1])
			}
			exists = false
			break
		}
		if err != nil {
			return false, fmt.Errorf("cannot inspect directory component %s", component)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return false, fmt.Errorf("directory component %s must be a real directory (symlinks are forbidden)", component)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			return false, fmt.Errorf("directory component %s must be owned by root", component)
		}
		if info.Mode().Perm()&0022 != 0 || info.Mode().Perm()&0100 == 0 {
			return false, fmt.Errorf("directory component %s must be non-writable by group/world and traversable by root", component)
		}
		if i == 0 && info.Mode().Perm()&0700 != 0700 {
			return false, fmt.Errorf("discovery directory %s must grant root read, write and execute access", component)
		}
		previous = info
	}
	return exists, nil
}

type monitoringHubContainerState struct {
	Service    string                       `json:"Service"`
	State      string                       `json:"State"`
	Status     string                       `json:"Status"`
	Publishers []monitoringHubPublishedPort `json:"Publishers"`
}

type monitoringHubPublishedPort struct {
	URL           string `json:"URL"`
	TargetPort    int    `json:"TargetPort"`
	PublishedPort int    `json:"PublishedPort"`
	Protocol      string `json:"Protocol"`
}

// monitoringHubOwnsListener allows the aggregate exporter preflight to exempt
// a docker-proxy listener only when Compose reports that exact service and
// host-to-container port binding as running.
func monitoringHubOwnsListener(ctx context.Context, rc *RunContext, service string, port int, listener tcpListener) bool {
	h := rc.Config.Modules.Monitoring.Hub.WithDefaults()
	expectedService, configuredPort, containerPort := monitoringHubServicePort(service, h)
	if expectedService == "" || configuredPort != port || !strings.Contains(listener.Process, "docker-proxy") {
		return false
	}
	res, err := rc.Runner.Query(ctx, "docker", "compose", "-f", monitoringHubCompose, "ps", "--all", "--format", "json")
	if err != nil {
		return false
	}
	rows, err := parseMonitoringHubContainerStates(res.Stdout)
	if err != nil {
		return false
	}
	listenAddress := normalizedIP(h.ListenAddress)
	listenerAddress, _, err := net.SplitHostPort(listener.Address)
	if err != nil || normalizedIP(listenerAddress) != listenAddress {
		return false
	}
	for _, row := range rows {
		if row.Service != expectedService || !monitoringHubContainerRunning(row) {
			continue
		}
		for _, published := range row.Publishers {
			if published.PublishedPort == port && published.TargetPort == containerPort && strings.EqualFold(published.Protocol, "tcp") && normalizedIP(published.URL) == listenAddress {
				return true
			}
		}
	}
	return false
}

// monitoringHubReloadWait bounds how long a reload retries while compose
// may still be recreating the container.
var monitoringHubReloadWait = 30 * time.Second

// reloadMonitoringHubService asks a running Prometheus or Alertmanager to
// reread its configuration through its /-/reload endpoint on the published
// port. Both answer 200 only when the new configuration loaded.
func reloadMonitoringHubService(ctx context.Context, h config.MonitoringHubConfig, service string) error {
	_, port, _ := monitoringHubServicePort(service, h)
	host := h.ListenAddress
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
		if ip.To4() == nil {
			host = "::1"
		}
	}
	url := "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/-/reload"
	client := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(monitoringHubReloadWait)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
		if err != nil {
			return fmt.Errorf("reloading %s: %w", service, err)
		}
		resp, err := client.Do(req)
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			return fmt.Errorf("reloading %s: POST %s returned %s: %s", service, url, resp.Status, strings.TrimSpace(string(body)))
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("reloading %s: POST %s: %w", service, url, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("reloading %s: %w", service, ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func monitoringHubServicePort(service string, h config.MonitoringHubConfig) (string, int, int) {
	switch strings.ToLower(service) {
	case "prometheus":
		return "prometheus", h.PrometheusPort, 9090
	case "alertmanager":
		return "alertmanager", h.AlertmanagerPort, 9093
	case "grafana":
		return "grafana", h.GrafanaPort, 3000
	default:
		return "", 0, 0
	}
}

func normalizedIP(value string) string {
	value = strings.Trim(value, "[]")
	if ip := net.ParseIP(value); ip != nil {
		return ip.String()
	}
	return value
}

func monitoringHubServiceChanges(ctx context.Context, rc *RunContext) ([]Change, error) {
	if !rc.Runner.FileExists(monitoringHubCompose) {
		return []Change{
			{Description: "Start monitoring hub service prometheus"},
			{Description: "Start monitoring hub service alertmanager"},
			{Description: "Start monitoring hub service grafana"},
		}, nil
	}
	res, err := rc.Runner.Query(ctx, "docker", "compose", "-f", monitoringHubCompose, "ps", "--all", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("checking monitoring hub container states: %w", err)
	}
	rows, err := parseMonitoringHubContainerStates(res.Stdout)
	if err != nil {
		return nil, fmt.Errorf("reading monitoring hub container states: %w", err)
	}
	states := make(map[string]monitoringHubContainerState, len(rows))
	for _, row := range rows {
		states[row.Service] = row
	}
	var changes []Change
	for _, service := range []string{"prometheus", "alertmanager", "grafana"} {
		row, ok := states[service]
		if !ok || !monitoringHubContainerRunning(row) {
			changes = append(changes, Change{Description: "Start monitoring hub service " + service})
		}
	}
	return changes, nil
}

func verifyMonitoringHubServices(ctx context.Context, rc *RunContext) error {
	changes, err := monitoringHubServiceChanges(ctx, rc)
	if err != nil {
		return err
	}
	if len(changes) > 0 {
		services := make([]string, 0, len(changes))
		for _, change := range changes {
			services = append(services, strings.TrimPrefix(change.Description, "Start monitoring hub service "))
		}
		return fmt.Errorf("monitoring hub services did not reach running state: %s", strings.Join(services, ", "))
	}
	return nil
}

func parseMonitoringHubContainerStates(output string) ([]monitoringHubContainerState, error) {
	output = strings.TrimSpace(output)
	if output == "" {
		return nil, nil
	}
	var rows []monitoringHubContainerState
	if err := json.Unmarshal([]byte(output), &rows); err == nil {
		return rows, nil
	}
	var one monitoringHubContainerState
	if err := json.Unmarshal([]byte(output), &one); err == nil && one.Service != "" {
		return []monitoringHubContainerState{one}, nil
	}
	for index, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row monitoringHubContainerState
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf("invalid JSON output at line %d", index+1)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func monitoringHubContainerRunning(state monitoringHubContainerState) bool {
	containerState := strings.TrimSpace(state.State)
	if containerState != "" {
		return strings.EqualFold(containerState, "running")
	}
	status := strings.TrimSpace(state.Status)
	return strings.EqualFold(status, "up") || strings.HasPrefix(strings.ToLower(status), "up ")
}

func validateMonitoringHubConfig(h config.MonitoringHubConfig) error {
	if !filepath.IsAbs(h.DataDir) || filepath.Clean(h.DataDir) == "/" {
		return fmt.Errorf("monitoring hub data_dir must be an absolute non-root path")
	}
	if !filepath.IsAbs(h.TargetsFile) {
		return fmt.Errorf("monitoring hub targets_file must be an absolute path")
	}
	if h.Retention == "" || strings.ContainsAny(h.Retention, " \t\r\n") {
		return fmt.Errorf("monitoring hub retention must be a Prometheus duration")
	}
	if h.AlertmanagerRetention != "" && !config.IsValidAlertmanagerDuration(h.AlertmanagerRetention) {
		return fmt.Errorf("monitoring hub alertmanager_retention must be a positive Alertmanager duration")
	}
	if strings.ContainsAny(h.ListenAddress, "\r\n") {
		return fmt.Errorf("monitoring hub listen_address is invalid")
	}
	if ip := net.ParseIP(h.ListenAddress); ip == nil {
		return fmt.Errorf("monitoring hub listen_address must be an IP address")
	}
	ports := map[int]string{}
	for name, port := range map[string]int{"Grafana": h.GrafanaPort, "Prometheus": h.PrometheusPort, "Alertmanager": h.AlertmanagerPort} {
		if port < 1 || port > 65535 {
			return fmt.Errorf("monitoring hub %s port is out of range", name)
		}
		if previous, ok := ports[port]; ok {
			return fmt.Errorf("monitoring hub %s and %s ports conflict", previous, name)
		}
		ports[port] = name
	}
	for field, value := range map[string]string{"Prometheus image": h.PrometheusImage, "Alertmanager image": h.AlertmanagerImage, "Grafana image": h.GrafanaImage} {
		if value == "" || strings.ContainsAny(value, " \t\r\n") || !strings.Contains(value, ":") {
			return fmt.Errorf("monitoring hub %s must be a pinned image reference", field)
		}
	}
	return nil
}

func validateMonitoringDiscoveryDir(h config.MonitoringHubConfig, tunnelTokenFile string) error {
	return config.ValidateMonitoringDiscoveryPath(h.TargetsFile, h.DataDir, filepath.Join(monitoringHubConfigDir, "discovery"),
		h.AlertReceiverFile, h.TelegramBotTokenFile, h.GrafanaAdminPasswordFile, tunnelTokenFile)
}

func unitActive(ctx context.Context, rc *RunContext) bool {
	res, err := rc.Runner.Query(ctx, "systemctl", "is-active", "--quiet", monitoringHubUnit)
	return err == nil && res != nil && res.ExitCode == 0
}

func validateRootSecretFile(path string) error {
	exists, err := validateOptionalRootSecretFile(path)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("file does not exist")
	}
	return nil
}

func validateOptionalRootSecretFile(path string) (bool, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, syscall.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("cannot safely open file (symlinks are refused)")
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false, errors.New("cannot inspect file safely")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return false, errors.New("must be a regular file with mode 0600")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return false, errors.New("must be owned by root")
	}
	return true, nil
}

func newMonitoringSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func monitoringHubDataDirs(root string) []string {
	return []string{filepath.Join(root, "prometheus"), filepath.Join(root, "alertmanager"), filepath.Join(root, "grafana")}
}

func renderMonitoringHubFiles(h config.MonitoringHubConfig, passwordFile string) (map[string][]byte, error) {
	alertConfig := monitoringHubDefaultAlertConfig
	alertmanagerArgs := []string{"--config.file=/etc/alertmanager/alertmanager.yml", "--storage.path=/alertmanager"}
	if h.AlertmanagerRetention != "" {
		alertmanagerArgs = append(alertmanagerArgs, "--data.retention="+h.AlertmanagerRetention)
	}
	prometheus := map[string]any{
		"global":     map[string]any{"scrape_interval": "30s", "evaluation_interval": "30s"},
		"rule_files": []string{"/etc/prometheus/alert-rules.yaml"},
		"alerting":   map[string]any{"alertmanagers": []any{map[string]any{"static_configs": []any{map[string]any{"targets": []string{"alertmanager:9093"}}}}}},
		"scrape_configs": []any{
			map[string]any{"job_name": "prometheus", "static_configs": []any{map[string]any{"targets": []string{"localhost:9090"}}}},
			map[string]any{"job_name": "fleet", "file_sd_configs": []any{map[string]any{"files": []string{filepath.Join("/etc/prometheus/discovery", filepath.Base(h.TargetsFile))}, "refresh_interval": "30s"}}},
		},
	}
	compose := map[string]any{
		"services": map[string]any{
			"prometheus":   monitoringHubService(h, h.PrometheusImage, []string{"--config.file=/etc/prometheus/prometheus.yaml", "--storage.tsdb.path=/prometheus", "--storage.tsdb.retention.time=" + h.Retention, "--web.enable-lifecycle"}, h.PrometheusPort, 9090, []monitoringHubMount{bindMount(filepath.Join(monitoringHubConfigDir, "prometheus.yaml"), "/etc/prometheus/prometheus.yaml", true), bindMount(monitoringHubAlertRules, "/etc/prometheus/alert-rules.yaml", true), bindMount(filepath.Dir(h.TargetsFile), "/etc/prometheus/discovery", true), bindMount(filepath.Join(h.DataDir, "prometheus"), "/prometheus", false)}, false),
			"alertmanager": monitoringHubService(h, h.AlertmanagerImage, alertmanagerArgs, h.AlertmanagerPort, 9093, monitoringAlertmanagerVolumes(h), false),
			"grafana":      monitoringHubService(h, h.GrafanaImage, nil, h.GrafanaPort, 3000, []monitoringHubMount{bindMount(filepath.Join(h.DataDir, "grafana"), "/var/lib/grafana", false), bindMount(filepath.Join(monitoringHubConfigDir, "grafana-datasource.yaml"), "/etc/grafana/provisioning/datasources/rootfiles.yaml", true), bindMount(passwordFile, "/run/secrets/grafana_admin_password", true)}, true),
		},
	}
	render := func(v any) ([]byte, error) { b, err := yaml.Marshal(v); return append(b, '\n'), err }
	files := map[string]any{
		monitoringHubCompose:     compose,
		monitoringHubPromConfig:  prometheus,
		monitoringHubAlertConfig: yamlText(alertConfig),
		monitoringHubAlertRules:  yamlText(monitoringHubRules),
		monitoringHubDatasource:  yamlText(monitoringHubDatasourceYAML),
	}
	out := make(map[string][]byte, len(files))
	for path, v := range files {
		if raw, ok := v.(yamlText); ok {
			out[path] = []byte(string(raw))
			continue
		}
		b, err := render(v)
		if err != nil {
			return nil, fmt.Errorf("rendering monitoring hub configuration: %w", err)
		}
		out[path] = b
	}
	return out, nil
}

type monitoringHubMount struct {
	Type     string `yaml:"type"`
	Source   string `yaml:"source"`
	Target   string `yaml:"target"`
	ReadOnly bool   `yaml:"read_only,omitempty"`
}

func bindMount(source, target string, readOnly bool) monitoringHubMount {
	return monitoringHubMount{Type: "bind", Source: source, Target: target, ReadOnly: readOnly}
}

func monitoringHubService(h config.MonitoringHubConfig, image string, command []string, hostPort, containerPort int, volumes []monitoringHubMount, grafana bool) map[string]any {
	service := map[string]any{
		"image":        image,
		"user":         "0:0",
		"ports":        []string{monitoringHubPortBinding(h.ListenAddress, hostPort, containerPort)},
		"volumes":      volumes,
		"cap_drop":     []string{"ALL"},
		"security_opt": []string{"no-new-privileges:true"},
		"restart":      "unless-stopped",
	}
	if command != nil {
		service["command"] = command
	}
	if grafana {
		service["cap_add"] = []string{"CHOWN"}
		service["environment"] = map[string]string{
			"GF_UID": "0", "GF_GID": "0",
			"GF_SECURITY_ADMIN_PASSWORD__FILE": "/run/secrets/grafana_admin_password",
			"GF_USERS_ALLOW_SIGN_UP":           "false",
		}
	}
	return service
}

func monitoringHubPortBinding(address string, hostPort, containerPort int) string {
	if ip := net.ParseIP(address); ip != nil && ip.To4() == nil {
		address = "[" + address + "]"
	}
	return fmt.Sprintf("%s:%d:%d", address, hostPort, containerPort)
}

type yamlText string

func monitoringAlertmanagerVolumes(h config.MonitoringHubConfig) []monitoringHubMount {
	alertConfigPath := filepath.Join(monitoringHubConfigDir, "alertmanager.yaml")
	containerConfigPath := "/etc/alertmanager/alertmanager.yml"
	if h.AlertReceiverFile != "" {
		alertConfigPath = h.AlertReceiverFile
	}
	volumes := []monitoringHubMount{bindMount(alertConfigPath, containerConfigPath, true), bindMount(filepath.Join(h.DataDir, "alertmanager"), "/alertmanager", false)}
	if h.TelegramBotTokenFile != "" {
		volumes = append(volumes, bindMount(h.TelegramBotTokenFile, monitoringHubBotTokenContainerPath, true))
	}
	return volumes
}

func monitoringHubSystemdUnit(composePath string) string {
	return "[Unit]\nDescription=rootfiles monitoring hub\nRequires=docker.service\nAfter=docker.service\n\n[Service]\nType=oneshot\nRemainAfterExit=yes\nWorkingDirectory=" + monitoringHubConfigDir + "\nExecStart=/usr/bin/docker compose -f " + composePath + " up -d\nExecStop=/usr/bin/docker compose -f " + composePath + " down\nTimeoutStartSec=0\n\n[Install]\nWantedBy=multi-user.target\n"
}

const monitoringHubDefaultAlertConfig = "route:\n  receiver: default\nreceivers:\n  - name: default\n"

const monitoringHubDatasourceYAML = `apiVersion: 1
datasources:
  - name: Prometheus
    type: prometheus
    access: proxy
    url: http://prometheus:9090
    isDefault: true
    editable: false
`

const monitoringHubRules = `groups:
  - name: rootfiles-host-health
    rules:
      - alert: FleetTargetDown
        expr: up{job="fleet"} == 0
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: 'Exporter unavailable on {{ if $labels.host }}{{ $labels.host }}{{ else }}unknown host{{ end }}'
          description: 'The fleet scrape target is down. Check exporter service health and reachability from the monitoring hub.'
      - alert: FleetFilesystemNearlyFull
        expr: (node_filesystem_size_bytes{fstype!~"tmpfs|overlay|squashfs"} - node_filesystem_avail_bytes{fstype!~"tmpfs|overlay|squashfs"}) / node_filesystem_size_bytes{fstype!~"tmpfs|overlay|squashfs"} > 0.99
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: 'Filesystem nearly full on {{ if $labels.host }}{{ $labels.host }}{{ else }}unknown host{{ end }}'
          description: 'Filesystem {{ if $labels.mountpoint }}{{ $labels.mountpoint }}{{ else }}with an unknown mount point{{ end }} is above 99% use. Check disk usage and free or expand space before writes fail.'
      - alert: RootfilesConfigurationDrift
        expr: rootfiles_module_satisfied == 0
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: 'Rootfiles configuration drift on {{ if $labels.host }}{{ $labels.host }}{{ else }}unknown host{{ end }}'
          description: 'At least one managed module is not satisfied. Run a read-only rootfiles check on the host and review the reported changes before applying them.'
      - alert: RootfilesDoctorFailure
        expr: rootfiles_doctor_findings{level="fail"} > 0
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: 'Rootfiles doctor failure on {{ if $labels.host }}{{ $labels.host }}{{ else }}unknown host{{ end }}'
          description: 'The host reports {{ $value }} doctor finding(s) at level {{ if $labels.level }}{{ $labels.level }}{{ else }}fail{{ end }}. Run rootfiles doctor and inspect each finding before remediation.'
      - alert: RootfilesReportStale
        expr: (time() - rootfiles_check_timestamp_seconds > 172800) or ((up{job="fleet",exporter="node"} == 1) unless on(host) rootfiles_check_timestamp_seconds)
        for: 15m
        labels:
          severity: warning
        annotations:
          summary: 'Rootfiles report stale on {{ if $labels.host }}{{ $labels.host }}{{ else }}unknown host{{ end }}'
          description: 'The latest scheduled rootfiles check report is older than 48 hours or has no timestamp. Confirm the schedule and inspect the latest check result on the host.'
      - alert: GPUXIDError
        expr: DCGM_FI_DEV_XID_ERRORS > 0
        labels:
          severity: critical
        annotations:
          summary: 'GPU XID error on {{ if $labels.host }}{{ $labels.host }}{{ else }}unknown host{{ end }}{{ if $labels.gpu }} GPU {{ $labels.gpu }}{{ end }}'
          description: 'DCGM reports XID code {{ $value }}. Check current GPU health and the host driver logs; this metric can retain the last XID after recovery.'
      - alert: GPUUncorrectableECCError
        expr: increase(DCGM_FI_DEV_ECC_DBE_VOL_TOTAL[5m]) > 0
        labels:
          severity: critical
        annotations:
          summary: 'Uncorrectable GPU ECC error on {{ if $labels.host }}{{ $labels.host }}{{ else }}unknown host{{ end }}{{ if $labels.gpu }} GPU {{ $labels.gpu }}{{ end }}'
          description: 'The uncorrectable ECC counter increased during the last five minutes. Check current GPU health and DCGM counters, then follow the hardware vendor remediation guidance.'
      - alert: GPUHighTemperature
        expr: DCGM_FI_DEV_GPU_TEMP > 85
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: 'GPU temperature high on {{ if $labels.host }}{{ $labels.host }}{{ else }}unknown host{{ end }}{{ if $labels.gpu }} GPU {{ $labels.gpu }}{{ end }}'
          description: 'GPU temperature is {{ $value }} C, above the configured 85 C alert threshold for 10 minutes. Check cooling, airflow and current device temperature.'
`
