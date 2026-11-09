---
id: doctor-schedule
type: module
title: Doctor and scheduled reports
status: active
parent: architecture
depends-on:
  - engine
paths:
  - internal/module/doctor.go
  - internal/cli/doctor.go
  - internal/cli/schedule.go
---

# Doctor and scheduled reports

## Purpose

`rootfiles doctor` runs read-only health checks of the live system that matter for operating a server remotely: can operators still get in, is a disk about to fill, is a reboot pending, is the clock synced, is the GPU driver loaded, are units failing, are exporters exposed. Unlike `check` it does not compare against the profile. `rootfiles schedule` installs a systemd timer that runs `check` and `doctor` periodically and publishes their JSON reports and Prometheus textfile metrics, which [fleet](fleet.md) and the [monitoring-hub](monitoring-hub.md) alert rules consume.

## Contracts and invariants

- **Doctor never changes the system.** `runDoctor` builds its own `RunContext` with a dry-run `exec.Runner` and needs no root or lock (not in `mutatingCommands`). `doctorSSH` reports an unavailable effective-config query with repair guidance and never creates runtime directories.
- **Finding shape is an output contract.** `module.Finding{check, level, detail, hint}` with `level` in `ok|warn|fail|skip` (`LevelOK`...`LevelSkip`). JSON output is `{findings, failed, applied_config_sha256, version}`; `fleet doctor` reads the exit code and `applied_config_sha256` and embeds the rest verbatim, and the schedule writes it to `doctor.json` for other readers. Renaming fields or levels is a breaking output change.
- **Exit code.** 0 when no finding is `fail`; 2 (`exitDrift`, `internal/cli/output.go`) when at least one is `fail`, or `warn` with `--strict`. Load errors are not fatal: doctor falls back to `config.Fallback` and adds a `configuration` fail finding.
- **Report target.** Doctor reports against the same config `status`/`check` use (`resolveTarget`, `reportedTarget`, `appliedFingerprint`; owned by [cli-runtime](cli-runtime.md)). Capability errors become a `capabilities` skip finding; a failed last apply (`state.Last`) is a warn.
- **Checks** (`module.Doctor`, in this order):
  - `ssh access`: effective `sshd -T`; fail when password auth is off and no account has a key; warn when no account has a key. Per-account password state uses `sshd -T -C user=...` so `ssh.password_auth_users` Match blocks are honoured; password-only and no-login accounts are warned.
  - `firewall`: shared UFW inspection distinguishes absent (skip), inactive (warn), unknown query/status (fail with the inspection cause and repair hint), and active without every SSH port allowed (fail).
  - `reboot`: `/var/run/reboot-required` (+ `.pkgs`) is warn.
  - `disk <path>`: `/`, `users.home_base`, `modules.docker.storage_dir`, `modules.storage.data_dir`, one finding per filesystem (deduplicated by device). Grading in `diskLevel` from `doctor.disk` (`config.DoctorDiskConfig.Thresholds`): warn at `warn_percent` (default 80), fail at `fail_percent` (default 90) and, when `min_free` is set, only if free space is also below it.
  - `time sync`: `timedatectl NTPSynchronized`; unavailable is skip.
  - `gpu driver` / `gpu persistence`: only when `nvidia-smi` exists; a failing `nvidia-smi` is fail (driver/library mismatch named), persistence mode off is warn.
  - `systemd`: any failed unit is warn.
  - `exporter firewall` / `exporter exposure` (`doctorExporterExposure`): an unmanaged `rootfiles-exporter-firewall` helper is warn; with `exporter_firewall` the guard's `check` must pass; otherwise exposure is ok only on a private `listen_address`, with `perimeter_firewall`, or with active UFW source-restricted rules for every exporter port. Unknown UFW inspection is an exposure warning rather than a protection claim. Parsing/known-state semantics are owned by [ssh-access](ssh-access.md); the guard itself is owned by [monitoring-exporters](monitoring-exporters.md).
