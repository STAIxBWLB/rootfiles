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

All modules are idempotent and support `--dry-run`.

| Module | Description |
|--------|-------------|
| `locale` | Locale generation, timezone (installs tzdata when missing) |
| `system` | Hostname, swapfile (only when no swap), sysctl, journald cap, Ubuntu APT mirror |
| `packages` | APT package installation (non-interactive, waits for the dpkg lock) |
| `users` | Custom home base, declared `users.accounts` (keys, groups), backup/restore |
| `ssh` | sshd hardening (root login, password + keyboard-interactive auth, port, MaxAuthTries) with lockout guard; per-user password exceptions (`ssh.password_auth_users`) |
| `security` | Security-only unattended upgrades (no reboot, NVIDIA/CUDA excluded; `apt.conf.d/zzzzzz-rootfiles-unattended-upgrades` clears inherited origins, and check reads the merged policy from `apt-config dump`), fail2ban sshd jail, NTP |
| `docker` | Docker CE + daemon.json merge + storage relocation |
| `nvidia` | NVIDIA Container Toolkit, Docker runtime, nvidia-persistenced / fabric manager |
| `gpu` | Per-user GPU allocation (env vars, cgroups) |
| `cloudflared` | Cloudflare Tunnel service (token in a root-only env file) + VLAN private network |
| `storage` | RAID/NVMe directory setup, symlinks |
| `network` | UFW firewall, port rules (SSH port always allowed) |
| `monitoring` | Opt-in Prometheus node exporter and DCGM GPU exporter, plus an optional Prometheus/Alertmanager/Grafana hub that reloads on rule changes |
| `tenant` | Opt-in host prep for `rootfiles tenant`: GPU device guard timer, tenant password and key sync, fstrim, operator sudoers rule, tenant volume mount units, optional sshpiper single-port SSH routing |

Modules run in this order; `users` precedes `ssh` so declared operators exist before password auth is turned off.

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
sudo rootfiles apply --config /raid/backup/rootfiles-backup-*/config-snapshot.yaml
```

Unattended (CI/automation — skips all interactive prompts). `--user`/`--ssh-pubkey` declare an account that is created before SSH is hardened, and `--tunnel-token` installs the tunnel service:

```bash
sudo rootfiles apply --profile dgx --yes --home-base /raid/home --tunnel-token "$CF_TUNNEL_TOKEN" --vlan-address "172.16.229.32/32" --user yjlee --ssh-pubkey "ssh-ed25519 AAAA..."
```

### Check system state

```bash
sudo rootfiles check --profile dgx
```

```bash
sudo rootfiles check --config /raid/backup/rootfiles-backup-*/config-snapshot.yaml
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

Run `check` and `doctor` on a schedule (reports go to `/var/lib/rootfiles/{check,doctor}.json`, and to the node exporter textfile directory when the `monitoring` module is enabled). When no report exists yet, `schedule enable` also starts the report service once, so a new host does not raise the stale-report alert until the first timer run:

```bash
sudo rootfiles schedule enable --on-calendar daily
sudo rootfiles schedule disable
```

### Rollback

```bash
rootfiles rollback                     # list backup sessions
sudo rootfiles rollback 20260926-101500 --dry-run
sudo rootfiles rollback 20260926-101500
```

Rollback restores files only (configs, units, symlinks); packages, accounts and running services are not reverted — restart affected services afterwards.

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

### Self-update

Update to the latest GitHub release:

```bash
sudo rootfiles update
```

Check for updates without installing:

```bash
rootfiles update --check
```

Pin a specific version:

```bash
sudo rootfiles update --version v0.9.0
```

Preview the upgrade plan without replacing the binary:

```bash
sudo rootfiles update --dry-run
```

`update` is the canonical spelling; `rootfiles upgrade` works as an alias for muscle-memory compatibility.

### System backup (for OS upgrade)

Captures system info, users, config files, Docker images, and a rootfiles-compatible config snapshot.

