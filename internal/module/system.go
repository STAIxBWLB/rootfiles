package module

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// SystemModule manages host basics: hostname, swap, sysctl, journald size
// and the Ubuntu APT mirror. It runs before packages so a mirror change
// takes effect for the first install.
type SystemModule struct{}

func NewSystemModule() *SystemModule { return &SystemModule{} }
func (m *SystemModule) Name() string { return "system" }

// Paths are overridable in tests.
var (
	hostnamePath       = "/etc/hostname"
	hostsPath          = "/etc/hosts"
	procSwapsPath      = "/proc/swaps"
	swapfilePath       = "/swapfile"
	fstabPath          = "/etc/fstab"
	sysctlPath         = "/etc/sysctl.d/90-rootfiles.conf"
	journaldConfPath   = "/etc/systemd/journald.conf.d/90-rootfiles.conf"
	aptSourcesPaths    = []string{"/etc/apt/sources.list.d/ubuntu.sources", "/etc/apt/sources.list"}
	ubuntuArchiveRegex = regexp.MustCompile(`https?://([a-z]{2}\.)?archive\.ubuntu\.com/ubuntu/?`)
)

func currentHostname(rc *RunContext) string {
	data, err := rc.Runner.ReadFile(hostnamePath)
	if err == nil && strings.TrimSpace(string(data)) != "" {
		return strings.TrimSpace(string(data))
	}
	h, _ := os.Hostname()
	return h
}

func hasSwap(rc *RunContext) bool {
	data, _ := rc.Runner.ReadFile(procSwapsPath)
	lines := nonEmptyLines(string(data))
	return len(lines) > 1 // header + at least one device
}

func sysctlContent(entries map[string]string) string {
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# Managed by rootfiles\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s = %s\n", k, entries[k])
	}
	return b.String()
}

func journaldContent(maxUse string) string {
	return "# Managed by rootfiles\n[Journal]\nSystemMaxUse=" + maxUse + "\n"
}

// mirroredSources returns path and rewritten content for each APT source
// file that still points at archive.ubuntu.com.
func mirroredSources(rc *RunContext, mirror string) map[string]string {
	out := map[string]string{}
	mirror = strings.TrimSuffix(mirror, "/")
	for _, p := range aptSourcesPaths {
		data, err := rc.Runner.ReadFile(p)
		if err != nil {
			continue
		}
		if updated := ubuntuArchiveRegex.ReplaceAllString(string(data), mirror+"/"); updated != string(data) {
			out[p] = updated
		}
	}
	return out
}

func (m *SystemModule) Check(_ context.Context, rc *RunContext) (*CheckResult, error) {
	cfg := rc.Config.Modules.System
	var changes []Change

	if cfg.Hostname != "" && currentHostname(rc) != cfg.Hostname {
		changes = append(changes, Change{Description: "Set hostname to " + cfg.Hostname, Command: "hostnamectl set-hostname " + cfg.Hostname})
	}
	if cfg.SwapSize != "" && !hasSwap(rc) {
		changes = append(changes, Change{Description: fmt.Sprintf("Create %s swapfile", cfg.SwapSize), Command: "fallocate -l " + cfg.SwapSize + " " + swapfilePath})
	}
	if len(cfg.Sysctl) > 0 && !fileEquals(rc, sysctlPath, sysctlContent(cfg.Sysctl)) {
		changes = append(changes, Change{Description: fmt.Sprintf("Apply %d sysctl setting(s)", len(cfg.Sysctl)), Command: "write " + sysctlPath + " && sysctl --system"})
	}
	if cfg.JournaldMaxUse != "" && !fileEquals(rc, journaldConfPath, journaldContent(cfg.JournaldMaxUse)) {
		changes = append(changes, Change{Description: "Cap journald at " + cfg.JournaldMaxUse, Command: "write " + journaldConfPath})
	}
	if cfg.AptMirror != "" {
		for p := range mirroredSources(rc, cfg.AptMirror) {
			changes = append(changes, Change{Description: "Point APT at " + cfg.AptMirror, Command: "rewrite " + p})
		}
	}
	return &CheckResult{Satisfied: len(changes) == 0, Changes: changes}, nil
}

