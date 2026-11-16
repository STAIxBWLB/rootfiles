# Commands

Every `rootfiles` command and flag, with exit codes, output formats and environment variables. Topic guides explain the workflows behind them; this file is the reference. The command runtime contract (root and lock preflight, flag readers, audit log) is in [docs/specs/cli-runtime.md](specs/cli-runtime.md).

`root` is installed as a symlink to `rootfiles`, so `sudo root apply` works the same as `sudo rootfiles apply`.

## Conventions

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. For report commands: nothing to report (all satisfied, no failures, every host `ok`, already current). |
| 1 | Operational or validation error, including missing required flags. The error is printed once as `rootfiles: ...` on stderr. Argument/flag parsing errors use 2 instead. |
| 2 | Argument/flag parsing error (with usage and a help hint), or report result: pending changes (`check`), failed findings (`doctor`), drift (`user audit`), an available update (`update --check`, `tunnel update --check`), or a fleet host that is not `ok`. |

Cobra argument/flag parsing errors exit 2. Missing required flags retain exit 1. The report-result uses of 2 below remain separately documented.

Operational errors with a known recovery step print the cause and next step on separate stderr lines, for example with `--ascii`:

```text
rootfiles: rootfiles apply: changes the system and must run as root
  -> sudo rootfiles 'apply' '--ascii'
```

On Linux, the same arguments are shell-quoted in a `sudo` retry. If they contain a password, password suffix or token, or any `ROOTFILES_*` environment variable is set, the next step shows the command's `--help` instead. This avoids a sudo retry that loses its environment-selected target. On other systems, a non-root native mutation still reports the root requirement first, but its next step is `rootfiles fleet --help` (`requireRootAndLock`). A held lock suggests `ps -p` for its holder, never deleting the lock file. Fatal config loads outside `config show/validate` suggest `rootfiles config validate` with the selected config or profile; unknown profiles list the available names and point to `rootfiles --help`. Except for unknown profiles (which retain root help), when `--home-base` or `--vlan-address` was set, a fatal load has no next step: the bare validation command would omit the override. These errors exit 1 and leave JSON stdout empty. Without `--ascii`, UTF-8 locales use `→` for the next step.

### Output formats