```bash
sudo rootfiles backup
```

```bash
sudo rootfiles backup -o /raid/backup
```

```bash
sudo rootfiles backup --skip-docker
```

```bash
sudo rootfiles backup --skip-etc
```

Backup output directory structure:

```
rootfiles-backup-{hostname}-{YYYYMMDD}/
├── system-info.json        # hostname, OS, GPU, arch, memory, mounts
├── users.json              # user metadata (from rootfiles DB)
├── etc-config.tar.gz       # /etc/ssh, docker, ufw, netplan, fstab
├── crontab-root.txt        # root crontab
├── root-ssh.tar.gz         # /root/.ssh/
├── usr-local-bin.tar.gz    # /usr/local/bin/
├── docker-images.txt       # docker image list
└── config-snapshot.yaml    # current system → rootfiles YAML config
```

After an OS reinstall, restore accounts first (their SSH keys are what allow the SSH hardening to proceed), then re-apply:

```bash
sudo rootfiles user restore
```

Restore from snapshot:

```bash
sudo rootfiles apply --config /raid/backup/rootfiles-backup-*/config-snapshot.yaml --dry-run
```

```bash
sudo rootfiles apply --config /raid/backup/rootfiles-backup-*/config-snapshot.yaml --yes
```

### User management

Users are created at a custom home base (e.g., `/raid/home/`) that survives OS reinstalls.

When `users.home_base` is unset (the built-in profiles except `dgx` leave it unset), it is detected on the host: an uncommented `HOME=` in `/etc/default/useradd` is kept (stock Ubuntu and DGX OS leave it commented out), a base where rootfiles already manages users (`.rootfiles` under `/home`, `/raid/home`, `/data/home` or `/nvme/home`) is kept, and otherwise, on Ubuntu and DGX OS, the first separate read-write local data drive mounted at `/raid`, `/data` or `/nvme` (ext4, xfs, btrfs or zfs) gets `<mount>/home`. A mount on the same device as `/` (such as a bind mount of the root filesystem), a btrfs subvolume of the root filesystem, a ZFS dataset in the root pool, or a read-only mount is not a data drive; when `<mount>/home` is itself a mount, it decides. Rocky keeps `/home` because rootfiles does not label homes outside `/home` for SELinux. `/mnt` and network filesystems are never chosen, nor is a base where `/` or any existing directory down to it is a symlink, not owned by root, or writable by group or others (a world-writable scratch mount keeps `/home`). With no data drive the home base is `/home`. The users module writes the applied home base, `/home` included, as `HOME=` in `/etc/default/useradd`, so a later run keeps it. If another of those bases already holds `.rootfiles/users.json` or `gpu-allocations.json` (for example `user` or `gpu` commands run without `--profile` on a `dgx` host), detection stops with an error naming the file instead of guessing; set `home_base`, `ROOTFILES_HOME_BASE` or `--home-base` to choose. A fleet rollout is not blocked by it, because its site config sets `home_base`. Only new users are affected; existing homes are not moved (`rootfiles user rehome` does that). Fleet site configs must set `home_base` explicitly. Any home base other than `/home`, whether set explicitly or kept from `HOME=` in `/etc/default/useradd`, must be controlled by root alone (a symlinked `/home` is followed once when its target passes the same rule): the users and storage modules, `user add` and `user rehome` refuse it when `/` or any existing directory down to the base is a symlink, not owned by root, or writable by group or others, and change nothing until the operator fixes it. `user restore` applies the rule to each restored home's parent directory, skipping and reporting a user whose parent fails it, and without a file it refuses to read the users database from such a base. The same rule, plus a `<home_base>/.rootfiles` whose every entry is a root-owned regular file or directory that group and others cannot write (no symlinks), guards the user and GPU databases: the users, storage and gpu modules, `user del`, `user key add`/`rm`, `user quota` and `gpu assign`/`revoke` refuse before any change, `backup` leaves the users database out with a warning, `status`, `user list`, `user audit`, `gpu list` and `gpu status` warn on stderr and still report, and no database write follows a symlink.

