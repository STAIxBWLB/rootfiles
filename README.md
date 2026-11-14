# rootfiles

[![Test](https://github.com/STAIxBWLB/rootfiles/actions/workflows/test.yaml/badge.svg)](https://github.com/STAIxBWLB/rootfiles/actions/workflows/test.yaml)
[![Release](https://img.shields.io/github/v/release/STAIxBWLB/rootfiles)](https://github.com/STAIxBWLB/rootfiles/releases/latest)

Declarative server bootstrapping for Ubuntu, NVIDIA DGX OS and Rocky Linux: one binary for the host, plus an SSH fleet controller for the operator.

`rootfiles` does everything that needs root and stops at the user account; each user's environment is the job of [dotfiles](https://github.com/STAIxBWLB/dotfiles) (`dot`).

- **Host**: `apply` converges a host to a profile or site config through [14 modules](docs/modules.md) (packages, accounts, SSH, security updates, Docker and NVIDIA, GPU allocation, Cloudflare Tunnel, storage, UFW, monitoring, tenants). `check` shows drift, `doctor` checks live health.
- **Fleet**: `fleet` runs reports in parallel and rollouts one host at a time over your `ssh` client, with no agent on the hosts.
- **Tenants**: `tenant` gives outside groups long-lived SSH containers with a size-capped volume, ledger-tracked GPUs, optional CPU/NUMA pinning and a shared SSH port.
- **Monitoring**: node and DCGM exporters, an optional Prometheus/Alertmanager/Grafana hub, and scheduled `check`/`doctor` reports as metrics.

## Supported systems

| System | Support |
|---|---|
| Ubuntu (22.04, 24.04 tested) | Every module |
| NVIDIA DGX OS | Every module (detected from `/etc/dgx-release`) |
| Rocky Linux 8.9, 8.10, 9.x | Core modules: locale, system, packages, users, ssh, security ([docs/rocky.md](docs/rocky.md)) |
| macOS | Fleet controller and read-only commands |

Anything else is refused before a change.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/STAIxBWLB/rootfiles/main/scripts/install.sh | sudo bash
# pin a release: ... | sudo bash -s -- --version v0.19.0
```

The installer verifies the checksum and installs `/usr/local/bin/rootfiles` (plus a `root` symlink on Linux). Other channels and updates: [docs/operations.md](docs/operations.md#install).

## Quick start

```bash
sudo rootfiles check --profile dgx              # preview; exit 2 when something would change
sudo rootfiles apply --profile dgx              # review each setting, then apply
rootfiles doctor                                # SSH access, firewall, disks, GPUs, time sync
```

Keep host-specific values in a site config that extends a profile:

```bash
rootfiles config init --extends dgx --file site.yaml
sudo rootfiles apply --config site.yaml --yes
```

From an operator machine, with the inventory in `~/.config/rootfiles/fleet.yaml` ([example](docs/fleet.example.yaml)):

```bash
rootfiles fleet check --group gpu
rootfiles fleet apply --group gpu --yes         # one host at a time, stops at the first failure
```

## Profiles

| Profile | Extends | Adds |
|---|---|---|
| `base` | | Locale, packages, SSH |
| `minimal` | `base` | Users, cloudflared, security baseline, journald cap, `ufw` package (firewall off) |
| `dgx` | `minimal` | Docker, NVIDIA toolkit and fabric manager, GPU allocation, RAID storage, UFW (80/443) |
| `gpu-server` | `minimal` | Docker, NVIDIA toolkit, GPU allocation, storage, UFW |
| `full` | `minimal` | Docker, storage, SSH password hardening (firewall off) |
| `rocky` | | Rocky core set: DNF packages, `wheel`, DNF automatic updates |

Addresses, mirrors and accounts belong in a site config, not a profile ([docs/configuration.md](docs/configuration.md)).

## Safety

- `check` and `--dry-run` change nothing and show the plan a real run would follow.
- Password login is turned off only when no account would be left without a key login (`apply --force` overrides this check only); `sshd -t` validates each change, and UFW always admits the SSH port.
- Unreadable firewall state blocks SSH port, UFW, exporter and tenant SSH changes, even with `--force`.
- Every overwritten or removed file is backed up; `rootfiles rollback <id>` restores it. Populated paths are never removed.
- Mutating runs need root, hold `/run/rootfiles.lock` and are logged to `/var/log/rootfiles.log` with secrets redacted.

Details: [docs/operations.md](docs/operations.md).

## Documentation

| Topic | File |
|---|---|
| Commands, flags, exit codes | [commands.md](docs/commands.md) |
| Config keys, profiles, `extends` | [configuration.md](docs/configuration.md) |
| Modules and firewall rules | [modules.md](docs/modules.md) |
| Accounts, home base, quotas | [users.md](docs/users.md) |
| GPU allocation and MIG | [gpu.md](docs/gpu.md) |
| Cloudflare Tunnel | [tunnel.md](docs/tunnel.md) |
| Exporters and monitoring hub | [monitoring.md](docs/monitoring.md) |
| Fleet controller | [fleet.md](docs/fleet.md) |
| Tenant containers | [tenant.md](docs/tenant.md) |
| Rocky Linux | [rocky.md](docs/rocky.md) |
| State, rollback, backup, schedule, self-update | [operations.md](docs/operations.md) |
| Code layout and run flow | [architecture.md](docs/architecture.md) |
| Build, tests, CI, releases | [development.md](docs/development.md) |
| Release notes, design specs | [releases/](docs/releases/), [specs/](docs/specs/architecture.md) |

## License

MIT

## AGENTS.md background

Human-readable background. Agent instructions: `AGENTS.md`.

- 2026-11-09: raised the Go baseline to 1.26.10 so CI and release share a supported, pinned toolchain from `go.mod` (#87).
- 2026-11-09: native-command test isolation now includes installed/failing ambient tools after remote-only module runs missed host sshd/UFW dependencies (#89, #104).
- 2026-11-12: module order and toggles moved to `moduleDefinitions` in `internal/config/modules.go` (#121); AGENTS.md now points there instead of naming four lists to keep in sync.

- 2026-11-15: repeated import diagnostics omitted recovery steps during #139 review; AGENTS.md now points to the existing Error messages pass in REVIEW.md before error/refusal changes.
