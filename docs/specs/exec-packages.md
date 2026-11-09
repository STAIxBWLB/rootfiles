---
id: exec-packages
type: module
title: Command Runner and Packages
status: active
parent: architecture
paths:
  - internal/exec/runner.go
  - internal/exec/managed_content.go
  - internal/exec/apt.go
  - internal/exec/package_manager.go
  - internal/module/packages.go
---

# Command Runner and Packages

## Purpose

The single path through which modules and commands run host commands and change files (`exec.Runner`), the package-manager contract shared by distro backends with its APT implementation, and the `packages` module that installs the profile's package list.

## Contracts and invariants

- **All host mutations go through `rc.Runner`** (AGENTS.md): `Run`, `RunEnv`, `RunInput`, `RunShell`, `WriteFile`, `MkdirAll`, `Symlink`, `Remove`, `Rename`. Each one is a no-op that only logs when `DryRun` is set. Never call `os.WriteFile`, `os.Remove`, `os.Rename`, `os.MkdirAll` or `os/exec` for a mutation in module code.
- **Backups before change**: when `Runner.Backup` is set, `WriteFile`, `Symlink`, `Remove` and `Rename` (its destination) call `Backup.Preserve` first, and a failed backup aborts the change. `MkdirAll` and plain `Run` commands are not backed up. See [state-rollback](state-rollback.md).
- `Remove` never recurses: a populated directory is an error, never data loss. Do not add a recursive remove for user data.
- **Reads are not dry-run gated**: `Query`, `ReadFile`, `FileExists` and `CommandExists` access the host even in dry run; the opt-in test query guard below can refuse command execution. `Query` is for read-only commands only (`dpkg-query`, `ufw status`, `systemctl is-active`); Check uses `Query`, never `Run`. Because dry-run does not suppress `Query`, tests must stub binaries on `PATH` (`fakeBin`) rather than rely on dry run.
- **Test query guard**: `Runner.QueryGuard`, when set, receives the executable resolved by `os/exec` before execution. Returning an error prevents execution, including during dry-run; nil preserves normal command execution. Module test contexts reject executable targets outside their private test root, following symlinks, and fail the test even when a caller ignores the query error. Fixture shell scripts may use `/bin/sh` as their interpreter.
- **Secrets on stdin**: `RunInput` never logs its input; use it for passwords and tokens instead of putting them on a command line. `Run*` log the full command string.
- Errors from `Run*`/`Query` include the command string and stderr; callers wrap them with what they were doing.
- **PackageManager** (`internal/exec/package_manager.go`): `Update`, `Install`, `IsInstalled`, `Installed`. `NewPackageManager` picks the backend from `config.ResolveDistro` (`apt` or `dnf`) and fails on an unsupported OS. The separate optional `APTRepositoryManager` owns `AddKeyring`/`AddSourceList`; concrete APT implements it, RPM does not. Docker/NVIDIA require that capability before queries or mutations, with no APT fallback for an RPM backend.
- **APT backend** (`internal/exec/apt.go`): every call runs with `DEBIAN_FRONTEND=noninteractive` and `NEEDRESTART_MODE=a` and waits up to 300 s for the dpkg lock. `Install` keeps modified conffiles (`--force-confdef`, `--force-confold`) and passes `--no-remove`, so an install aborts instead of removing conflicting packages. `Installed` is one `dpkg-query` call; only `ok installed` lines count and arch suffixes are stripped. `AddKeyring` is a no-op when the key file exists.
- **packages module** (`internal/module/packages.go`): installs `Config.AllPackages()` (profile `packages` plus accumulated `packages_extra`, de-duplicated). Check reports one change listing the missing packages with a distro-correct command (`packageInstallCommand`); Apply refreshes the index and installs only the missing ones, and reports `Changed` only when it installed something. It never upgrades or removes packages.

- **Managed header compatibility** (`ManagedContentEqual`): compare the old `# Managed by rootfiles-v2` and new `# Managed by rootfiles` as equivalent, also for `//` comments and after a shebang. Only the leading management marker is normalized; suffixes, credentials and body bytes remain exact. New/actually changed files use the new marker. Header-only differences never trigger a write, backup or service reload. Module and schedule comparisons use this shared rule to preserve converged hosts.

## Behaviour

- Enabled by `modules.packages.enabled` (on in `base` and `rocky`). Runs third in `defaultOrder`, after `system`, so an APT mirror applies to the first install.
- `rc.APT` is the field name for the package manager on every distro. `apply` and `check` set it with `NewPackageManager`; reports use `statusPackageManager`; `buildRunContext` in `internal/cli/tunnel.go` always sets the APT backend (see #91).

## Distro support

- Ubuntu/DGX OS: APT backend above.
- Rocky: the RPM backend in `internal/exec/rpm.go` (owned by [distro-rocky](distro-rocky.md)) implements the same interface; package names in the `rocky` profile are native RPM names.

## Safety

- Package installs never remove packages (`--no-remove`) and never prompt.
- A new Runner method that mutates must honour `DryRun` and call `preserve` before overwriting or removing a path.
- Do not log secrets: use `RunInput` for anything confidential.

- Deferred (2026-11-10, from the 2026-09 improvement plan): no default timeout on native commands. Commands run under the caller's context (Ctrl-C cancels them) and fleet rollouts are bounded per host by `--timeout`; a default per-command limit could cut off long package installs or image pulls mid-way. Revisit with a measured hang.

## Tests that pin it

- `internal/exec/exec_test.go`: `TestRunner_DryRunDoesNotExecute`, `TestRunner_RunEnvAndInput`, `TestRunner_RemoveIsNotRecursive`, `TestParseDpkgQuery`.
- `internal/exec/runner_test.go`: optional query guard permits fixtures and prevents a refused executable from creating its marker.
- `internal/module/hostfree_test.go`: private test paths, fixture queries, and ambient binary and symlink refusal.
- `internal/exec/backup_test.go`: `TestBackupPreserveAndRestore` (Runner writes preserved for rollback).
- `internal/module/packages_test.go`: `TestPackagesModule_CheckNoPackagesIsSatisfied`, `TestPackagesModule_ApplyNoPackagesNoChange`.
- `tests/integration/run-module.sh` and `tests/integration/run-profile.sh` (CI jobs `module`, `integration`) install real packages in containers; `tests/scenarios/dry-run-all-profiles.sh` (CI job `scenario`) checks dry run changes nothing.
