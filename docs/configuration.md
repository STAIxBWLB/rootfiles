# Configuration

Every config key, how profiles and site configs combine, which overrides apply, and which config each command uses. The contract (merge rules, validation, fingerprint, detection) is [docs/specs/config.md](specs/config.md).

## Profiles and site configs

- Six profiles are built in: `base`, `minimal`, `dgx`, `gpu-server`, `full` and `rocky`. What each adds is in the [README profile table](../README.md#profiles); the sources are `internal/config/profiles/*.yaml`.
- `--profile NAME` or `ROOTFILES_PROFILE` picks a profile. Without either, `apply` suggests one from the host: `rocky` on supported Rocky Linux, `dgx` on DGX OS (`/etc/dgx-release`), `gpu-server` with an NVIDIA GPU, else `minimal`.
- A site config is a YAML file passed with `--config FILE` (or `-` for stdin). Its `extends` names a built-in profile or another file, relative to the including file. Chains are at most 5 deep, and cycles are refused.
- Files are merged as YAML trees, parent first:
  - Any key the child sets wins, including `false`, `0` and `""`, so a site config can switch a module off.
  - Nested maps merge key by key.
  - Lists replace the parent's list, except `packages_extra`, which accumulates along the chain.
- Unknown keys are errors, reported with the file name and line.
- `rootfiles config init` writes a starting site config, `config validate` checks one and `config show` prints the merged result ([commands.md](commands.md#rootfiles-config)).

```yaml
# site.yaml
extends: dgx
timezone: UTC
users:
  accounts:
    - name: admin
      ssh_pubkeys: ["ssh-ed25519 AAAA... admin@laptop"]
      groups: [sudo]
modules:
  cloudflared:
    enabled: false
  system:
    hostname: gpu01
```

## Overrides

Applied in this order, later wins:

1. The merged profile or site config.
2. Environment variables: `ROOTFILES_HOME_BASE`, `ROOTFILES_TIMEZONE`, `ROOTFILES_TUNNEL_TOKEN`, `ROOTFILES_VLAN_ADDRESS`, `ROOTFILES_VLAN_INTERFACE`, `ROOTFILES_DOCKER_ROOT`, `ROOTFILES_DATA_DIR` ([commands.md](commands.md#environment-variables)).
3. Flags of the commands that read them: `--home-base`, `--vlan-address`, `--tunnel-token`, and `--user` with `--ssh-pubkey`, which add an account to `users.accounts`.
4. Home base detection, only when `users.home_base` is still unset ([users.md](users.md#home-base)).
5. Validation of the result. Because the overrides come first, `--vlan-address` can satisfy a site config that enables the private network without an address.

The fleet controller resolves site configs without its own environment overrides, and each site config must set `users.home_base` ([fleet.md](fleet.md#inventory)).

## Which config a command uses

| Command | Without `--config`, `--profile` or `ROOTFILES_PROFILE` |
|---|---|
| `apply` | The detected suggestion; without `--yes`, a prompt with the suggestion preselected. |
| `check`, `status`, `doctor`, `config show`, `config validate` | The kept copy of the last apply, else the recorded absolute config path when the file exists, else the recorded profile, else the detected suggestion. |
| `user`, `gpu`, `tunnel`, `schedule`, `backup`, `tenant` | The kept copy of the last apply, else `minimal` with the detected home base. Never the detected suggestion, which on DGX OS would pin `/raid/home`. |

The kept copy is `/var/lib/rootfiles/applied-config.yaml`:

- Every recorded `apply`, from a profile or a config file (fleet rollouts included), writes the fully resolved config there (root, 0600): with its `--home-base`, `--vlan-address`, environment and interactive changes, without `extends` and without an inline tunnel token.
- It is reused only when its bytes match the fingerprint of the last recorded run, the file and every directory above it are root-owned and not writable by group or others, and the caller can read it. A non-root `--dry-run` of a mutating subcommand warns that it previews `minimal` instead.
- Any `rollback` deletes it, because the host no longer matches it.
- Reports name the recorded profile or config path, never the copy. A recorded relative config path (from older versions) is ignored rather than resolved against the current directory.

When the config does not load:

- `user`, `gpu`, `backup`, `tenant`, `tunnel setup` and `tunnel uninstall` stop with the error.
- `tunnel status`, `tunnel restart`, `tunnel update` and `schedule` continue with the environment overrides and `--home-base` only.
- `status` and `doctor` report the error; without an absolute home base they skip the user and GPU databases.

## Validation

`config validate`, `apply` and `check` report every problem at once, each naming its key. Besides unknown keys they check ports, absolute paths, account and group names, sysctl keys, sizes, CIDRs and addresses, interface names, durations, pinned image references, the combinations noted in the tables below, and on Rocky Linux the supported modules ([rocky.md](rocky.md)).

## Keys

A module runs when its `enabled` key is true in the resolved config; the `gpu` module uses `modules.nvidia.gpu_allocation.enabled`. "Default" is the value when no profile or site config sets the key; profile values are noted where a profile sets one.

### Top level

| Key | Type | Default | Purpose |
|---|---|---|---|
| `extends` | string | none | Built-in profile or file this config inherits from. |
| `locale` | string | `""`; `en_US.UTF-8` in `base` and `rocky` | System locale (`locale` module). |
| `timezone` | string | `""`; `Asia/Seoul` in `base` and `rocky` | Timezone (`locale` module); `ROOTFILES_TIMEZONE` overrides it. |
| `packages` | list | `[]`; a base set in `base`, an RPM set in `rocky` | Packages the `packages` module installs. |
| `packages_extra` | list | `[]`; `openssh-server`, `ufw`, `fail2ban` in `minimal` | More packages; accumulates along `extends`. |
| `modules` | map | | Module toggles and settings, below. |
| `users` | map | | Accounts and the home base. |
| `ssh` | map | | sshd hardening. |
| `doctor` | map | | `doctor` grading. |

### Users

| Key | Type | Default | Purpose |
|---|---|---|---|
| `users.home_base` | path | detected; `/raid/home` in `dgx` | Parent directory of new homes ([users.md](users.md#home-base)). Required in fleet site configs. |
| `users.default_shell` | path | `""`; `/usr/bin/zsh` in `minimal` and `rocky` | Login shell of created accounts. |
| `users.default_groups` | list | `[]`; `sudo` in `minimal`, `sudo`, `docker` in `dgx` and `gpu-server`, `wheel` in `rocky` | Groups every created account joins. |
| `users.sudo_nopasswd` | bool | `false`; `true` in `minimal` and `rocky` | Passwordless sudo for managed accounts. |
| `users.accounts` | list | `[]` | Declared accounts: created if missing, missing keys and groups added, never removed. |
| `users.accounts[].name` | string | | Account name. |
| `users.accounts[].ssh_pubkeys` | list | `[]` | OpenSSH public key lines added to `authorized_keys`. |
| `users.accounts[].groups` | list | `[]` | Groups in addition to `users.default_groups`. |
| `users.fleet_sudo_users` | list | `[]` | Accounts allowed passwordless sudo for the fleet's read-only reports only ([fleet.md](fleet.md#privilege)). Needs `modules.users.enabled`; an empty list removes the rule. |

### SSH

| Key | Type | Default | Purpose |
|---|---|---|---|
| `ssh.disable_root_login` | bool | `false`; `true` in `minimal` and `rocky` | `PermitRootLogin no`. |
| `ssh.disable_password_auth` | bool | `false`; `true` in `dgx`, `gpu-server` and `full` | Turn off password and keyboard-interactive login, guarded against lockout ([modules.md](modules.md#ssh)). |
| `ssh.port` | int | `0` (keep the current port) | sshd port. A change is checked against the firewall first ([modules.md](modules.md#firewall-state)). |
| `ssh.max_auth_tries` | int | `0` (sshd default) | `MaxAuthTries`, 0-100. |
| `ssh.password_auth_users` | list | `[]` | Accounts that keep password login while it is off for everyone else (a `Match User` block). Not `root`. |

### Doctor

| Key | Type | Default | Purpose |
|---|---|---|---|
| `doctor.disk` | map | | Disk use grading of `rootfiles doctor`, its metrics and the hub's doctor alert. |
| `doctor.disk.warn_percent` | number | `80` | Warn at this use. |
| `doctor.disk.fail_percent` | number | `90` | Fail at this use; `0 < warn_percent <= fail_percent <= 100`. |
| `doctor.disk.min_free` | size | unset | With it, a mount fails only when it is over `fail_percent` and also has less than this free. Sizes in K, M, G or T, optional `iB` (`100GiB`, `500G`). |

### Module toggles

| Key | Type | Default | Purpose |
|---|---|---|---|
| `modules.locale` | map | | `locale` module. |
| `modules.locale.enabled` | bool | `false`; `true` in `base` and `rocky` | Set the locale and timezone. |
| `modules.packages` | map | | `packages` module. |
| `modules.packages.enabled` | bool | `false`; `true` in `base` and `rocky` | Install `packages` and `packages_extra`. |
| `modules.ssh` | map | | `ssh` module. |
| `modules.ssh.enabled` | bool | `false`; `true` in `base` and `rocky` | Apply the `ssh` keys. |
| `modules.users` | map | | `users` module. |
| `modules.users.enabled` | bool | `false`; `true` in `minimal` and `rocky` | Apply the `users` keys. |

### System

| Key | Type | Default | Purpose |
|---|---|---|---|
| `modules.system` | map | | Host basics. |
| `modules.system.enabled` | bool | `false`; `true` in `minimal` and `rocky` | Run the `system` module. |
| `modules.system.hostname` | string | unset | Hostname. |
| `modules.system.swap_size` | size | unset | `/swapfile` of this size (`8G`), only when the host has no swap. |
| `modules.system.journald_max_use` | size | unset; `2G` in `minimal` and `rocky` | Persistent journal cap. |
| `modules.system.sysctl` | map | `{}` | Entries for `/etc/sysctl.d/90-rootfiles.conf`. |
| `modules.system.apt_mirror` | URL | unset | Replaces `archive.ubuntu.com` (not `security.ubuntu.com`). Refused on Rocky Linux. |

### Security

| Key | Type | Default | Purpose |
|---|---|---|---|
| `modules.security` | map | | Baseline hardening beyond sshd. |
| `modules.security.enabled` | bool | `false`; `true` in `minimal` and `rocky` | Run the `security` module. |
| `modules.security.unattended_upgrades` | bool | `false`; `true` in `minimal` and `rocky` | Automatic security-only updates, never rebooting, NVIDIA and CUDA excluded. |
| `modules.security.fail2ban` | bool | `false`; `true` in `minimal` | sshd jail on the SSH port. Refused on Rocky Linux. |
| `modules.security.time_sync` | bool | `false`; `true` in `minimal` and `rocky` | NTP through the installed daemon, else systemd-timesyncd (chronyd on Rocky). |

### Docker and NVIDIA

| Key | Type | Default | Purpose |
|---|---|---|---|
| `modules.docker` | map | | Docker CE. |
| `modules.docker.enabled` | bool | `false`; `true` in `dgx`, `gpu-server` and `full` | Run the `docker` module. |
| `modules.docker.storage_dir` | path | unset (`/var/lib/docker`); `/raid/docker` in `dgx` | Docker data root, merged into `/etc/docker/daemon.json`; `ROOTFILES_DOCKER_ROOT` overrides it. |
| `modules.nvidia` | map | | NVIDIA container runtime. |
| `modules.nvidia.enabled` | bool | `false`; `true` in `dgx` and `gpu-server` | Run the `nvidia` module. |
| `modules.nvidia.persistenced` | bool | `false`; `true` in `dgx` and `gpu-server` | Enable `nvidia-persistenced` where installed. |
| `modules.nvidia.fabric_manager` | bool | `false`; `true` in `dgx` | Enable `nvidia-fabricmanager` where installed (NVSwitch systems). |
| `modules.nvidia.gpu_allocation` | map | | Per-user GPU allocation ([gpu.md](gpu.md)). |
| `modules.nvidia.gpu_allocation.enabled` | bool | `false`; `true` in `dgx` and `gpu-server` | Run the `gpu` module. |
| `modules.nvidia.gpu_allocation.method` | string | `env`; `both` in `dgx` | `env`, `cgroup` or `both`. |

### Cloudflare tunnel

| Key | Type | Default | Purpose |
|---|---|---|---|
| `modules.cloudflared` | map | | Cloudflare Tunnel ([tunnel.md](tunnel.md)). |
| `modules.cloudflared.enabled` | bool | `false`; `true` in `minimal` | Run the `cloudflared` module; without a token it installs only the binary. |
| `modules.cloudflared.tunnel_token` | string | unset | Inline token. Masked by `config show`, left out of the applied copy and the fingerprint, refused in fleet site configs. |
| `modules.cloudflared.tunnel_token_file` | path | unset | Root-owned 0600 file holding the token. Not together with `tunnel_token`. |
| `modules.cloudflared.private_network` | map | | Private network address routed by the tunnel. No built-in profile sets it. |
| `modules.cloudflared.private_network.enabled` | bool | `false` | Configure the address. Needs an address from the config, `ROOTFILES_VLAN_ADDRESS` or `--vlan-address`. |
| `modules.cloudflared.private_network.interface` | string | `vlan0` | Dummy interface name (15 characters at most); `ROOTFILES_VLAN_INTERFACE` overrides it. |
| `modules.cloudflared.private_network.address` | CIDR | unset | Address, such as `203.0.113.10/32`. |

### Storage and network

| Key | Type | Default | Purpose |
|---|---|---|---|
| `modules.storage` | map | | Data directories and symlinks. |
| `modules.storage.enabled` | bool | `false`; `true` in `dgx`, `gpu-server` and `full` | Run the `storage` module. |
| `modules.storage.data_dir` | path | unset; `/raid/data` in `dgx` | Data directory to create; `ROOTFILES_DATA_DIR` overrides it. |
| `modules.storage.symlinks` | map | `{}`; `/data: /raid/data` in `dgx` | Symlink path to target. A path that holds data blocks its link. |
| `modules.network` | map | | UFW. |
| `modules.network.enabled` | bool | `false`; `true` in `dgx` and `gpu-server` | Run the `network` module. |
| `modules.network.ufw` | bool | `false`; `true` in `dgx` and `gpu-server` | Install and enable UFW; the SSH port is always allowed. |
| `modules.network.allowed_ports` | list | `[]`; `80`, `443` in `dgx` | More TCP ports to allow. |

### Monitoring

| Key | Type | Default | Purpose |
|---|---|---|---|
| `modules.monitoring` | map | | Exporters and the hub ([monitoring.md](monitoring.md)). |
| `modules.monitoring.enabled` | bool | `false` | Run the `monitoring` module. |
| `modules.monitoring.node_exporter` | bool | `false` | Prometheus node exporter, with the textfile collector for scheduled reports. |
| `modules.monitoring.dcgm_exporter` | bool | `false` | DCGM GPU exporter container; needs Docker and the NVIDIA runtime. |
| `modules.monitoring.node_exporter_port` | int | `9100` | Node exporter port. |
| `modules.monitoring.dcgm_exporter_port` | int | `9400` | DCGM exporter port; must differ from the node exporter port. |
| `modules.monitoring.listen_address` | IP | unset (all addresses) | Address the exporters bind to. |
| `modules.monitoring.allow_from` | list | `[]` | CIDRs admitted to the exporter ports (the hub). |
| `modules.monitoring.perimeter_firewall` | bool | `false` | The segment is closed by a perimeter firewall; no host rules needed. |
| `modules.monitoring.exporter_firewall` | bool | `false` | iptables guard; needs an IPv4 `listen_address`, exactly one IPv4 `/32` in `allow_from`, the default ports and an enabled exporter. |
| `modules.monitoring.dcgm_exporter_image` | image | `nvcr.io/nvidia/k8s/dcgm-exporter:4.6.1-4.8.4-distroless` | DCGM exporter image; a tag or digest, never `:latest`. |

### Monitoring hub

| Key | Type | Default | Purpose |
|---|---|---|---|
| `modules.monitoring.hub` | map | | Prometheus, Alertmanager and Grafana on this host. |
| `modules.monitoring.hub.enabled` | bool | `false` | Run the hub. |
| `modules.monitoring.hub.data_dir` | path | `/data/monitoring` | Persistent data; never `/`. |
| `modules.monitoring.hub.retention` | duration | `30d` | Prometheus retention. |
| `modules.monitoring.hub.alertmanager_retention` | duration | unset (image default) | Alertmanager `--data.retention`; positive, no years. |
| `modules.monitoring.hub.targets_file` | path | `/etc/rootfiles/monitoring/discovery/targets.json` | Scrape targets written by `fleet targets --push`. |
| `modules.monitoring.hub.alert_receiver_file` | path | unset (a receiver that sends nothing) | Complete Alertmanager configuration, root-owned 0600. |
| `modules.monitoring.hub.listen_address` | IP | `127.0.0.1` | Address of the web ports. |
| `modules.monitoring.hub.grafana_port` | int | `3000` | Grafana port. |
| `modules.monitoring.hub.prometheus_port` | int | `9090` | Prometheus port. |
| `modules.monitoring.hub.alertmanager_port` | int | `9093` | Alertmanager port. |
| `modules.monitoring.hub.prometheus_image` | image | `prom/prometheus:v3.15.0` | Prometheus image. |
| `modules.monitoring.hub.alertmanager_image` | image | `prom/alertmanager:v0.34.1` | Alertmanager image. |
| `modules.monitoring.hub.grafana_image` | image | `grafana/grafana:13.2.3` | Grafana image. |
| `modules.monitoring.hub.grafana_admin_password_file` | path | `/etc/rootfiles/monitoring/grafana-admin-password` | Grafana admin password, generated when missing. |
| `modules.monitoring.hub.telegram_bot_token_file` | path | unset | Telegram bot token, mounted at `/run/secrets/telegram-bot-token`. |

### Tenant

| Key | Type | Default | Purpose |
|---|---|---|---|
| `modules.tenant` | map | | Host preparation for `rootfiles tenant` ([tenant.md](tenant.md)). |
| `modules.tenant.enabled` | bool | `false` | Run the `tenant` module. |
| `modules.tenant.root` | path | `/raid/tenants` | Volume images, mount points and system files. |
| `modules.tenant.ops_group` | string | `rootfiles-tenant-ops` | Group whose members may run `sudo rootfiles tenant ops`. |
| `modules.tenant.sshpiper` | map | | One SSH port for every tenant. |
| `modules.tenant.sshpiper.enabled` | bool | `false` | Run sshpiper. |
| `modules.tenant.sshpiper.port` | int | `2222` | Listen port; never the host's SSH port. |
| `modules.tenant.sshpiper.address` | IP | unset (all addresses) | Listen address. |
| `modules.tenant.sshpiper.version` | string | `v1.6.1` | sshpiper release; the archive checksum is verified. |
| `modules.tenant.sshpiper.failtoban_ignore` | list | `[]` | Addresses or CIDRs never banned after failed logins, such as a shared VPN gateway. |
