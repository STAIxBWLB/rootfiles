# Commands

Every `rootfiles` command and flag, with exit codes, output formats and environment variables. Topic guides explain the workflows behind them; this file is the reference. The command runtime contract (root and lock preflight, flag readers, audit log) is in [docs/specs/cli-runtime.md](specs/cli-runtime.md).

`root` is installed as a symlink to `rootfiles`, so `sudo root apply` works the same as `sudo rootfiles apply`.

## Conventions

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. For report commands: nothing to report (all satisfied, no failures, every host `ok`, already current). |
| 1 | Error: invalid input, a failed load or a failed run. The error is printed as `Error: ...` on stderr. |
| 2 | Report result: pending changes (`check`), failed findings (`doctor`), drift (`user audit`), an available update (`update --check`, `tunnel update --check`), or a fleet host that is not `ok`. |

Commands not listed with exit 2 below use only 0 and 1.

### Output formats

- `-o/--output` selects a report format: `text` (default) or `json`; `check` and `doctor` also take `prometheus` (text exposition for the node exporter textfile collector).
- Text output is coloured on a terminal. Colour is dropped when stdout is not a TTY or `NO_COLOR` is set.
- Fleet commands accept only `text` and `json`, and refuse anything else before connecting.

### Root flags and their readers

The flags of `### rootfiles` below are accepted on every command, but each command reads only some of them. A command refuses `--profile`, `--config`, `--home-base`, `--user`, `--ssh-pubkey`, `--tunnel-token` or `--vlan-address` when it does not read it, instead of ignoring it.

| Readers | Root flags they read |
|---|---|
| `apply`, `check`, `config show`, `config validate` | `--profile`, `--config`, `--home-base`, `--user`, `--ssh-pubkey`, `--tunnel-token`, `--vlan-address` |
| `backup`, `doctor`, `status`, `gpu ...`, `schedule enable`, `schedule disable`, `tenant ...` (not `tenant ops`), `tunnel ...`, `user ...` | `--profile`, `--config`, `--home-base` |
| `tunnel setup` | also `--tunnel-token`, `--vlan-address` |
| `user add` | also `--user`, `--ssh-pubkey` |
| `config init`, `rollback`, `schedule status`, `update` | none of them |

`--yes` and `--dry-run` are read by every command that prompts or changes something. `fleet` commands refuse the native-host flags with their own message, and `tenant ops` refuses every flag.

Command-local flags such as `--module` and `--force` go after the subcommand: `rootfiles apply --module ssh`, not `rootfiles --module ssh apply`.

### Root and the lock