```bash
sudo rootfiles user add yjlee --pubkey "ssh-ed25519 AAAA..."
```

```bash
sudo rootfiles user list
```

```bash
sudo rootfiles user list --names
```

List system users (UID 1000-65533):

```bash
sudo rootfiles user list --system
```

```bash
sudo rootfiles user list --system --names
```

Show UID/GID/groups for a user:

```bash
sudo rootfiles user id yjlee
```

List all groups or groups for a specific user:

```bash
sudo rootfiles user groups
```

```bash
sudo rootfiles user groups yjlee
```

Add/remove a user from groups:

```bash
sudo rootfiles user group-add yjlee --docker --sudo
```

```bash
sudo rootfiles user group-add yjlee --groups dev,ops
```

```bash
sudo rootfiles user group-del yjlee --docker
```

Set passwords in batch. By default a random password is generated per user and printed once; `--suffix` keeps the legacy `username + suffix` scheme and `--expire` forces a change at next login. Passwords are passed to `chpasswd` on stdin, never on a command line:

```bash
sudo rootfiles user passwd alice bob --expire
```

```bash
sudo rootfiles user passwd alice bob --suffix '!@'
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
sudo rootfiles user backup
```

```bash
sudo rootfiles user restore
```

```bash
sudo rootfiles user rehome yjlee              # verified copy; old home kept as <old>.rootfiles-bak-<ts>
sudo rootfiles user rehome yjlee --remove-old
```

Lifecycle:

```bash
sudo rootfiles user key add yjlee "ssh-ed25519 AAAA... yjlee@laptop"
sudo rootfiles user key list yjlee
sudo rootfiles user key rm yjlee yjlee@laptop     # by comment, key, or list index
sudo rootfiles user lock yjlee                    # blocks password *and* SSH key logins
sudo rootfiles user unlock yjlee
sudo rootfiles user expire yjlee 2026-12-31       # or: never
sudo rootfiles user del yjlee --archive           # tar.gz home under <home_base>/.rootfiles/archive
sudo rootfiles user du                            # home usage, largest first
sudo rootfiles user audit                         # users.json vs. system accounts (exit 2 on drift)
```

Disk quotas on the home_base filesystem (XFS project quota with `prjquota`, or ext4 user quota with `usrquota` + `quotaon`; the limit is re-applied by `user restore`):

```bash
sudo rootfiles user quota set yjlee 500G
sudo rootfiles user quota show
sudo rootfiles user quota rm yjlee
```

### GPU allocation

Assign GPUs to individual users to prevent resource contention on shared GPU servers.

```bash
sudo rootfiles gpu assign alice --gpus 0,1,2,3 --method env
```

```bash
sudo rootfiles gpu assign bob --gpus 4,5,6,7 --method cgroup
```

```bash
sudo rootfiles gpu list
```

```bash
sudo rootfiles gpu status
```

```bash
sudo rootfiles gpu revoke alice
```

Methods:

| Method | Mechanism | Scope |
|--------|-----------|-------|
| `env` | Sets `CUDA_VISIBLE_DEVICES` / `NVIDIA_VISIBLE_DEVICES` via `/etc/profile.d/` script | Login shells |
| `cgroup` | systemd slice with `DeviceAllow` rules | All processes in user session |
| `both` | env + cgroup combined | Full isolation |

The default method is configured per profile (`env` for gpu-server, `both` for dgx).

MIG mode and instances (read-only; repartition with `nvidia-smi mig`):

```bash
rootfiles gpu mig status
```

### Tenant containers

Long-lived SSH containers for outside groups, one per tenant, with a size-capped volume and GPUs from the allocation ledger. Full guide: [docs/tenant.md](docs/tenant.md).

