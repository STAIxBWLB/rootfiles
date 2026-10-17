package module

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

type CloudflaredModule struct{}

func NewCloudflaredModule() *CloudflaredModule { return &CloudflaredModule{} }
func (m *CloudflaredModule) Name() string      { return "cloudflared" }

// Paths are overridable in tests.
var (
	cloudflaredBinary   = "/usr/local/bin/cloudflared"
	cloudflaredUnitPath = "/etc/systemd/system/cloudflared.service"
	cloudflaredEnvPath  = "/etc/cloudflared/tunnel.env"
	vlanNetdevPath      = "/etc/systemd/network/10-cloudflared-vlan.netdev"
	vlanNetworkPath     = "/etc/systemd/network/10-cloudflared-vlan.network"
)

const cloudflaredService = "cloudflared"

// The tunnel token is a credential: it lives in a root-only env file read
// by systemd, never in the (world-readable) unit file or on a command line.
// `cloudflared tunnel run` picks it up from TUNNEL_TOKEN.
func cloudflaredUnit() string {
	return fmt.Sprintf(`# Managed by rootfiles-v2
[Unit]
Description=cloudflared tunnel
After=network-online.target
Wants=network-online.target

[Service]
Type=notify
EnvironmentFile=%s
ExecStart=%s --no-autoupdate tunnel run
Restart=on-failure
RestartSec=5s
TimeoutStartSec=0

[Install]
WantedBy=multi-user.target
`, cloudflaredEnvPath, cloudflaredBinary)
}

func cloudflaredEnv(token string) string { return "TUNNEL_TOKEN=" + token + "\n" }

func vlanIface(cfg config.PrivateNetworkConfig) string {
	if cfg.Interface == "" {
		return "vlan0"
	}
	return cfg.Interface
}

func vlanNetdev(iface string) string {
	return fmt.Sprintf(`# Managed by rootfiles-v2 — cloudflared private network
[NetDev]
Name=%s
Kind=dummy
`, iface)
}

func vlanNetwork(iface, addr string) string {
	return fmt.Sprintf(`# Managed by rootfiles-v2 — cloudflared private network
[Match]
Name=%s

[Network]
Address=%s
`, iface, addr)
}

func fileEquals(rc *RunContext, path, want string) bool {
	data, err := rc.Runner.ReadFile(path)
	return err == nil && string(data) == want
}

// tunnelServiceDrift reports whether the unit or token file differs.
func tunnelServiceDrift(rc *RunContext, token string) bool {
	return !fileEquals(rc, cloudflaredUnitPath, cloudflaredUnit()) ||
		!fileEquals(rc, cloudflaredEnvPath, cloudflaredEnv(token))
}

func vlanDrift(rc *RunContext, pn config.PrivateNetworkConfig) bool {
	iface := vlanIface(pn)
	return !fileEquals(rc, vlanNetdevPath, vlanNetdev(iface)) ||
		!fileEquals(rc, vlanNetworkPath, vlanNetwork(iface, pn.Address))
}

// tunnelTokenFileUID is the owner a tunnel_token_file must have; tests point
// it at the test user.
var tunnelTokenFileUID uint32 = 0

