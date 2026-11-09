# rootfiles

[![Test](https://github.com/STAIxBWLB/rootfiles/actions/workflows/test.yaml/badge.svg)](https://github.com/STAIxBWLB/rootfiles/actions/workflows/test.yaml)
[![Release](https://img.shields.io/github/v/release/STAIxBWLB/rootfiles)](https://github.com/STAIxBWLB/rootfiles/releases/latest)

Server bootstrapping for Ubuntu, NVIDIA DGX OS and Rocky Linux core profiles. Single binary, declarative host configuration, and an SSH fleet controller.

Managed hosts must run Linux; macOS is not a supported host OS. The macOS build exists only so an operator can run the fleet controller and read-only commands from a Mac, and native host changes such as `apply` are refused there.

## What it does

`rootfiles` is the root half of a two-tool server setup. It prepares a machine with everything that needs root and stops at the user account; [dotfiles](https://github.com/STAIxBWLB/dotfiles) (the `dot` binary) then sets up each user's own environment inside that account, on servers as well as on macOS and Linux workstations.

- **One host, declaratively.** `rootfiles apply` converges a host to a profile or site config through 14 modules. `check` reports drift without changing anything, `doctor` checks live health (SSH access, disks, reboot, GPUs), and every file a run changes is backed up for `rollback`.
- **A fleet over SSH.** `rootfiles fleet` runs `status`, `check` and `doctor` in parallel and `apply`, `update`, `bootstrap` and `schedule` serially from an operator's Mac or Linux machine. Config fingerprints show which configuration each host applied, and `fleet update` flags hosts that still need `apply`.
- **Tenant containers.** `rootfiles tenant` gives outside groups a long-lived SSH container each, with a size-capped volume, accounts and host keys that survive image changes, and GPUs from the shared allocation ledger ([guide](docs/tenant.md)).
- **Monitoring.** Node and DCGM GPU exporters on each host, an optional Prometheus, Alertmanager and Grafana hub, and scheduled `check`/`doctor` reports exported as metrics.

```
rootfiles (root, per server)              dotfiles / dot (user, server or workstation)
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━    ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
System packages (APT / DNF)               User packages (Homebrew / Linuxbrew)
User accounts, home base, quotas          zsh + Oh My Zsh, starship, git and SSH config
SSH hardening with lockout guard          tmux, terminal, Nerd Fonts
Security-only updates, fail2ban, NTP      AI CLIs and their settings (Claude Code, Codex, ...)
Docker, NVIDIA runtime, GPU allocation    Conda defaults, one `container` command
Cloudflare tunnel + VLAN, UFW             Workspace clone, sync and vault links
Locale, timezone, storage, journald       Age-encrypted secrets, GPG commit signing
Exporters, monitoring hub, SSH fleet      Backup and restore across machines
```

On a new GPU server the two run in this order:

1. An operator runs `sudo rootfiles apply --profile dgx` on the host, or `rootfiles fleet apply` from the controller. This creates the accounts, hardens SSH and installs Docker with the NVIDIA runtime.
2. Each user installs `dot` (see the dotfiles README), then runs `dot init --yes`, which picks the `server` profile when it detects an NVIDIA GPU or CUDA, and `dot apply --yes`.

Both tools share one design: a single Go binary, embedded YAML profiles with inheritance, and modules with a side-effect-free `Check` and an idempotent `Apply` behind a dry-run-aware runner.

## Install

Latest stable release (recommended):

```bash
curl -fsSL https://raw.githubusercontent.com/STAIxBWLB/rootfiles/main/scripts/install.sh | sudo bash
```

Specific version:

```bash
curl -fsSL https://raw.githubusercontent.com/STAIxBWLB/rootfiles/main/scripts/install.sh | sudo bash -s -- --version v0.1.0
```

Dev channel (build from source, requires Go):

```bash
curl -fsSL https://raw.githubusercontent.com/STAIxBWLB/rootfiles/main/scripts/install.sh | sudo bash -s -- --channel dev
```

The installer downloads a prebuilt binary, verifies its SHA256 checksum, and places it at `/usr/local/bin/rootfiles`. A `root` symlink is also created in the same directory — every example below works with either name:

```bash
sudo rootfiles apply    # long form
sudo root apply         # same thing, shorter
```

## Quick start

```bash
sudo rootfiles apply
```

```bash
sudo rootfiles apply --profile dgx --yes
```

In interactive mode, `apply` presents each configurable setting (SSH, firewall, VLAN, storage, etc.) for review, pre-filled with the profile's values, and flags any choice that is weaker than the profile before asking to apply. Use `--yes` to skip all prompts for CI/automation.

### Safety

`apply` is designed to be safe to re-run on a live server:

- **No SSH lockout** — password auth is only disabled when at least one account can log in with a key (declared accounts count while the `users` module is in the same run and has not failed) and no other account would be left with only a password; enabling UFW always admits the SSH port; `sshd -t` validates the config
  - Accounts that still log in with passwords are listed by `check` and `doctor`. Give them keys, or keep password login for them while keys are rolled out with `ssh.password_auth_users: [alice, bob]` (a `Match User` block in the drop-in) and a failing change is reverted; a port change on socket-activated Ubuntu restarts `ssh.socket`. `--force` overrides the key check.
- **No data loss** — existing directories are never `rm -rf`'d (a populated path blocks a symlink instead), `daemon.json` is merged key by key, `user rehome` keeps the old home as a backup.
- **Backups & rollback** — every file a run overwrites or removes is saved under `/var/lib/rootfiles/backups/<id>`; `rootfiles rollback <id>` restores it.
- **One run at a time** — mutating commands need root and take `/run/rootfiles.lock`; Ctrl-C cancels in-flight commands.
- **Audit trail** — real runs are logged as JSON to `/var/log/rootfiles.log`; the last result is kept in `/var/lib/rootfiles/state.json` (shown by `status`).

## Profiles

| Profile | Extends | Use case |
|---------|---------|----------|
| `base` | — | Locale, packages, SSH |
| `minimal` | base | + users, cloudflared, ufw, security baseline, journald cap |
| `dgx` | minimal | + Docker, NVIDIA toolkit, persistenced, fabric manager, VLAN, RAID storage |
| `gpu-server` | minimal | + Docker, NVIDIA toolkit, persistenced (non-DGX) |
| `full` | minimal | + Docker, storage, network |

DGX OS is auto-detected (`/etc/dgx-release`) and the appropriate profile is suggested.

### Site configs

Keep host- or site-specific settings in a small file that extends a profile. Any key it sets wins — including `false`, so a site config can switch a module off:

```yaml
# site.yaml
extends: dgx            # a built-in profile, or a path relative to this file
timezone: UTC
users:
  accounts:             # created if missing; missing keys/groups added; never removed
    - name: admin
      ssh_pubkeys: ["ssh-ed25519 AAAA... admin@laptop"]
      groups: [sudo]
modules:
  cloudflared:
    enabled: false
  system:
    hostname: gpu01
    apt_mirror: http://mirror.kakao.com/ubuntu
```

```bash
rootfiles config init --extends dgx --file site.yaml   # scaffold (or --from-system to capture this host)
rootfiles config validate --config site.yaml           # unknown keys, paths, ports, CIDRs …
rootfiles config show --config site.yaml               # fully merged result (token masked)
sudo rootfiles apply --config site.yaml
```

`--config -` reads fully resolved configuration from stdin. For multiple hosts, use the `rootfiles fleet` controller below; it resolves inventory configs before streaming and preserves applied provenance.

## Modules

What each module does, its config toggle, the execution order and Rocky support: [docs/modules.md](docs/modules.md).

## Usage

### Apply configuration

Interactive — prompts for profile, then walks through each setting (SSH, Users, Docker, Cloudflared, Network, Storage):

```bash
sudo rootfiles apply
```

```bash
sudo rootfiles apply --profile dgx
```

Specific modules only:

```bash
sudo rootfiles apply --module cloudflared,docker
```

Dry-run (preview changes, no execution):

```bash
sudo rootfiles apply --profile dgx --dry-run
```

From a backup snapshot:

```bash
sudo rootfiles apply --config /var/lib/rootfiles/system-backup/rootfiles-backup-*/config-snapshot.yaml
```

Unattended (CI/automation — skips all interactive prompts). `--user`/`--ssh-pubkey` declare an account that is created before SSH is hardened, and `--tunnel-token` installs the tunnel service. This example uses a site config with `modules.cloudflared.private_network.enabled: true`; the address flag supplies its CIDR:

```bash
sudo rootfiles apply --config /etc/rootfiles/dgx-site.yaml --yes --home-base /raid/home --tunnel-token "$CF_TUNNEL_TOKEN" --vlan-address "203.0.113.10/32" --user alice --ssh-pubkey "ssh-ed25519 AAAA..."
```

### Check system state

```bash
sudo rootfiles check --profile dgx
```

```bash
sudo rootfiles check --config /var/lib/rootfiles/system-backup/rootfiles-backup-*/config-snapshot.yaml
```

`check` exits **0** when everything is satisfied, **2** when changes are pending and **1** on errors. Machine-readable output:

```bash
rootfiles check -o json          # modules, satisfied, changes[{description, command}]
rootfiles check -o prometheus    # rootfiles_module_satisfied{module="…"} …
```

### Health check

`doctor` inspects the live system rather than the profile: SSH access (keys vs. `sshd -T`), firewall vs. SSH port, reboot-required, disk usage, NTP, GPU driver / persistence mode, failed systemd units, and the last apply. Exit code 2 on failures (`--strict`: also on warnings); `-o json|prometheus` supported.

```bash
rootfiles doctor
```

Disk usage fails at 90% and warns at 80% by default. Large data disks can set their own limits; with `min_free`, a mount fails only when it is over `fail_percent` and also has less than `min_free` free (sizes in K/M/G/T, optional `iB`). The doctor metrics and the hub's doctor alert follow the same grading.

```yaml
doctor:
  disk:
    warn_percent: 80
    fail_percent: 90
    min_free: 100GiB
```

Scheduled `check` and `doctor` reports (`rootfiles schedule`): [docs/operations.md](docs/operations.md#scheduled-reports).
Undo a run with `rootfiles rollback`: [docs/operations.md](docs/operations.md#file-backups-and-rollback).

### Status dashboard

Unified at-a-glance view — system info, active profile, module satisfaction, GPU allocations, tunnel service, and managed users in one pass:

```bash
rootfiles status
```

Without flags, `status`, `check` and the `user`, `gpu`, `tunnel`, `schedule` and `backup` subcommands use the profile/config last applied on this host; with nothing recorded, `status` and `check` fall back to the profile detection suggests and the subcommands to `minimal` with the detected home base, never the suggested profile. `--profile` and `--config` override it. `apply` records `--config` as an absolute path, and a recorded relative path (from older versions) is ignored rather than resolved against the current directory. After every recorded apply, from a profile or a config file (fleet rollouts included), `apply` keeps the fully resolved config, with its `--home-base`, `--vlan-address`, env and interactive changes, without an inline tunnel token, as `/var/lib/rootfiles/applied-config.yaml` (root, 0600); the subcommands reuse that copy instead of re-reading the original file or re-resolving the profile (any `rollback` removes it), and only when it is the config of the last recorded run (same fingerprint), it and every directory above it are root-owned and not writable by group or others, and they can read it; a non-root `--dry-run` of a mutating subcommand warns that it previews `minimal` instead. Otherwise they use the `minimal` profile with the detected home base, as before, so running them without `sudo` still works. `user`, `gpu`, `backup` and the tunnel commands that read tunnel settings stop on a config load error; `tunnel status`, `restart`, `update` and `schedule` continue with env overrides and `--home-base` only. When the config does not load and no absolute home base is given, `status` skips the user and GPU databases instead of reading them under `/home`; `doctor` uses the same fallback. `-o json` is available. Evaluate against a specific profile without applying anything:

```bash
rootfiles status --profile dgx
```

Output uses terminal colour via lipgloss. Colour is stripped automatically for non-TTY output (`rootfiles status | less`, CI logs) and when `NO_COLOR=1` is set.

### Flag scope and host capabilities

- `--module` belongs to `apply`, `check` and `status`; `status` filters module checks and the users/GPU/tunnel sections. `doctor` rejects it.
- `apply --force` overrides the SSH lockout guard. Use `config init --overwrite` to replace a config file; its local `--force` alias is deprecated.
- Fleet commands reject native-host settings such as `--module`, `--profile`, `--config`, `--force` and `--tunnel-token` before SSH. Set them in inventory or each remote site config instead. Fleet report output must be `text` or `json`.
- Native confirmation prompts use `--yes` or `ROOTFILES_YES=true`, including rollback. Fleet rollouts still require explicit `--yes`.
- `backup` requires root and the host lock, and writes a command audit record; `--dry-run` remains unprivileged and writes nothing.
- Native mutation contexts validate the capabilities of the modules they touch before running commands, including dry runs. GPU and tunnel mutations refuse unsupported Rocky capabilities; supported contexts select APT or DNF from the detected distribution.

### Self-update

Update to the latest GitHub release:

```bash
sudo rootfiles update
```

Check for updates without installing:

```bash
rootfiles update --check
```

Both `rootfiles update --check` and `rootfiles tunnel update --check` run without root or the host lock. They exit 0 when current, 2 when an update (or a missing cloudflared installation) is available, and 1 on a lookup error. Scripts that treated an available update as exit 0 must handle exit 2.

Pin a specific version:

```bash
sudo rootfiles update --version v0.9.0
```

Preview the upgrade plan without replacing the binary:

```bash
sudo rootfiles update --dry-run
```

`update` is the canonical spelling; `rootfiles upgrade` works as an alias for muscle-memory compatibility.

Audit command arguments and backup-session notes redact `--tunnel-token`, `--password` (both separated and `=` forms), and the positional token in `tunnel setup`. Existing audit logs and backup notes are not rewritten; review and securely remove or redact any historical copies containing credentials, and rotate exposed credentials. Backup payloads may contain the original managed configuration and must remain protected.

System backup before an OS reinstall (`rootfiles backup`) and recovery afterwards: [docs/operations.md](docs/operations.md#system-backup), [docs/users.md](docs/users.md#os-reinstall-recovery).

### User management

Home base detection, the root-only trust rules and every `user` command (list, groups, keys, lock, expire, delete, rehome, quotas): [docs/users.md](docs/users.md).

```bash
sudo rootfiles user add alice --ssh-pubkey "ssh-ed25519 AAAA..."
```

Set passwords in batch. By default a random password is generated per user and printed once; `--suffix` keeps the legacy `username + suffix` scheme and `--expire` forces a change at next login. Passwords are passed to `chpasswd` on stdin, never on a command line:

```bash
sudo rootfiles user passwd alice bob --expire
```

```bash
sudo rootfiles user passwd --file passwords.csv --expire
```

```bash
sudo rootfiles user passwd --all --dry-run
```

```bash
sudo rootfiles user passwd --file users.txt
```

```bash
sudo rootfiles user passwd alice bob --password 'shared-pass'
```

```bash
sudo rootfiles user backup --file users-backup.json
```

```bash
sudo rootfiles user restore
```

### GPU allocation, tenants and tunnel

- Per-user GPU allocation: [docs/gpu.md](docs/gpu.md).
- Tenant containers: [docs/tenant.md](docs/tenant.md).
- Cloudflare tunnel and private network address: [docs/tunnel.md](docs/tunnel.md).

## Environment variables

All flags can be set via environment variables for unattended operation:

| Variable | Description | Default |
|----------|-------------|---------|
| `ROOTFILES_PROFILE` | Profile name | `apply`: detected (prompted without `--yes`); `status`, `check`: last applied, else detected; `user`, `gpu`, `tunnel`, `schedule`, `backup`: the kept copy of the last apply, else `minimal` |
| `ROOTFILES_YES` | Skip all prompts | `false` |
| `ROOTFILES_HOME_BASE` | Custom home directory | detected (see [User management](#user-management)) |
| `ROOTFILES_USER` | Username to create | — |
| `ROOTFILES_TUNNEL_TOKEN` | Cloudflare tunnel token | — |
| `ROOTFILES_VLAN_ADDRESS` | VLAN private IP | — |
| `ROOTFILES_SSH_PUBKEY` | SSH public key | — |
| `ROOTFILES_TIMEZONE` | Timezone | `Asia/Seoul` |
| `ROOTFILES_DOCKER_ROOT` | Docker storage path | `/var/lib/docker` |
| `ROOTFILES_STATE_DIR` | State, history and file backups | `/var/lib/rootfiles` |
| `ROOTFILES_LOG_FILE` | JSON audit log | `/var/log/rootfiles.log` |
| `ROOTFILES_LOCK_FILE` | Global lock | `/run/rootfiles.lock` |

## Development

Build, tests, CI jobs and releases: [docs/development.md](docs/development.md). Package layout and the module flow: [docs/architecture.md](docs/architecture.md).

## License

MIT

## Fleet controller

Manage many hosts over SSH from an operator machine: [docs/fleet.md](docs/fleet.md).

## Monitoring

Host exporters, the exporter firewall guard and the Prometheus, Alertmanager and Grafana hub: [docs/monitoring.md](docs/monitoring.md).

## Rocky Linux support

The native Rocky profile covers 8.9, 8.10 and 9.x with RPM/DNF package queries and installation, native locale configuration, timezone, host basics, accounts/wheel, sshd and security-only DNF automatic updates with chronyd. Existing accounts retain passwords, keys, homes and unrelated groups. The installer verifies a privileged command path without replacing unrelated files.

The Rocky capability gate refuses Docker, NVIDIA/GPU allocation, Cloudflare, storage, firewall/network and monitoring modules until those paths are qualified on RPM-family systems. `system.apt_mirror` and EPEL-dependent fail2ban are also gated. This is a core-profile support boundary; it does not claim Ubuntu profile parity or compatibility with other RHEL derivatives. Use `--profile rocky`, or adapt an existing restricted site configuration explicitly. Security-only updates require usable repository advisory metadata; missing metadata is reported instead of treated as successful protection.

The gate runs before application, including dry-run. Backup/rollback restores managed files; it does not uninstall packages, reverse service effects or move user data. Verify actual SSH access after any change before ending the administrative session.

### Unknown firewall state

Before changing a configured SSH port, rootfiles checks installed UFW and firewalld state. A failed or unrecognized status blocks the change, including when `--force` is set. Review `sudo ufw status` or `sudo firewall-cmd --state`, repair the reported query or port allowance, then retry; restoring `ssh.port` is another way to avoid the port change. An active UFW can receive a missing unrestricted TCP allowance before sshd changes, and `check` reports that command. Existing source-limited or family-limited rules require an explicit operator adjustment rather than automatic widening. On Ubuntu, an unchanged managed SSH port leaves the existing UFW policy alone.

Read-only `check` and `status` report unknown firewall inspection as a blocked pending change instead of aborting the module report. Apply still refuses before changing the affected component. The network module also refuses UFW rule changes or enablement while firewall state is unknown, or while firewalld is active. Tenant SSH and exporter setup treat unknown UFW state as a blocker, and doctor reports it explicitly. Inspection reuses the supported UFW/firewalld boundaries; it does not probe or manage direct iptables or native nftables policies.

## AGENTS.md background

Human-readable background. Agent instructions: `AGENTS.md`.

- 2026-11-09: raised the Go baseline to 1.26.10 so CI and release share a supported, pinned toolchain from `go.mod` (#87).
- 2026-11-09: native-command test isolation now includes installed/failing ambient tools after remote-only module runs missed host sshd/UFW dependencies (#89, #104).