```bash
sudo rootfiles apply --module tenant          # once per host (modules.tenant.enabled)
sudo rootfiles tenant create est --image IMAGE --ssh-port 3889 --gpus 2,3 --size 2T --account est:1000 --sudo
sudo rootfiles tenant image est IMAGE2         # recreate; data, passwords, host key and GPUs kept
sudo rootfiles tenant volume resize est 3T     # grows online
sudo rootfiles tenant status
```

### Cloudflare tunnel + VLAN

```bash
sudo rootfiles tunnel setup "$TOKEN" --vlan-address "172.16.229.32/32"
```

On a shared host, keep the token out of command lines and site configs: put it in a root-owned file with mode `0600` and point the site config at it. `check` and `apply` read the file on the server, refuse it when it is group/other readable or not owned by root, and `config show` prints only the path. `--tunnel-token` and `ROOTFILES_TUNNEL_TOKEN` still override the file; a config file may set `tunnel_token` or `tunnel_token_file`, not both.

```yaml
modules:
  cloudflared:
    enabled: true
    tunnel_token_file: /etc/rootfiles/tunnel-token   # root:root 0600
```

```bash
sudo rootfiles tunnel status
```

Upgrade just the `cloudflared` binary to the latest upstream release (no other modules touched). The service is restarted when present; binary-only refreshes on hosts without the tunnel skip the restart:

```bash
sudo rootfiles tunnel update                     # fetch latest
sudo rootfiles tunnel update --check             # compare installed vs. latest, no download
sudo rootfiles tunnel update --version 2024.9.1  # pin to a specific cloudflared release
```

```bash
sudo rootfiles tunnel restart
```

```bash
sudo rootfiles tunnel uninstall
```

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

## Build from source

```bash
make build
```

```bash
make test
```

Requires Go 1.23+.

## Architecture

Interactive blueprint: [docs/architecture/rootfiles-v2-rendered.html](docs/architecture/rootfiles-v2-rendered.html) (spec: [rootfiles-v2.architecture.json](docs/architecture/rootfiles-v2.architecture.json)).

```
cmd/rootfiles/        Entry point
internal/
  cli/                Cobra commands (apply, backup, check, config, doctor, gpu, rollback,
                      schedule, status, tenant, tunnel, update, user) + root/lock preflight
  config/             YAML profiles (tree-merged extends), validation, system detector
    profiles/         Embedded profile YAMLs (go:embed)
  module/             14 modules implementing the Module interface, doctor checks
  tenant/             Tenant specs, containers, volumes, passwords, import, audit
  exec/               Shell runner (dry-run aware, per-run file backups), APT wrapper
  state/              Applied-state record, history, global lock
  ui/                 Interactive prompts (Charm huh) + shared output styling
                      (lipgloss palette, ✓ ✗ → ⚠ markers, WriteHeader/
                      Section/KV/Hint/Bullet helpers)
```

`NewRegistry()` and `defaultOrder` in `internal/module/module.go` are a two-part module contract enforced by `TestRegistryDefaultOrderSync`. The GPU allocation database (`<home-base>/.rootfiles/gpu-allocations.json`) is read-modify-written under `syscall.Flock` with atomic tmp-and-rename writes, so concurrent `gpu assign` / `revoke` calls cannot lose an allocation.

## CI

Every push and pull request runs:

| Job | Purpose |
|-----|---------|
| `decision` | Skips the jobs below when only non-test files changed, or when a main push carries a tree its PR already validated (`scripts/ci-decision.sh`) |
| `release-notes` | Always runs: `scripts/check-release-notes.sh` checks every `docs/releases/*.md` against the AGENTS.md format |
| `lint` | `gofmt`, `go vet`, `go mod tidy` drift |
| `vuln` | `govulncheck ./...` (stdlib + deps) |
| `unit` | `go test ./... -race -count=1` + per-function coverage summary, coverage artifact |
| `integration` | 3 OS images × 4 profiles (11 combinations) |
| `module` | 2 OS × 9 modules + GPU on DGX mock (isolated module execution) |
| `scenario` | 14 E2E flows: dry-run-all-profiles, user backup/restore, user rehome, user list names, user lifecycle, user quota, declarative accounts, tunnel setup/teardown, OS reinstall recovery, system backup, GPU allocation, status, SSH lockout guard, state/rollback/lock |
| `fleet-scenario` | Fleet controller against two SSH containers (`tests/scenarios/fleet.sh`): status, serial stop rule, `--skip-unreachable` |
| `tenant-scenario` | Tenant lifecycle on the runner's Docker and systemd (`tests/scenarios/tenant-lifecycle.sh`): temporary password login, recreate, quota and growth, operator delegation, dry run, import |
| `rocky` | Rocky 8.9 / 8.10 / 9 images: native core profile, SSH and installer (`tests/run-rocky.sh`) |
| `monitoring-hub` | Generated monitoring hub acceptance (`tests/scenarios/monitoring-hub.sh`) |
| `release` | GoReleaser on every `v*` tag push, after the tagged commit's main-push Test run succeeded (Test does not run for tags); publishes `docs/releases/<tag>.md` as the release body and fails when it is missing or fails `scripts/check-release-notes.sh` (format: `AGENTS.md`, Release notes) |

## License

MIT

## Fleet controller

The fleet controller reads an operator-owned inventory file. Start with [`docs/fleet.example.yaml`](docs/fleet.example.yaml), copy it outside the repository, and edit the SSH aliases, scrape addresses, groups, and site config paths. Site config paths are relative to the inventory and are fully validated before the controller opens SSH connections. Each site config must set `users.home_base` explicitly, because the controller fingerprints it without seeing the host; add it to the monitoring examples below when you use them as site configs.

```sh
rootfiles fleet status --inventory ~/.config/rootfiles/fleet.yaml --all
rootfiles fleet check --inventory ~/.config/rootfiles/fleet.yaml --group gpu -o json
rootfiles fleet doctor --inventory ~/.config/rootfiles/fleet.yaml --host gpu01
rootfiles fleet apply --inventory ~/.config/rootfiles/fleet.yaml --host gpu01 --yes --dry-run
rootfiles fleet update --inventory ~/.config/rootfiles/fleet.yaml --group gpu --version v1.2.3 --yes --timeout 25m
```

Read-only reports and rollout preflight checks use a 45-second SSH deadline. Mutating fleet commands allow 15 minutes per host by default; set `--timeout` to another positive duration up to 24 hours when package or image work needs longer. The deadline covers one host's remote command, not the entire rollout.

Read-only commands use bounded parallel SSH and never prompt for a sudo password. Rollouts require an explicit host, group, or `--all` selection plus `--yes`; they run serially and stop after the first failure. Try one host as a canary before widening a rollout. A timeout or cancellation can leave the current host partially changed or its remote process still running, while later hosts remain skipped. Inspect that host with `fleet status`, `fleet check`, and the rootfiles lock state before retrying.

`--skip-unreachable` lets `fleet apply`, `update`, `bootstrap` and `schedule` continue past a host whose SSH connection fails (state `skipped-unreachable`); any other failure, including a timeout, still stops the run. The run then exits 2 and prints a ready-to-run command for the skipped hosts. A failed host's reason is the remote `Error:` line (else the last stderr line); the full output stays in the run log.

`fleet update` runs a read-only `check` with the new binary on each updated host. A host whose check plans changes is reported `apply-pending` with the affected modules, the rollout continues, and the run exits 2; nothing is applied automatically. Single-host `rootfiles update` prints the same hint.

## Host exporters

