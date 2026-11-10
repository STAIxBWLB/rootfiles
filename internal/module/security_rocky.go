package module

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

var (
	rockyAutomaticConfigPath = "/etc/dnf/automatic.conf"
	rockyAutomaticTimer      = "dnf-automatic-install.timer"
	rockyPlatformPython      = "/usr/libexec/platform-python"
)

const rockyUpdateinfoCheck = `import dnf
import os
import sys
import xml.etree.ElementTree as ET

required = set(("baseos", "appstream"))
base = dnf.Base()
base.conf.cacheonly = True
base.conf.substitutions.update_from_etc(base.conf.installroot)
strict = len(sys.argv) > 1 and sys.argv[1] == "strict"
base.read_all_repos()
repos = list(base.repos.iter_enabled())
if not repos:
    raise RuntimeError("no enabled DNF repositories")
enabled = set(repo.id for repo in repos)
missing_required = required - enabled
if missing_required:
    raise RuntimeError("required Rocky security repositories are not enabled: " + ",".join(sorted(missing_required)))
verified = []
refresh = []
coverage_gaps = []
for repo in repos:
    required_repo = repo.id in required
    try:
        repo.load()
    except dnf.exceptions.RepoError as exc:
        if strict and required_repo:
            raise
        if required_repo:
            refresh.append(repo.id)
        else:
            coverage_gaps.append(repo.id)
        continue
    if strict and repo._repo.isExpired():
        if required_repo:
            raise RuntimeError("expired DNF metadata for required repository " + repo.id)
        coverage_gaps.append(repo.id)
        continue
    path = repo.get_metadata_path("updateinfo")
    if not path:
        if strict and required_repo:
            raise RuntimeError("missing updateinfo metadata for enabled repository " + repo.id)
        if required_repo:
            refresh.append(repo.id)
        else:
            coverage_gaps.append(repo.id)
        continue
    if not os.path.isfile(path) or os.path.getsize(path) == 0:
        if strict and required_repo:
            raise RuntimeError("missing updateinfo metadata for enabled repository " + repo.id)
        if required_repo:
            refresh.append(repo.id)
        else:
            coverage_gaps.append(repo.id)
        continue
    if path.endswith(".solvx"):
        pass
    else:
        content = repo.get_metadata_content("updateinfo")
        if not content:
            if strict and required_repo:
                raise RuntimeError("empty updateinfo metadata for enabled repository " + repo.id)
            if required_repo:
                refresh.append(repo.id)
            else:
                coverage_gaps.append(repo.id)
            continue
        try:
            root = ET.fromstring(content)
        except ET.ParseError:
            if strict and required_repo:
                raise
            if required_repo:
                refresh.append(repo.id)
            else:
                coverage_gaps.append(repo.id)
            continue
        if root.tag.rsplit("}", 1)[-1] != "updates":
            if strict and required_repo:
                raise RuntimeError("invalid updateinfo root for enabled repository " + repo.id)
            if required_repo:
                refresh.append(repo.id)
            else:
                coverage_gaps.append(repo.id)
            continue
    verified.append(repo.id)
base.close()
if refresh:
    print("rootfiles-updateinfo-refresh-needed:" + ",".join(refresh))
else:
    print("rootfiles-updateinfo-ok:" + ",".join(sorted(required.intersection(verified))))
    if coverage_gaps:
        print("rootfiles-updateinfo-coverage-gap:" + ",".join(sorted(set(coverage_gaps))))
`

const rockyAutomaticConfig = `# Managed by rootfiles
[commands]
upgrade_type = security
download_updates = yes
apply_updates = yes
reboot = never

[base]
exclude = nvidia*,libnvidia*,cuda*,cudnn*,nccl*,datacenter-gpu-manager*,nvidia-fabricmanager*
`

func rockySecurityCheck(ctx context.Context, rc *RunContext) (*CheckResult, error) {
	cfg := rc.Config.Modules.Security
	var changes []Change
	missing := missingPackages(rc, (&SecurityModule{}).packages(rc))
	if len(missing) > 0 {
		changes = append(changes, Change{Description: fmt.Sprintf("Install %v", missing), Command: packageInstallCommand(rc, missing)})
	}
	if cfg.UnattendedUpgrades {
		ready, _, err := verifyRockyUpdateinfo(ctx, rc, false)
		if err != nil {
			return nil, err
		}
		if !ready {
			changes = append(changes, Change{Description: "Refresh and verify DNF updateinfo metadata for enabled repositories", Command: "dnf makecache && verify enabled-repository updateinfo metadata"})
		}
		if !fileEquals(rc, rockyAutomaticConfigPath, rockyAutomaticConfig) {
			changes = append(changes, Change{Description: "Configure security-only automatic updates for Rocky baseos/appstream (no reboot, GPU packages excluded)", Command: "write " + rockyAutomaticConfigPath})
		}
		if !rockyUnitActive(ctx, rc, rockyAutomaticTimer) {
			changes = append(changes, Change{Description: "Enable DNF automatic security update timer", Command: "systemctl enable --now " + rockyAutomaticTimer})
		}
	}
	if cfg.TimeSync {
		if !rockyUnitActive(ctx, rc, "chronyd") {
			changes = append(changes, Change{Description: "Enable chronyd time synchronization", Command: "systemctl enable --now chronyd"})
		}
	}
	return &CheckResult{Satisfied: len(changes) == 0, Changes: changes}, nil
}