func (m *SystemModule) Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	cfg := rc.Config.Modules.System
	var messages, warnings []string
	changed := false

	if cfg.Hostname != "" && currentHostname(rc) != cfg.Hostname {
		if _, err := rc.Runner.Run(ctx, "hostnamectl", "set-hostname", cfg.Hostname); err != nil {
			if err := rc.Runner.WriteFile(hostnamePath, []byte(cfg.Hostname+"\n"), 0644); err != nil {
				return nil, fmt.Errorf("writing %s: %w", hostnamePath, err)
			}
			warnings = append(warnings, "hostnamectl unavailable; /etc/hostname written, effective after reboot")
		}
		if err := ensureHostsEntry(rc, cfg.Hostname); err != nil {
			return nil, err
		}
		messages = append(messages, "hostname set to "+cfg.Hostname)
		changed = true
	}

	if cfg.SwapSize != "" && !hasSwap(rc) {
		w, err := createSwapfile(ctx, rc, cfg.SwapSize)
		if err != nil {
			return nil, err
		}
		warnings = append(warnings, w...)
		messages = append(messages, cfg.SwapSize+" swapfile created")
		changed = true
	}

	if len(cfg.Sysctl) > 0 && !fileEquals(rc, sysctlPath, sysctlContent(cfg.Sysctl)) {
		if err := rc.Runner.WriteFile(sysctlPath, []byte(sysctlContent(cfg.Sysctl)), 0644); err != nil {
			return nil, fmt.Errorf("writing %s: %w", sysctlPath, err)
		}
		if _, err := rc.Runner.Run(ctx, "sysctl", "-p", sysctlPath); err != nil {
			warnings = append(warnings, "sysctl -p: "+firstLine(err.Error()))
		}
		messages = append(messages, fmt.Sprintf("%d sysctl setting(s) applied", len(cfg.Sysctl)))
		changed = true
	}

	if cfg.JournaldMaxUse != "" && !fileEquals(rc, journaldConfPath, journaldContent(cfg.JournaldMaxUse)) {
		if err := rc.Runner.MkdirAll(filepath.Dir(journaldConfPath), 0755); err != nil {
			return nil, err
		}
		if err := rc.Runner.WriteFile(journaldConfPath, []byte(journaldContent(cfg.JournaldMaxUse)), 0644); err != nil {
			return nil, fmt.Errorf("writing %s: %w", journaldConfPath, err)
		}
		if _, err := rc.Runner.Run(ctx, "systemctl", "restart", "systemd-journald"); err != nil {
			warnings = append(warnings, "restarting journald: "+firstLine(err.Error()))
		}
		messages = append(messages, "journald capped at "+cfg.JournaldMaxUse)
		changed = true
	}

	if cfg.AptMirror != "" {
		for p, content := range mirroredSources(rc, cfg.AptMirror) {
			if err := rc.Runner.WriteFile(p, []byte(content), 0644); err != nil {
				return nil, fmt.Errorf("writing %s: %w", p, err)
			}
			messages = append(messages, "APT mirror set in "+p)
			changed = true
		}
	}

	return &ApplyResult{Changed: changed, Messages: messages, Warnings: warnings}, nil
}

// ensureHostsEntry maps the hostname to 127.0.1.1 (Debian convention) so
// sudo and friends do not stall on name resolution.
func ensureHostsEntry(rc *RunContext, hostname string) error {
	data, _ := rc.Runner.ReadFile(hostsPath)
	var lines []string
	found := false
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == "127.0.1.1" {
			if !found {
				lines = append(lines, "127.0.1.1\t"+hostname)
				found = true
			}
			continue
		}
		lines = append(lines, line)
	}
	if !found {
		lines = append(lines, "127.0.1.1\t"+hostname)
	}
	return rc.Runner.WriteFile(hostsPath, []byte(strings.Join(lines, "\n")+"\n"), 0644)
}

func createSwapfile(ctx context.Context, rc *RunContext, size string) ([]string, error) {
	if rc.Runner.FileExists(swapfilePath) {
		return nil, fmt.Errorf("%s exists but no swap is active; enable or remove it manually", swapfilePath)
	}
	steps := [][]string{
		{"fallocate", "-l", size, swapfilePath},
		{"chmod", "600", swapfilePath},
		{"mkswap", swapfilePath},
	}
	for _, s := range steps {
		if _, err := rc.Runner.Run(ctx, s[0], s[1:]...); err != nil {
			_ = rc.Runner.Remove(swapfilePath)
			return nil, fmt.Errorf("creating swapfile (%s): %w", s[0], err)
		}
	}
	var warnings []string
	if _, err := rc.Runner.Run(ctx, "swapon", swapfilePath); err != nil {
		warnings = append(warnings, "swapon: "+firstLine(err.Error()))
	}
	fstab, _ := rc.Runner.ReadFile(fstabPath)
	if !strings.Contains(string(fstab), swapfilePath) {
		content := strings.TrimRight(string(fstab), "\n") + "\n" + swapfilePath + " none swap sw 0 0\n"
		if err := rc.Runner.WriteFile(fstabPath, []byte(content), 0644); err != nil {
			return warnings, fmt.Errorf("adding swapfile to fstab: %w", err)
		}
	}
	return warnings, nil
}