```yaml
modules:
  monitoring:
    enabled: true
    node_exporter: true
    dcgm_exporter: true
    node_exporter_port: 9100
    dcgm_exporter_port: 9400
    # For hosts validated against newer drivers, pin a compatible image:
    # dcgm_exporter_image: nvcr.io/nvidia/k8s/dcgm-exporter:4.6.1-4.8.4-distroless
    listen_address: 10.0.0.11
    allow_from:
      - 10.0.0.5/32
    perimeter_firewall: false
```

DCGM exporter requires an already working NVIDIA GPU, driver, Docker daemon, and NVIDIA container runtime. A missing prerequisite shows as `blocked` in `rootfiles check` and fails `apply --dry-run`; `apply` still sets up node exporter and the rest of the module, then fails with the DCGM error and the fix (for a missing runtime: `sudo nvidia-ctk runtime configure --runtime=docker && sudo systemctl reload docker`). If UFW is active, `allow_from` only applies when its incoming default is deny/reject and there are no broader allow rules for either exporter port. rootfiles reports conflicts and leaves existing firewall rules unchanged. It does not enable UFW.

The default DCGM image is pinned to `nvcr.io/nvidia/k8s/dcgm-exporter:3.3.8-3.6.0-ubuntu22.04` for the fleet's older R535 driver baseline. Blackwell and newer driver combinations have not been qualified with that old DCGM release. Select a pinned `dcgm_exporter_image` only after checking NVIDIA's supported GPU, driver, and paired DCGM/exporter versions and validating the endpoint on that host. NVIDIA documents that driver 580.126.16 requires DCGM 4.3.x or newer, so the default image must not be assumed compatible with R580 hosts. The suggested 4.6.1-4.8.4 distroless override needs real GPU validation before rollout.

The managed collector file selects only temperature, utilization, framebuffer used/free, XID, and volatile double-bit ECC fields shared by the legacy and newer image versions. ECC series may be absent on GPUs or configurations that do not expose ECC counters. The DCGM 3.3.8 XID gauge reports the last observed XID and may stay stale after recovery.

## Monitoring hub

The generated acceptance scenario (`tests/scenarios/monitoring-hub.sh`) requires Docker with both Compose and Buildx plugins, plus SSH, curl and Python 3. Verify `docker compose version` and `docker buildx version` before running the scenario; it uses Buildx to check the default NVIDIA image's registry manifest.

The opt-in monitoring hub runs Prometheus, Alertmanager and Grafana on one Docker host. It reads scrape targets from Prometheus file discovery; `rootfiles fleet targets --push <hub>` replaces that target file atomically, and Prometheus reloads it from the mounted discovery directory without restarting the containers. When `apply` rewrites the Prometheus configuration or alert rules on a running hub, it calls Prometheus's `/-/reload` endpoint (and Alertmanager's when its configuration changed), so new rules take effect in the same run; a failed reload fails the apply.

The discovery directory must contain only the configured targets file. Check and Apply refuse other entries before mounting the directory, including credentials, symlinks, subdirectories and temporary staging files. A check that overlaps an atomic target push can be retried after the rename completes.

Hub web ports bind to `127.0.0.1` by default. For remote Grafana access, route a Cloudflare Tunnel to `http://127.0.0.1:<grafana_port>` and apply the access policy in Cloudflare. The hub does not open a public port. Prometheus, Alertmanager and Grafana use the pinned profile images. The generated administrator password is stored in a root-only file; no default password is used.

The hub stores persistent data below `data_dir`. Existing data directories must be real root-owned directories with root read/write/execute permission; rootfiles refuses directories that do not meet this requirement and leaves existing ownership and contents untouched.

Enable the hub on the selected host:

```yaml
modules:
  docker:
    enabled: true
  monitoring:
    enabled: true
    node_exporter: true
    hub:
      enabled: true
      data_dir: /data/monitoring
      retention: 30d
      targets_file: /etc/rootfiles/monitoring/discovery/targets.json
      alert_receiver_file: /etc/rootfiles/monitoring/receiver.yaml
      telegram_bot_token_file: /etc/rootfiles/monitoring/telegram-bot-token
      listen_address: 127.0.0.1
      grafana_port: 3000
      prometheus_port: 9090
      alertmanager_port: 9093
      # Optional: alertmanager_retention: <positive-duration>
```