func rockySecurityApply(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	cfg := rc.Config.Modules.Security
	var messages, warnings []string
	changed := false
	metadataReady := false
	if cfg.UnattendedUpgrades {
		if err := rc.APT.Update(ctx); err != nil {
			return nil, fmt.Errorf("dnf makecache: %w", err)
		}
		ready, coverageGaps, err := verifyRockyUpdateinfo(ctx, rc, true)
		if err != nil {
			return nil, err
		}
		if !ready {
			return nil, fmt.Errorf("enabled Rocky repositories still lack current updateinfo metadata after DNF refresh")
		}
		metadataReady = true
		if len(coverageGaps) > 0 {
			warnings = append(warnings, "security advisory coverage gap for enabled repositories without verified updateinfo: "+strings.Join(coverageGaps, ", "))
		}
	}
	missing := missingPackages(rc, (&SecurityModule{}).packages(rc))
	if len(missing) > 0 {
		if !metadataReady {
			if err := rc.APT.Update(ctx); err != nil {
				return nil, fmt.Errorf("dnf makecache: %w", err)
			}
		}
		if err := rc.APT.Install(ctx, missing); err != nil {
			return nil, fmt.Errorf("installing %v: %w", missing, err)
		}
		messages = append(messages, "installed "+strings.Join(missing, ", "))
		changed = true
	}
	if cfg.UnattendedUpgrades {
		if !fileEquals(rc, rockyAutomaticConfigPath, rockyAutomaticConfig) {
			if err := rc.Runner.MkdirAll(filepath.Dir(rockyAutomaticConfigPath), 0755); err != nil {
				return nil, err
			}
			wrote, err := ensureFile(rc, rockyAutomaticConfigPath, rockyAutomaticConfig, 0644)
			if err != nil {
				return nil, fmt.Errorf("writing %s: %w", rockyAutomaticConfigPath, err)
			}
			if wrote {
				messages = append(messages, "security-only automatic DNF updates configured for baseos/appstream; automatic reboot disabled")
				changed = true
			}
		}
		if !rockyUnitActive(ctx, rc, rockyAutomaticTimer) {
			if err := enableUnit(ctx, rc, rockyAutomaticTimer, true); err != nil {
				warnings = append(warnings, "enabling "+rockyAutomaticTimer+": "+firstLine(err.Error()))
			} else {
				messages = append(messages, rockyAutomaticTimer+" enabled")
				changed = true
			}
		}
	}
	if cfg.TimeSync && !rockyUnitActive(ctx, rc, "chronyd") {
		if err := enableUnit(ctx, rc, "chronyd", true); err != nil {
			warnings = append(warnings, "enabling chronyd: "+firstLine(err.Error()))
		} else {
			messages = append(messages, "chronyd enabled")
			changed = true
		}
	}
	return &ApplyResult{Changed: changed, Messages: messages, Warnings: warnings}, nil
}

func rockyUnitActive(ctx context.Context, rc *RunContext, unit string) bool {
	res, err := rc.Runner.Query(ctx, "systemctl", "is-active", unit)
	return err == nil && strings.TrimSpace(res.Stdout) == "active"
}

func verifyRockyUpdateinfo(ctx context.Context, rc *RunContext, strict bool) (bool, []string, error) {
	python := ""
	if rc.Runner.CommandExists(rockyPlatformPython) {
		python = rockyPlatformPython
	} else if rc.Runner.CommandExists("python3") {
		python = "python3"
	}
	if python == "" {
		return false, nil, fmt.Errorf("Rocky security updates require platform-python with the DNF API; refusing to enable unverified security-only updates")
	}
	args := []string{"-c", rockyUpdateinfoCheck}
	if strict {
		args = append(args, "strict")
	}
	res, err := rc.Runner.Query(ctx, python, args...)
	if err != nil {
		return false, nil, fmt.Errorf("cannot verify updateinfo metadata for each enabled Rocky repository; refusing to enable security-only automatic updates: %w", err)
	}
	output := strings.TrimSpace(res.Stdout)
	var coverageGaps []string
	ready := false
	if strings.HasPrefix(output, "rootfiles-updateinfo-refresh-needed:") {
		return false, nil, nil
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "rootfiles-updateinfo-ok:") {
			ready = line == "rootfiles-updateinfo-ok:appstream,baseos" || line == "rootfiles-updateinfo-ok:baseos,appstream"
		}
		if rest, ok := strings.CutPrefix(line, "rootfiles-updateinfo-coverage-gap:"); ok && rest != "" {
			coverageGaps = append(coverageGaps, strings.Split(rest, ",")...)
		}
	}
	if !ready {
		return false, nil, fmt.Errorf("DNF API did not verify parseable updateinfo metadata for required Rocky repositories baseos and appstream")
	}
	return true, coverageGaps, nil
}
