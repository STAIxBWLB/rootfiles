---
id: rootfiles
type: goal
title: rootfiles
status: active
---

# rootfiles

## Purpose

- rootfiles prepares a Linux server with everything that needs root and stops at the user account. Each user's own environment is the job of the separate dotfiles tool (`dot`).
- It is one Go binary with embedded YAML profiles. It converges one host at a time (`apply`), reports drift without side effects (`check`), checks live health (`doctor`), and restores what a run changed (`rollback`).
- On top of the single-host tool it ships an operator-side SSH fleet controller (`rootfiles fleet`), tenant containers on GPU hosts (`rootfiles tenant`), and host exporters plus an optional monitoring hub.

## Users

- **Host operators** who run `sudo rootfiles ...` on Ubuntu, NVIDIA DGX OS and Rocky Linux servers, often GPU hosts shared by many accounts.
- **Fleet operators** who run `rootfiles fleet ...` unprivileged from a Mac or Linux machine and reach hosts over OpenSSH.
- **Tenant operators**: host accounts in the tenant ops group that may run a fixed set of `rootfiles tenant ops` actions through sudo.
- **Automation**: the `schedule` timer, CI and the fleet controller consume `-o json` / `-o prometheus` output and exit codes.

## Contracts and invariants

- Managed hosts run Linux. Supported distributions are Ubuntu, DGX OS and Rocky Linux 8.9, 8.10 and 9.x (`internal/config/distro.go` `ResolveDistro`); anything else is refused before host changes. The macOS build exists only for the fleet controller and read-only commands.
- Re-running on a live server is safe: no SSH lockout, no `rm -rf` of user data, every overwritten or removed file is backed up for `rollback`, one mutating run at a time. Each feature spec states how it keeps this.
- `Check` has no side effects, `Apply` is idempotent and acts only on what `Check` reported ([engine](engine.md)).
- Unsupported capabilities are rejected before any host change ([distro-rocky](distro-rocky.md)).
- No new long-running agent or daemon is added to hosts beyond what modules install; fleet work goes over SSH ([fleet](fleet.md)).
- Secrets travel as root-owned files on the target host (`tunnel_token_file`, alert receiver file), never in a site config sent by the controller or in logs.
- Contracts live in `docs/specs/<id>.md`. A change to a stated contract updates the owning spec in the same PR; [architecture](architecture.md) lists the specs.

## Behaviour

- Spec tree: this goal, then [architecture](architecture.md), then module specs for runtime, config, engine, state, packages, distro, each host module, fleet, tenant and self-update.

## Distro support

- See [distro-rocky](distro-rocky.md) and `docs/rocky-support.md` for the per-module Rocky matrix. Ubuntu and DGX OS support every module.

## Safety

- Root-mutating commands need root and the global lock ([cli-runtime](cli-runtime.md)); `--dry-run` writes nothing; the audit log records real runs.

## Non-goals

- GPU scheduling, reservations, user portals, SSO, fractional GPUs (separate layers).
- Log aggregation, bare-metal provisioning, OS installation, shipped Grafana dashboards.
- A pull-based agent or multi-host placement; the controller drives existing per-host commands.
- Per-user environment setup (dotfiles).
- RHEL derivatives other than the Rocky versions above.

## Tests that pin it

- Feature specs list their tests. Whole-tool coverage: `.github/workflows/test.yaml` jobs `unit`, `integration`, `module`, `scenario`, `rocky`, `fleet-scenario`, `tenant-scenario`, `exporter-firewall-scenario`, `monitoring-hub`.

## Open issues

- [#98](https://github.com/STAIxBWLB/rootfiles/issues/98) 2026-11-09 audit plan; its child issues are listed in the specs whose paths they touch.