`alert_receiver_file` is a complete Alertmanager configuration. For a Telegram receiver, point `bot_token_file` at the fixed container path shown below; rootfiles mounts the configured host token file there. The receiver file, token file and any configured Grafana password file must be regular files owned by root with mode `0600`.

When configured, `alertmanager_retention` adds Alertmanager's `--data.retention` flag. Use positive integer duration components supported by the pinned Alertmanager parser; year units are not supported. When unset, rootfiles leaves the flag out and preserves the pinned image's default. Choose a duration at least as long as the receiver's repeat interval so unchanged firing alerts do not repeat solely because Alertmanager discarded its notification log.

```yaml
route:
  receiver: operations
receivers:
  - name: operations
    telegram_configs:
      - bot_token_file: /run/secrets/telegram-bot-token
        # Add the operator-owned chat_id here.
        send_resolved: true
        parse_mode: ""
        message: |-
          {{ $firing := .Alerts.Firing }}{{ range $index, $alert := $firing }}{{ if lt $index 4 }}[{{ $alert.Labels.severity }}] {{ printf "%.120s" $alert.Annotations.summary }}
          {{ printf "%.160s" $alert.Annotations.description }}
          {{ if and $alert.Annotations.runbook_url (le (len $alert.Annotations.runbook_url) 128) }}Runbook: {{ $alert.Annotations.runbook_url }}{{ end }}
          {{ end }}{{ end }}{{ if gt (len $firing) 4 }}{{ len (slice $firing 4) }} additional firing alert(s) omitted.{{ if and .ExternalURL (le (len .ExternalURL) 160) }} Details: {{ .ExternalURL }}{{ else }} Details link unavailable; verify Alertmanager externalURL.{{ end }}{{ end }}
          {{ $resolved := .Alerts.Resolved }}{{ range $index, $alert := $resolved }}{{ if lt $index 4 }}[resolved] {{ printf "%.120s" $alert.Annotations.summary }}
          {{ printf "%.160s" $alert.Annotations.description }}
          {{ if and $alert.Annotations.runbook_url (le (len $alert.Annotations.runbook_url) 128) }}Runbook: {{ $alert.Annotations.runbook_url }}{{ end }}
          {{ end }}{{ end }}{{ if gt (len $resolved) 4 }}{{ len (slice $resolved 4) }} additional resolved alert(s) omitted.{{ if and .ExternalURL (le (len .ExternalURL) 160) }} Details: {{ .ExternalURL }}{{ else }} Details link unavailable; verify Alertmanager externalURL.{{ end }}{{ end }}
```

The managed rules provide a concise summary and diagnostic description. The example keeps firing and resolved alerts visibly distinct, renders at most four of each, limits summary and description lengths, and states how many alerts were omitted. It includes Alertmanager's `ExternalURL` in an overflow notice only when present and within the template's length bound; configure and verify that URL is reachable by recipients before using the template. It prints `runbook_url` only when an operator-supplied rule provides one of usable length. Do not use Prometheus's generated `GeneratorURL`, which can point at the hub's container-only service name, or derive a URL from a host label. Rootfiles does not configure receiver URLs, and its default receiver remains inert. The deployment owner controls notification grouping, repeat intervals, acknowledgement and maintenance policy in this complete receiver file.

Configure exporter hosts with the addresses reachable from the hub and the appropriate node/DCGM exporters. Inventory group labels are published as `group_<name>=true`, alongside `host` and `exporter=node|dcgm`. Prometheus also adds `instance=<address>:<port>`; use the inventory `host` label to identify a machine because the DCGM container's own `Hostname` label may be its container hostname.

