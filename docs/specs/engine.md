---
id: engine
type: module
title: Module Engine
status: active
parent: architecture
paths:
  - internal/module/module.go
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
- **Registry and order**: `NewRegistry()` registers all 14 modules; `defaultOrder` is the static execution order (locale, system, packages, users, ssh, security, docker, nvidia, gpu, cloudflared, storage, network, monitoring, tenant). The two lists must stay in sync. Ordering carries the dependencies (comments in `defaultOrder`: system before packages for the APT mirror, users before ssh so keyed accounts exist before password auth is disabled, security after ssh for the fail2ban port, tenant after docker and nvidia). Do not add a dependency mechanism; place the module in the order.
- `Registry.Resolve(cfg, filter)` returns enabled modules (`config.IsModuleEnabled`) in `defaultOrder`, narrowed by `--module`.
- **RunAll**: Check then Apply per module; a module error is recorded and the remaining modules still run; a cancelled context marks the rest `skipped` and returns `interrupted`. Outcome statuses are `satisfied`, `changed`, `failed`, `skipped` (and `pending` in dry run). `rc.moduleSucceeding(name)` lets a later module rely on an earlier one only while that one is in the run and has not failed.
- **CheckAll** returns on the first Check error (used by `check` and `status`).
- **Preflight** (`internal/module/preflight.go`): cross-module safety checks run before any module mutates the host. Today only `monitoringPreflight` (port ownership, exporter firewall); a new cross-module guard belongs here, not inside a module's Apply.
- **Change commands** (`internal/module/package_commands.go`): `packageInstallCommand`/`packageUpdateCommand` render the distro-correct command for `Change.Command`; use them instead of hard-coding `apt-get`.
- **apply** (`internal/cli/apply.go`), in this order: detect system; pick the profile (`selectProfile`: flag or `--config`, else the detected suggestion, interactive pick unless `--yes`/`ROOTFILES_YES`); load config with `--home-base`; apply flag overrides (`--tunnel-token`, `--vlan-address`) and account flags (`--user`/`--ssh-pubkey` or `ROOTFILES_USER`/`ROOTFILES_SSH_PUBKEY` become a declared account); `Validate`; interactive review; `Validate` again; `config.ValidateCapabilities` for the resolved modules; `Fingerprint`; `module.Preflight`; `RunAll`. Every refusal happens before the first module mutates the host.
- After a real (non-dry-run) apply, failed or not: `state.Record` writes the run with `ConfigSHA256`, profile, absolute config path, backup id and module outcomes; then the resolved config (`cfg.Effective()`, no `extends`, no inline token) is kept as the applied copy. If saving the copy fails it is removed, so a stale copy never outlives its run. A dry run records nothing.
- The interactive review flags settings weaker than the profile (`securityDowngrades`) and then defaults the confirmation to "no".
- **check** (`internal/cli/check.go`): always uses a dry-run Runner, `ValidateCapabilities` before `CheckAll`, exit 2 (`ExitError{Code: exitDrift}`, silent) when any module is unsatisfied, 1 on error, 0 otherwise. Formats: text, json (`checkReport` with `applied_config_sha256` when provenance holds), prometheus (`rootfiles_module_satisfied`, `rootfiles_module_pending_changes`, `rootfiles_check_timestamp_seconds`). These metric names and the JSON fields are consumed by the schedule timer, fleet and hub alert rules; renaming one is a contract change.
- **status** (`internal/cli/status.go`): read-only overview that always exits 0. A config load error falls back to `config.Fallback` and is reported (`config_error`, `home_base_ambiguous`), not returned. JSON comes from `collectStatus` (modules, last apply, GPU ledger, tunnel, users, provenance); capability errors become `module_check_error`. Without a known home base it skips the user and GPU databases rather than reading `/home`.
- check, status and config inspection resolve their target with `resolveTarget` and report `reportedTarget`; see [cli-runtime](cli-runtime.md).

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

## Tests that pin it

- `internal/module/module_test.go`: `TestRegistryDefaultOrderSync`, `TestRegistry_ResolveAll`, `TestRegistry_ResolveFiltered`, `TestRegistry_ResolveDisabledSkipped`.
- `internal/module/ssh_test.go`: `TestRunAllTracksModuleOutcomes`.
- `internal/module/monitoring_test.go`: `TestDryRunFailsOnBlockedChange`, `TestMonitoringPreflight*`.
- `internal/cli/apply_test.go`: `TestSecurityDowngrades`, `TestApplyCmd_InheritsRootFlags`.
- `internal/cli/check_test.go`: `TestCheck_JSONOutputAndExitCode`, `TestCheck_UsesAppliedCopy`.
- `internal/cli/status_test.go`: `TestStatusCmd_RendersAllSections`, `TestStatus_UsesAppliedCopy`; `internal/cli/tunnel_test.go`: `TestSaveAppliedConfig`, `TestStatus_ConfigLoadErrorHomeBase`.
- Scenarios (CI jobs `integration`, `module`, `scenario`): `tests/integration/run-profile.sh`, `tests/integration/run-module.sh`, `tests/scenarios/dry-run-all-profiles.sh`, `tests/scenarios/status.sh`.

## Open issues

- [#91](https://github.com/STAIxBWLB/rootfiles/issues/91): `status` honours `--module`; `ROOTFILES_YES` read in one helper.
- [#92](https://github.com/STAIxBWLB/rootfiles/issues/92): stale `gpu assign` hint in status, `Example` on `apply` and `check`, VLAN example address in the apply prompt.
- [#94](https://github.com/STAIxBWLB/rootfiles/issues/94): single status collector for text and JSON (text skips `ValidateCapabilities` today), one module-name list checked against `defaultOrder`, module output through `internal/ui` and `rc.Out`.