// resolveTunnelToken returns the token to install. An explicit token
// (--tunnel-token, ROOTFILES_TUNNEL_TOKEN or tunnel_token) wins; otherwise
// the content of tunnel_token_file, which must be a regular file owned by
// root and not readable by group or others.
func resolveTunnelToken(rc *RunContext) (string, error) {
	cfg := rc.Config.Modules.Cloudflared
	if cfg.TunnelToken != "" || cfg.TunnelTokenFile == "" {
		return cfg.TunnelToken, nil
	}
	path := cfg.TunnelTokenFile
	// Stat and read through one descriptor so the checked file is the one
	// read, and refuse a symlink in place of the file.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, syscall.ELOOP) {
		return "", fmt.Errorf("tunnel_token_file %s: is a symlink, point it at the file itself", path)
	}
	if err != nil {
		return "", fmt.Errorf("tunnel_token_file: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("tunnel_token_file: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("tunnel_token_file %s: not a regular file", path)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return "", fmt.Errorf("tunnel_token_file %s: mode %04o is readable by group or others (want 0600)", path, perm)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid != tunnelTokenFileUID {
		return "", fmt.Errorf("tunnel_token_file %s: owned by uid %d, want root", path, st.Uid)
	}
	data, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return "", fmt.Errorf("tunnel_token_file: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("tunnel_token_file %s: empty", path)
	}
	// Same rule installTunnelService enforces, so Check and Apply agree.
	if strings.ContainsAny(token, "\n\r \"'") {
		return "", fmt.Errorf("tunnel_token_file %s: token contains invalid characters", path)
	}
	return token, nil
}

func (m *CloudflaredModule) Check(_ context.Context, rc *RunContext) (*CheckResult, error) {
	var changes []Change
	cfg := rc.Config.Modules.Cloudflared
	token, err := resolveTunnelToken(rc)
	if err != nil {
		return nil, err
	}

	if !rc.Runner.FileExists(cloudflaredBinary) {
		changes = append(changes, Change{
			Description: "Install cloudflared binary",
			Command:     "download cloudflared → " + cloudflaredBinary,
		})
	}

	if token != "" && tunnelServiceDrift(rc, token) {
		changes = append(changes, Change{
			Description: "Install/update cloudflared tunnel service",
			Command:     "write " + cloudflaredUnitPath + " + " + cloudflaredEnvPath,
		})
	}

	if pn := cfg.PrivateNetwork; pn.Enabled && pn.Address != "" && vlanDrift(rc, pn) {
		changes = append(changes, Change{
			Description: fmt.Sprintf("Configure VLAN interface %s (%s)", vlanIface(pn), pn.Address),
			Command:     "configure systemd-networkd dummy interface",
		})
	}

	return &CheckResult{
		Satisfied: len(changes) == 0,
		Changes:   changes,
	}, nil
}

func (m *CloudflaredModule) Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	var messages, warnings []string
	changed := false
	cfg := rc.Config.Modules.Cloudflared
	token, err := resolveTunnelToken(rc)
	if err != nil {
		return nil, err
	}

	if !rc.Runner.FileExists(cloudflaredBinary) {
		if err := m.installBinary(ctx, rc); err != nil {
			return nil, err
		}
		messages = append(messages, "cloudflared binary installed")
		changed = true
	}

	if token != "" && tunnelServiceDrift(rc, token) {
		w, err := installTunnelService(ctx, rc, token)
		if err != nil {
			return nil, err
		}
		warnings = append(warnings, w...)
		messages = append(messages, "tunnel service installed")
		changed = true
	}

	if pn := cfg.PrivateNetwork; pn.Enabled && pn.Address != "" && vlanDrift(rc, pn) {
		w, err := m.setupVLAN(ctx, rc)
		if err != nil {
			return nil, err
		}
		warnings = append(warnings, w...)
		messages = append(messages, fmt.Sprintf("VLAN %s configured (%s)", vlanIface(pn), pn.Address))
		changed = true
	}

	return &ApplyResult{Changed: changed, Messages: messages, Warnings: warnings}, nil
}

// installTunnelService writes the unit and token file and (re)starts the
// service. systemctl failures are warnings: the files are in place and the
// service starts on next boot.
func installTunnelService(ctx context.Context, rc *RunContext, token string) ([]string, error) {
	if strings.ContainsAny(token, "\n\r \"'") {
		return nil, fmt.Errorf("tunnel token contains invalid characters")
	}
	if err := rc.Runner.MkdirAll(filepath.Dir(cloudflaredEnvPath), 0700); err != nil {
		return nil, fmt.Errorf("creating %s: %w", filepath.Dir(cloudflaredEnvPath), err)
	}
	if err := rc.Runner.WriteFile(cloudflaredEnvPath, []byte(cloudflaredEnv(token)), 0600); err != nil {
		return nil, fmt.Errorf("writing tunnel token: %w", err)
	}
	if err := rc.Runner.WriteFile(cloudflaredUnitPath, []byte(cloudflaredUnit()), 0644); err != nil {
		return nil, fmt.Errorf("writing cloudflared unit: %w", err)
	}
	var warnings []string
	if w := systemdReload(ctx, rc); w != "" {
		return append(warnings, w), nil
	}
	if _, err := rc.Runner.Run(ctx, "systemctl", "enable", cloudflaredService); err != nil {
		warnings = append(warnings, "enabling cloudflared: "+firstLine(err.Error()))
	}
	if _, err := rc.Runner.Run(ctx, "systemctl", "restart", cloudflaredService); err != nil {
		warnings = append(warnings, "starting cloudflared: "+firstLine(err.Error()))
	}
	return warnings, nil
}