- **Prometheus output** (`-o prometheus`): `rootfiles_doctor_findings{level}` and `rootfiles_doctor_check_ok{check}` (1 for ok or skip). The hub alert rules (`internal/module/monitoring_hub.go`) consume `rootfiles_doctor_findings{level="fail"}` and the `rootfiles_check_timestamp_seconds` that `check -o prometheus` emits; changing a metric name needs the rules changed in the same PR.
- **Schedule units** (`scheduleService`, `scheduleTimer`): `rootfiles-check.service` (oneshot, `SuccessExitStatus=2` because drift is a report, not a failure) writes `check.json` and `doctor.json` into `state.Dir()` through a `.tmp` + `mv`, then writes `rootfiles.prom` (check + doctor Prometheus text) into `module.NodeExporterTextfileDir` only when that directory exists. `rootfiles-check.timer` uses `--on-calendar` (default `daily`), `RandomizedDelaySec=15m`, `Persistent=true`. The service runs the resolved path of the current executable.
- **First report.** `schedule enable` starts the service once (`--no-block`) when no `check.json` exists yet, so a new host is not reported stale; a failure only warns.
- Unit writes go through `rc.Runner` (dry-run aware); `--on-calendar` values containing a newline or `'` are rejected.

## Behaviour

- Schedule enable writes only units with real content changes, preserving old headers. It runs daemon-reload only after an actual unit write; existing enable/first-report behavior is unchanged.

- `rootfiles doctor [-o text|json|prometheus] [--strict]`.
- `rootfiles schedule enable [--on-calendar EXPR]`, `schedule disable` (both in `mutatingCommands`; context from `buildConfigFreeRunContext`). `disable` stops the timer, removes both units and reloads systemd, ignoring systemctl errors.
- The schedule runs reports only; it never applies changes.

## Distro support

- Ubuntu, DGX OS and Rocky. The firewall check reads UFW only (firewalld is not inspected), so a Rocky host without UFW gets a skip finding. Hints name APT packages (`openssh-server`).

## Safety

- Generated-file comparisons preserve legacy headers using the [shared managed-header rule](exec-packages.md#contracts-and-invariants); changing the marker alone never rewrites a converged file or reloads its service.

- SSH inspection never creates `/run/sshd`. If read-only `sshd -T` fails because of configuration, host-key access or missing runtime state, report failure with the operator command to inspect and repair it.

- Doctor must stay read-only and runnable by the fleet sudoers rule (`/usr/local/bin/rootfiles doctor -o json` exactly, see [fleet](fleet.md)); adding a flag requirement or a write would break fleet reporting.
- Report files are replaced atomically so readers never see partial JSON.

## Tests that pin it

- `internal/module/doctor_test.go`: `TestDiskLevel`, `TestDoctorReboot`, `TestDoctorDisksDedupesFilesystems`, `TestDoctorSSHWarnsPasswordOnlyAccounts`.
- `internal/module/monitoring_test.go`, `internal/module/monitoring_guard_test.go`: exporter exposure findings.
- `internal/config/loader_test.go`: `TestLoad_DoctorDiskThresholds`.
- `internal/cli/runtime_test.go`: `TestScheduleUnits`, `TestScheduleEnableStartsFirstReportOnlyWithoutOne`.
- `tests/scenarios/fleet.sh` (CI `fleet-scenario`) runs remote `doctor -o json`.

## Open issues

- [#91](https://github.com/STAIxBWLB/rootfiles/issues/91): `doctor` ignores `--module`; reject it.
- [#92](https://github.com/STAIxBWLB/rootfiles/issues/92): no `schedule status`; help text says how `check`/`status`/`doctor` differ.
- [#94](https://github.com/STAIxBWLB/rootfiles/issues/94): repeated capability validation and read-only `RunContext` construction across doctor/status/check.
