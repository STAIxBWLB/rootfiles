# Operations

Where rootfiles keeps its records on a host, how to undo a run, how to back up a host before an OS reinstall, how to schedule reports and how to update the binary. Contracts: [docs/specs/state-rollback.md](specs/state-rollback.md), [docs/specs/doctor-schedule.md](specs/doctor-schedule.md), [docs/specs/self-update.md](specs/self-update.md).

## Files on the host

| Path | Content | Override |
|---|---|---|
| `/var/lib/rootfiles/state.json` | Result of the last `apply` (profile, config, per-module outcome), shown by `status` | `ROOTFILES_STATE_DIR` |
| `/var/lib/rootfiles/history.jsonl` | One line per recorded `apply` | `ROOTFILES_STATE_DIR` |
| `/var/lib/rootfiles/applied-config.yaml` | Resolved config of the last recorded `apply` (root, 0600, no inline tunnel token) | `ROOTFILES_STATE_DIR` |
| `/var/lib/rootfiles/backups/<id>/` | Files saved before a run changed them | `ROOTFILES_STATE_DIR` |
| `/var/lib/rootfiles/check.json`, `doctor.json` | Scheduled reports | `ROOTFILES_STATE_DIR` |
| `/var/log/rootfiles.log` | JSON audit log of real mutating runs | `ROOTFILES_LOG_FILE` |
| `/run/rootfiles.lock` | Global lock | `ROOTFILES_LOCK_FILE` |

Tenant commands keep their own audit log ([tenant.md](tenant.md#audit)); the fleet controller keeps a run log on the operator machine ([fleet.md](fleet.md#run-log)).

## Lock and audit log

- Commands that change the host need root and take the global lock. A second mutating command fails at once and names the PID that holds the lock. `--dry-run` needs neither root nor the lock.
- `Ctrl-C` cancels commands that are still running instead of leaving them behind.
- Each real mutating run appends a `start` record with its arguments to the audit log, followed by the log lines of the run. `--tunnel-token`, `--password` (both `--flag value` and `--flag=value`) and the token argument of `tunnel setup` are written as `***`. Logs written by older versions are not rewritten: remove or redact credentials found there and rotate them.

## File backups and rollback

Every real mutating run saves each file it overwrites or removes under `/var/lib/rootfiles/backups/<id>`, where `<id>` is the start time (`YYYYMMDD-HHMMSS`).

```bash
rootfiles rollback                                 # list backup sessions
sudo rootfiles rollback 20260926-101500 --dry-run  # show what would be restored
sudo rootfiles rollback 20260926-101500
```

- Rollback restores the saved files and removes files the run created. It asks for confirmation unless `--yes` or `ROOTFILES_YES=true` is set.
- Packages, accounts and running services are not reverted. Restart affected services (for example `ssh`, `docker`) afterwards.
- Any rollback deletes the kept `applied-config.yaml`, because the host no longer matches it. Until the next `apply`, subcommands fall back to the `minimal` profile with the detected home base.

## System backup

`rootfiles backup` captures what you need to rebuild a host after an OS reinstall. It needs root and the lock, unless run with `--dry-run`.

```bash
sudo rootfiles backup                    # writes under /raid/backup
sudo rootfiles backup -o /data/backup
sudo rootfiles backup --skip-docker      # no Docker image list
sudo rootfiles backup --skip-etc         # no /etc archive
```

The backup directory is `rootfiles-backup-<hostname>-<YYYYMMDD>/` under the output directory. It is created with mode 0700 and its files with 0600, because it holds SSH keys and configs. A directory that someone other than root could control is refused.

| File | Content |
|---|---|
| `system-info.json` | Hostname, OS, GPU, architecture, memory, mounts |
| `users.json` | Users database, merged with the accounts found on the host. Left out with a warning when the database fails the trust rules in [users.md](users.md#home-base). |
| `etc-config.tar.gz` | `/etc/ssh/sshd_config.d/`, `/etc/docker/daemon.json`, `/etc/systemd/network/`, `/etc/ufw/`, `/etc/fstab`, `/etc/netplan/`, `/etc/default/locale`, `/etc/timezone`, `/etc/default/useradd` (those that exist) |
| `crontab-root.txt` | Root crontab |
| `root-ssh.tar.gz` | `/root/.ssh/` |
| `usr-local-bin.tar.gz` | `/usr/local/bin/` |
| `docker-images.txt` | Docker image list |
| `config-snapshot.yaml` | The current host expressed as a rootfiles config |

Restoring after a reinstall: [users.md](users.md#os-reinstall-recovery).

## Scheduled reports

`rootfiles schedule` installs a systemd timer that runs `check` and `doctor` and stores the results. It changes nothing on the host.

```bash
sudo rootfiles schedule enable                          # daily
sudo rootfiles schedule enable --on-calendar hourly     # any systemd OnCalendar expression
sudo rootfiles schedule disable
```

- `enable` writes `rootfiles-check.service` and `rootfiles-check.timer` under `/etc/systemd/system` and starts the timer. The timer adds a random delay of up to 15 minutes and catches up on runs missed while the host was off.
- Each run writes `/var/lib/rootfiles/check.json` and `doctor.json`. When `/var/lib/prometheus/node-exporter` exists (the `monitoring` module creates it), it also writes `rootfiles.prom` there for the node exporter textfile collector.
- Exit code 2 (drift or findings) counts as a successful run of the service; the report carries the result.
- When no report exists yet, `enable` also starts the service once, so a new host does not raise the stale-report alert ([monitoring.md](monitoring.md#alerts)) before the first timer run.
- `disable` stops the timer and removes both units.

## Self-update

```bash
sudo rootfiles update                    # latest GitHub release
rootfiles update --check                 # report only
sudo rootfiles update --version v0.9.0   # a specific release
sudo rootfiles update --dry-run          # download and verify, do not replace
```

- `update` downloads the release archive for this OS and architecture, verifies its SHA-256 checksum, and replaces the running binary atomically. It works on Linux only.
- It also creates the `root` symlink next to the binary when it is missing.
- After replacing the binary it runs `rootfiles check`. When the new release plans changes on this host, it prints an "Apply pending" hint; nothing is applied automatically.
- `update --check` and `tunnel update --check` need neither root nor the lock. Exit codes: 0 when current, 2 when an update (or, for the tunnel, a missing cloudflared) is available, 1 on a lookup error.
- `rootfiles upgrade` is an alias of `update`.
- For a fleet, use `rootfiles fleet update --version vX.Y.Z` ([fleet.md](fleet.md#rollouts)).