func (m *CloudflaredModule) installBinary(ctx context.Context, rc *RunContext) error {
	return m.installBinaryVersion(ctx, rc, "")
}

// installBinaryVersion downloads cloudflared at the given version (e.g.
// "2024.9.1"). Empty version falls back to the "latest" alias URL, which
// GitHub resolves server-side to whatever release cloudflared has tagged
// as latest. Non-empty versions use the explicit release download path
// so operators can pin. The download goes to a temp file that is renamed
// into place, so a failed download never leaves a truncated binary.
func (m *CloudflaredModule) installBinaryVersion(ctx context.Context, rc *RunContext, version string) error {
	arch := runtime.GOARCH
	if arch != "amd64" && arch != "arm64" {
		return fmt.Errorf("cloudflared has no prebuilt binary for arch %q", arch)
	}

	var url string
	if version == "" {
		url = fmt.Sprintf(
			"https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-%s",
			arch,
		)
	} else {
		url = fmt.Sprintf(
			"https://github.com/cloudflare/cloudflared/releases/download/%s/cloudflared-linux-%s",
			version, arch,
		)
	}

	tmp := cloudflaredBinary + ".rootfiles-download"
	if _, err := rc.Runner.Run(ctx, "curl", "-fsSL", "--retry", "3", "-o", tmp, url); err != nil {
		_ = rc.Runner.Remove(tmp)
		return fmt.Errorf("downloading cloudflared: %w", err)
	}
	if _, err := rc.Runner.Run(ctx, "chmod", "0755", tmp); err != nil {
		return fmt.Errorf("chmod cloudflared: %w", err)
	}
	if err := rc.Runner.Rename(tmp, cloudflaredBinary); err != nil {
		return fmt.Errorf("installing cloudflared: %w", err)
	}
	return nil
}

func (m *CloudflaredModule) setupVLAN(ctx context.Context, rc *RunContext) ([]string, error) {
	cfg := rc.Config.Modules.Cloudflared.PrivateNetwork
	iface := vlanIface(cfg)

	if err := rc.Runner.MkdirAll(filepath.Dir(vlanNetdevPath), 0755); err != nil {
		return nil, fmt.Errorf("creating systemd-networkd dir: %w", err)
	}
	if err := rc.Runner.WriteFile(vlanNetdevPath, []byte(vlanNetdev(iface)), 0644); err != nil {
		return nil, fmt.Errorf("writing netdev: %w", err)
	}
	if err := rc.Runner.WriteFile(vlanNetworkPath, []byte(vlanNetwork(iface, cfg.Address)), 0644); err != nil {
		return nil, fmt.Errorf("writing network: %w", err)
	}

	var warnings []string
	if _, err := rc.Runner.Run(ctx, "systemctl", "restart", "systemd-networkd"); err == nil {
		return nil, nil
	}
	// No systemd-networkd (containers, NetworkManager hosts): bring the
	// interface up directly so the address is live now; the files take
	// effect whenever networkd runs.
	if _, err := rc.Runner.Query(ctx, "ip", "link", "show", iface); err != nil {
		if _, err := rc.Runner.Run(ctx, "ip", "link", "add", iface, "type", "dummy"); err != nil {
			warnings = append(warnings, fmt.Sprintf("creating %s: %s", iface, firstLine(err.Error())))
			return warnings, nil
		}
	}
	if _, err := rc.Runner.Run(ctx, "ip", "addr", "replace", cfg.Address, "dev", iface); err != nil {
		warnings = append(warnings, fmt.Sprintf("assigning %s: %s", cfg.Address, firstLine(err.Error())))
	}
	if _, err := rc.Runner.Run(ctx, "ip", "link", "set", iface, "up"); err != nil {
		warnings = append(warnings, fmt.Sprintf("bringing up %s: %s", iface, firstLine(err.Error())))
	}
	return warnings, nil
}

