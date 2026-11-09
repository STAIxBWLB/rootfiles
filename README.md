# rootfiles

[![Test](https://github.com/STAIxBWLB/rootfiles/actions/workflows/test.yaml/badge.svg)](https://github.com/STAIxBWLB/rootfiles/actions/workflows/test.yaml)
[![Release](https://img.shields.io/github/v/release/STAIxBWLB/rootfiles)](https://github.com/STAIxBWLB/rootfiles/releases/latest)

Server bootstrapping for Ubuntu, NVIDIA DGX OS and Rocky Linux core profiles. One binary, declarative host configuration, and an SSH fleet controller.

## What it does

`rootfiles` prepares a server with everything that needs root and stops at the user account. Each user's own environment (shell, editors, AI CLIs, workspace) is the job of the separate [dotfiles](https://github.com/STAIxBWLB/dotfiles) tool (`dot`), which runs inside that account on servers and workstations.

- **One host, declaratively.** `rootfiles apply` converges a host to a profile or site config through 14 modules: packages, accounts and the home base, SSH hardening, security updates, fail2ban and NTP, Docker and the NVIDIA runtime, per-user GPU allocation, Cloudflare Tunnel, storage, UFW and monitoring exporters. `check` reports drift without changing anything, and `doctor` checks live health.
- **A fleet over SSH.** `rootfiles fleet` runs reports in parallel and rollouts one host at a time from an operator's Mac or Linux machine. Config fingerprints show which configuration each host applied.
- **Tenant containers.** `rootfiles tenant` gives outside groups a long-lived SSH container each, with a size-capped volume and GPUs from the shared allocation ledger.
- **Monitoring.** Node and DCGM GPU exporters on each host, an optional Prometheus, Alertmanager and Grafana hub, and scheduled `check`/`doctor` reports exported as metrics.

On a new GPU server, an operator runs `sudo rootfiles apply --profile dgx` (or `rootfiles fleet apply`), which creates the accounts, hardens SSH and installs Docker with the NVIDIA runtime. Each user then installs `dot` and runs `dot init --yes` and `dot apply --yes`.

## Supported systems

| System | Support |
|---|---|
| Ubuntu (22.04 and 24.04 in CI) | Every module |
| NVIDIA DGX OS | Every module; detected from `/etc/dgx-release` |
| Rocky Linux 8.9, 8.10, 9.x | Core modules only: locale, system, packages, users, ssh, security ([docs/rocky.md](docs/rocky.md)) |
| macOS | Fleet controller and read-only commands only; host changes are refused |

Any other distribution is refused before anything changes.

## Install

```bash
# latest release: verifies the SHA-256 checksum, installs /usr/local/bin/rootfiles
curl -fsSL https://raw.githubusercontent.com/STAIxBWLB/rootfiles/main/scripts/install.sh | sudo bash

# a specific release
curl -fsSL https://raw.githubusercontent.com/STAIxBWLB/rootfiles/main/scripts/install.sh | sudo bash -s -- --version v0.18.1

# dev channel: build from source (needs Go)
curl -fsSL https://raw.githubusercontent.com/STAIxBWLB/rootfiles/main/scripts/install.sh | sudo bash -s -- --channel dev
```

The installer also creates a `root` symlink, so `sudo root apply` works like `sudo rootfiles apply`. Later updates: `sudo rootfiles update` ([docs/operations.md](docs/operations.md#self-update)).

## Quick start

### Single host

```bash
sudo rootfiles check --profile dgx         # what would change; exit 2 when something would
sudo rootfiles apply --profile dgx --dry-run
sudo rootfiles apply --profile dgx         # reviews each setting, then applies
rootfiles doctor                           # live health: SSH access, firewall, disks, GPUs, time sync
rootfiles status                           # one-screen overview
```

- Without `--profile`, `apply` suggests one from the host and asks; `--yes` skips every prompt.
- Keep host-specific settings in a site config that extends a profile, and pass it with `--config`:

```bash
rootfiles config init --extends dgx --file site.yaml
rootfiles config validate --config site.yaml
sudo rootfiles apply --config site.yaml --yes
```

- Unattended example that declares an account and sets the tunnel token from the environment:

```bash
sudo rootfiles apply --config /etc/rootfiles/site.yaml --yes \
  --user alice --ssh-pubkey "ssh-ed25519 AAAA..." --tunnel-token "$CF_TUNNEL_TOKEN"
```

### Fleet

Run the controller on an operator machine; it uses your `ssh` client and adds no agent to the hosts.

```bash
# inventory: ~/.config/rootfiles/fleet.yaml, started from docs/fleet.example.yaml
rootfiles fleet status --all
rootfiles fleet check --group gpu
rootfiles fleet apply --host gpu01 --dry-run                # preview on one host
rootfiles fleet apply --group gpu --yes                     # one host at a time, stops at the first failure
```

Inventory format, privilege, rollouts and the run log: [docs/fleet.md](docs/fleet.md).

## Safety model

`apply` is meant to be re-run on live servers.

- **Preview first.** `check` and `--dry-run` change nothing and need neither root nor the lock. A dry run shows the same plan a real run would act on.
- **No SSH lockout.** Password login is turned off only when an account can log in with a key and no other account would be left with a password only; `sshd -t` validates every change. Enabling UFW always admits the SSH port. `apply --force` overrides the key check only.
- **Unknown firewall state blocks.** An SSH port change, a UFW change or exporter and tenant SSH setup stop when the installed UFW or firewalld state cannot be read, even with `--force` ([docs/modules.md](docs/modules.md#firewall-state)).
- **Backups and rollback.** Every file a run overwrites or removes is saved under `/var/lib/rootfiles/backups/<id>`; `rootfiles rollback <id>` restores it. Existing data is never removed: a populated path blocks a symlink instead.
- **Root and one run at a time.** Mutating commands need root on Linux and take `/run/rootfiles.lock`; Ctrl-C cancels running commands.
- **Unsupported is refused early.** Modules or options a host does not support are refused before any change, dry runs included.
- **Audit trail.** Real runs are logged as JSON to `/var/log/rootfiles.log` with tokens and passwords redacted; the last result is in `/var/lib/rootfiles/state.json`.

Details: [docs/operations.md](docs/operations.md).

## Profiles

| Profile | Extends | Use case |
|---|---|---|
| `base` | none | Locale, packages, SSH |
| `minimal` | `base` | + users, cloudflared, security baseline, journald cap, the `ufw` package (firewall left off) |
| `dgx` | `minimal` | + Docker, NVIDIA toolkit, persistenced, fabric manager, GPU allocation, RAID storage, UFW (80/443) |
| `gpu-server` | `minimal` | + Docker, NVIDIA toolkit, persistenced (non-DGX), GPU allocation, storage, UFW |
| `full` | `minimal` | + Docker, storage, SSH password-authentication hardening (firewall off; enable it in a site config) |
| `rocky` | none | Rocky core matrix: DNF package set, `wheel` group, DNF automatic updates |

No profile carries a site's addresses: a tunnel private network, mirrors and accounts go in a site config ([docs/configuration.md](docs/configuration.md)).

## Documentation

| Topic | File |
|---|---|
| Every command, flag, exit code, output format and environment variable | [docs/commands.md](docs/commands.md) |
| Every config key, profiles and `extends`, overrides, which config a command uses | [docs/configuration.md](docs/configuration.md) |
| What each module does, its toggle, firewall rules, Rocky column | [docs/modules.md](docs/modules.md) |
| Home base, trust rules, accounts, quotas, backup and restore | [docs/users.md](docs/users.md) |
| Per-user GPU allocation and MIG | [docs/gpu.md](docs/gpu.md) |
| Cloudflare tunnel, token file, private network address | [docs/tunnel.md](docs/tunnel.md) |
| Exporters, exporter firewall guard, monitoring hub, alerts | [docs/monitoring.md](docs/monitoring.md) |
| Fleet inventory, selection, rollouts, run log, scrape targets | [docs/fleet.md](docs/fleet.md) and [docs/fleet.example.yaml](docs/fleet.example.yaml) |
| Tenant containers | [docs/tenant.md](docs/tenant.md) |
| Rocky Linux support | [docs/rocky.md](docs/rocky.md) |
| State, logs, lock, rollback, system backup, scheduled reports, self-update | [docs/operations.md](docs/operations.md) |
| Package map and the apply/check flow | [docs/architecture.md](docs/architecture.md) |
| Build, tests, CI jobs, releases | [docs/development.md](docs/development.md) |
| Release notes | [docs/releases/](docs/releases/) |
| Design specs (contracts) | [docs/specs/](docs/specs/), index [docs/specs/architecture.md](docs/specs/architecture.md) |

## License

MIT

## AGENTS.md background

Human-readable background. Agent instructions: `AGENTS.md`.

- 2026-11-09: raised the Go baseline to 1.26.10 so CI and release share a supported, pinned toolchain from `go.mod` (#87).
- 2026-11-09: native-command test isolation now includes installed/failing ambient tools after remote-only module runs missed host sshd/UFW dependencies (#89, #104).
