---
id: architecture
type: architecture
title: Architecture
status: active
parent: rootfiles
paths:
  - cmd/rootfiles/main.go
  - internal/ui/**
---

# Architecture

## Purpose

- Package layout, dependency direction, the process entry point and the shared terminal output layer. Feature contracts live in the module specs listed below.

## Contracts and invariants

- **Package layering** (non-test imports on this branch). Keep it acyclic and do not add upward edges:
  - `internal/ui`, `internal/config`, `internal/state` import no other internal package.
  - `internal/exec` imports `config` only.
  - `internal/tenant` imports `config`, `exec` and the leaf `ui` package. It must not import `internal/module` (module imports tenant).
  - `internal/module` imports `config`, `exec`, `state`, `tenant`, `ui`.
  - `internal/fleet` imports `config` and `module` (`internal/fleet/rollout.go` resolves the enabled modules of a site config through `module.NewRegistry`).
  - `internal/cli` is the only package that imports `fleet` and the only one `cmd/rootfiles` imports.
- **Entry point** (`cmd/rootfiles/main.go`): `main` builds a context cancelled on SIGINT/SIGTERM and calls `cli.Execute`, which writes diagnostics once to the configured error stream. Errors use `rootfiles: <message>`, a failure marker only on TTY stderr and an optional supplied next step. `main` exits with `ExitError.Code` when present, otherwise 1. Empty-message report exits stay silent. Cobra argument/flag parsing errors exit 2 with usage and a help hint; missing required flags retain exit 1. `version` and `commit` are set by goreleaser ldflags (`-X main.version`, `-X main.commit`; see [self-update](self-update.md)).
- **Output styling** (`internal/ui`): human reports use the nine semantic ANSI roles (header and key blue, section magenta, value default, success green, warning yellow, error red, hint bright black, accent cyan). Bold is reserved for header, section and status roles. lipgloss v2 styles render decoration independently of terminal detection; `ConfigureWriter` resolves color per stream in this precedence: explicit `--color`, nonempty `NO_COLOR`, nonzero `FORCE_COLOR` or `CLICOLOR_FORCE`, `FORCE_COLOR=0` or `CLICOLOR=0`, TTY with `TERM` other than `dumb`, otherwise off. JSON and Prometheus bypass human transformations and retain their existing bytes. Tenant output keeps its explicit Manager.Out writer; tenant logs and tenant ops logs bypass human transformations byte for byte, as do config show and rendered fleet targets.
- **Markers and layout** (`internal/ui/markers.go`, `format.go`, `table.go`): use named `Mark*` constants, never inline glyphs. Pending is `~`; the next step is `→`. ASCII markers follow `--ascii` or the first nonempty locale (`LC_ALL`, `LC_CTYPE`, `LANG`) lacking UTF-8. `WriteBullet` reserves two cells for markers. Reports start with one title line; sections have one leading blank line. `WriteTable` uses uppercase headings, a two-space gutter, drops empty columns, caps non-flexible cells at 32 cells with visible `...`, and wraps its flexible text column to the terminal width (or explicit `COLUMNS` on a pipe). Callers provide JSON or a detail command for full shortened values. `WrapText` is for free text only; identifiers and states remain in their own columns. Machine payloads never inherit wrapping or marker substitution. CLI/module text uses an explicit command/context writer; names-only output uses `WriteRaw` to preserve bytes.

- **Display fallback** (`ui.FirstNonEmpty`): returns the first nonempty string verbatim; shared by native status and cloudflared update text without trimming or formatting changes.
- **Prompts** (`internal/ui/prompt.go`, Charm huh): every prompt helper takes `unattended` and returns its default (`Confirm` returns true) without reading the terminal when it is true. `--yes` must reach every prompt as `unattended`, so unattended runs never block.

## Behaviour

- Spec index (`docs/specs/<id>.md`):
  - [cli-runtime](cli-runtime.md): root flags, root/lock preflight, audit log, Runner construction, output formats.
  - [engine](engine.md): Module interface, registry and order, `apply`/`check`/`status`.
  - [config](config.md): config structs, profiles, loader, validation, fingerprints, detection, `config` command.
  - [distro-rocky](distro-rocky.md): distro resolution, capability validation, Rocky SSH/security, RPM backend.
  - [exec-packages](exec-packages.md): Runner, APT backend, package manager interface, `packages` module.
  - [state-rollback](state-rollback.md): state file, history, global lock, backups, `rollback`, `backup`.
  - Host modules under [engine](engine.md): [host-base](host-base.md), [ssh-access](ssh-access.md), [security](security.md), [users](users.md), [docker-nvidia](docker-nvidia.md), [gpu-allocation](gpu-allocation.md), [cloudflared](cloudflared.md), [monitoring-exporters](monitoring-exporters.md) with [monitoring-hub](monitoring-hub.md), [doctor-schedule](doctor-schedule.md).
  - [self-update](self-update.md): `update`, installer, release and CI workflows.
  - [fleet](fleet.md): operator-side SSH controller and the read-only sudoers rule.
  - [tenant](tenant.md): tenant containers, host-prep module, sshpiper.
- A change to a contract stated in a spec updates that spec in the same PR. Every non-test Go file, `scripts/*`, `.goreleaser.yaml` and the workflows belong to exactly one spec (`ahub specs covers <file>`).

## Distro support

- The binary builds for linux and darwin, amd64 and arm64 (`.goreleaser.yaml`). Native host mutations require Linux ([cli-runtime](cli-runtime.md)); darwin runs the fleet controller and read-only commands.

## Safety

- The process context is cancelled on SIGINT/SIGTERM so in-flight commands stop instead of being orphaned; `cli.Execute` releases the held global lock on return.

## Tests that pin it

- `internal/ui/output_boundary_test.go` guards against stdout-only fmt.Print calls in module/CLI code and glyph literals outside ui. Writer tests cover payload bytes and the RunContext nil fallback.

- `internal/ui/format_test.go`: header, section, KV and bullet layout with per-writer color policy.
- Exit-code handling is exercised through the CLI tests listed in [cli-runtime](cli-runtime.md) and [engine](engine.md).
- No test enforces the package layering; `go build` rejects import cycles only.
