package module

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

// SecurityModule applies the baseline beyond sshd: automatic security
// updates, an sshd fail2ban jail and NTP time sync.
type SecurityModule struct{}

func NewSecurityModule() *SecurityModule { return &SecurityModule{} }
func (m *SecurityModule) Name() string   { return "security" }

// Paths are overridable in tests.
var (
	autoUpgradesPath     = "/etc/apt/apt.conf.d/20auto-upgrades"
	unattendedPolicyPath = "/etc/apt/apt.conf.d/52rootfiles-unattended-upgrades"
	fail2banJailPath     = "/etc/fail2ban/jail.d/rootfiles-sshd.conf"
)

const autoUpgradesContent = `// Managed by rootfiles-v2
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::AutocleanInterval "7";
`

// The NVIDIA driver/CUDA stack must only change together with a planned
// reboot and matching kernel modules, so it is never upgraded unattended.
// Reboots are never automatic; `rootfiles doctor` reports reboot-required.
const unattendedPolicyContent = `// Managed by rootfiles-v2
Unattended-Upgrade::Allowed-Origins {
        "${distro_id}:${distro_codename}-security";
        "${distro_id}ESMApps:${distro_codename}-apps-security";
        "${distro_id}ESM:${distro_codename}-infra-security";
};
Unattended-Upgrade::Package-Blacklist {
        "nvidia-";
        "libnvidia-";
        "cuda-";
        "libcudnn";
        "libnccl";
        "datacenter-gpu-manager";
        "nvidia-fabricmanager";
};
Unattended-Upgrade::Automatic-Reboot "false";
Unattended-Upgrade::Remove-Unused-Kernel-Packages "true";
`

func fail2banJail(ports []int) string {
	ps := make([]string, len(ports))
	for i, p := range ports {
		ps[i] = strconv.Itoa(p)
	}
	return fmt.Sprintf(`# Managed by rootfiles-v2
[sshd]
enabled  = true
port     = %s
backend  = systemd
maxretry = 5
findtime = 10m
bantime  = 1h
`, strings.Join(ps, ","))
}

// timeSyncPackage returns the NTP daemon to manage: whichever one is
// already installed (chrony is common on DGX; ntp/ntpsec/openntpd conflict
// with systemd-timesyncd and would be removed by installing it), otherwise
// systemd-timesyncd.
func timeSyncPackage(rc *RunContext) (pkg, unit string) {
	candidates := []struct{ pkg, unit string }{
		{"chrony", "chrony"}, {"ntpsec", "ntpsec"}, {"ntp", "ntp"}, {"openntpd", "openntpd"},
	}
	names := make([]string, len(candidates))
	for i, c := range candidates {
		names[i] = c.pkg
	}
	installed := rc.APT.Installed(names)
	for _, c := range candidates {
		if installed[c.pkg] {
			return c.pkg, c.unit
		}
	}
	return "systemd-timesyncd", "systemd-timesyncd"
}

func (m *SecurityModule) packages(rc *RunContext) []string {
	cfg := rc.Config.Modules.Security
	var pkgs []string
	if config.IsRocky(rc.Config.System) {
		if cfg.UnattendedUpgrades {
			pkgs = append(pkgs, "dnf-automatic")
		}
		if cfg.TimeSync {
			pkgs = append(pkgs, "chrony")
		}
		if cfg.Fail2ban {
			pkgs = append(pkgs, "fail2ban", "python3-systemd")
		}
		return pkgs
	}
	if cfg.UnattendedUpgrades {
		pkgs = append(pkgs, "unattended-upgrades")
	}
	if cfg.Fail2ban {
		pkgs = append(pkgs, "fail2ban", "python3-systemd")
	}
	if cfg.TimeSync {
		pkg, _ := timeSyncPackage(rc)
		pkgs = append(pkgs, pkg)
	}
	return pkgs
}