Commands that change the host need root on Linux and take the global lock (`/run/rootfiles.lock`). With `--dry-run`, and with the read-only `update --check` and `tunnel update --check`, they need neither. A second mutating command fails at once and names the PID holding the lock. Each real mutating run writes an audit record ([operations.md](operations.md#lock-and-audit-log)).

### Deprecated flags and aliases

| Old | Use instead | Notes |
|---|---|---|
| `backup -o/--output DIR` | `backup --dir DIR` | Hidden, prints a deprecation notice. |
| `user backup -o/--output FILE` | `user backup --file FILE` | Hidden, prints a deprecation notice. |
| `user add --pubkey KEY` | `user add --ssh-pubkey KEY` | Hidden; `--ssh-pubkey` wins when both are set. |
| `user passwd --suffix S` | `user passwd --password P` or `--file F` | Hidden; predictable passwords are removed in the next minor release. |
| `config init --force` | `config init --overwrite` | Hidden. |
| `rootfiles upgrade` | `rootfiles update` | Command alias. |
| `rootfiles tunnel install` | `rootfiles tunnel setup` | Command alias. |
| `rootfiles tenant reset` | `rootfiles tenant passwd` | Command alias. |

## Environment variables

Only these variables are read. Flags win over the environment.

| Variable | Read by | Effect | Default |
|---|---|---|---|
| `ROOTFILES_PROFILE` | `apply`, `check`, `status`, `config show`, `config validate`, `doctor`, and the subcommands that load a config | Profile name, as `--profile` | `apply`: detected suggestion (prompted without `--yes`); reports: the last applied target, else the suggestion; subcommands: the kept copy of the last apply, else `minimal` ([configuration.md](configuration.md#which-config-a-command-uses)) |
| `ROOTFILES_YES` | every native command that prompts | `true` answers every prompt, as `--yes`. Fleet rollouts still need an explicit `--yes`. | unset |
| `ROOTFILES_HOME_BASE` | config loading | Sets `users.home_base` | detected ([users.md](users.md#home-base)) |
| `ROOTFILES_TIMEZONE` | config loading | Sets `timezone` | from the profile |
| `ROOTFILES_TUNNEL_TOKEN` | config loading, `tunnel setup` | Sets `modules.cloudflared.tunnel_token` | unset |
| `ROOTFILES_VLAN_ADDRESS` | config loading, `tunnel setup` | Sets `modules.cloudflared.private_network.address` | unset |
| `ROOTFILES_VLAN_INTERFACE` | config loading | Sets `modules.cloudflared.private_network.interface` | `vlan0` |
| `ROOTFILES_DOCKER_ROOT` | config loading | Sets `modules.docker.storage_dir` | from the profile |
| `ROOTFILES_DATA_DIR` | config loading | Sets `modules.storage.data_dir` | from the profile |
| `ROOTFILES_USER` | `apply`, `check`, `config show`, `config validate`, `user add` | Account to declare or create, as `--user` | unset |
| `ROOTFILES_SSH_PUBKEY` | same as `ROOTFILES_USER` | Key for that account, as `--ssh-pubkey` | unset |
| `ROOTFILES_STATE_DIR` | every command | State, history, applied-config copy, file backups, scheduled reports | `/var/lib/rootfiles` |
| `ROOTFILES_LOG_FILE` | mutating commands | JSON audit log | `/var/log/rootfiles.log` |
| `ROOTFILES_LOCK_FILE` | mutating commands | Global lock file | `/run/rootfiles.lock` |
| `NO_COLOR` | text reports | Disables colour | unset |
| `XDG_CONFIG_HOME` | `fleet` | Default inventory `$XDG_CONFIG_HOME/rootfiles/fleet.yaml` | `~/.config` |
| `XDG_STATE_HOME` | `fleet` | Run log `$XDG_STATE_HOME/rootfiles/fleet.jsonl` | `~/.local/state` |

The config-loading variables apply wherever a config is loaded, after the file and before the CLI flags. The fleet controller does not pass its own `ROOTFILES_*` variables into the site configs it resolves. `tenant ops` clears every `ROOTFILES_*` variable before it runs.

## Command reference

### rootfiles

Root command. Run `rootfiles help <command>` or `rootfiles <command> --help` for the built-in help.

| Flag | Default | Purpose |
|---|---|---|
| `--yes` | `false` | Unattended mode: answer every prompt with its default. `ROOTFILES_YES=true` does the same. |
| `--dry-run` | `false` | Show what would change without changing anything; needs neither root nor the lock. |
| `--profile` | unset | Built-in profile: `base`, `minimal`, `dgx`, `gpu-server`, `full`, `rocky`. |
| `--config` | unset | Site config file, or `-` to read a resolved config from stdin. |
| `--home-base` | unset | Home base directory, such as `/raid/home`; wins over the config and `ROOTFILES_HOME_BASE`. |
| `--user` | unset | Account to declare (`apply`, `check`, config inspection) or create (`user add`). |
| `--ssh-pubkey` | unset | SSH public key for `--user`. |
| `--tunnel-token` | unset | Cloudflare tunnel token. Redacted in the audit log. |
| `--vlan-address` | unset | Private network address of the tunnel, a CIDR such as `203.0.113.10/32`. |
| `--version` | | Print the version and commit. |

See [Root flags and their readers](#root-flags-and-their-readers) for which command reads which flag.

### rootfiles apply

Converge this host to a profile or site config through the enabled modules ([modules.md](modules.md)). Mutating.

```bash
sudo rootfiles apply --profile dgx --dry-run
sudo rootfiles apply --config /etc/rootfiles/site.yaml --yes
sudo rootfiles apply --module cloudflared,docker
```

| Flag | Default | Purpose |
|---|---|---|
| `--module` | all enabled | Comma-separated module names to run; others are skipped. A module that is not enabled in the config does not run even when named. |
| `--force` | `false` | Override the SSH key check of the lockout guard. It does not override the unknown-firewall-state refusal. |

- Without `--profile` or `--config`, `apply` uses `ROOTFILES_PROFILE`, else the detected suggestion; without `--yes` it asks for the profile and then shows each setting for review.
- `--user` and `--ssh-pubkey` declare an account that is created before SSH is hardened. `--tunnel-token` and `--vlan-address` override the tunnel settings.
- A failing module is reported and the rest still run. A real run records state and history and keeps the resolved config ([operations.md](operations.md#files-on-the-host)).
- Exit codes: 0 when every module succeeded (or the prompt was aborted), 1 otherwise.

### rootfiles backup

Capture what is needed to rebuild this host after an OS reinstall ([operations.md](operations.md#system-backup)). Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--dir` | `<state dir>/system-backup` (`/var/lib/rootfiles/system-backup`) | Directory that receives the `rootfiles-backup-<hostname>-<YYYYMMDD>/` archive directory. |
| `--skip-docker` | `false` | Leave out the Docker image list. |
| `--skip-etc` | `false` | Leave out the `/etc` archive. |

`-o/--output` is a deprecated alias of `--dir`.

### rootfiles check

Report which modules have pending changes. Read-only: every module runs `Check` only.

| Flag | Default | Purpose |
|---|---|---|
| `--module` | all enabled | Check only these modules. |
| `-o`, `--output` | `text` | `text`, `json` or `prometheus`. |
| `-v`, `--verbose` | `false` | Show the command behind each pending change. |

- Target: `--profile`, `--config` or `ROOTFILES_PROFILE`, else the last applied target, else the detected suggestion ([configuration.md](configuration.md#which-config-a-command-uses)).
- JSON: `version`, `profile`, `config_path`, `applied_config_sha256`, `satisfied`, and `modules[]` with `name`, `satisfied` and `changes[]` (`description`, `command`, `state`; `state` is `blocked` when the operator must act first).
- Prometheus: `rootfiles_module_satisfied{module}`, `rootfiles_module_pending_changes{module}`, `rootfiles_check_timestamp_seconds`.
- Exit codes: 0 all satisfied, 2 pending changes, 1 error.

### rootfiles config

Inspect, validate and scaffold configuration ([configuration.md](configuration.md)).

### rootfiles config init

Write a site config: a commented template that extends a profile, or a capture of this host.

| Flag | Default | Purpose |
|---|---|---|
| `--extends` | detected suggestion | Profile the template extends. |
| `--file` | stdout | Write to this file instead of stdout. A new file is created with mode 0600; an existing file overwritten with `--overwrite` keeps its current mode and owner, so restrict it (`chmod 600`) first, because a `--from-system` capture can hold the tunnel token (#119). |
| `--from-system` | `false` | Capture the running host's settings instead of the template; works on hosts not set up by rootfiles. |
| `--overwrite` | `false` | Replace an existing `--file`. |

`--force` is a deprecated alias of `--overwrite`.

### rootfiles config show

Print the fully merged config that `check` would use, with `extends`, environment and flag overrides applied. The first line names the source.

| Flag | Default | Purpose |
|---|---|---|
| `--reveal` | `false` | Print the inline tunnel token instead of masking it. |

### rootfiles config validate

Load and validate the same target as `config show`: unknown keys, paths, ports, CIDRs, names and, on Rocky Linux, unsupported modules. Prints `<source> is valid`; exit 1 with every problem listed otherwise.

### rootfiles doctor

Read-only health checks of the live host, as opposed to `check`, which compares against the config. Checks: SSH access (keys against `sshd -T`, password-only and no-login accounts, the effective config), firewall state and the SSH port, exporter exposure and the exporter firewall guard, reboot required, disk use, time sync, GPU driver and persistence mode, failed systemd units, the config and its capabilities, and the last apply.

| Flag | Default | Purpose |
|---|---|---|
| `-o`, `--output` | `text` | `text`, `json` or `prometheus`. |
| `--strict` | `false` | Count warnings as failures for the exit code. |

- JSON: `findings[]` (`check`, `level` of `ok`, `warn`, `fail` or `skip`, `detail`, `hint`), `failed`, `applied_config_sha256`, `version`.
- Prometheus: `rootfiles_doctor_findings{level}` and `rootfiles_doctor_check_ok{check}`.
- Disk thresholds come from `doctor.disk` ([configuration.md](configuration.md#doctor)).
- Exit codes: 0 no failures, 2 at least one failure (or warning with `--strict`), 1 error.

### rootfiles fleet

Operator-side SSH controller ([fleet.md](fleet.md)). It runs as the operator, takes no host lock and refuses native-host flags (`--module`, `--profile`, `--config`, `--force`, `--home-base`, `--user`, `--ssh-pubkey`, `--tunnel-token`, `--vlan-address`) before connecting.

Flags shared by every fleet subcommand:

| Flag | Default | Purpose |
|---|---|---|
| `--inventory` | `$XDG_CONFIG_HOME/rootfiles/fleet.yaml`, else `~/.config/rootfiles/fleet.yaml` | Inventory file. |
| `--host` | none | Host names (comma-separated or repeated). |
| `--group` | none | Group names; the selection is the union with `--host`. |
| `--all` | `false` | Every inventory host; cannot be combined with `--host` or `--group`. |

Rollouts (`apply`, `update`, `bootstrap`, `schedule`) also take:

| Flag | Default | Purpose |
|---|---|---|
| `--timeout` | `15m` | Limit for each host's mutating SSH command; positive, up to 24h. |
| `--skip-unreachable` | `false` | Continue past hosts whose SSH connection fails; other failures still stop the run. |

- Rollouts need a selection and `--yes`; a `--dry-run` preview needs no `--yes`. They run one host at a time and stop at the first failure.
- Exit codes: 0 when every host is `ok`, 2 when any host is not, 1 for controller or inventory errors.

### rootfiles fleet apply

Apply each selected host's site config, one host at a time.

| Flag | Default | Purpose |
|---|---|---|
| `--inventory`, `--host`, `--group`, `--all` | | Selection, see [rootfiles fleet](#rootfiles-fleet). |
| `--timeout`, `--skip-unreachable` | `15m`, `false` | See [rootfiles fleet](#rootfiles-fleet). |
| `-o`, `--output` | `text` | `text` or `json`. |

### rootfiles fleet bootstrap

Install a pinned, checksum-verified release on selected hosts where rootfiles is missing.

| Flag | Default | Purpose |
|---|---|---|
| `--version` | required | Release to install, `vX.Y.Z`. |
| `--inventory`, `--host`, `--group`, `--all` | | Selection. |
| `--timeout`, `--skip-unreachable` | `15m`, `false` | Rollout limits. |
| `-o`, `--output` | `text` | `text` or `json`. |

### rootfiles fleet check

Run `rootfiles check -o json` on the selected hosts in parallel and summarise the host states.

| Flag | Default | Purpose |
|---|---|---|
| `--inventory`, `--host`, `--group`, `--all` | | Selection; required. |
| `--parallel` | `defaults.parallel` of the inventory (2) | Concurrent SSH processes, 1-128. |
| `-o`, `--output` | `text` | `text` or `json` (`hosts[]` with `host`, `command`, `state`, `reason`, `remote`, `exit_code`). |

### rootfiles fleet doctor

Run `rootfiles doctor -o json` on the selected hosts in parallel. Flags as [rootfiles fleet check](#rootfiles-fleet-check): `--inventory`, `--host`, `--group`, `--all`, `--parallel`, `-o`/`--output`.

### rootfiles fleet schedule

`rootfiles fleet schedule enable|disable` runs `rootfiles schedule enable` or `disable` on each selected host.

| Flag | Default | Purpose |
|---|---|---|
| `--on-calendar` | remote default (`daily`) | OnCalendar expression for `enable`; refused with `disable`. |
| `--inventory`, `--host`, `--group`, `--all` | | Selection. |
| `--timeout`, `--skip-unreachable` | `15m`, `false` | Rollout limits. |
| `-o`, `--output` | `text` | `text` or `json`. |

### rootfiles fleet status

Run `rootfiles status -o json` on the selected hosts in parallel. Flags as [rootfiles fleet check](#rootfiles-fleet-check): `--inventory`, `--host`, `--group`, `--all`, `--parallel`, `-o`/`--output`.

### rootfiles fleet targets

Print Prometheus `file_sd` scrape targets as JSON, or push them to the monitoring hub.

| Flag | Default | Purpose |
|---|---|---|
| `--inventory` | see above | Inventory file. |
| `--host`, `--group`, `--all` | whole inventory | Narrow the targets to these hosts. |
| `--push` | unset | Hub host to receive the targets file; needs `--yes`, or `--dry-run` to print the path only. |
| `--timeout` | `15m` | Limit for the push. |

Exit codes: 0 printed or pushed, 2 when the push failed on the hub, 1 on errors.

### rootfiles fleet update

Update the selected hosts to a pinned release, then run a read-only `check` on each. A host that now plans changes is reported `apply-pending`.

| Flag | Default | Purpose |
|---|---|---|
| `--version` | required | Release to install, `vX.Y.Z`. |
| `--inventory`, `--host`, `--group`, `--all` | | Selection. |
| `--timeout`, `--skip-unreachable` | `15m`, `false` | Rollout limits. |
| `-o`, `--output` | `text` | `text` or `json`. |

### rootfiles gpu

Per-user GPU allocation ([gpu.md](gpu.md)). Reads `--profile`, `--config` and `--home-base` to find the ledger.

### rootfiles gpu assign

`sudo rootfiles gpu assign USER --gpus 0,1`. Mutating. Assigning a user again replaces the allocation.

| Flag | Default | Purpose |
|---|---|---|
| `--gpus` | required | Comma-separated GPU indices. |
| `--method` | `modules.nvidia.gpu_allocation.method` (`env` when unset) | `env`, `cgroup` or `both`. |

### rootfiles gpu list

List the allocations in the ledger.

### rootfiles gpu mig

Multi-Instance GPU commands (read-only).

### rootfiles gpu mig status

Show the current and pending MIG mode and the instances of each GPU.

### rootfiles gpu revoke

`sudo rootfiles gpu revoke USER`. Mutating. Refuses a tenant's entry; use `tenant gpu`.

### rootfiles gpu status

Show each GPU with its owner.

### rootfiles rollback

`rootfiles rollback [BACKUP_ID]`. Without an id, list backup sessions. With an id, restore the files that run saved and remove the files it created; `--dry-run` shows the plan, `--yes` skips the confirmation. Restoring needs root and the lock. Packages, accounts and services are not reverted ([operations.md](operations.md#file-backups-and-rollback)).

### rootfiles schedule

Systemd timer that writes `check` and `doctor` reports into the state directory ([operations.md](operations.md#scheduled-reports)). It changes nothing else.

### rootfiles schedule disable

Stop the timer and remove `rootfiles-check.timer` and `rootfiles-check.service`. Mutating.

### rootfiles schedule enable

Install and start the timer; when no report exists yet, also run the first report. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--on-calendar` | `daily` | systemd OnCalendar expression, such as `hourly` or `*-*-* 06:00`. Line breaks, NUL and apostrophes are refused. |

### rootfiles schedule status

Show whether the units are installed, the timer's enabled and active state, its OnCalendar expression and the age of `check.json` and `doctor.json`. A report older than 48 hours is marked stale. Read-only; exit 0.

### rootfiles status

Read-only overview: system, profile, last apply, module satisfaction, GPU allocations, tunnel and users.

| Flag | Default | Purpose |
|---|---|---|
| `--module` | all enabled | Report only these modules; also limits the GPU (`gpu`), tunnel (`cloudflared`) and users (`users`) sections. |
| `-o`, `--output` | `text` | `text` or `json`. |

Exit code 0 for the overview, including reported findings; 1 for invalid input. When the config does not load, `status` reports the error and skips the user and GPU databases unless an absolute home base is known.

### rootfiles tenant

Long-lived tenant containers ([tenant.md](tenant.md)). Every subcommand except `ops` reads `--profile`, `--config` and `--home-base` to find the tenant specs. Not supported on Rocky Linux.

### rootfiles tenant apply

`rootfiles tenant apply [TENANT]`. Converge a tenant to its spec; recreates the container when the image, GPUs or ports changed. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--all` | `false` | Apply every tenant. |

### rootfiles tenant create

`rootfiles tenant create TENANT`. Write the spec, create the volume, start the container and print a temporary password per account. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--image` | required | Container image; it needs `openssh-server`. |
| `--ssh-port` | required | Host port published to the container's sshd. |
| `--size` | required | Volume size, such as `500G` or `2T`. |
| `--fs` | `ext4` | Volume filesystem: `ext4` (can shrink offline) or `xfs`. |
| `--gpus` | none | Comma-separated GPU indices. |
| `--account` | none | `NAME[:UID[:GID]]`, repeatable; UIDs default to 1000, 1001, ... |
| `--sudo` | `false` | Give the accounts sudo inside the container. |
| `--operator` | none | Host account allowed `sudo rootfiles tenant ops` on this tenant (repeatable). |
| `--port` | none | Extra published port `HOST:CONTAINER[/udp]` (repeatable). |
| `--bind` | none | Extra bind mount `SRC:DST[:ro\|rw]` outside the volume quota (repeatable). |

### rootfiles tenant gpu

`rootfiles tenant gpu TENANT --gpus 0,1`. Change a tenant's GPUs and recreate the container. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--gpus` | required | Comma-separated GPU indices, or `none`. |

### rootfiles tenant image

`rootfiles tenant image TENANT IMAGE`. Change the image and recreate the container. Mutating.

### rootfiles tenant import

`rootfiles tenant import TENANT --from CONTAINER --size SIZE`. Move a hand-made container under rootfiles; the old container is kept stopped. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--from` | required | Running container to import. |
| `--size` | required | Size of the new volume. |
| `--fs` | `ext4` | Volume filesystem: `ext4` or `xfs`. |
| `--data` | unset | Host directory copied into the new `/data`. |
| `--keep` | none | Path under `/home` or `/tmp` in the old container to copy (repeatable). |
| `--operator` | none | Host account allowed `sudo rootfiles tenant ops` (repeatable). |

### rootfiles tenant list

Print the tenant names.

### rootfiles tenant logs

`rootfiles tenant logs TENANT`. Show the container log.

| Flag | Default | Purpose |
|---|---|---|
| `--tail` | `200` | Lines to show. |

### rootfiles tenant ops

`sudo rootfiles tenant ops TENANT ACTION [USER]` with `ACTION` one of `status`, `logs`, `gpu-heal`, `restart` or `reset USER`. For members of the tenant operator group listed in the spec's `operators`. It takes no flags, ignores `ROOTFILES_*` variables, reads only the applied host config, and audits every attempt, refused ones included.

### rootfiles tenant passwd

`rootfiles tenant passwd TENANT USER`. Issue a temporary password that must be changed at first login. Alias: `tenant reset`. Mutating.

### rootfiles tenant port

`rootfiles tenant port TENANT SSH_PORT`. Change the SSH host port and recreate the container. Mutating.

### rootfiles tenant restart

`rootfiles tenant restart TENANT`. Restart the container; it is kept. Mutating.

### rootfiles tenant status

`rootfiles tenant status [TENANT]`. Show state, spec drift and volume use; with a name also the writable-layer size.

### rootfiles tenant sync

Save the password hashes and refresh the sshpiper keys of every tenant. Run by a 2-minute timer; needs root but not the global lock.

### rootfiles tenant user

Tenant accounts.

### rootfiles tenant user add

`rootfiles tenant user add TENANT NAME`. Add an account to a running tenant and print its temporary password. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--uid` | next free from 1000 | UID. |
| `--gid` | the UID | GID. |
| `--sudo` | `false` | Give the account sudo inside the container. |

### rootfiles tenant volume

Tenant volumes.

### rootfiles tenant volume resize

`rootfiles tenant volume resize TENANT SIZE`. Grow a volume online, or shrink an ext4 volume. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--offline` | `false` | Stop the container to shrink an ext4 volume. |

### rootfiles tunnel

Cloudflare tunnel and its private network address ([tunnel.md](tunnel.md)). Not supported on Rocky Linux for changes.

### rootfiles tunnel restart

Restart `cloudflared.service`. Mutating.

### rootfiles tunnel setup

`sudo rootfiles tunnel setup [TOKEN]`. Install the cloudflared binary, the tunnel service and the private network address. Alias: `tunnel install`. Mutating.

- Token: the argument, else `--tunnel-token`, else `ROOTFILES_TUNNEL_TOKEN`, else the config (`tunnel_token` or `tunnel_token_file`). Without a token only the binary and a configured address are managed.
- Address: `--vlan-address`, else `ROOTFILES_VLAN_ADDRESS`, which also turns the private network on for this run; else the config.
- The token argument is redacted in the audit log.

### rootfiles tunnel status

Show the tunnel service and private network state. Read-only.

### rootfiles tunnel uninstall

Remove the tunnel service, the private network interface and the binary. Mutating.

### rootfiles tunnel update

Replace the cloudflared binary with the latest upstream release, or a pinned one, and restart the service when it exists. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--check` | `false` | Report the installed and latest versions only; needs neither root nor the lock. |
| `--version` | latest | Pin a cloudflared release, such as `2024.9.1`. |

Exit codes with `--check`: 0 current, 2 an update is available or cloudflared is missing, 1 lookup error.

### rootfiles update

Replace this binary with the latest GitHub release, or a pinned one, after verifying its SHA-256 checksum; then run `check` and print an `Apply pending` hint when the new release plans changes. Linux only. Alias: `rootfiles upgrade`. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--check` | `false` | Report whether an update is available; needs neither root nor the lock. |
| `--version` | latest | Release to install, such as `v0.9.0`. |

`--dry-run` downloads and verifies the archive without replacing the binary. Exit codes with `--check`: 0 current, 2 an update is available, 1 lookup error.

### rootfiles user

Accounts and the home base ([users.md](users.md)). Every subcommand reads `--profile`, `--config` and `--home-base`.

### rootfiles user add

`sudo rootfiles user add [USERNAME]`. Create an account under the home base. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--groups` | none | Additional groups. |
| `--no-docker` | `false` | Do not add the account to the `docker` group. |

- The name comes from the argument, `--user` or `ROOTFILES_USER`; the key from `--ssh-pubkey` or `ROOTFILES_SSH_PUBKEY`. Without a name and without `--yes`, it prompts.
- `--pubkey` is a deprecated alias of `--ssh-pubkey`.

### rootfiles user audit

Compare `users.json` with the accounts on the host. Exit 2 on drift.

### rootfiles user backup

Write the users database, merged with the accounts found on the host, to a JSON file.

| Flag | Default | Purpose |
|---|---|---|
| `--file` | `rootfiles-users-<hostname>-<YYYYMMDD>.json` in the current directory | Output file. |

`-o/--output` is a deprecated alias of `--file`.

### rootfiles user del

`sudo rootfiles user del USERNAME`. Remove the account, its sudoers drop-in, GPU allocation and database entry. The home is kept by default. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--archive` | `false` | Archive the home to `<home_base>/.rootfiles/archive`, then remove it. |
| `--remove-home` | `false` | Remove the home without an archive. |

### rootfiles user du

Show home directory usage per user, largest first.

### rootfiles user expire

`sudo rootfiles user expire USERNAME YYYY-MM-DD|never`. Set or clear the account expiry date. Mutating.

### rootfiles user group-add

`sudo rootfiles user group-add USERNAME`. Add the account to groups. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--groups` | none | Groups to add. |
| `--docker` | `false` | Include the `docker` group. |
| `--sudo` | `false` | Include the `sudo` group. |

### rootfiles user group-del

`sudo rootfiles user group-del USERNAME`. Remove the account from groups. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--groups` | none | Groups to remove. |
| `--docker` | `false` | Include the `docker` group. |
| `--sudo` | `false` | Include the `sudo` group. |

### rootfiles user groups

`rootfiles user groups [USERNAME]`. List all groups, or one user's groups.

### rootfiles user id

`rootfiles user id USERNAME`. Show UID, GID and groups.

### rootfiles user key

A user's SSH authorized keys.

### rootfiles user key add

`sudo rootfiles user key add USERNAME 'ssh-ed25519 AAAA... comment'`. Mutating.

### rootfiles user key list

`rootfiles user key list USERNAME`. List the authorized keys, numbered.

### rootfiles user key rm

`sudo rootfiles user key rm USERNAME INDEX|KEY|COMMENT`. Remove keys by list index, key or comment. Mutating.

### rootfiles user list

List managed users.

| Flag | Default | Purpose |
|---|---|---|
| `-s`, `--system` | `false` | List all accounts with UID 1000-65533 instead. |
| `--names` | `false` | Print names only, one per line. |

### rootfiles user lock

`sudo rootfiles user lock USERNAME`. Block password and SSH key logins. Mutating.

### rootfiles user passwd

`sudo rootfiles user passwd [USERNAME...]`. Set passwords in batch. By default each user gets a random password, printed once. Passwords reach `chpasswd` on stdin. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--password` | unset | One password for every target user. Redacted in the audit log. |
| `-f`, `--file` | unset | File with a username, or `username,password`, per line. |
| `--all` | `false` | Every system user. |
| `--expire` | `false` | Force a password change at the next login (`chage -d 0`). |

`--suffix` (password = username + suffix) is deprecated and will be removed in the next minor release.

### rootfiles user quota

Per-user hard limits on the home base filesystem: XFS project quota or ext4 user quota.

### rootfiles user quota rm

`sudo rootfiles user quota rm USERNAME`. Remove a user's limit. Mutating.

### rootfiles user quota set

`sudo rootfiles user quota set USERNAME SIZE`, such as `500G`. The limit is recorded and applied again by `user restore`. Mutating.

### rootfiles user quota show

Show the quota report.

### rootfiles user rehome

`sudo rootfiles user rehome USERNAME`. Copy the home to `<home_base>/<user>`, verify the copy, switch the account and leave a symlink at the old path. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--remove-old` | `false` | Delete the original home after the verified copy instead of keeping `<old>.rootfiles-bak-<timestamp>`. |

### rootfiles user restore

`sudo rootfiles user restore [BACKUP_FILE]`. Recreate accounts from a `user backup` file, or from `<home_base>/.rootfiles/users.json`, reconnecting surviving homes. Mutating.

### rootfiles user unlock

`sudo rootfiles user unlock USERNAME`. Re-enable logins. Mutating.
