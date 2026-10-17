package module

import (
	"context"
	"fmt"
	"os"
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
	autoUpgradesPath = "/etc/apt/apt.conf.d/20auto-upgrades"
	// APT reads apt.conf.d in name order and merges list values, so the
	// policy sorts after vendor and installer files and clears their origins.
	unattendedPolicyPath = "/etc/apt/apt.conf.d/zzzzzz-rootfiles-unattended-upgrades"
	fail2banJailPath     = "/etc/fail2ban/jail.d/rootfiles-sshd.conf"
	// legacyUnattendedPaths are the earlier managed file and the hand-placed
	// fleet fix it replaces; apply backs them up and removes them.
	legacyUnattendedPaths = []string{
		"/etc/apt/apt.conf.d/52rootfiles-unattended-upgrades",
		"/etc/apt/apt.conf.d/zzzzz-rootfiles-security-origins.conf",
	}
)

// securityOrigins are the only origins the managed policy allows.
var securityOrigins = []string{
	"${distro_id}:${distro_codename}-security",
	"${distro_id}ESMApps:${distro_codename}-apps-security",
	"${distro_id}ESM:${distro_codename}-infra-security",
}

const autoUpgradesContent = `// Managed by rootfiles-v2
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::AutocleanInterval "7";
`

// The NVIDIA driver/CUDA stack must only change together with a planned
// reboot and matching kernel modules, so it is never upgraded unattended.
// Reboots are never automatic; `rootfiles doctor` reports reboot-required.
const unattendedPolicyContent = `// Managed by rootfiles-v2
#clear Unattended-Upgrade::Allowed-Origins;
#clear Unattended-Upgrade::Origins-Pattern;
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
	if cfg.UnattendedUpgrades {
		if !fileEquals(rc, autoUpgradesPath, autoUpgradesContent) || !fileEquals(rc, unattendedPolicyPath, unattendedPolicyContent) || len(existingLegacyUnattended(rc)) > 0 || len(unreadableAptConf()) > 0 {
			changes = append(changes, Change{Description: "Enable security-only unattended upgrades (no reboot, NVIDIA stack excluded)", Command: "write " + unattendedPolicyPath})
		} else if extra := nonSecurityOrigins(ctx, rc); len(extra) > 0 {
			changes = append(changes, Change{Description: "Effective unattended-upgrades policy is not security-only: " + strings.Join(extra, "; ")})
		}
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
		// Non-root apt reads apt.conf.d too; v0.15.0 fleet applies left 0600.
		for _, path := range unreadableAptConf() {
			if _, err := rc.Runner.Run(ctx, "chmod", "0644", path); err != nil {
				return nil, fmt.Errorf("making %s world-readable: %w", path, err)
			}
			wrote = true
		}
		for _, path := range existingLegacyUnattended(rc) {
			if err := rc.Runner.Remove(path); err != nil {
				return nil, fmt.Errorf("removing %s: %w", path, err)
			}
			messages = append(messages, "replaced "+path+" (backed up)")
			wrote = true
		}
		if wrote {
			messages = append(messages, "unattended security upgrades enabled (no auto-reboot, NVIDIA stack excluded)")
			changed = true
		}
		// A file sorting after the managed one can still widen the policy;
		// apply cannot fix that, so it fails instead of reporting success.
		if !rc.DryRun {
			if extra := nonSecurityOrigins(ctx, rc); len(extra) > 0 {
				return nil, fmt.Errorf("effective unattended-upgrades policy is not security-only after writing %s: %s", unattendedPolicyPath, strings.Join(extra, "; "))
			}
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

func existingLegacyUnattended(rc *RunContext) []string {
	var found []string
	for _, path := range legacyUnattendedPaths {
		if rc.Runner.FileExists(path) {
			found = append(found, path)
		}
	}
	return found
}

// nonSecurityOrigins reads the merged APT configuration and returns every
// allowed origin or origin pattern beyond the managed security origins.
func nonSecurityOrigins(ctx context.Context, rc *RunContext) []string {
	res, err := rc.Runner.Query(ctx, "apt-config", "dump")
	if err != nil {
		return []string{"cannot read the effective policy (apt-config dump): " + firstLine(err.Error())}
	}
	allowed := map[string]bool{}
	for _, o := range securityOrigins {
		allowed[o] = true
	}
	var extra []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		for _, key := range []string{"Unattended-Upgrade::Allowed-Origins:: ", "Unattended-Upgrade::Origins-Pattern:: "} {
			value, ok := strings.CutPrefix(line, key)
			if !ok {
				continue
			}
			value = strings.Trim(strings.TrimSuffix(strings.TrimSpace(value), ";"), `"`)
			if key == "Unattended-Upgrade::Origins-Pattern:: " {
				extra = append(extra, "origin pattern "+value)
			} else if !allowed[value] {
				extra = append(extra, "origin "+value)
			}
		}
	}
	return extra
}

// unreadableAptConf lists managed apt.conf.d files that others cannot read.
func unreadableAptConf() []string {
	var out []string
	for _, path := range []string{autoUpgradesPath, unattendedPolicyPath} {
		if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0044 != 0044 {
			out = append(out, path)
		}
	}
	return out
}