// TunnelSetup configures VLAN, installs cloudflared and the tunnel service.
// Called from CLI `rootfiles tunnel setup`.
func TunnelSetup(ctx context.Context, rc *RunContext, token, vlanAddr string) error {
	m := NewCloudflaredModule()
	var warnings []string

	// VLAN first: it does not depend on the binary.
	if vlanAddr != "" {
		fmt.Println("Configuring VLAN private network...")
		rc.Config.Modules.Cloudflared.PrivateNetwork.Address = vlanAddr
		w, err := m.setupVLAN(ctx, rc)
		if err != nil {
			return err
		}
		warnings = append(warnings, w...)
	}

	if !rc.Runner.FileExists(cloudflaredBinary) {
		fmt.Println("Installing cloudflared...")
		if err := m.installBinary(ctx, rc); err != nil {
			return err
		}
	}

	if token != "" {
		fmt.Println("Setting up tunnel service...")
		w, err := installTunnelService(ctx, rc, token)
		if err != nil {
			return err
		}
		warnings = append(warnings, w...)
	}

	for _, w := range warnings {
		fmt.Println("  " + ui.MarkWarn + " " + w)
	}
	fmt.Println("Tunnel setup complete.")
	return nil
}

// TunnelStatus shows cloudflared and VLAN status.
func TunnelStatus(ctx context.Context, rc *RunContext) error {
	ui.WriteSection(os.Stdout, "Tunnel")

	// Binary version
	if rc.Runner.FileExists(cloudflaredBinary) {
		if res, err := rc.Runner.Query(ctx, cloudflaredBinary, "--version"); err == nil {
			ui.WriteKV(os.Stdout, "Binary", strings.TrimSpace(res.Stdout))
		} else {
			ui.WriteKV(os.Stdout, "Binary", ui.StyleHint.Render("installed (version unknown)"))
		}
	} else {
		ui.WriteKV(os.Stdout, "Binary", ui.StyleHint.Render("not installed"))
	}

	// Service status
	res, err := rc.Runner.Query(ctx, "systemctl", "is-active", cloudflaredService)
	if err != nil {
		ui.WriteKV(os.Stdout, "Service", ui.StyleHint.Render("inactive"))
	} else {
		state := strings.TrimSpace(res.Stdout)
		if state == "active" {
			ui.WriteKV(os.Stdout, "Service", ui.StyleSuccess.Render("active"))
		} else {
			ui.WriteKV(os.Stdout, "Service", ui.StyleHint.Render(state))
		}
	}

	// VLAN interface
	cfg := rc.Config.Modules.Cloudflared.PrivateNetwork
	iface := cfg.Interface
	if iface == "" {
		iface = "vlan0"
	}
	res, err = rc.Runner.Query(ctx, "ip", "addr", "show", iface)
	if err != nil {
		ui.WriteKV(os.Stdout, fmt.Sprintf("VLAN (%s)", iface), ui.StyleHint.Render("not configured"))
	} else {
		// Extract address line
		found := false
		for _, line := range strings.Split(res.Stdout, "\n") {
			if strings.Contains(line, "inet ") {
				ui.WriteKV(os.Stdout, fmt.Sprintf("VLAN (%s)", iface), strings.TrimSpace(line))
				found = true
				break
			}
		}
		if !found {
			ui.WriteKV(os.Stdout, fmt.Sprintf("VLAN (%s)", iface), ui.StyleHint.Render("up (no inet)"))
		}
	}

	return nil
}

