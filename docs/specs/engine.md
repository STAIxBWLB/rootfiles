---
id: engine
type: module
title: Module Engine
status: active
parent: architecture
paths:
  - internal/module/module.go
  - internal/module/files.go
  - internal/module/jsondb.go
  - internal/module/preflight.go
  - internal/module/package_commands.go
  - internal/cli/apply.go
  - internal/cli/check.go
  - internal/cli/status.go
---

# Module Engine

## Purpose

The Module interface, the registry and execution order, and the three commands that drive modules on one host: `apply` (Check then Apply, recorded in state), `check` (drift report, exit 2) and `status` (overview, exit 0). Per-feature module specs are children of [architecture](architecture.md) and declare `depends-on: [engine]`, because they implement the contract stated here.

## Contracts and invariants

- **Module interface** (`internal/module/module.go`): `Name()`, `Check(ctx, rc)`, `Apply(ctx, rc)`. `Check` has no side effects and reports pending work as `Change`s; `CheckResult.Satisfied` gates Apply. `Apply` acts only on what Check reports, is idempotent, and sets `ApplyResult.Changed` only for real work. Non-fatal problems go in `ApplyResult.Warnings`, not errors.
- A change apply cannot make itself (the operator must act first) is a `Change` with `State: ChangeBlocked`. A dry run (`RunAll` with `rc.DryRun`) fails the module when any change is blocked, so a dry run never passes when the real apply is bound to fail.
- **Registry and order**: `NewRegistry()` registers all 14 modules; `defaultOrder` comes from `config.ModuleNames()` and keeps the static execution order (locale, system, packages, users, ssh, security, docker, nvidia, gpu, cloudflared, storage, network, monitoring, tenant). The two lists must stay in sync. Ordering carries the dependencies (comments in the config module metadata: system before packages for the APT mirror, users before ssh so keyed accounts exist before password auth is disabled, security after ssh for the fail2ban port, tenant after docker and nvidia). Do not add a dependency mechanism; place the module in the order.
- `Registry.Resolve(cfg, filter)` returns enabled modules (`config.IsModuleEnabled`) in `defaultOrder`, narrowed by `--module`.
- **RunAll**: Check then Apply per module; a module error is recorded and the remaining modules still run; a cancelled context marks the rest `skipped` and returns `interrupted`. Outcome statuses are `satisfied`, `changed`, `failed`, `skipped` (and `pending` in dry run). `rc.moduleSucceeding(name)` lets a later module rely on an earlier one only while that one is in the run and has not failed.
- **CheckAll** returns on the first Check error (used by `check` and `status`).
- **Preflight** (`internal/module/preflight.go`): cross-module safety checks run before any module mutates the host. Today only `monitoringPreflight` (port ownership, exporter firewall); a new cross-module guard belongs here, not inside a module's Apply.
- **Change commands** (`internal/module/package_commands.go`): `packageInstallCommand`/`packageUpdateCommand` render the distro-correct command for `Change.Command`; use them instead of hard-coding `apt-get`.
- **apply** (`internal/cli/apply.go`), in this order: detect system; pick the profile (`selectProfile`: flag or `--config`, else the detected suggestion, interactive pick unless `--yes`/`ROOTFILES_YES`); load config with `--home-base`; apply flag overrides (`--tunnel-token`, `--vlan-address`) and account flags (`--user`/`--ssh-pubkey` or `ROOTFILES_USER`/`ROOTFILES_SSH_PUBKEY` become a declared account); `Validate`; interactive review; `Validate` again; `config.ValidateCapabilities` for the resolved modules; `Fingerprint`; `module.Preflight`; `RunAll`. Every refusal happens before the first module mutates the host.
- After a real (non-dry-run) apply, failed or not: `state.Record` writes the run with `ConfigSHA256`, profile, absolute config path, backup id and module outcomes; then the resolved config (`cfg.Effective()`, no `extends`, no inline token) is kept as the applied copy. If saving the copy fails it is removed, so a stale copy never outlives its run. A dry run records nothing.
- The interactive review flags settings weaker than the profile (`securityDowngrades`) and then defaults the confirmation to "no".
- **check** (`internal/cli/check.go`): always uses a dry-run Runner, `ValidateCapabilities` before `CheckAll`, exit 2 (`ExitError{Code: exitDrift}`, silent) when any module is unsatisfied, 1 on error, 0 otherwise. Formats: text, json (`checkReport` with `applied_config_sha256` when provenance holds), prometheus (`rootfiles_module_satisfied`, `rootfiles_module_pending_changes`, `rootfiles_check_timestamp_seconds`). These metric names and the JSON fields are consumed by the schedule timer, fleet and hub alert rules; renaming one is a contract change.
- **status** (`internal/cli/status.go`): read-only overview that always exits 0. A config load error falls back to `config.Fallback` and is reported (`config_error`, `home_base_ambiguous`), not returned. Text and JSON come from one `collectStatus` snapshot (modules, last apply, GPU ledger, tunnel, users, provenance); text renderers never query again. Capability errors remain `module_check_error` in JSON and now appear in text before unsupported module checks. Text-only captured details retain existing missing-binary/query-failure, database-error and scan-error display behavior without extending the JSON schema. Without a known home base it skips the user and GPU databases rather than reading `/home`.
- check, status and config inspection resolve their target with `resolveTarget` and report `reportedTarget`; see [cli-runtime](cli-runtime.md).