func (m *SecurityModule) Check(ctx context.Context, rc *RunContext) (*CheckResult, error) {
	if config.IsRocky(rc.Config.System) {
		return rockySecurityCheck(ctx, rc)
	}
	cfg := rc.Config.Modules.Security
	var changes []Change

	if missing := missingPackages(rc, m.packages(rc)); len(missing) > 0 {
		changes = append(changes, Change{Description: fmt.Sprintf("Install %v", missing), Command: "apt-get install " + strings.Join(missing, " ")})
	}
	if cfg.UnattendedUpgrades && (!fileEquals(rc, autoUpgradesPath, autoUpgradesContent) || !fileEquals(rc, unattendedPolicyPath, unattendedPolicyContent)) {
		changes = append(changes, Change{Description: "Enable security-only unattended upgrades (no reboot, NVIDIA stack excluded)", Command: "write " + unattendedPolicyPath})
	}
	if cfg.Fail2ban && !fileEquals(rc, fail2banJailPath, fail2banJail(sshPorts(ctx, rc))) {
		changes = append(changes, Change{Description: "Configure fail2ban sshd jail", Command: "write " + fail2banJailPath})
	}
	if cfg.TimeSync {
		if res, err := rc.Runner.Query(ctx, "timedatectl", "show", "-p", "NTP", "--value"); err == nil && strings.TrimSpace(res.Stdout) != "yes" {
			changes = append(changes, Change{Description: "Enable NTP time sync", Command: "timedatectl set-ntp true"})
		}
	}
	return &CheckResult{Satisfied: len(changes) == 0, Changes: changes}, nil
}

func (m *SecurityModule) Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	if config.IsRocky(rc.Config.System) {
		return rockySecurityApply(ctx, rc)
	}
	cfg := rc.Config.Modules.Security
	var messages, warnings []string
	changed := false

	if missing := missingPackages(rc, m.packages(rc)); len(missing) > 0 {
		if err := rc.APT.Update(ctx); err != nil {
			return nil, fmt.Errorf("apt update: %w", err)
		}
		if err := rc.APT.Install(ctx, missing); err != nil {
			return nil, fmt.Errorf("installing %v: %w", missing, err)
		}
		messages = append(messages, fmt.Sprintf("installed %v", missing))
		changed = true
	}

	if cfg.UnattendedUpgrades {
		wrote := false
		for path, content := range map[string]string{autoUpgradesPath: autoUpgradesContent, unattendedPolicyPath: unattendedPolicyContent} {
			if fileEquals(rc, path, content) {
				continue
			}
			if err := rc.Runner.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return nil, err
			}
			if err := rc.Runner.WriteFile(path, []byte(content), 0644); err != nil {
				return nil, fmt.Errorf("writing %s: %w", path, err)
			}
			wrote = true
		}
		if wrote {
			messages = append(messages, "unattended security upgrades enabled (no auto-reboot, NVIDIA stack excluded)")
			changed = true
		}
	}

	if cfg.Fail2ban {
		jail := fail2banJail(sshPorts(ctx, rc))
		if !fileEquals(rc, fail2banJailPath, jail) {
			if err := rc.Runner.MkdirAll(filepath.Dir(fail2banJailPath), 0755); err != nil {
				return nil, err
			}
			if err := rc.Runner.WriteFile(fail2banJailPath, []byte(jail), 0644); err != nil {
				return nil, fmt.Errorf("writing %s: %w", fail2banJailPath, err)
			}
			if _, err := rc.Runner.Run(ctx, "systemctl", "enable", "fail2ban"); err != nil {
				warnings = append(warnings, "enabling fail2ban: "+firstLine(err.Error()))
			} else if _, err := rc.Runner.Run(ctx, "systemctl", "restart", "fail2ban"); err != nil {
				warnings = append(warnings, "restarting fail2ban: "+firstLine(err.Error()))
			}
			messages = append(messages, "fail2ban sshd jail configured")
			changed = true
		}
	}

	if cfg.TimeSync {
		if res, err := rc.Runner.Query(ctx, "timedatectl", "show", "-p", "NTP", "--value"); err == nil && strings.TrimSpace(res.Stdout) != "yes" {
			_, unit := timeSyncPackage(rc)
			if _, err := rc.Runner.Run(ctx, "systemctl", "enable", "--now", unit); err != nil {
				warnings = append(warnings, "enabling "+unit+": "+firstLine(err.Error()))
			}
			if _, err := rc.Runner.Run(ctx, "timedatectl", "set-ntp", "true"); err != nil {
				warnings = append(warnings, "timedatectl set-ntp: "+firstLine(err.Error()))
			}
			messages = append(messages, "NTP time sync enabled ("+unit+")")
			changed = true
		}
	}

	return &ApplyResult{Changed: changed, Messages: messages, Warnings: warnings}, nil
}