// TunnelUpdate updates the cloudflared binary and restarts the service.
// An empty version selects the upstream "latest" release; a pinned version
// (e.g. "2024.9.1") downloads that specific release.
func TunnelUpdate(ctx context.Context, rc *RunContext, version string) error {
	m := NewCloudflaredModule()

	current := currentCloudflaredVersion(ctx, rc)
	target := version
	if target == "" {
		latest, err := FetchLatestCloudflaredVersion(ctx)
		if err != nil {
			fmt.Printf("Warning: could not resolve latest cloudflared release (%v); using /latest/download/ alias.\n", err)
		} else {
			target = latest
		}
	}

	ui.WriteSection(os.Stdout, "Cloudflared update")
	ui.WriteKV(os.Stdout, "Current", firstNonEmptyCloudflared(current, "(not installed)"))
	ui.WriteKV(os.Stdout, "Target", firstNonEmptyCloudflared(target, "latest"))

	if target != "" && current == target {
		ui.WriteHint(os.Stdout, "already on target version — nothing to do.")
		return nil
	}

	fmt.Println()
	fmt.Printf("Downloading cloudflared %s ...\n", firstNonEmptyCloudflared(target, "latest"))
	if err := m.installBinaryVersion(ctx, rc, target); err != nil {
		return err
	}

	// Restart the service only if it's actually installed, so a pure binary
	// refresh on a host that's not running the tunnel doesn't surface a
	// confusing "Unit cloudflared.service not loaded" message.
	if res, err := rc.Runner.Query(ctx, "systemctl", "is-enabled", cloudflaredService); err == nil && strings.TrimSpace(res.Stdout) != "" {
		if _, err := rc.Runner.Run(ctx, "systemctl", "restart", cloudflaredService); err != nil {
			return fmt.Errorf("restarting cloudflared: %w", err)
		}
		fmt.Println(ui.StyleSuccess.Render(ui.MarkOK + " cloudflared updated and service restarted."))
	} else {
		fmt.Println(ui.StyleSuccess.Render(ui.MarkOK + " cloudflared binary updated (service not installed, skip restart)."))
	}
	return nil
}

// FetchLatestCloudflaredVersion queries GitHub's release API for the current
// upstream tag of cloudflared/cloudflare. Surfaced publicly so the cli can
// call it for `--check` without duplicating the HTTP plumbing.
func FetchLatestCloudflaredVersion(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/repos/cloudflare/cloudflared/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github API returned %d", resp.StatusCode)
	}

	var info struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", err
	}
	if info.TagName == "" {
		return "", fmt.Errorf("no tag_name in response")
	}
	return info.TagName, nil
}

// currentCloudflaredVersion parses `cloudflared --version` output. Returns
// the empty string when the binary is missing or the output cannot be parsed.
// Example output: "cloudflared version 2024.9.1 (built 2024-09-13-10:42 UTC)".
func currentCloudflaredVersion(ctx context.Context, rc *RunContext) string {
	if !rc.Runner.FileExists(cloudflaredBinary) {
		return ""
	}
	res, err := rc.Runner.Query(ctx, cloudflaredBinary, "--version")
	if err != nil {
		return ""
	}
	for _, token := range strings.Fields(res.Stdout) {
		// Versions look like "YYYY.M.P" — leading digit followed by a dot.
		if len(token) > 3 && token[0] >= '0' && token[0] <= '9' && strings.Contains(token, ".") {
			return token
		}
	}
	return ""
}

func firstNonEmptyCloudflared(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// TunnelUninstall removes tunnel service, VLAN, and binary.
func TunnelUninstall(ctx context.Context, rc *RunContext) error {
	var warnings []string
	warn := func(what string, err error) {
		warnings = append(warnings, what+": "+firstLine(err.Error()))
	}

	// Stop and remove the service (ours, or a legacy `cloudflared service
	// install` unit at the same path).
	if _, err := rc.Runner.Run(ctx, "systemctl", "disable", "--now", cloudflaredService); err != nil {
		warn("stopping cloudflared", err)
	}
	for _, p := range []string{cloudflaredUnitPath, cloudflaredEnvPath} {
		if err := rc.Runner.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing %s: %w", p, err)
		}
	}
	if w := systemdReload(ctx, rc); w != "" {
		warnings = append(warnings, w)
	}

	// Remove VLAN
	iface := vlanIface(rc.Config.Modules.Cloudflared.PrivateNetwork)
	if _, err := rc.Runner.Query(ctx, "ip", "link", "show", iface); err == nil {
		if _, err := rc.Runner.Run(ctx, "ip", "link", "delete", iface); err != nil {
			warn("deleting "+iface, err)
		}
	}
	for _, p := range []string{vlanNetdevPath, vlanNetworkPath, cloudflaredBinary} {
		if err := rc.Runner.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing %s: %w", p, err)
		}
	}

	for _, w := range warnings {
		fmt.Println("  " + ui.MarkWarn + " " + w)
	}
	fmt.Println("Tunnel uninstalled (service, VLAN, binary removed).")
	return nil
}
