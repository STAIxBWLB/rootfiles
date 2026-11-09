# Modules

`apply` converges a host through 14 modules. Every module has a side-effect-free `Check` and an idempotent `Apply`, and supports `--dry-run`. Run a subset with `--module`, for example `sudo rootfiles apply --module cloudflared,docker`. How a run works: [architecture.md](architecture.md#apply-and-check-flow). Contract: [docs/specs/engine.md](specs/engine.md).

## Overview

Modules run in this order. A module runs when its toggle is `true` in the resolved config.

| # | Module | Toggle | Rocky |
|---|---|---|---|
| 1 | `locale` | `modules.locale.enabled` | Supported |
| 2 | `system` | `modules.system.enabled` | Supported, without `apt_mirror` |
| 3 | `packages` | `modules.packages.enabled` | Supported |
| 4 | `users` | `modules.users.enabled` | Supported |
| 5 | `ssh` | `modules.ssh.enabled` | Supported |
| 6 | `security` | `modules.security.enabled` | Supported with limits, without `fail2ban` |
| 7 | `docker` | `modules.docker.enabled` | Unsupported |
| 8 | `nvidia` | `modules.nvidia.enabled` | Unsupported |
| 9 | `gpu` | `modules.nvidia.gpu_allocation.enabled` | Unsupported |
| 10 | `cloudflared` | `modules.cloudflared.enabled` | Unsupported |
| 11 | `storage` | `modules.storage.enabled` | Unsupported |
| 12 | `network` | `modules.network.enabled` | Unsupported |
| 13 | `monitoring` | `modules.monitoring.enabled` | Unsupported |
| 14 | `tenant` | `modules.tenant.enabled` | Unsupported |

- `system` runs before `packages`, so an APT mirror applies to the first install.
- `users` runs before `ssh`, so declared accounts and their keys exist before password login is turned off.
- `security` runs after `ssh`, so the fail2ban jail follows the configured SSH port.
- `tenant` runs after `docker` and `nvidia`, which tenant containers need.

On Rocky Linux an enabled unsupported module or option is refused before anything changes, dry runs included. Supported versions and the shared rules: [rocky.md](rocky.md).

## locale

Generates the locale and sets the timezone from the top-level `locale` and `timezone` keys; installs `tzdata` when it is missing.

- Rocky: uses `/etc/locale.conf`, `localectl` and the installed glibc language packs; a requested locale must be available through them. Timezone data comes from `tzdata`.

## system

Host basics under `modules.system`: `hostname`, a `/swapfile` of `swap_size` (only when the host has no swap at all; existing swap is never changed), `sysctl` entries in `/etc/sysctl.d/90-rootfiles.conf`, a persistent journal cap (`journald_max_use`), and on Ubuntu an `apt_mirror` that replaces `archive.ubuntu.com` (not `security.ubuntu.com`).

- Rocky: hostname, sysctl, swap and journald use the existing system tools. `apt_mirror` is refused; configure DNF repositories separately.

## packages

Installs the top-level `packages` and `packages_extra` lists, non-interactively; APT waits for the dpkg lock.

- Rocky: package names must be native RPM names.

## users

Converges accounts and the home base from the top-level `users` keys: `home_base`, `default_shell`, `default_groups`, `sudo_nopasswd`, declared `accounts` (created if missing; missing keys and groups added; never removed) and `fleet_sudo_users`. Details: [users.md](users.md); the fleet sudo rule: [fleet.md](fleet.md#privilege).

- Rocky: administrator membership uses `wheel`. Existing homes, keys, passwords and other groups are kept.

## ssh

Hardens sshd through a drop-in from the top-level `ssh` keys: `disable_root_login`, `disable_password_auth` (password and keyboard-interactive), `port`, `max_auth_tries`, and `password_auth_users` (accounts that keep password login in a `Match User` block). `sshd -t` validates the result.

- Lockout guard: password login is turned off only when at least one account can log in with a key (declared accounts count while `users` is in the same run and has not failed) and no other account would be left with only a password. `check` and `doctor` list accounts that still log in with passwords. `apply --force` overrides the key check.
- A port change on socket-activated Ubuntu restarts `ssh.socket`. Before changing the port, the firewall state is checked; an unknown state blocks the change, even with `--force`.
- Rocky: if the main sshd config lacks `Include /etc/ssh/sshd_config.d/*.conf`, Apply backs it up and adds the include at the top, keeping its mode. It validates with `sshd -t` and the effective settings before reloading, and restores the main config and the drop-in when validation fails. A port change needs a compatible SELinux `ssh_port_t` label and an existing unrestricted TCP allowance for both address families in UFW, or in every active firewalld zone. Source-, family- or zone-limited and complex deny or rich-rule policies need operator preparation and console verification; rootfiles never opens or widens firewall policy on Rocky.

## security

Baseline hardening under `modules.security`: `unattended_upgrades`, `fail2ban` (an sshd jail on the SSH port) and `time_sync` (chrony when installed, else systemd-timesyncd).

- Ubuntu: unattended upgrades install security updates only, never reboot and exclude the NVIDIA and CUDA stack. `/etc/apt/apt.conf.d/zzzzzz-rootfiles-unattended-upgrades` clears inherited origins, and `check` reads the merged policy from `apt-config dump`.
- Rocky (supported with limits): `dnf-automatic-install.timer` applies security updates only from the Rocky `baseos` and `appstream` repositories, excludes NVIDIA and CUDA packages, and never reboots. Check reads the DNF cache without refreshing it: expired but parseable metadata does not show a correct policy as drift, while absent or malformed required metadata reports a pending refresh; this does not promise current patches. Apply refreshes metadata and requires fresh `updateinfo` for both required repositories before writing the policy. Other enabled repositories without usable `updateinfo` get a coverage warning and are not covered. `chronyd` provides time sync. `fail2ban` is refused until an explicit EPEL setup is verified.

## docker

Installs Docker CE and a `docker-compose` compatibility symlink. With `modules.docker.storage_dir`, it moves Docker's data root there by merging keys into `/etc/docker/daemon.json`; other keys in that file are kept.

## nvidia

Installs the NVIDIA Container Toolkit, configures Docker's `nvidia` runtime, and enables `nvidia-persistenced` (`persistenced`) and `nvidia-fabricmanager` (`fabric_manager`, for NVSwitch systems) where installed.

## gpu

Per-user GPU allocation: profile scripts, cgroup device rules and a Docker wrapper written from the allocation ledger. It is toggled by `modules.nvidia.gpu_allocation.enabled`, with `method` choosing `env`, `cgroup` or `both`. Details: [gpu.md](gpu.md).

## cloudflared

Cloudflare Tunnel service (token in a root-only env file) and an optional private network address. Details: [tunnel.md](tunnel.md).

## storage

Creates `modules.storage.data_dir`, the home base and its `.rootfiles` metadata directory, and Docker's `storage_dir`, and points the `symlinks` map (link: target) at their targets. A path that already holds data blocks its symlink instead of being replaced. A home base that fails the trust rules in [users.md](users.md#root-only-trust-rules) is refused.

## network

Installs and enables UFW (`modules.network.ufw`) and allows `allowed_ports`. The SSH port is always allowed. UFW changes are refused while the firewall state cannot be read or firewalld is active.

## monitoring

Opt-in Prometheus node exporter and DCGM GPU exporter, an optional exporter firewall guard, and an optional Prometheus, Alertmanager and Grafana hub. Details: [monitoring.md](monitoring.md).

## tenant

Opt-in host preparation for `rootfiles tenant`: GPU device guard timer, tenant password and key sync, `fstrim`, the operator sudoers rule, volume mount units and optional single-port SSH routing through sshpiper. Details: [tenant.md](tenant.md#host-preparation).
