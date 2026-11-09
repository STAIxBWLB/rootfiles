---
id: security
type: module
title: Security baseline
status: active
parent: architecture
depends-on:
  - engine
paths:
  - internal/module/security.go
---

# Security baseline

## Purpose

- `security` module: security-only automatic updates, an sshd fail2ban jail and NTP time sync. Runs after `ssh` in `defaultOrder` because the fail2ban jail follows the SSH port.
- Ubuntu/DGX OS logic lives in `internal/module/security.go`; on Rocky, `SecurityModule.Check`/`Apply` delegate to `rockySecurityCheck`/`rockySecurityApply` (spec [distro-rocky](distro-rocky.md)).

## Contracts and invariants

- Unattended updates are security-only, never reboot and never touch the NVIDIA/CUDA stack. The managed policy `unattendedPolicyContent` at `/etc/apt/apt.conf.d/zzzzzz-rootfiles-unattended-upgrades` (`unattendedPolicyPath`) must:
  - sort after vendor and installer files (APT reads `apt.conf.d` in name order and merges lists), and start with `#clear` for `Allowed-Origins` and `Origins-Pattern`;
  - allow only the three `securityOrigins` (`-security`, ESM apps security, ESM infra security);
  - blacklist `nvidia-`, `libnvidia-`, `cuda-`, `libcudnn`, `libnccl`, `datacenter-gpu-manager`, `nvidia-fabricmanager`;
  - set `Automatic-Reboot "false"`. `rootfiles doctor` reports reboot-required instead.
- `20auto-upgrades` (`autoUpgradesPath`) enables periodic list update and unattended-upgrade.
- The effective policy is judged, not just the file: `nonSecurityOrigins` reads `apt-config dump` and reports any allowed origin outside `securityOrigins` and any `Origins-Pattern`. Check reports it as drift; real Apply fails after writing when the effective policy is still wider (it cannot fix a later-sorting file, so it must not report success). An `apt-config` failure counts as not security-only.
- Managed apt.conf.d files must be world-readable (non-root apt reads them); Check flags and Apply `chmod 0644`s them (`unreadableAptConf`).
- Legacy files in `legacyUnattendedPaths` (the earlier `52rootfiles-unattended-upgrades` and a hand-placed origins fix) are drift; Apply removes them through `rc.Runner.Remove`, which backs them up first. Keep this compatibility cleanup through v0.19; removal is eligible for v0.20.0 only after rollout verification.
- fail2ban jail `/etc/fail2ban/jail.d/rootfiles-sshd.conf` (`fail2banJail`) uses `sshPorts` (see [ssh-access](ssh-access.md)), `backend = systemd`, `maxretry = 5`, `findtime = 10m`, `bantime = 1h`. A port change rewrites the jail.
- Time sync manages the NTP daemon already installed (`chrony`, `ntpsec`, `ntp`, `openntpd`, in that order, `timeSyncPackage`) and only falls back to `systemd-timesyncd`, because installing it would remove a conflicting daemon. A failed `timedatectl` status query adds no pending change, matching the warning-only Apply path; it is not a claim that NTP is configured. Apply reports the query failure with repair guidance and attempts no time-sync mutation. Dry-run must not block an operation that real Apply only warns about.
- All writes go through `rc.Runner` (`MkdirAll`, `WriteFile`, `Remove`, `Run`), so dry-run and backups apply.

## Behaviour

- Ordinary policy/jail/automatic-file writes share `ensureFile`. APT mode repair, legacy cleanup and effective-policy validation stay separate; content equality does not claim to repair permissions. Unit enable helpers preserve prior flags and warning policies.

- Check (Ubuntu): missing packages (`unattended-upgrades`, `fail2ban` + `python3-systemd`, the time-sync package); policy file drift, legacy files or unreadable files as one change, else effective-policy findings; jail drift; no NTP change when `timedatectl show -p NTP --value` fails, or both the service-enable and NTP-enable commands when a successful query returns a value other than `yes`. Package installation hints use `packageInstallCommand`.
- Apply (Ubuntu): `apt update` + install only when packages are missing; writes the two policy files when they differ; chmod, legacy removal; effective-policy verification (skipped in dry-run). Jail write then `systemctl enable` and `restart fail2ban` (failures are warnings). Time sync: `systemctl enable --now <unit>` and `timedatectl set-ntp true` (failures are warnings).
- `Changed` is true only for a successful write or pending operation. For time sync, each successful service-enable or NTP-enable operation contributes independently; failed commands produce warnings and no success message. When both fail, time sync alone does not set `Changed`.

## Distro support

- Ubuntu and DGX OS: full module.
- Rocky: DNF automatic and chronyd via `security_rocky.go`. `security.fail2ban` is rejected by `ValidateCapabilities` (it would need an explicitly configured EPEL repository), so the package list contains no Rocky fail2ban branch.

## Safety

- Generated-file comparisons preserve legacy headers using the [shared managed-header rule](exec-packages.md#contracts-and-invariants); changing the marker alone never rewrites a converged file or reloads its service.

- Never widen the update policy to non-security origins, enable automatic reboot, or drop the NVIDIA/CUDA blacklist; driver changes need a planned reboot with matching kernel modules.
- Never report the policy applied when the effective configuration is wider than the managed one.
- Removing a legacy file must stay a Runner removal (backup first).

## Tests that pin it

- `TestAgreementTimeSyncPartialFailures` keeps query failures warning-only; CLI `TestApplyDryRunUnknownNTPDoesNotFail` proves dry-run exits successfully whether security packages are pending or installed.

- `internal/module/agreement_test.go`: Ubuntu/Rocky install plans, current/unknown NTP status, both time-sync commands failing, partial success and exact attempted commands.
- `internal/module/security_test.go`: NVIDIA exclusion and no reboot, jail uses SSH ports, `#clear` and hand-fix migration, Check judges the effective policy, world-readable policy.
- `internal/module/rocky_test.go`: Rocky security-only policy, cache-only updateinfo, refresh before writing, coverage gap.
- CI `module` job: `security` on `ubuntu-22.04` and `ubuntu-24.04` (`tests/integration/run-module.sh` asserts every effective allowed origin ends in `-security`).
- CI `rocky` job: DNF automatic readback (see [distro-rocky](distro-rocky.md)).