- `--json` selects JSON on report commands; `-o/--output` selects a report format: `text` (default) or `json`; `check` and `doctor` also take `prometheus` (text exposition for the node exporter textfile collector).
- Text output resolves color separately for stdout and stderr (`ui.ResolveColor`); JSON and raw payloads bypass decoration. See [environment variables](#environment-variables) for color, width and locale precedence. `--ascii` forces ASCII markers and relation separators.
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
| `user passwd --suffix S` | `user passwd --password P` or `--file F` | Hidden; predictable passwords are removed in v0.21.0. |
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
| `NO_COLOR` | human output | Nonempty disables color unless `--color=always` overrides it | unset |
| `FORCE_COLOR` | human output | Nonempty other than `0` requests color; `0` requests no color, subject to the precedence below | unset |
| `CLICOLOR_FORCE` | human output | Nonempty other than `0` requests color; `0` does not force color off | unset |
| `CLICOLOR` | human output | `0` requests no color after force-color requests are checked; other values do not force color on | unset |
| `TERM` | human output | `dumb` disables automatic TTY color when no earlier color rule applies | inherited |
| `COLUMNS` | human report layout | Positive fallback width; a readable TTY's actual width wins (`ui.OutputWidth`). On a pipe, unset or invalid means no wrapping limit | unset |
| `LC_ALL` | human output | First locale considered for automatic ASCII markers and relation separators | inherited |
| `LC_CTYPE` | human output | Locale considered only when `LC_ALL` is empty | inherited |
| `LANG` | human output | Locale considered only when both `LC_ALL` and `LC_CTYPE` are empty | inherited |
| `XDG_CONFIG_HOME` | `fleet` | Default inventory `$XDG_CONFIG_HOME/rootfiles/fleet.yaml` | `~/.config` |
| `XDG_STATE_HOME` | `fleet` | Run log `$XDG_STATE_HOME/rootfiles/fleet.jsonl` | `~/.local/state` |

For each stream, `ui.ResolveColor` uses the first matching rule: `--color=never` / `always` disables / enables color; nonempty `NO_COLOR` disables it; nonempty, nonzero `FORCE_COLOR` or `CLICOLOR_FORCE` enables it; `FORCE_COLOR=0` or `CLICOLOR=0` disables it; otherwise a TTY enables color unless `TERM=dumb`, and a pipe stays plain. `--color=auto` uses the environment and TTY rules. JSON and raw payloads stay undecorated regardless.

`ui.ASCIIMarkers` uses `--ascii` first, then the first nonempty value of `LC_ALL`, `LC_CTYPE`, `LANG`. That locale must contain `UTF-8` or `UTF8` (case-insensitive) to use Unicode; another locale or no locale selects ASCII. `ui.OutputWidth` uses the actual TTY width when available, then a positive `COLUMNS`. `ui.WriteTable` still caps fixed columns and can exceed a narrow requested width to retain its flexible-column minimum.

The config-loading variables apply wherever a config is loaded, after the file and before the CLI flags. The fleet controller does not pass its own `ROOTFILES_*` variables into the site configs it resolves. `tenant ops` clears every `ROOTFILES_*` variable before it runs.

## Command reference

Each section opens with the summary `rootfiles <command> --help` shows for that command; `TestDocsCommandsCoverCobraTree` fails when that summary, a flag or a command in this reference no longer matches the help.

### rootfiles

Bootstrap Ubuntu, DGX OS and Rocky servers.
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
| `--color` | `auto` | Color policy: `auto`, `always` or `never`, resolved separately for stdout and stderr. |
| `--ascii` | `false` | Use ASCII markers (also automatic for a non-UTF-8 locale). |
| `--verbose` | `false` | Enable INFO diagnostics except on `check`, whose local verbose flag shadows it in every position. |
| `-v`, `--version` | | Print the version and commit. |

Unknown commands exit 2 with Cobra suggestions (for example, `rootfiles stauts` suggests `status`); bare parent commands print help. Usage classification follows flag/argument parsing, not diagnostic message words.

See [Root flags and their readers](#root-flags-and-their-readers) for which command reads which flag.

### rootfiles apply

Apply server configuration.
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

Back up the system for an OS upgrade or restore.
Capture what is needed to rebuild this host after an OS reinstall ([operations.md](operations.md#system-backup)). Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--dir` | `<state dir>/system-backup` (`/var/lib/rootfiles/system-backup`) | Directory that receives the `rootfiles-backup-<hostname>-<YYYYMMDD>/` archive directory. |
| `--skip-docker` | `false` | Leave out the Docker image list. |
| `--skip-etc` | `false` | Leave out the `/etc` archive. |

`-o/--output` is a deprecated alias of `--dir`.

### rootfiles check

Check current system state against profile.
Report which modules have pending changes. Read-only: every module runs `Check` only. `check -v`, `check --verbose` and `rootfiles --verbose check` all expand the report while keeping the console logger at WARN. Check has no INFO diagnostic mode; its local verbose flag shadows the root flag in every position.

| Flag | Default | Purpose |
|---|---|---|
| `--module` | all enabled | Check only these modules. |
| `-o`, `--output` | `text` | `text`, `json` or `prometheus`. |
| `--json` | `false` | Alias for `--output json`; conflicting formats are rejected. |
| `-v`, `--verbose` | `false` | Show the command behind each pending change; report details only, without INFO query logs. |

- Target: `--profile`, `--config` or `ROOTFILES_PROFILE`, else the last applied target, else the detected suggestion ([configuration.md](configuration.md#which-config-a-command-uses)).
- JSON: `version`, `profile`, `config_path`, `applied_config_sha256`, `satisfied`, and `modules[]` with `name`, `satisfied` and `changes[]` (`description`, `command`, `state`; `state` is `blocked` when the operator must act first).
- Prometheus: `rootfiles_module_satisfied{module}`, `rootfiles_module_pending_changes{module}`, `rootfiles_check_timestamp_seconds`.
- Exit codes: 0 all satisfied, 2 pending changes, 1 error.

### rootfiles config

Inspect, validate or scaffold configuration.
Inspect, validate and scaffold configuration ([configuration.md](configuration.md)).

### rootfiles config init

Scaffold a site config.
Write a site config: a commented template that extends a profile, or a capture of this host.

| Flag | Default | Purpose |
|---|---|---|
| `--extends` | detected suggestion | Profile the template extends. |
| `--file` | stdout | Write to this file instead of stdout. The file ends with mode 0600 and owned by you, new or overwritten, because a `--from-system` capture can hold the tunnel token. A symlink, special file or file owned by another non-root user is refused; a failed write leaves the previous file unchanged. |
| `--from-system` | `false` | Capture the running host's settings instead of the template; works on hosts not set up by rootfiles. |
| `--overwrite` | `false` | Replace an existing `--file`. |

`--force` is a deprecated alias of `--overwrite`.

### rootfiles config show

Print the fully merged configuration.
Print the fully merged config that `check` would use, with `extends`, environment and flag overrides applied. The first line names the source.

| Flag | Default | Purpose |
|---|---|---|
| `--reveal` | `false` | Print the inline tunnel token instead of masking it. |

### rootfiles config validate

Validate a profile or config file.
Load and validate the same target as `config show`: unknown keys, paths, ports, CIDRs, names and, on Rocky Linux, unsupported modules. Prints `<source> is valid`; exit 1 with every problem listed otherwise.

### rootfiles doctor

Check disks, reboot, time sync, GPU, systemd and access.
On non-Linux controllers, `--inventory` selects the local fleet inventory; `--json` includes `mode: "controller"`. Controller checks do not connect to hosts.

Read-only health checks of the live host, as opposed to `check`, which compares against the config. Checks: SSH access (keys against `sshd -T`, password-only and no-login accounts, the effective config), firewall state and the SSH port, exporter exposure and the exporter firewall guard, reboot required, disk use, time sync, GPU driver and persistence mode, failed systemd units, the config and its capabilities, and the last apply.

| Flag | Default | Purpose |
|---|---|---|
| `-o`, `--output` | `text` | `text`, `json` or `prometheus`. |
| `--json` | `false` | Alias for `--output json`; conflicting formats are rejected. |
| `--strict` | `false` | Count warnings as failures for the exit code. |

- JSON: `findings[]` (`check`, `level` of `ok`, `warn`, `fail` or `skip`, `detail`, `hint`), `failed`, `applied_config_sha256`, `version`.
- Prometheus: `rootfiles_doctor_findings{level}` and `rootfiles_doctor_check_ok{check}`.
- Disk thresholds come from `doctor.disk` ([configuration.md](configuration.md#doctor)).
- Exit codes: 0 no failures, 2 at least one failure (or warning with `--strict`), 1 error.

### rootfiles fleet

Manage and monitor an SSH host fleet.
Operator-side SSH controller ([fleet.md](fleet.md)). It runs as the operator, takes no host lock and refuses native-host flags (`--module`, `--profile`, `--config`, `--force`, `--home-base`, `--user`, `--ssh-pubkey`, `--tunnel-token`, `--vlan-address`) before connecting.

Selection flags shared by fleet report and rollout commands:

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

### rootfiles fleet init

Create a fleet inventory.
Create an inventory with `--inventory`, optionally importing SSH aliases with `--from-ssh-config`, `--match` and `--group`. `--overwrite` backs up the existing file; `--dry-run` prints YAML.

### rootfiles fleet hosts

List and validate inventory hosts.
Validate and list hosts without connecting. `--inventory`, `--host`, `--group` and `--all` select the inventory and hosts; without a selection all hosts are listed. `--json` or `--output json` returns an array of objects with `name`, `groups`, `ssh`, `sudo` and `config`.

### rootfiles fleet log

Show recorded fleet rollouts, newest first.
Read the controller's own run log back, newest first. Every fleet mutation appends to `~/.local/state/rootfiles/fleet.jsonl`, so this needs no SSH and no inventory.

| Flag | Default | Purpose |
|---|---|---|
| `--host` | every host | Only runs that touched this host. |
| `--op` | every operation | Only this operation: `apply`, `update`, `bootstrap`, `schedule` or `targets`. |
| `--since` | every run | How far back to look: `24h`, `7d`, a date (`2026-11-01`, UTC) or an RFC 3339 timestamp. |
| `--limit` | `0` (every run) | Print at most this many runs, newest first. |
| `-o`, `--output` | `text` | `text`, or `json` for the stored records unchanged, one per line. |
| `--json` | `false` | Alias for `--output json`; conflicting formats are rejected. |

The text report has one row per run and host: time, operation, host, state, exit code and the short reason. A missing log prints nothing and exits 0, and a corrupt line is reported on stderr with its line number and skipped.

### rootfiles fleet apply

Apply selected hosts' site configs serially.
Apply each selected host's site config, one host at a time.

| Flag | Default | Purpose |
|---|---|---|
| `--inventory`, `--host`, `--group`, `--all` | | Selection, see [rootfiles fleet](#rootfiles-fleet). |
| `--timeout`, `--skip-unreachable` | `15m`, `false` | See [rootfiles fleet](#rootfiles-fleet). |
| `-o`, `--output` | `text` | `text` or `json`. |
| `--json` | `false` | Alias for `--output json`; conflicting formats are rejected. |

### rootfiles fleet bootstrap

Install a pinned release on selected hosts.
Install a pinned, checksum-verified release on selected hosts where rootfiles is missing.

| Flag | Default | Purpose |
|---|---|---|
| `--version` | required | Release to install, `vX.Y.Z`. |
| `--inventory`, `--host`, `--group`, `--all` | | Selection. |
| `--timeout`, `--skip-unreachable` | `15m`, `false` | Rollout limits. |
| `-o`, `--output` | `text` | `text` or `json`. |
| `--json` | `false` | Alias for `--output json`; conflicting formats are rejected. |

### rootfiles fleet check

Run a read-only fleet check report.
Run `rootfiles check -o json` on the selected hosts in parallel and summarise the host states.

| Flag | Default | Purpose |
|---|---|---|
| `--inventory`, `--host`, `--group`, `--all` | | Selection; required. |
| `--parallel` | `defaults.parallel` of the inventory (2) | Concurrent SSH processes, 1-128. |
| `-o`, `--output` | `text` | `text` or `json` (`hosts[]` with `host`, `command`, `state`, `reason`, `remote`, `exit_code`). |
| `--json` | `false` | Alias for `--output json`; conflicting formats are rejected. |

### rootfiles fleet doctor

Run a read-only fleet doctor report.
Run `rootfiles doctor -o json` on the selected hosts in parallel. Flags as [rootfiles fleet check](#rootfiles-fleet-check): `--inventory`, `--host`, `--group`, `--all`, `--parallel`, `-o`/`--output`, `--json`.

### rootfiles fleet ping

Check fleet connectivity and privilege.
Check connectivity, version and privilege without writing to hosts. Select using `--inventory`, `--host`, `--group`, or `--all`; `--parallel` controls concurrency. Use `--json` or `--output json` for structured results. Exit 0 when all hosts are ready, 2 for findings and 1 for controller errors.

### rootfiles fleet schedule

Enable or disable scheduled reports serially.
`rootfiles fleet schedule enable|disable` runs `rootfiles schedule enable` or `disable` on each selected host.

| Flag | Default | Purpose |
|---|---|---|
| `--on-calendar` | remote default (`daily`) | OnCalendar expression for `enable`; refused with `disable`. |
| `--inventory`, `--host`, `--group`, `--all` | | Selection. |
| `--timeout`, `--skip-unreachable` | `15m`, `false` | Rollout limits. |
| `-o`, `--output` | `text` | `text` or `json`. |
| `--json` | `false` | Alias for `--output json`; conflicting formats are rejected. |

### rootfiles fleet shutdown

Schedule, cancel or inspect fleet poweroffs.
Schedule, cancel or inspect a poweroff across the fleet ([fleet.md](fleet.md#shutdown), [specs/shutdown.md](specs/shutdown.md)). Every selected host receives the same absolute UTC instant; scheduling stays serial, and only the timers fire together.

### rootfiles fleet shutdown schedule

Schedule every selected host for one absolute instant.

```bash
rootfiles fleet shutdown schedule --group gpu --at 2026-11-21T18:00:00+09:00 --message 'Building power work' --yes
```

Pre-flights every host before writing anything: the host clock must be within 30 seconds of the controller's, rootfiles there must have `shutdown`, and no schedule may exist unless `--replace` is given. If a host fails while being scheduled, every host this run touched is cancelled - the failing one included, because its poweroff timer may already be enabled - and reported `cancelled`, or `cancel-failed` with the exact rerun command.

| Flag | Default | Purpose |
|---|---|---|
| `--at` | required | Shared instant, RFC 3339 with an explicit offset, 1 minute to 30 days ahead. |
| `--reboot` | `false` | Reboot instead of powering off. |
| `--message` | unset | Reason shown in every notice (printable, at most 300 characters). |
| `--notice` | `24h,1h,15m,5m` | Lead times per host. |
| `--replace` | `false` | Replace a schedule a host already has. |
| `--include-self` | `false` | Allow scheduling this controller's own host; without it such a selection is refused. |
| `--inventory`, `--host`, `--group`, `--all` | | Selection. |
| `--timeout`, `--skip-unreachable` | `15m`, `false` | Rollout limits. |
| `-o`, `--output` | `text` | `text` or `json`; `--json` is its alias. |

### rootfiles fleet shutdown now

Schedule every selected host within --in and announce it now.
The same schedule with `--in` instead of `--at`, and one notice sent immediately.

| Flag | Default | Purpose |
|---|---|---|
| `--in` | `2m` | Lead time before the poweroff; at least 1 minute, at most 30 days minus the 35s margin (the allowed clock skew plus transport), so a host never receives a lead past its own maximum. The margin is what keeps the host's own one-minute minimum true when the command arrives, and a run whose instant is already closer than that minimum plus the skew is refused before any host is contacted. |
| `--reboot` | `false` | Reboot instead of powering off. |
| `--message` | unset | Reason shown in every notice. |
| `--notice` | `24h,1h,15m,5m` | Lead times that still fire if `--in` is far enough away. |
| `--replace` | `false` | Replace a schedule a host already has. |
| `--include-self` | `false` | Allow scheduling this controller's own host. |
| `--inventory`, `--host`, `--group`, `--all` | | Selection. |
| `--timeout`, `--skip-unreachable` | `15m`, `false` | Rollout limits. |
| `-o`, `--output` | `text` | `text` or `json`; `--json` is its alias. |

### rootfiles fleet shutdown cancel

Cancel any scheduled shutdown on every selected host.
Cancel any schedule on every selected host. This is the one mutating fleet command that continues past failures, so cancel reaches every host it can; the run exits 2 when any host failed. It is also the recovery for a host whose state file cannot be read: the host finds its units by name and removes them.

Flags: `--inventory`, `--host`, `--group`, `--all` for the selection, `--timeout` and `--skip-unreachable` for the rollout limits, and `-o`/`--output` (`--json` is its alias) for the report. It takes no instant, message or notice flag.

### rootfiles fleet shutdown status

Show each selected host's scheduled shutdown.
Read-only, parallel, from `scheduled_shutdown` in each host's `status -o json`. Prints the same table as `rootfiles fleet status`, whose `SHUTDOWN` column carries the instant and the action, or `unknown` when the host's state file cannot be read.

Flags as [rootfiles fleet check](#rootfiles-fleet-check): `--inventory`, `--host`, `--group`, `--all`, `--parallel`, `-o`/`--output`, `--json`.

### rootfiles fleet status

Run a read-only fleet status report.
Run `rootfiles status -o json` on the selected hosts in parallel. Flags as [rootfiles fleet check](#rootfiles-fleet-check): `--inventory`, `--host`, `--group`, `--all`, `--parallel`, `-o`/`--output`, `--json`.

`--view gpu` or `--view users` adds a fleet-wide section rendered from that same remote report: GPU allocations, or the managed and system account counts, per host. A host that did not report the section shows `-` instead of failing the run. With `--json` the section is added beside `hosts` under its own key (`gpu` or `users`, keyed by host name); without `--view` the report is byte-for-byte what it was.

### rootfiles fleet targets

Render scrape targets or push them to a hub.
Print Prometheus `file_sd` scrape targets as JSON, or push them to the monitoring hub.

| Flag | Default | Purpose |
|---|---|---|
| `--inventory` | see above | Inventory file. |
| `--host`, `--group`, `--all` | whole inventory | Narrow the targets to these hosts. |
| `--push` | unset | Hub host to receive the targets file; needs `--yes`, or `--dry-run` to print the path only. |
| `--timeout` | `15m` | Limit for the push. |

Exit codes: 0 printed or pushed, 2 when the push failed on the hub, 1 on errors.

### rootfiles fleet update

Update selected hosts serially.
Update the selected hosts to a pinned release, then run a read-only `check` on each. A host that now plans changes is reported `apply-pending`.

| Flag | Default | Purpose |
|---|---|---|
| `--version` | required | Release to install, `vX.Y.Z`. |
| `--inventory`, `--host`, `--group`, `--all` | | Selection. |
| `--timeout`, `--skip-unreachable` | `15m`, `false` | Rollout limits. |
| `-o`, `--output` | `text` | `text` or `json`. |
| `--json` | `false` | Alias for `--output json`; conflicting formats are rejected. |

### rootfiles gpu

Manage per-user GPU allocation.
Per-user GPU allocation ([gpu.md](gpu.md)). Reads `--profile`, `--config` and `--home-base` to find the ledger.

### rootfiles gpu assign

Assign GPUs to a user.
`sudo rootfiles gpu assign <USERNAME> --gpus 0,1`. Mutating. Assigning a user again replaces the allocation.

| Flag | Default | Purpose |
|---|---|---|
| `--gpus` | required | Comma-separated GPU indices. |
| `--method` | `modules.nvidia.gpu_allocation.method` (`env` when unset) | `env`, `cgroup` or `both`. |

### rootfiles gpu list

List current GPU allocations.
List the allocations in the ledger.

### rootfiles gpu mig

Show Multi-Instance GPU mode and instances.
Multi-Instance GPU commands (read-only).

### rootfiles gpu mig status

Show MIG mode and instances per GPU.
Show the current and pending MIG mode and the instances of each GPU.

### rootfiles gpu revoke

Revoke GPU allocation from a user.
`sudo rootfiles gpu revoke <USERNAME>`. Mutating. Refuses a tenant's entry; use `tenant gpu`.

### rootfiles gpu status

Show GPU status with allocation info.
Show each GPU with its owner.

### rootfiles rollback

Restore files changed by an earlier run.
`rootfiles rollback [BACKUP_ID]`. Without an id, list backup sessions. With an id, restore the files that run saved and remove the files it created; `--dry-run` shows the plan, `--yes` skips the confirmation. Restoring needs root and the lock. Packages, accounts and services are not reverted ([operations.md](operations.md#file-backups-and-rollback)). The listing is a table of the session id, the number of paths it saved and its note, newest first; there is no machine format, so a script that needs the newest id takes the first `YYYYMMDD-HHMMSS` cell of the first column.

### rootfiles schedule

Install check and doctor report timers.
Systemd timer that writes `check` and `doctor` reports into the state directory ([operations.md](operations.md#scheduled-reports)). It changes nothing else.

### rootfiles schedule disable

Stop and remove the report timer.
Stop the timer and remove `rootfiles-check.timer` and `rootfiles-check.service`. Mutating.

### rootfiles schedule enable

Install and start the report timer.
Install and start the timer; when no report exists yet, also run the first report. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--on-calendar` | `daily` | systemd OnCalendar expression, such as `hourly` or `*-*-* 06:00`. Line breaks, NUL and apostrophes are refused. |

### rootfiles schedule status

Show the report timer state and the age of its reports.
Show whether the units are installed, the timer's enabled and active state, its OnCalendar expression and the age of `check.json` and `doctor.json`. A report older than 48 hours is marked stale. Read-only; exit 0.

### rootfiles shutdown

Schedule a poweroff or reboot with advance notice.
Schedule one poweroff or reboot at an absolute UTC instant, with advance notice to host sessions and tenant containers ([specs/shutdown.md](specs/shutdown.md)). `schedule`, `cancel` and the internal `notify` are mutating: they need root and the lock. Only one schedule exists per host.

### rootfiles shutdown schedule

Schedule the poweroff or reboot.
`sudo rootfiles shutdown schedule --at 2026-11-21T18:00:00+09:00`. Writes `rootfiles-shutdown.service`, `rootfiles-shutdown.timer`, one notice timer pair per future lead time and `/var/lib/rootfiles/shutdown.json`.

| Flag | Default | Purpose |
|---|---|---|
| `--at` | required | RFC 3339 instant **with an explicit offset** (`Z` or `+09:00`). A time without one is refused, and the accepted window is 1 minute to 30 days ahead. |
| `--reboot` | `false` | Reboot instead of powering off. |
| `--message` | unset | Reason shown in every notice; printable text, at most 300 characters. |
| `--notice` | `24h,1h,15m,5m` | Lead times before the instant, at most 8. A lead whose moment has passed is skipped; when none is left, one notice is sent immediately, and a schedule created inside the five-minute login-block window blocks new logins at once. |
| `--replace` | `false` | Replace a schedule that already exists; without it a second schedule is refused and names the existing one. |
| `--notify-now` | `false` | Also announce immediately. `rootfiles fleet shutdown now` uses it. |

The timer sets `OnCalendar` to the UTC instant, `Persistent=false` so a host that boots late never acts on a moment that has passed, and `AccuracySec=1s` so systemd's one-minute default cannot move the moment. A separate tenant login-block timer fires five minutes before the instant whatever `--notice` says; host SSH remains available for cancellation. A lead whose moment has passed still owes users one notice right away. `--dry-run` writes nothing.

### rootfiles shutdown cancel

Remove the schedule, the units and any tenant login block.
Remove the units, the state file and any `/run/nologin` this schedule wrote, then announce the cancellation to host sessions and tenant containers. Running it again is a no-op. A timer that will not stop or a unit that will not be removed is an error, not a warning: the state file stays so the schedule remains on record. It is also the recovery for a state file that cannot be read, which it removes by name. Mutating.

### rootfiles shutdown status

Show the schedule, next notice and tenant login block.
Show the schedule, the instant in UTC and host local time, the time left, the notice instants and whether logins are blocked. Read-only; exit 0, including when nothing is scheduled. An instant that has already passed is not a schedule, so it is not reported. `-o`/`--output` takes `text` or `json`, and `--json` is its alias; the JSON report holds `scheduled_shutdown`, `next_notice`, `logins_blocked` (true if any checked tenant is blocked, false if none are blocked, null when inspection is incomplete), `tenant_logins` (checked, blocked and unknown counts), `legacy_host_block`, `units_installed` and, when the state file cannot be read, `scheduled_shutdown_error`.

`rootfiles status -o json` also carries a `scheduled_shutdown` object, but only when a schedule exists; an unreadable state file appears as `scheduled_shutdown_error` so a fleet pre-flight never reads silence as "nothing scheduled".

### rootfiles status

Show full system status at a glance.
On non-Linux controllers, `--inventory` selects the local fleet inventory; `--json` includes `mode: "controller"`, inventory counts and latest fleet run per host.

Read-only overview: system, profile, last apply, module satisfaction, GPU allocations, tunnel and users.

| Flag | Default | Purpose |
|---|---|---|
| `--module` | all enabled | Report only these modules; also limits the GPU (`gpu`), tunnel (`cloudflared`) and users (`users`) sections. |
| `-o`, `--output` | `text` | `text` or `json`. |
| `--json` | `false` | Alias for `--output json`; conflicting formats are rejected. |

Exit code 0 for the overview, including reported findings; 2 for argument/flag parsing errors, and 1 for other invocation errors. When the config does not load, `status` reports the error and skips the user and GPU databases unless an absolute home base is known.

### rootfiles tenant

Manage long-lived tenant containers.
Long-lived tenant containers ([tenant.md](tenant.md)). Every subcommand except `ops` reads `--profile`, `--config` and `--home-base` to find the tenant specs. Not supported on Rocky Linux.

### rootfiles tenant apply

Converge a tenant to its spec.
`rootfiles tenant apply [TENANT]`. Converge a tenant to its spec; recreates the container when the image, GPUs or ports changed. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--all` | `false` | Apply every tenant. |

### rootfiles tenant create

Create a tenant: volume, container and temporary passwords.
`rootfiles tenant create <TENANT>`. Write the spec, create the volume, start the container and print a temporary password per account. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--image` | required | Container image; it needs `openssh-server`. |
| `--ssh-port` | required | Host port published to the container's sshd. |
| `--size` | required | Volume size, such as `500G` or `2T`. |
| `--fs` | `ext4` | Volume filesystem: `ext4` (can shrink offline) or `xfs`. |
| `--gpus` | none | Comma-separated GPU indices. |
| `--cpus` | unmanaged | Host CPU list (`N` or `N-M`, comma-separated), not a CPU quota. |
| `--mems` | unmanaged | NUMA memory-node list (`N` or `N-M`, comma-separated). |
| `--account` | none | `NAME[:UID[:GID]]`, repeatable; UIDs default to 1000, 1001, ... |
| `--sudo` | `false` | Give the accounts sudo inside the container. |
| `--operator` | none | Host account allowed `sudo rootfiles tenant ops` on this tenant (repeatable). |
| `--port` | none | Extra published port `HOST:CONTAINER[/udp]` (repeatable). |
| `--bind` | none | Extra bind mount `SRC:DST[:ro\|rw]` outside the volume quota (repeatable). |

### rootfiles tenant cpuset

Pin tenant CPUs and NUMA memory.
`rootfiles tenant cpuset <TENANT> --cpus 0-3 --mems 0`. Save CPU/NUMA pinning and update the existing running or stopped container in place, without restarting. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--cpus` | retain spec | Host CPU list (`N` or `N-M`, comma-separated); explicit empty makes the key unmanaged. |
| `--mems` | retain spec | NUMA memory-node list; explicit empty makes the key unmanaged. |
| `--clear` | `false` | Remove cpuset from the spec and reset both container keys to the full host lists. Exclusive with the list flags. |

Absent or empty spec keys are unmanaged, so hand-set pins stay untouched. Other pending settings remain pending; run `rootfiles tenant apply TENANT` separately to recreate for them. A failed update restores the previous spec. On cgroup v2, changing memory nodes migrates existing pages and may take time for large processes. `tenant status` shows `update` for live drift and `status TENANT` shows spec and actual pins.

### rootfiles tenant gpu

Change a tenant's GPUs (recreates the container).
`rootfiles tenant gpu <TENANT> --gpus 0,1`. Change a tenant's GPUs and recreate the container. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--gpus` | required | Comma-separated GPU indices, or `none`. |

### rootfiles tenant image

Change a tenant's image (recreates the container).
`rootfiles tenant image <TENANT> <IMAGE>`. Change the image and recreate the container. Mutating.

### rootfiles tenant import

Adopt an existing container as a tenant.
`rootfiles tenant import <TENANT> --from <CONTAINER> --size <SIZE>`. Move a hand-made container under rootfiles; the old container is kept stopped. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--from` | required | Running container to import. |
| `--size` | required | Size of the new volume. |
| `--fs` | `ext4` | Volume filesystem: `ext4` or `xfs`. |
| `--data` | unset | Host directory copied into the new `/data`. |
| `--keep` | none | Path under `/home` or `/tmp` in the old container to copy (repeatable). |
| `--operator` | none | Host account allowed `sudo rootfiles tenant ops` (repeatable). |

### rootfiles tenant list

List tenant names.
Print the tenant names.

### rootfiles tenant logs

Show the container log.
`rootfiles tenant logs <TENANT>`. Show the container log.

| Flag | Default | Purpose |
|---|---|---|
| `--tail` | `200` | Lines to show. |

### rootfiles tenant ops

Run tenant operator actions (sudo).
`sudo rootfiles tenant ops <TENANT> <ACTION> [USER]` with `ACTION` one of `status`, `logs`, `gpu-heal`, `restart` or `reset USER`. For members of the tenant operator group listed in the spec's `operators`. It takes no flags, ignores `ROOTFILES_*` variables, reads only the applied host config, and audits every attempt, refused ones included.

### rootfiles tenant passwd

Issue a temporary password for a tenant user.
`rootfiles tenant passwd <TENANT> <USER>`. Issue a temporary password that must be changed at first login. Alias: `tenant reset`. Mutating.

### rootfiles tenant port

Change a tenant's SSH host port (recreates the container).
`rootfiles tenant port <TENANT> <SSH_PORT>`. Change the SSH host port and recreate the container. Mutating.

### rootfiles tenant restart

Restart a tenant container.
`rootfiles tenant restart <TENANT>`. Restart the container; it is kept. Mutating.

### rootfiles tenant status

Show tenant containers, spec drift and volume use.
`rootfiles tenant status [TENANT]`. Show state, spec drift and volume use; with a name also the writable-layer size.

### rootfiles tenant sync

Refresh tenant password hashes and sshpiper keys.
Save the password hashes and refresh the sshpiper keys of every tenant. Run by a 2-minute timer; needs root but not the global lock.

### rootfiles tenant user

Manage tenant accounts.
Tenant accounts.

### rootfiles tenant user add

Add an account and issue its temporary password.
`rootfiles tenant user add <TENANT> <NAME>`. Add an account to a running tenant and print its temporary password. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--uid` | next free from 1000 | UID. |
| `--gid` | the UID | GID. |
| `--sudo` | `false` | Give the account sudo inside the container. |

### rootfiles tenant volume

Manage tenant volumes.
Tenant volumes.

### rootfiles tenant volume resize

Grow a volume, or shrink it offline.
`rootfiles tenant volume resize <TENANT> <SIZE>`. Grow a volume online, or shrink an ext4 volume. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--offline` | `false` | Stop the container to shrink an ext4 volume. |

### rootfiles tunnel

Manage cloudflared tunnel and VLAN private network.
Cloudflare tunnel and its private network address ([tunnel.md](tunnel.md)). Not supported on Rocky Linux for changes.

### rootfiles tunnel restart

Restart cloudflared service.
Restart `cloudflared.service`. Mutating.

### rootfiles tunnel setup

Set up the tunnel, VLAN and cloudflared.
`sudo rootfiles tunnel setup [TOKEN]`. Install the cloudflared binary, the tunnel service and the private network address. Alias: `tunnel install`. Mutating.

- Token: the argument, else `--tunnel-token`, else `ROOTFILES_TUNNEL_TOKEN`, else the config (`tunnel_token` or `tunnel_token_file`). Without a token only the binary and a configured address are managed.
- Address: `--vlan-address`, else `ROOTFILES_VLAN_ADDRESS`, which also turns the private network on for this run; else the config.
- The token argument is redacted in the audit log.

### rootfiles tunnel status

Show tunnel service and VLAN status.
Show the tunnel service and private network state. Read-only.

### rootfiles tunnel uninstall

Remove tunnel service, VLAN, and binary.
Remove the tunnel service, the private network interface and the binary. Mutating.

### rootfiles tunnel update

Upgrade cloudflared binary to latest (or a pinned version).
Replace the cloudflared binary with the latest upstream release, or a pinned one, and restart the service when it exists. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--check` | `false` | Report the installed and latest versions only; needs neither root nor the lock. |
| `--version` | latest | Pin a cloudflared release, such as `2024.9.1`. |

Exit codes with `--check`: 0 current, 2 an update is available or cloudflared is missing, 1 lookup error.

### rootfiles update

Update rootfiles to the latest release.
Replace this binary with the latest GitHub release, or a pinned one, after verifying its SHA-256 checksum; then run `check` and print an `Apply pending` hint when the new release plans changes. Linux only. Alias: `rootfiles upgrade`. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--check` | `false` | Report whether an update is available; needs neither root nor the lock. |
| `--version` | latest | Release to install, such as `v0.9.0`. |

`--dry-run` downloads and verifies the archive without replacing the binary. Exit codes with `--check`: 0 current, 2 an update is available, 1 lookup error.

### rootfiles user

Manage system users (custom home, backup, restore).
Accounts and the home base ([users.md](users.md)). Every subcommand reads `--profile`, `--config` and `--home-base`.

### rootfiles user add

Create a user with custom home directory.
`sudo rootfiles user add [USERNAME]`. Create an account under the home base. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--groups` | none | Additional groups. |
| `--no-docker` | `false` | Do not add the account to the `docker` group. |

- The name comes from the argument, `--user` or `ROOTFILES_USER`; the key from `--ssh-pubkey` or `ROOTFILES_SSH_PUBKEY`. Without a name and without `--yes`, it prompts.
- `--pubkey` is a deprecated alias of `--ssh-pubkey`.

### rootfiles user audit

Compare users.json metadata with the accounts on this system.
Compare `users.json` with the accounts on the host. Exit 2 on drift.

### rootfiles user backup

Backup user list and metadata to JSON.
Write the users database, merged with the accounts found on the host, to a JSON file.

| Flag | Default | Purpose |
|---|---|---|
| `--file` | `rootfiles-users-<hostname>-<YYYYMMDD>.json` in the current directory | Output file. |

`-o/--output` is a deprecated alias of `--file`.

### rootfiles user del

Delete a user (home kept by default).
`sudo rootfiles user del <USERNAME>`. Remove the account, its sudoers drop-in, GPU allocation and database entry. The home is kept by default. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--archive` | `false` | Archive the home to `<home_base>/.rootfiles/archive`, then remove it. |
| `--remove-home` | `false` | Remove the home without an archive. |

### rootfiles user du

Show home directory disk usage per user (largest first).
Show home directory usage per user, largest first.

### rootfiles user expire

Set or clear the account expiry date.
`sudo rootfiles user expire <USERNAME> <YYYY-MM-DD|never>`. Set or clear the account expiry date. Mutating.

### rootfiles user group-add

Add a user to groups.
`sudo rootfiles user group-add <USERNAME>`. Add the account to groups. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--groups` | none | Groups to add. |
| `--docker` | `false` | Include the `docker` group. |
| `--sudo` | `false` | Include the `sudo` group. |

### rootfiles user group-del

Remove a user from groups.
`sudo rootfiles user group-del <USERNAME>`. Remove the account from groups. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--groups` | none | Groups to remove. |
| `--docker` | `false` | Include the `docker` group. |
| `--sudo` | `false` | Include the `sudo` group. |

### rootfiles user groups

List groups (all groups or for a specific user).
`rootfiles user groups [USERNAME]`. List all groups, or one user's groups.

### rootfiles user id

Show UID, GID, and groups for a user.
`rootfiles user id <USERNAME>`. Show UID, GID and groups.

### rootfiles user key

Manage a user's SSH authorized keys.
A user's SSH authorized keys.

### rootfiles user key add

Add an authorized key.
`sudo rootfiles user key add <USERNAME> <KEY>`. Mutating.

### rootfiles user key list

List authorized keys (numbered).
`rootfiles user key list <USERNAME>`. List the authorized keys, numbered.

### rootfiles user key rm

Remove authorized key(s) by list index, key or comment.
`sudo rootfiles user key rm <USERNAME> <INDEX|KEY|COMMENT>`. Remove keys by list index, key or comment. Mutating.

### rootfiles user list

List managed users (or system users with --system).
List managed users.

| Flag | Default | Purpose |
|---|---|---|
| `-s`, `--system` | `false` | List all accounts with UID 1000-65533 instead. |
| `--names` | `false` | Print names only, one per line. |

### rootfiles user lock

Block all logins (password and SSH key).
`sudo rootfiles user lock <USERNAME>`. Block password and SSH key logins. Mutating.

### rootfiles user passwd

Set passwords for users (batch).
`sudo rootfiles user passwd [USERNAME...]`. Set passwords in batch. By default each user gets a random password, printed once. Passwords reach `chpasswd` on stdin. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--password` | unset | One password for every target user. Redacted in the audit log. |
| `-f`, `--file` | unset | File with a username, or `username,password`, per line. |
| `--all` | `false` | Every system user. |
| `--expire` | `false` | Force a password change at the next login (`chage -d 0`). |

`--suffix` (password = username + suffix) is deprecated and will be removed in v0.21.0.

### rootfiles user quota

Limit per-user disk usage on the home base.
Per-user hard limits on the home base filesystem: XFS project quota or ext4 user quota.

### rootfiles user quota rm

Remove a user's limit.
`sudo rootfiles user quota rm <USERNAME>`. Remove a user's limit. Mutating.

### rootfiles user quota set

Set a hard quota, e.g. 500G.
`sudo rootfiles user quota set <USERNAME> <SIZE>`, such as `500G`. The limit is recorded and applied again by `user restore`. Mutating.

### rootfiles user quota show

Show the quota report.
Show the quota report.

### rootfiles user rehome

Move user home to custom home base directory.
`sudo rootfiles user rehome <USERNAME>`. Copy the home to `<home_base>/<user>`, verify the copy, switch the account and leave a symlink at the old path. Mutating.

| Flag | Default | Purpose |
|---|---|---|
| `--remove-old` | `false` | Delete the original home after the verified copy instead of keeping `<old>.rootfiles-bak-<timestamp>`. |

### rootfiles user restore

Restore users from backup (reconnect existing home dirs).
`sudo rootfiles user restore [BACKUP_FILE]`. Recreate accounts from a `user backup` file, or from `<home_base>/.rootfiles/users.json`, reconnecting surviving homes. Mutating.

### rootfiles user unlock

Re-enable logins for a locked user.
`sudo rootfiles user unlock <USERNAME>`. Re-enable logins. Mutating.
