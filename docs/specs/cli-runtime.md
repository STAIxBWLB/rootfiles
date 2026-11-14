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
  - internal/cli/report_helpers.go
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
- **Display policy**: persistent `--color=auto|always|never` and `--ascii` configure stdout and stderr independently through `ui.ConfigureWriter`, after flag parsing and before validation/preflight, and for help/diagnostics. The shared v1 color precedence and marker alphabet live in `internal/ui`; JSON and Prometheus bypass human transformations. `--verbose` enables INFO diagnostics (`check` also keeps its local `-v`); without it the operator logger starts at WARN. Audit records keep their INFO records.
- **Output writers**: CLI constructors and report callers put `cmd.OutOrStdout()` in `RunContext.Out`; module reports resolve it through `rc.Output()` with a stdout fallback for older callers. CLI text uses the command writer; console logging and apply-recording warnings use the error writer. Explicit metadata/audit stderr warnings remain separate from machine stdout.
- **Runner** (`newRunner`): dry-run is passed through; a backup session (`exec.NewBackup(state.BackupsDir(), ...)`) is attached only for real mutating runs as root, so `rootfiles rollback` can undo them. See [state-rollback](state-rollback.md) and [exec-packages](exec-packages.md).
- **Exit codes** (`ExitError`, `exitDrift` in `internal/cli/output.go`; printing at the `cli.Execute` boundary): 0 success, 2 drift or findings on read-only reports, 1 any other error. An `ExitError` with an empty `Msg` exits silently. Cobra flag/argument errors exit 2, print the error, usage and a help command. Errors print once as `rootfiles: <what>: <why>` on stderr; only a TTY stderr gets a failure marker. An optional `ExitError.Next` prints the known next step, never an invented fix. Never print an error inside a command and also return it; `SilenceErrors` leaves printing to `Execute`.
- **Standard v1 adoption exception** (#133 AC3): all existing `exitDrift` results retain code 2, including `check` pending changes, `doctor` findings, status drift, fleet read reports and partial rollouts, `update --check`, `tunnel update --check`, the user lifecycle diff (`user audit`) and fleet targets. This preserves deployed monitoring, generated `SuccessExitStatus=2` units, post-update checks and mixed-version fleet interpretation. Usage errors also use code 2; scripts distinguish invocation errors by the diagnostic/usage on stderr. Only the canonical standard's generic result-failure code is excepted, not the display policy.
- **Output formats** (`addOutputFlag`, `addOutputFlagWithMetrics`, `outputFormat`): `--json` is the standard JSON spelling; `-o/--output text|json` remains accepted. Conflicting explicit output choices are usage errors; `prometheus` is accepted only on commands annotated `rootfiles/prometheus`. Unknown values are an error. Backup destinations use `backup --dir` and `user backup --file`; their old `-o/--output` spellings remain deprecated path aliases for one minor release. Canonical path flags win when both are supplied. Native JSON goes through `writeJSON` (indented, no HTML escaping). Fleet results share the encoding implementation with their existing HTML escaping enabled; field order, indentation and final newline are unchanged. Shared `exitDrift` retains controller exit code 2.
- **Target resolution for reports** (`resolveTarget`, used by check, status, doctor and config show/validate): `--profile`/`ROOTFILES_PROFILE` or `--config` first; then the applied copy `state.AppliedConfigPath()` only when `reusableConfig` (root-owned, not group/other-writable, regular file, every ancestor root-only via `config.RootOnlyBase`, readable) and `sameRun` (its SHA-256 equals the last run's `ConfigSHA256`) both pass; then an absolute recorded config path that still exists; then the recorded profile; then `SuggestProfile()`.
- **Target resolution for root-mutating subcommands** (`resolveRunTarget`, used by user, gpu, tunnel, schedule, tenant and backup through `buildRunContext`/`buildConfigFreeRunContext` in `internal/cli/tunnel.go`; the config-free variant falls back to `config.Fallback` on a load error): flags/env first; then the applied copy under the same two checks; otherwise `minimal` with home-base detection. They never re-read a recorded path, re-resolve a recorded profile or take the detected suggestion. A dry run of a mutating command that cannot reuse the copy warns. This is the AGENTS.md rule; `buildRunContext` lives in the [cloudflared](cloudflared.md) spec's file but the contract is owned here.
- **Reported target** (`reportedTarget`): when the applied copy was used, reports name the recorded run's profile and path, never the copy's path.
- **Provenance** (`appliedFingerprint` in `internal/cli/provenance.go`): `applied_config_sha256` is reported only when the last run succeeded, carries a digest, targets the same profile and absolute config path (`recordedConfigPath`), and the digest equals the current config's `Fingerprint()`. Legacy, failed or unrelated runs report nothing, never a guess.
- `validateModules` extracts the caller-selected module names without loading a target or broadening selection. Apply/check return capability failures, status collects them, doctor adds a skip finding, and config inspection keeps its Rocky-only validation.
- `readOnlyRunContext` shares the dry-run Runner, caller logger, report backend and unattended flag, without audit, backups, target resolution or home-base defaults. Check retains its strict `NewPackageManager` guard; overview/doctor retain their backend fallback.
- `maskSecret` shares the config-show policy (empty stays empty, up to eight bytes fully masked, longer values show four leading bytes). Interactive apply summaries use this same safer policy; token-file labels and machine outputs are unchanged.
- `statusPackageManager` picks a package backend for reports even on unsupported systems; capability errors become report findings, never package mutations.
- `detectSystem` (`internal/cli/native_system.go`) is a package variable so tests never depend on the host distro.

## Behaviour

- `tenant cpuset` is a native mutation: it uses the same root, Linux and global-lock preflight as the other tenant mutations.

- Aliases resolve to one command object: `tenant reset` -> `passwd`, `tunnel install` -> `setup`. Reader and mutation annotations stay on that canonical command. Deprecated backup/user flags retain old invocations; `user passwd --suffix` remains accepted until the next minor release.
- Root help includes Rocky/fleet and derives profile names from `config.AvailableProfiles()`. The audited commands carry examples; Use strings contain positional syntax only, checked against Args arity by `cli_surface_test.go`. VLAN help/prompt examples use `203.0.113.10/32`.

- Root `-v` remains the version shorthand; it is not a verbose alias. Persistent root flags: `--color`, `--ascii`, `--verbose`, `--yes`, `--dry-run`, `--profile`, `--config`, `--home-base`, `--user`, `--ssh-pubkey`, `--tunnel-token`, `--vlan-address`. `--force` and `--module` are command-scoped. A root input is accepted only when the command or a reader parent declares it. `schedule status` is not a config reader; enable/disable remain config-target readers.
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
