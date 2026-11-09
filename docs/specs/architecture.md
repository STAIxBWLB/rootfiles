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
  - `internal/tenant` imports `config` and `exec`. It must not import `internal/module` (module imports tenant).
  - `internal/module` imports `config`, `exec`, `state`, `tenant`, `ui`.
  - `internal/fleet` imports `config` and `module` (`internal/fleet/rollout.go` resolves the enabled modules of a site config through `module.NewRegistry`).
  - `internal/cli` is the only package that imports `fleet` and the only one `cmd/rootfiles` imports.
- **Entry point** (`cmd/rootfiles/main.go`): `main` builds a context cancelled on SIGINT/SIGTERM and calls `cli.Execute`. An error that unwraps to `*cli.ExitError` exits with its `Code`, printing `Error: <Msg>` to stderr only when `Msg` is non-empty (drift exit 2 is silent). Any other error is printed as `Error: <err>` and exits 1. Never let an error exit without output. `version` and `commit` are set by goreleaser ldflags (`-X main.version`, `-X main.commit`; see [self-update](self-update.md)).
- **Output styling** (`internal/ui/styles.go`, `markers.go`, `format.go`): status-style reports go through `WriteHeader`, `WriteSection`, `WriteKV`, `WriteHint`, `WriteBullet` and the `OKMark`/`FailMark`/`PendingMark`/`WarnMark` helpers, writing to an `io.Writer`. lipgloss degrades to plain text when stdout is not a TTY or `NO_COLOR` is set; do not add manual colour gating. Use the `Mark*` constants instead of glyph literals. Several packages still print literal glyphs or raw `fmt` (`internal/module/users.go`, `ssh.go`, `module.go`, `internal/cli/rollback.go`, `upgrade.go`, `doctor.go`, `internal/tenant/manager.go` and others); that is known debt tracked in #94, not a pattern to copy.
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

- `internal/ui/format_test.go`: header, section, KV and bullet layout with a forced plain colour profile.
- Exit-code handling is exercised through the CLI tests listed in [cli-runtime](cli-runtime.md) and [engine](engine.md).
- No test enforces the package layering; `go build` rejects import cycles only.

## Open issues

- [#94](https://github.com/STAIxBWLB/rootfiles/issues/94) route module and CLI output through `internal/ui` and an injected writer; remove literal glyphs outside `internal/ui`.
