---
id: cli-runtime
type: module
title: CLI Runtime
status: active
parent: architecture
paths:
  - internal/cli/root.go
  - internal/cli/runtime.go
  - internal/cli/output.go
  - internal/cli/provenance.go
  - internal/cli/native_system.go
---

# CLI Runtime

## Purpose

Shared plumbing every native `rootfiles` command runs through: the cobra root and its persistent flags, the root/lock preflight for mutating commands, the audit log, the per-run `exec.Runner` with its backup session, output formats and exit codes, target resolution (which profile or config a command evaluates) and applied-config provenance.

## Contracts and invariants

- **Mutating commands are listed once.** `mutatingCommands` in `internal/cli/runtime.go` is the only list of root-mutating command paths; `markMutating` turns it into the `rootfiles/mutates` annotation. A new command that changes the host must be added there, or it runs without root, lock or audit.
- **Preflight order** (`preflight`, `requireRootAndLock`): reject ignored inherited inputs first, using reader declarations at the command that consumes them; non-annotated commands then pass; `--dry-run` passes without root or lock; otherwise euid must be 0, then the host OS must be Linux (`nativeHostOS`), then `state.Acquire()` takes the global non-blocking flock, then an audit `start` record is written. Root is checked before the OS so the error names the real cause. The lock is held until process exit (`heldLock`, released in `Execute`).
- Commands that are read-only by default but mutate on a sub-path (`rollback <id>`) call `requireRootAndLock` themselves at the point of mutation.
- **Audit log** (`openAudit`, `auditf`): JSON lines via `slog` to `state.LogPath()` (default `/var/log/rootfiles.log`, mode 0640), each with `pid`, `version` and `cmd`. It is opened lazily and silently skipped when it cannot be opened (non-root). `newLogger` tees operator log lines into it only for real (non-dry-run) mutating runs.
- **Runner** (`newRunner`): dry-run is passed through; a backup session (`exec.NewBackup(state.BackupsDir(), ...)`) is attached only for real mutating runs as root, so `rootfiles rollback` can undo them. See [state-rollback](state-rollback.md) and [exec-packages](exec-packages.md).
- **Exit codes** (`ExitError`, `exitDrift` in `internal/cli/output.go`; printing in `cmd/rootfiles/main.go`): 0 success, 2 drift or findings on read-only reports, 1 any other error. An `ExitError` with an empty `Msg` exits silently. Never print an error inside a command and also return it; `SilenceErrors` leaves printing to `main`.
- **Output formats** (`addOutputFlag`, `addOutputFlagWithMetrics`, `outputFormat`): `-o text|json`; `prometheus` is accepted only on commands annotated `rootfiles/prometheus`. Unknown values are an error. Backup destinations use `backup --dir` and `user backup --file`; their old `-o/--output` spellings remain deprecated path aliases for one minor release. Canonical path flags win when both are supplied. JSON goes through `writeJSON` (indented, no HTML escaping).
- **Target resolution for reports** (`resolveTarget`, used by check, status, doctor and config show/validate): `--profile`/`ROOTFILES_PROFILE` or `--config` first; then the applied copy `state.AppliedConfigPath()` only when `reusableConfig` (root-owned, not group/other-writable, regular file, every ancestor root-only via `config.RootOnlyBase`, readable) and `sameRun` (its SHA-256 equals the last run's `ConfigSHA256`) both pass; then an absolute recorded config path that still exists; then the recorded profile; then `SuggestProfile()`.
- **Target resolution for root-mutating subcommands** (`resolveRunTarget`, used by user, gpu, tunnel, schedule, tenant and backup through `buildRunContext`/`buildConfigFreeRunContext` in `internal/cli/tunnel.go`; the config-free variant falls back to `config.Fallback` on a load error): flags/env first; then the applied copy under the same two checks; otherwise `minimal` with home-base detection. They never re-read a recorded path, re-resolve a recorded profile or take the detected suggestion. A dry run of a mutating command that cannot reuse the copy warns. This is the AGENTS.md rule; `buildRunContext` lives in the [cloudflared](cloudflared.md) spec's file but the contract is owned here.
- **Reported target** (`reportedTarget`): when the applied copy was used, reports name the recorded run's profile and path, never the copy's path.
- **Provenance** (`appliedFingerprint` in `internal/cli/provenance.go`): `applied_config_sha256` is reported only when the last run succeeded, carries a digest, targets the same profile and absolute config path (`recordedConfigPath`), and the digest equals the current config's `Fingerprint()`. Legacy, failed or unrelated runs report nothing, never a guess.
- `statusPackageManager` picks a package backend for reports even on unsupported systems; capability errors become report findings, never package mutations.
- `detectSystem` (`internal/cli/native_system.go`) is a package variable so tests never depend on the host distro.

## Behaviour

- Aliases resolve to one command object: `tenant reset` -> `passwd`, `tunnel install` -> `setup`. Reader and mutation annotations stay on that canonical command. Deprecated backup/user flags retain old invocations; `user passwd --suffix` remains accepted until the next minor release.
- Root help includes Rocky/fleet and derives profile names from `config.AvailableProfiles()`. The audited commands carry examples; Use strings contain positional syntax only, checked against Args arity by `cli_surface_test.go`. VLAN help/prompt examples use `203.0.113.10/32`.

- Persistent root flags: `--yes`, `--dry-run`, `--profile`, `--config`, `--home-base`, `--user`, `--ssh-pubkey`, `--tunnel-token`, `--vlan-address`. `--force` and `--module` are command-scoped. A root input is accepted only when the command or a reader parent declares it. `schedule status` is not a config reader; enable/disable remain config-target readers.
- Context is cancelled on SIGINT/SIGTERM (`cmd/rootfiles/main.go`), so in-flight commands stop instead of leaving orphans.

## Distro support

- Native mutations require Linux; on another OS (operator laptops) they fail before the lock with a pointer to the fleet controller. Read-only commands run anywhere the binary builds.
- Distro-specific behaviour is not decided here; see [distro-rocky](distro-rocky.md).

## Safety

- Never write secrets to the audit log or the backup note. Audit `args` and backup notes redact password/token flags and the positional token for both `tunnel setup` and its `install` alias.
- The applied copy is trusted only under the root-only and same-run checks above; do not relax them, since a file a non-root user controls would decide what root configures.
- Tests stub `geteuid`, `nativeHostOS`, `detectSystem`, `configTrustRoot`, `configOwnerUID` and the `ROOTFILES_*_FILE`/`ROOTFILES_STATE_DIR` paths; never let a test take the real lock or write the real audit log.

## Tests that pin it

- `internal/cli/runtime_test.go`: `TestMarkMutating`, `TestPreflight` (non-root refused, read-only and dry-run exempt, second lock fails), `TestNativeMutationsRefuseNonLinuxBeforeLock`.
- `internal/cli/output_test.go`: `TestResolveTarget_PrefersAppliedCopy`, `TestReportedTarget_UsesResolvedSnapshot`.
- `internal/cli/provenance_test.go`: `TestAppliedFingerprintRequiresSuccessfulMatchingRecordedIntent`.
- `internal/cli/tunnel_test.go`: `TestBuildRunContext_ConfigSelection`, `TestResolveRunTarget_FallbackAfterConfigApply`, `TestResolveRunTarget_WarnsMutatingPreview`.
- `internal/cli/check_test.go`: `TestCheck_RejectsUnknownOutput`.
- `internal/cli/docs_test.go`: `TestDocsCommandsCoverCobraTree` (every visible command has a `### <path>` heading in `docs/commands.md` that names its visible local flags).
- `tests/scenarios/state-rollback-lock.sh` (CI job `scenario`): real refusal without root and while another command holds the lock.

## Open issues

- [#94](https://github.com/STAIxBWLB/rootfiles/issues/94): shared CLI helpers (`validateModules`, `readOnlyRunContext`, `maskSecret`) and output through `internal/ui`.