- **Ordinary managed files** (`internal/module/files.go`): `ensureFile` compares through `ManagedContentEqual` before writing via Runner, reports whether content changed, and propagates non-ENOENT read errors with repair guidance. It does not create parent directories or claim to fix matching-file metadata; callers retain directory modes, ownership checks, atomic/transactional replacement and special whitespace policies.
- **Systemd primitives**: `systemdReload` reports a warning with a manual repair command. It does not promise a future automatic retry: a file write may consume drift so a later converged Check skips Apply. Callers keep their existing pending-work guards. `enableUnit` only constructs the previous enable/start command; it adds no hidden query, explicit reload or Changed accounting. The underlying enable keeps its existing implicit reload semantics; specialized no-reload sequences stay at their caller. Service reload/rollback and SSH socket ordering remain caller-specific.

## Behaviour

- `rootfiles apply [--profile P | --config F|-] [--module m,...] [--dry-run] [--yes]`: root and lock via the CLI preflight unless `--dry-run`.
- `rootfiles check [-v] [-o text|json|prometheus]`, `rootfiles status [-o text|json]`: no root needed.

## Distro support

- The engine itself is distro-neutral. `apply` and `check` select the package backend with `exec.NewPackageManager` and reject unsupported modules or options with `config.ValidateCapabilities` before mutation; `status` reports those as findings. See [distro-rocky](distro-rocky.md).

## Safety

- Upgrade stability (#115): converged v0.18.1 state must stay clean without new managed-file writes or service restarts. `TestUpgradeFromV0181ConvergedModulesStayClean` seeds the previous Docker daemon/compose link, enabled NTP and current locale directly, including missing locale tools, then asserts clean checks and mutation-free Apply. Native/live rollout proof is separate from these isolated fixtures.

- Check must stay side-effect free: no writes, no installs, no metadata refresh; Runner writes are dry-run gated but `Runner.Query` is not, so a Check must only query.
- `ensureMetaDir` refuses a legacy `<home_base>/.rootfiles` file without moving it, including in dry run, and names the manual relocation to `.rootfiles/users.json`. Shared JSON ledger reads distinguish absence from corruption; atomic sibling writes and destination replacement go through Runner so dry-run and backups apply.
- Status lines in `RunAll` are printed to stdout with literal glyphs, not through `internal/ui` (see #94).

- Not planned (retired 2026-11-10, from the 2026-09 improvement plan): a common disk-space and network-reachability preflight. `doctor` already reports disk use against thresholds, and package or download failures already stop with their error. A refusal on low disk would block `apply` on converged hosts whose root filesystem is close to full, which contract #115 forbids.

## Tests that pin it

- `internal/cli/report_machine_contract_test.go` pins pre-refactor check/doctor JSON and Prometheus bytes, field/metric ordering, HTML/quote/newline escaping, empty check modules as null, and exit codes. A test clock and synthetic doctor findings keep captures deterministic without host reads.

- `internal/cli/status_collector_contract_test.go` pins eleven JSON and ten text baselines captured before the refactor; only fixture-root/hostname are normalized. It covers raw version/HTML escaping, omitted/empty fields, filters, fallback, unknown home and failed probes. `status_snapshot_test.go` changes fixture files after collection and proves text displays the snapshot without more queries. Unsupported capability text is the named reconciliation with existing JSON behavior.

- `internal/module/module_test.go`: `TestRegistryDefaultOrderSync`, `TestRegistry_ResolveAll`, `TestRegistry_ResolveFiltered`, `TestRegistry_ResolveDisabledSkipped`.
- `internal/module/ssh_test.go`: `TestRunAllTracksModuleOutcomes`.
- `internal/module/monitoring_test.go`: `TestDryRunFailsOnBlockedChange`, `TestMonitoringPreflight*`.
- `internal/cli/apply_test.go`: `TestSecurityDowngrades`, `TestApplyCmd_InheritsRootFlags`.
- `internal/cli/check_test.go`: `TestCheck_JSONOutputAndExitCode`, `TestCheck_UsesAppliedCopy`.
- `internal/cli/status_test.go`: `TestStatusCmd_RendersAllSections`, `TestStatus_UsesAppliedCopy`; `internal/cli/tunnel_test.go`: `TestSaveAppliedConfig`, `TestStatus_ConfigLoadErrorHomeBase`.
- Scenarios (CI jobs `integration`, `module`, `scenario`): `tests/integration/run-profile.sh`, `tests/integration/run-module.sh`, `tests/scenarios/dry-run-all-profiles.sh`, `tests/scenarios/status.sh`.

## Open issues

- [#91](https://github.com/STAIxBWLB/rootfiles/issues/91): `status` honours `--module`; `ROOTFILES_YES` read in one helper.
- [#94](https://github.com/STAIxBWLB/rootfiles/issues/94): remaining module output through `internal/ui` and `rc.Out`; status text/JSON already share one collector.