```yaml
defaults:
  sudo: nopasswd
hosts:
  monitor01:
    ssh: monitor01
    address: 10.0.0.5
    groups: [monitoring]
    config: sites/monitor01.yaml
  gpu01:
    ssh: gpu01
    address: 10.0.0.11
    groups: [gpu, dgx]
    config: sites/gpu01.yaml
```

For an exporter host with existing Docker and NVIDIA runtime, enable only monitoring and bind the exporters to its private address and allow the hub address through the host firewall. If a perimeter firewall already restricts the segment, set `perimeter_firewall: true` instead of adding host firewall rules.

```yaml
modules:
  docker:
    enabled: true
  nvidia:
    enabled: true
  monitoring:
    enabled: true
    node_exporter: true
    dcgm_exporter: true
    listen_address: 10.0.0.11
    allow_from:
      - 10.0.0.5/32
```

Apply the hub and exporters, enable periodic host reports, then publish the targets:

```bash
sudo rootfiles check --config sites/monitor01.yaml
sudo rootfiles apply --yes --config sites/monitor01.yaml
sudo rootfiles schedule enable --on-calendar hourly
rootfiles fleet targets --inventory fleet.yaml --push monitor01 --yes
```

Check service and endpoint health on the hub:

```bash
sudo systemctl is-active rootfiles-monitoring-hub.service
sudo docker compose -f /etc/rootfiles/monitoring/compose.yaml ps --format json
curl -fsS http://127.0.0.1:9090/-/ready
curl -fsS http://127.0.0.1:9090/api/v1/targets
curl -fsS http://127.0.0.1:9093/-/ready
curl -fsS http://127.0.0.1:3000/api/health
```

Confirm that host reports reach Prometheus and that warnings are distinct from doctor failures:

```bash
sudo rootfiles check -o prometheus | rg '^rootfiles_(module_satisfied|check_timestamp_seconds)'
sudo rootfiles doctor -o prometheus | rg '^rootfiles_doctor_findings'
```

The doctor alert counts only `rootfiles_doctor_findings{level="fail"}`. Warnings do not fire it. The stale-report alert checks each inventory `host` label and fires when a node exporter is up but its timestamp metric is missing, or when the most recent report is older than 48 hours. This accommodates the default daily timer and its randomized delay; use a daily or more frequent schedule for this alert expectation.

The hub checks target reachability, node filesystem use, rootfiles drift and failed doctor findings, stale reports, and DCGM XID, ECC and temperature metrics. The default temperature alert is above 85 C for 10 minutes. This is an alert threshold, not a GPU operating limit; tune it to the vendor thresholds for each model. The managed DCGM collector enables `DCGM_FI_DEV_ECC_DBE_VOL_TOTAL`; the pinned exporter otherwise comments out ECC collection. Its `DCGM_FI_DEV_XID_ERRORS` metric reports the last XID code and may remain stale after recovery.

## Rocky Linux support

The native Rocky profile covers 8.9, 8.10 and 9.x with RPM/DNF package queries and installation, native locale configuration, timezone, host basics, accounts/wheel, sshd and security-only DNF automatic updates with chronyd. Existing accounts retain passwords, keys, homes and unrelated groups. The installer verifies a privileged command path without replacing unrelated files.

The Rocky capability gate refuses Docker, NVIDIA/GPU allocation, Cloudflare, storage, firewall/network and monitoring modules until those paths are qualified on RPM-family systems. `system.apt_mirror` and EPEL-dependent fail2ban are also gated. This is a core-profile support boundary; it does not claim Ubuntu profile parity or compatibility with other RHEL derivatives. Use `--profile rocky`, or adapt an existing restricted site configuration explicitly. Security-only updates require usable repository advisory metadata; missing metadata is reported instead of treated as successful protection.

The gate runs before application, including dry-run. Backup/rollback restores managed files; it does not uninstall packages, reverse service effects or move user data. Verify actual SSH access after any change before ending the administrative session.
