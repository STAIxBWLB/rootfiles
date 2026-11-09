# Architecture

How the code is laid out and how a run flows through it. The contracts behind this overview live in the specs: start at [docs/specs/architecture.md](specs/architecture.md), which also indexes every other spec.

## Package map

| Path | Role |
|---|---|
| `cmd/rootfiles/` | Entry point. Cancels the run on SIGINT/SIGTERM and maps errors to exit codes. |
| `internal/cli/` | Cobra commands: `apply`, `backup`, `check`, `config`, `doctor`, `fleet`, `gpu`, `rollback`, `schedule`, `status`, `tenant`, `tunnel`, `update` (alias `upgrade`), `user`. `runtime.go` holds the root and lock preflight for native mutating commands, the audit logger and the runner factory. |
| `internal/config/` | Config structs, embedded profiles (`profiles/*.yaml`, merged as YAML trees through `extends`), validation, config fingerprints and host detection. |
| `internal/module/` | The `Module` interface, the 14 modules, the registry and execution order, and the `doctor` checks. |
| `internal/exec/` | Shell runner (dry-run aware, saves files before it changes them) and the APT and DNF/RPM package backends. |
| `internal/state/` | State file, run history, applied-config copy and the global lock. |
| `internal/fleet/` | Operator-side SSH controller: inventory loading, host selection, rollouts and scrape targets. |
| `internal/tenant/` | Tenant container specs, volumes, passwords, import and audit. It does not import `internal/module`; the `tenant` module imports it. |
| `internal/ui/` | Interactive prompts and the shared output styling for status-style reports. |

The import direction between these packages is fixed; see the package layering rule in [docs/specs/architecture.md](specs/architecture.md).

## Modules

Every module implements one interface:

```go
type Module interface {
    Name() string
    Check(ctx context.Context, rc *RunContext) (*CheckResult, error)
    Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error)
}
```

- `Name()` is the id used in the execution order and in `--module`.
- `Check()` reports pending changes and never changes the host.
- `Apply()` makes those changes idempotently and reports whether anything changed.

`NewRegistry()` and `defaultOrder` in `internal/module/module.go` list the same 14 modules; a test keeps the two lists in sync. What each module does: [modules.md](modules.md). Contract: [docs/specs/engine.md](specs/engine.md).

## Apply and check flow

1. The command loads the configuration: `--config`, `--profile` or `ROOTFILES_PROFILE`, with environment and flag overrides on top.
2. A native mutating command checks that it runs as root on Linux and takes the global lock (`/run/rootfiles.lock`). `--dry-run` skips this.
3. The registry resolves the enabled modules in `defaultOrder`, filtered by `--module`.
4. For each module, `Check` runs first. A satisfied module is skipped. Otherwise its pending changes are printed; a dry run stops there, a real run calls `Apply`.
5. A failing module is reported and the remaining modules still run. A cancelled run marks the rest as skipped.
6. Every file the runner overwrites or removes is saved in a backup session first. `apply` then records the result in the state file and history, and keeps a copy of the resolved config.

`check` and `status` run step 4 without `Apply`. Paths and recovery: [operations.md](operations.md).

## Fleet controller

`rootfiles fleet` runs on an operator machine and drives the same per-host commands over the system `ssh` client. It adds no agent to the hosts. Usage: [fleet.md](fleet.md). Contract: [docs/specs/fleet.md](specs/fleet.md).
