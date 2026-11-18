# Changelog

Generated from docs/releases/ by scripts/gen-changelog.sh; edit the release notes, not this file.

## v0.22.1

Fleet commands work again when the default inventory is a symlink to an inventory kept elsewhere.

### Fixed
- With `~/.config/rootfiles/fleet.yaml` symlinked to an inventory in another folder, the `rootfiles fleet` commands that read the inventory (`hosts`, `ping`, `status`, `check`, `apply`, `update` and the others that select hosts) failed without `--inventory` with "reading config ...: no such file or directory", and the controller's `status`, `doctor` and `monitor` could not read the inventory, because relative `config:` paths were looked up next to the link. They now resolve next to the file the link points to. (#193)

### Action needed
- Update the fleet controller with `rootfiles update` on macOS, or `sudo rootfiles update` on a Linux controller. The fix runs only on the controller, so hosts can stay on v0.22.0; to keep versions aligned, run `rootfiles fleet update --all --version v0.22.1 --yes`. (#194)

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.22.0...v0.22.1

## v0.22.0

This release adds a live monitoring dashboard, restyles help and report output, and removes the five spellings deprecated in v0.21.0.

### New
- `rootfiles monitor` (alias `top`) opens a live terminal dashboard of host vitals, GPUs, tenant containers and doctor findings; on a macOS controller it monitors the fleet inventory. `rootfiles status`, `rootfiles gpu status`, `rootfiles tenant status` and `rootfiles fleet status` take `-w`/`--watch` and `--interval` (at least `500ms`) to refresh in place; `--watch` refuses `--json` and `-o json`. (#176)
- `ROOTFILES_COLOR_THEME` (or `DOT_COLOR_THEME`) selects the output palette: `ansi`, `tokyo-night-dark` or `tokyo-night-light`. An unknown name prints one warning and falls back to `ansi`. (#175)

### Improved
- Root help groups commands under Setup; Users, GPUs and tenants; Fleet; and Help, and every help page honors `--color` and `--ascii`. Dry-run lines read `~ would <action>`, next steps are marked with an arrow, and `rootfiles apply` prints its plan and summary under the shared report headings. (#175)
- A prompt run without a terminal (a plain `ssh host rootfiles ...`, a timer, CI) now fails with "no terminal for an interactive prompt; pass --yes, or run from a terminal" instead of a bubbletea TTY error. (#175)

### Fixed
- `TERM=dumb rootfiles apply` with stdin closed or redirected from `/dev/null` no longer takes the default answer and applies without `--yes`; it stops with "stdin ended before an answer; pass --yes, or answer the prompt". (#183)
- `rootfiles tenant status` and the tenant dashboard reported a stopped Docker daemon or a permission error as a missing container. They now show the read as failed with a `docker inspect` next step. (#182)
- `rootfiles gpu mig status` and the GPU dashboard showed MIG modes with no instances when `nvidia-smi -L` failed. They now report the failed read. (#181)
- The unknown-theme warning ignored `--color=never` and `--ascii` and printed in color with Unicode markers. It now follows them. (#184)
- A usage error for a misspelled flag ignored `--color` and `--ascii` written after it. They now apply to the error. (#186)

### Action needed
- Update with `sudo rootfiles update` on a host, or `rootfiles fleet update --all --version v0.22.0 --yes` from the fleet controller (`--host` or `--group` to narrow it, `--dry-run` to preview). Converged hosts need no `apply` afterwards. (#189)
- These deprecated spellings are removed and now fail as unknown flags (exit 2): `backup -o`/`--output` (use `--dir`), `user backup -o`/`--output` (use `--file`), `user add --pubkey` (use `--ssh-pubkey`), `config init --force` (use `--overwrite`) and `user passwd --suffix` (use `--password` or `--file`). (#188)
- Scripts that read `rootfiles apply` output: the `===`/`---` section banners are gone, dry-run lines start with `~ would` instead of `[dry-run]`, and warnings start with `rootfiles: warning:` instead of `Warning:`. (#175)

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.21.0...v0.22.0

## v0.21.0

A macOS machine can now act as the fleet controller without root, and the fleet gains onboarding, a connectivity preflight, a run-log viewer and a coordinated shutdown with advance notice.

### New
- `scripts/install.sh` installs on macOS without root, into `$HOME/.local/bin` or `--prefix DIR`, and `rootfiles update` replaces a binary you own there without sudo. Linux hosts still install to `/usr/local/bin` as root. (#164)
- `rootfiles fleet init` creates an inventory, optionally importing hosts from your SSH config, and backs up an existing one before replacing it. `rootfiles fleet hosts` validates and lists the inventory without connecting. (#163)
- `rootfiles fleet ping` checks SSH reachability, the installed version and the configured privilege mode on each host before a rollout. It names authentication, host-key, timeout, missing-binary and privilege failures, and never changes host-key checking. (#165)
- On a macOS controller, `rootfiles doctor` checks the SSH client, inventory, identity, run-log directory and release version without contacting hosts, and `rootfiles status` shows inventory counts and the last recorded run per host. (#167)
- `rootfiles fleet log` reads the controller's run log newest first, filtered by `--host`, `--op`, `--since` and `--limit`. `rootfiles fleet status --view gpu|users` shows GPU allocations and accounts across the fleet. (#168)
- `rootfiles shutdown schedule|cancel|status` schedules a host poweroff or reboot at an absolute time. Logged-in users and tenant containers get notices at 24h, 1h, 15m and 5m by default, and new tenant logins are blocked from five minutes out. Host SSH stays open, so a cancel can still reach the host. `rootfiles fleet shutdown schedule|now|cancel|status` does this across hosts for one shared instant: it checks every host first and cancels the ones already scheduled if any host fails. `rootfiles fleet status` gains a SHUTDOWN column when a schedule exists. (#169)

### Improved
- Every command's help fits 80 columns, opens with a one-line imperative summary, shows working examples and writes arguments as `<required>` or `[optional]`. Boolean flags print without `[=true|false]`, and a controller no longer lists `--profile`, `--config` or `--home-base` for `status` and `doctor`. (#170)
- Human-readable reports share one layout, warnings read `rootfiles: warning: <what>: <why>` with the next step when one is known, and `docs/commands.md` lists exactly the flags each command takes. JSON and Prometheus output are unchanged. (#171)

### Fixed
- `rootfiles update` on a macOS controller no longer ends with a post-update warning or an `apply` hint that a controller cannot run. (#166)

### Action needed
- Update with `sudo rootfiles update` on a host, or `rootfiles fleet update --version v0.21.0` from the fleet controller. Converged hosts need no `apply` afterwards. (#164)
- Scripts that match warning text: warnings now start with `rootfiles: warning:`, the tenant audit-log warning included, and `tunnel`'s "up to date" line has no trailing period. (#170, #171)
- Scripts that read `rootfiles rollback` output: the backup listing is now a `BACKUP PATHS NOTE` table, so a match on the old `path(s)` text finds nothing. Read the first column and match the id shape `YYYYMMDD-HHMMSS` instead. (#171)
- The deprecated spellings (`backup -o`, `user backup -o`, `user add --pubkey`, `config init --force`, `user passwd --suffix`) still work in this release; their removal moves to v0.22.0. Switch to `--dir`, `--file`, `--ssh-pubkey`, `--overwrite` and `--password` or `--file`. (#105)

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.20.0...v0.21.0

## v0.20.0

Command output follows one display standard with color, ASCII and JSON switches, errors say what to do next, and `tenant import --keep` works on Docker's containerd image store.

### New
- Every command with `-o json` (`status`, `check`, `doctor` and the `fleet` reports and rollouts) also accepts `--json`; `-o json` keeps working. New root flags: `--color=auto|always|never` (stdout and stderr are decided separately, and `NO_COLOR`, `FORCE_COLOR`, `CLICOLOR_FORCE` and `CLICOLOR` are honored), `--ascii` (ASCII markers, also chosen automatically for a non-UTF-8 locale) and `--verbose` (INFO diagnostics). (#135)
- Errors with a known fix print it on a second line. For example, running `rootfiles apply` without root shows the `sudo rootfiles ...` command to rerun, a held lock names the PID to check with `ps -p`, and an unknown profile lists the available ones. When the arguments contain a password or token, or a `ROOTFILES_*` variable is set, the hint shows the command's `--help` instead of a replay. (#150, #151)

### Improved
- Reports use the terminal's own color theme, wrap long text to the terminal width, and keep fleet tables readable on narrow terminals. `rootfiles doctor` ends with a summary of findings. (#135, #149)
- A mistyped command suggests the closest one, for example `rootfiles stauts` suggests `status`. (#149)
- `rootfiles status` shows GPU allocation rows with a marker and the user name, and `rootfiles doctor` marks skipped checks as "skipped" in both the rows and the summary. (#149)
- Warnings from `tenant` and `user` commands follow `--color` and `--ascii`. (#149)
- `rootfiles update` and `rootfiles fleet update` only report "Apply pending" when the post-update check exits 2 with valid JSON on stdout. On any other check failure, `rootfiles update` prints a warning and still succeeds, while `rootfiles fleet update` marks that host as an error and skips the remaining hosts. (#146)

### Fixed
- `rootfiles tenant import --keep` no longer fails on Docker with the containerd image store. Kept paths are copied through Docker with owners and modes preserved, and the size checks still run before anything changes, including in `--dry-run`. (#140)
- Fleet reports no longer show an exit-2 failure without a JSON report (such as a remote crash) as drift or findings. (#146)
- `rootfiles user passwd --suffix` values are now masked in the audit log and in backup notes, like `--password`. (#150)
- Binaries are built with Go 1.26.11, which fixes the standard library vulnerability GO-2026-6859. (#140)

### Action needed
- Update with `sudo rootfiles update` on a host, or `rootfiles fleet update --version v0.20.0` from the fleet controller. Converged hosts need no `apply` afterwards. (#135)
- Scripts that call `rootfiles` and read its exit code: argument and flag errors (unknown flag, unknown command, wrong argument count, conflicting `-o`/`--json`) now exit 2 instead of 1. Report results keep their codes (0 ok, 2 drift or findings), so tell the two apart by the `Usage:` text on stderr. Missing required flags still exit 1. (#135)
- Scripts that match error text: errors now start with `rootfiles:` instead of `Error:`, and a known fix follows on its own line. (#135, #150)
- INFO log lines (for example the commands a module runs) now need `--verbose`; `--dry-run` still shows its command previews. `check -v` only expands the report. (#135, #149)
- The deprecated spellings announced in v0.19.0 (`backup -o`, `user backup -o`, `user add --pubkey`, `config init --force`, `user passwd --suffix`) still work in this release and are removed in v0.21.0. Switch to `--dir`, `--file`, `--ssh-pubkey`, `--overwrite` and `--password` or `--file`. (#105)

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.19.0...v0.20.0

## v0.19.0

Tenant containers can be pinned to CPUs and NUMA memory nodes without a restart, commands and flags are consistent, and safety checks refuse more risky changes before they touch a host; updating a converged host changes no files and restarts no services.

### New
- `rootfiles tenant cpuset TENANT --cpus LIST --mems LIST` pins a tenant container to host CPUs and NUMA memory nodes and keeps the pinning across recreates. It updates a running container in place, without a restart; `--clear` removes the pinning. A tenant without `cpuset` in its spec is left as it is, including a pinning set by hand with `docker update`. (#130)
- `rootfiles schedule status` reports the report timer state, its calendar and the age of the saved reports, without root. (#105)
- `rootfiles fleet schedule enable --on-calendar EXPR` sets a validated report calendar on the selected hosts. (#105)

### Improved
- `rootfiles update --check` (and its `upgrade` alias) and `rootfiles tunnel update --check` run without root or the host lock, and exit 0 when current, 2 when an update is available and 1 when the lookup fails. (#100)
- `rootfiles status --module` filters the module checks and the user, GPU and tunnel details. `rootfiles config init --overwrite` replaces an existing file; `--force` still works but is deprecated. (#102)
- `ROOTFILES_YES=true` also skips the `rootfiles rollback` confirmation, as it already did for `apply` and the tunnel commands. Fleet rollouts still need an explicit `--yes`. (#102)
- `rootfiles fleet targets` honors `--host`, `--group` and `--all`, and fleet `--dry-run` previews no longer ask for `--yes`. (#105)
- Command help shows working examples, the current profile names and the exit codes of each report command. (#105)
- `rootfiles tunnel setup` keeps the configured token file and VLAN settings. (#105)
- The `dgx` profile no longer sets a tunnel VLAN address, and its `allowed_ports` lists only 80 and 443 (the SSH port is always allowed). The `gpu-server` profile no longer asks for a VLAN with an empty address. The `full` profile drops a `network` block that never enabled the firewall. (#101)
- `rootfiles config validate` rejects `modules.cloudflared.private_network.enabled: true` without an address and names the key, where `apply` used to finish without configuring anything. (#101)

### Fixed
- `apply --tunnel-token`, `tunnel setup TOKEN`, `tunnel install TOKEN` and `user passwd --password` no longer write the token or password into new audit records or into the backup notes that `rootfiles rollback` shows. (#100, #105)
- Fleet commands refuse native-only flags such as `--module`, `--profile`, `--config`, `--force` and `--tunnel-token`, and reject an invalid `-o` value before opening SSH. (#102)
- GPU and tunnel commands refuse capabilities Rocky does not support before running anything, dry runs included. (#102)
- `rootfiles backup` requires root, takes the host lock and writes an audit record; `--dry-run` still runs without root. (#102)
- When the UFW or firewalld state cannot be read, `rootfiles check` and `rootfiles status` show it as a blocked change instead of stopping the module report, and `apply` refuses SSH port changes, UFW rules, exporter setup and tenant SSH setup instead of guessing. (#103)
- On Rocky, `rootfiles check` and `rootfiles apply` no longer refuse an unchanged SSH port because the host has source-limited UFW rules or firewalld rich rules. Real port changes still get the firewall checks, including when the current port comes from the main sshd config. (#106)
- `rootfiles apply --dry-run` no longer rewrites the old single-file user metadata; check reports the old layout and how to move it. User and GPU metadata are replaced atomically and kept for `rootfiles rollback`, and a corrupt GPU ledger is reported before any allocation changes. (#114)
- `rootfiles check`, `status` and `doctor` leave the SSH runtime directories untouched. Previewed install and locale changes match what `apply` does, a failed time-sync step no longer reports success, and Docker service work is shown only when it is needed. (#114)
- `rootfiles config init --file FILE --overwrite` always leaves the file at mode 0600 and owned by you. It refuses a symlink, a special file or a file owned by another user, and a failed write keeps the previous file. Its error messages say what to do next. (#127, #128)

### Action needed
- Update with `sudo rootfiles update` on a host, or `rootfiles fleet update --version v0.19.0` from the fleet controller. Converged hosts need no `apply` afterwards. (#131)
- Scripts that run `rootfiles update --check` or `rootfiles tunnel update --check` must treat exit 2 as "update available" instead of expecting 0. (#100)
- Earlier audit logs (`/var/log/rootfiles.log` or `ROOTFILES_LOG_FILE`) and backup notes under `/var/lib/rootfiles/backups` are not rewritten. Rotate any token or password passed on the command line before this release, then review and redact those files. (#100)
- Remove native-only flags from fleet scripts and put those settings in the inventory or the remote site config. (#102)
- A DGX host that uses the tunnel's private VLAN must set `modules.cloudflared.private_network` (`enabled`, `interface`, `address`) in a site config that extends `dgx`, then run `sudo rootfiles apply --config <that file>`. A site config with `private_network.enabled: true` and no address now fails validation: add the address, pass `--vlan-address` or set `enabled: false`. (#101)
- When a change is blocked because the firewall state is unknown, inspect `sudo ufw status` or `sudo firewall-cmd --state`, repair it and rerun. A UFW DENY or REJECT rule blocks an SSH port change until it is reviewed, and source-limited SSH rules are not widened automatically. (#103)
- If `<home_base>/.rootfiles` is still a file, move it aside, create `<home_base>/.rootfiles` as a root-owned directory, place the file at `<home_base>/.rootfiles/users.json` with mode 0600 and rerun. The old layout is no longer migrated automatically. (#114)
- Replace `rootfiles backup -o/--output DIR` with `--dir DIR`; the default folder moves from `/raid/backup` to `<state dir>/system-backup`, so pass `--dir /raid/backup` to keep the old one. Replace `rootfiles user backup -o/--output FILE` with `--file FILE` and `rootfiles user add --pubkey` with `--ssh-pubkey`. The old spellings still work with a notice. Stop using `rootfiles user passwd --suffix`: it is removed in the next minor release. (#105)
- Before downgrading a host to an older release, remove `cpuset` from its tenant specs: older releases refuse a spec with that field. The container keeps its pinning. (#130)

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.18.1...v0.19.0

## v0.18.1

Updating to v0.18.0 made every host look unverified to the fleet controller; v0.18.1 restores the recorded configuration match.

### Fixed
- After `rootfiles fleet update --version v0.18.0`, the post-update check reported "remote config fingerprint is missing; inventory configuration is unverified" on hosts whose config does not set `exporter_firewall`, and the rollout stopped at the first host. Update to v0.18.1 and the check matches the last apply again. (#85)

### Action needed
- A host where `rootfiles apply` already ran under v0.18.0 recorded the v0.18.0 fingerprint. After updating it to v0.18.1, run `sudo rootfiles apply` (or `rootfiles fleet apply --host <name>`) once so the check matches again. (#85)

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.18.0...v0.18.1

## v0.18.0

rootfiles can now manage the exporter firewall guard, and node exporter, the DCGM exporter and the monitoring hub stay up when Docker restarts.

### New
- `modules.monitoring.exporter_firewall: true` installs and checks an iptables guard that admits the exporter ports only from `allow_from` (one IPv4 /32, default ports). A guard installed by hand with the same `rootfiles-exporter-firewall` helper is adopted without changing its rules. `rootfiles doctor` counts the exporters as covered only while the guard's check passes, and names a guard that rootfiles does not manage. (#84)

### Fixed
- After Docker stopped and started again (a package upgrade or a manual restart), node exporter on hosts with the exporter firewall guard stayed down until the next `rootfiles apply`, and the DCGM exporter and the monitoring hub did not come back by themselves. Exporters now keep running while the guard unit or Docker stops, refuse to start only while their firewall rules are missing, and the DCGM exporter and hub start again with Docker. (#84)

### Action needed
- On hosts with a hand-installed exporter firewall guard, set `exporter_firewall: true` under `modules.monitoring` (and `perimeter_firewall: false` unless a network firewall closes the segment), then run `sudo rootfiles apply --module monitoring` or `rootfiles fleet apply`. The apply replaces the guard's systemd drop-ins and restarts the DCGM exporter once; the firewall rules stay in place. (#84)
- Hosts with the DCGM exporter or the monitoring hub need one `rootfiles apply` after updating so their units start with Docker. (#84)

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.17.1...v0.18.0

## v0.17.1

Shared-port tenant SSH can now exempt a VPN or NAT gateway from the failed-login ban, so one user's typos no longer lock out everyone behind it.

### New
- `modules.tenant.sshpiper.failtoban_ignore` lists addresses or CIDRs that are never banned after failed logins on the shared port. (#82)

### Action needed
- On hosts where many users reach the shared port through one gateway address, add it to `failtoban_ignore` and run `sudo rootfiles apply --module tenant` (or `rootfiles fleet apply`). The apply restarts sshpiper, which clears current bans and drops sessions open through it. (#82)

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.17.0...v0.17.1

## v0.17.0

Tenant containers can now share one SSH port: with sshpiper enabled, `ssh -p 2222 <tenant>.<user>@host` reaches any tenant.

### New
- `modules.tenant.sshpiper` installs a pinned, checksum-verified sshpiper as `rootfiles-sshpiper.service` and routes `<tenant>.<user>` to each tenant's own sshd. Passwords are checked by the tenant's sshd, which also enforces the change of a temporary password; keys in the user's `~/.ssh/authorized_keys` inside the container work after the next sync. Repeated failed logins ban the address for an hour. `rootfiles tenant status` and `create` print the routed command. With `modules.network.ufw` the port is admitted in the firewall; an active UFW without it, or a port held by another process, stops `apply` before sshpiper starts. (#81)

### Improved
- `rootfiles tenant sync` runs every 2 minutes instead of daily and no longer takes the global lock, so it cannot fail while `rootfiles apply` runs. (#81)
- Reading accounts and keys from a tenant container is limited in size and time, so a tenant cannot stall a sync or an import. (#81)

### Action needed
- To use single-port SSH, set `modules.tenant.sshpiper.enabled: true` and run `sudo rootfiles apply --module tenant` (or `rootfiles fleet apply`). Existing tenants need `sudo rootfiles tenant apply <tenant>` and `sudo rootfiles tenant restart <tenant>` once before key logins work through the shared port. (#81)

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.16.0...v0.17.0

## v0.16.0

GPU hosts can now run long-lived SSH containers for outside groups with `rootfiles tenant`, each with a size-capped volume, accounts that survive image changes and GPUs from the shared allocation ledger.

### New
- `rootfiles tenant create` starts a tenant container with its own SSH port, GPUs on the NVIDIA runtime (kept after a `systemctl daemon-reload`) and a volume for `/data`, `/home` and `/tmp` capped at `--size`. Each account gets a temporary password that must be changed at first login; it is printed once and never logged. (#79)
- `rootfiles tenant image`, `gpu` and `port` recreate the container and keep the volume, the accounts' passwords, the SSH host key and GPU access. A failed check of the new container rolls back to the old one, which is otherwise kept as `tenant-<name>-prev`. (#79)
- `rootfiles tenant volume resize` grows a volume while the container runs; `--offline` shrinks an ext4 volume. `rootfiles tenant status` shows volume use, spec drift and whether the volume sizes exceed the free space. (#79)
- `rootfiles tenant user add`, `passwd`/`reset`, `restart`, `logs`, `apply`, `list` and `sync` manage accounts and containers; every change is recorded in `/var/log/rootfiles-tenant.jsonl`. (#79)
- Host accounts in the operator group can run `sudo rootfiles tenant ops <tenant> status|logs|gpu-heal|restart|reset` for the tenants that list them, without other sudo rights. (#79)
- `rootfiles tenant import` moves a hand-made container under rootfiles: it copies its accounts, passwords, host keys and the data you name into a new volume, keeps the old container stopped, and refuses before any change when the data does not fit. (#79)
- The opt-in `tenant` module (`modules.tenant.enabled`) installs a GPU device guard that restores GPU access of `--gpus` containers after a `systemctl daemon-reload`, a daily password sync, `fstrim.timer`, the operator sudoers rule and the tenant volume mount units. See `docs/tenant.md`. (#79)
- `rootfiles gpu assign` now also refuses GPUs that a tenant holds. (#79)

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.15.1...v0.16.0

## v0.15.1

Files that `rootfiles fleet apply` creates on a host are readable again by non-root users where they should be.

### Fixed
- `rootfiles fleet apply` ran the remote `apply` with a private umask, so files it created were readable only by root. On Ubuntu hosts, the new `/etc/apt/apt.conf.d/zzzzzz-rootfiles-unattended-upgrades` was 0600 and `apt` run as a regular user warned that it could not read it. `rootfiles check` now reports such a file and `rootfiles apply` makes it readable again. (#74)

### Action needed
- Update the controller (`sudo rootfiles update --version v0.15.1` on Linux; on macOS install `rootfiles_0.15.1_darwin_<arch>.tar.gz` from the release after checking `checksums.txt`), then the hosts with `rootfiles fleet update --version v0.15.1`. Hosts already applied with v0.15.0 then report `apply-pending` for `security` until `rootfiles fleet apply` fixes the file mode. (#74)

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.15.0...v0.15.1

## v0.15.0

Monitoring rule changes now take effect in the same `apply`, unattended upgrades stay security-only, and fleet rollouts can skip unreachable hosts and report hosts that still need `apply`.

### New
- `rootfiles fleet apply`, `update`, `bootstrap` and `schedule` take `--skip-unreachable`: a host whose SSH connection fails is reported `skipped-unreachable`, the rollout continues, and the run ends with a ready-to-run command for the skipped hosts. Any other failure still stops the run. (#70)
- `rootfiles fleet update` runs a read-only check with the new binary on each updated host and reports `apply-pending` with the modules that still need `rootfiles apply`. Nothing is applied automatically. Single-host `rootfiles update` prints the same hint. (#70)
- `doctor.disk.warn_percent`, `doctor.disk.fail_percent` and `doctor.disk.min_free` in the site config set the disk thresholds of `rootfiles doctor`. With `min_free`, a large disk that still has enough free space no longer raises the doctor failure alert. (#71)

### Improved
- A missing DCGM exporter prerequisite, such as the Docker NVIDIA runtime, shows as `blocked` in `rootfiles check` and fails `rootfiles apply --dry-run`. `apply` still sets up node exporter first, then fails with the fix command. (#68)
- Registering the NVIDIA runtime on a host that already has the toolkit reloads Docker instead of restarting it, so running containers keep running. (#68)

### Fixed
- `rootfiles apply` on the monitoring hub now reloads Prometheus and Alertmanager when their configuration or alert rules change. Before, the old rules stayed active until a manual reload. (#68)
- Unattended upgrades stayed open to general updates when vendor or installer files added their own origins. The managed policy now clears inherited origins, and `rootfiles check` reads the effective APT policy. (#69)
- A failed fleet host showed the first 240 characters of progress output; it now shows the remote error line. (#70)
- `rootfiles schedule enable` writes the first report right away, so a new host no longer raises `RootfilesReportStale` until the first timer run. (#71)

### Action needed
- Update the controller binary first, then the hosts with `rootfiles fleet update --version v0.15.0`. Hosts reported `apply-pending` need `sudo rootfiles apply` (or `rootfiles fleet apply`). (#70)
- On Ubuntu hosts, `apply` installs `/etc/apt/apt.conf.d/zzzzzz-rootfiles-unattended-upgrades` and backs up and removes `52rootfiles-unattended-upgrades` and a hand-placed `zzzzz-rootfiles-security-origins.conf`; `rootfiles rollback` restores them. (#69)

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.14.2...v0.15.0

## v0.14.2

The project moved from `entelecheia/rootfiles-v2` to `STAIxBWLB/rootfiles`; nothing changes on your servers.

### Improved
- Self-update, the installer and fleet rollouts now download from `STAIxBWLB/rootfiles`. Binaries installed from the old repository keep updating through GitHub redirects, and files `rootfiles` manages on servers stay byte-identical. (#53)

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.14.1...v0.14.2

## v0.14.1

The filesystem-full alert now fires only when a filesystem is about to run out of space.

### Improved
- The `FleetFilesystemNearlyFull` alert threshold rises from 90% to 99% used, so hosts that sit at a steady high usage no longer page continuously. The `doctor` disk check is unchanged. (#51)

### Action needed
- `rootfiles update` replaces only the binary. On the monitoring hub, run `sudo rootfiles apply` afterwards to regenerate the alert rules. (#51)

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.14.0...v0.14.1

## v0.14.0

Generated monitoring alerts now say which host or GPU is affected and what to check.

### New
- `modules.monitoring.hub.alertmanager_retention` sets how long Alertmanager keeps its data; unset keeps the image default. (#50)

### Improved
- Every generated host and GPU alert carries a summary and diagnostic description; expressions, severities and hold times are unchanged. The README's Telegram receiver example uses them. (#50)

### Action needed
- `rootfiles update` replaces only the binary. On the monitoring hub, run `sudo rootfiles apply` afterwards to regenerate the alert rules. (#50)

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.13.2...v0.14.0

## v0.13.2

`rootfiles backup` output is now readable only by root, and status reports match the last applied configuration.

### Fixed
- `rootfiles backup` creates its directory 0700 and its files 0600, so other local users can no longer read archived root SSH keys or `/etc` configs; the config snapshot no longer contains the tunnel token, and `--dry-run` writes nothing. (#47)
- `rootfiles status`, `check`, `doctor` and `config show` report against the config the last `apply` resolved, so overrides such as `--home-base` no longer make reports disagree with what was applied. (#48)

### Action needed
- Backups written by earlier versions are not changed. Review their permissions if other users can log in to the host. (#47)

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.13.1...v0.13.2

## v0.13.1

Subcommands now use the same configuration as the last `apply`, and user and GPU state is protected under untrusted or unusual home base layouts.

### Fixed
- `rootfiles user` and `gpu` subcommands use the config the last `apply` resolved, including `--home-base` and other overrides; before, `user add` could use a different home base than `apply`, and `--config` was ignored. (#40)
- User and GPU commands refuse to change the user or GPU databases when `<home_base>/.rootfiles` contains symlinks or files other users could have planted or modified; read-only reports warn instead. (#41)
- The SSH lockout guard no longer counts declared accounts as key logins when the users module failed or was not part of the run (for example `apply --module ssh`), which could disable password login before those accounts existed. (#42)
- A home base under `/home` is accepted on hosts where `/home` is a symlink to a root-only data drive, and detection no longer picks a btrfs subvolume or ZFS dataset of the root filesystem as a data drive. (#43)

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.13.0...v0.13.1

## v0.13.0

User homes now default to a separate data drive when one is present, and user and GPU commands refuse unsafe home bases and broken configs instead of guessing.

### Improved
- When `users.home_base` is not set, `rootfiles` keeps an existing layout, otherwise uses `<mount>/home` on the first local data drive at `/raid`, `/data` or `/nvme` on Ubuntu and DGX OS, else `/home`. Rocky keeps `/home`. `--home-base` overrides detection. Ubuntu hosts on `/home` see a one-time users change (`HOME=/home` in `/etc/default/useradd`) until the next `apply`. (#24)

### Fixed
- Home base detection no longer picks a bind mount of the root filesystem or a read-only mount as a data drive. (#29)
- `rootfiles user` and `gpu` subcommands stop with the error when the config does not load (for example a mistyped `--profile`); they used to continue with an empty config and read or write user and GPU state under `/home`. (#30)
- A custom home base (anything other than `/home`) must be controlled by root alone (every directory down to it root-owned and not group- or other-writable, no symlinks). The users and storage modules, `user add`, `user rehome` and `user restore` now refuse other bases before making changes. (#31)

### Action needed
- Fleet site configs must set `users.home_base` explicitly; the `minimal`, `rocky` and `gpu-server` profiles no longer pin it, and the inventory is rejected otherwise. (#24)
- If `rootfiles check` reports that your custom home base is refused, fix the ownership or permissions it names before the next `apply`. (#31)

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.12.2...v0.13.0

## v0.12.2

Fixes the Rocky core profile being blocked on hosts where firewalld is not running.

### Fixed
- `rootfiles apply --profile rocky` no longer stops at the SSH firewall check when firewalld is inactive. (#22)

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.12.1...v0.12.2

## v0.12.1

Fixes the GPU exporter failing to start on its default image.

### Fixed
- The `monitoring` module's default DCGM exporter image no longer fails to pull with `manifest unknown`; it now points to a published image tag. (#20)

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.12.0...v0.12.1

## v0.12.0

Adds Rocky Linux core support, a fleet controller that runs `rootfiles` across many servers over SSH, and fleet monitoring with GPU metrics.

### New
- `--profile rocky` configures Rocky Linux 8.9, 8.10 and 9.x: DNF packages, locale, timezone, accounts with `wheel`, sshd and security-only DNF updates with chronyd. Modules not yet qualified on Rocky (Docker, NVIDIA, cloudflared, storage, network, monitoring) are refused before any change. (#19)
- `rootfiles fleet status|check|doctor|apply|update` runs against hosts listed in an inventory file (start from `docs/fleet.example.yaml`) from a Linux or macOS workstation. Read-only commands run in parallel; `apply` and `update` need `--yes` plus `--host`, `--group` or `--all`, run one host at a time, stop at the first failure and accept `--timeout`. Release builds now include macOS binaries. (#19)
- The `monitoring` module adds a pinned DCGM GPU exporter and an opt-in monitoring hub (Prometheus, Alertmanager, Grafana) on one Docker host; `rootfiles fleet targets --push <hub>` updates its scrape targets without restarting it. (#19)
- `rootfiles apply --config -` reads a fully resolved config from stdin. (#19)

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.11.0...v0.12.0

## v0.11.0

A large hardening release: `apply` now guards against SSH lockout and data loss, every run backs up the files it changes so they can be restored, and new commands cover health checks, config validation and the full user account lifecycle.

### New
- `rootfiles doctor` checks the live system: SSH key access against `sshd -T`, firewall vs. SSH port, pending reboot, disk usage, NTP, GPU driver and persistence mode, failed systemd units and the last apply. It exits 2 on failures (`--strict` also on warnings). (#2)
- `rootfiles config init|validate|show` scaffolds, validates and prints the fully merged config. Profiles now merge as YAML trees (a child can set `false`), `--config` files can use `extends`, and unknown keys are rejected with line numbers. (#2)
- `check` and `status` accept `-o json`; `check` and `doctor` accept `-o prometheus`. `rootfiles schedule enable` runs `check` and `doctor` on a systemd timer and writes the reports to `/var/lib/rootfiles/`. (#2)
- Every file a run overwrites or removes is saved under `/var/lib/rootfiles/backups/<id>`, and `rootfiles rollback <id>` restores those files; packages, accounts and running services are not rolled back, and restored services may need a restart. Mutating commands require root, hold a lock, stop cleanly on Ctrl-C and write an audit log. (#2)
- Account lifecycle commands: `user del`, `lock`, `unlock`, `expire`, `key add|rm|list`, `du`, `audit` and `quota set|show|rm` (per-user disk quotas on the home base filesystem). Accounts can be declared in config under `users.accounts`. (#2)
- New `system`, `security` and `monitoring` modules (host basics, security-only unattended upgrades, fail2ban, NTP, journald cap, opt-in node exporter), NVIDIA persistence daemon and fabric manager setup, and a read-only `rootfiles gpu mig status`. (#2)
- `ssh.password_auth_users` keeps password login for listed accounts while keys are rolled out; `check` and `doctor` list accounts that can still log in only with a password. (#11)
- `modules.cloudflared.tunnel_token_file` reads the tunnel token from a root-owned 0600 file instead of the config. (#10)

### Fixed
- Hardening SSH no longer locks you out: password login is disabled only when an account can log in with a key and no account is left password-only, the sshd config is validated and reverted on failure, and UFW always admits the SSH port. (#2, #11)
- `apply` no longer deletes existing data: `storage` refuses to replace a populated directory with a symlink, and `user rehome` verifies the copy and keeps the old home as a backup. (#2)
- `apply` merges `/etc/docker/daemon.json` instead of overwriting it, which removed the NVIDIA runtime. (#2)
- Pressing Enter through the interactive `apply` prompts keeps the profile's values; it used to re-enable root and password login and disable UFW on `dgx`. (#2)
- `apply --tunnel-token`, `--user` and `--ssh-pubkey` now take effect, apt runs fully unattended, and passwords are passed through stdin and no longer appear in process lists. (#2)

### Action needed
- `rootfiles check` now exits 2 when changes are pending; update scripts that treat any non-zero exit as an error. (#2)
- `minimal` and larger profiles now enable the security baseline and a 2G journald cap, and `dgx` and `gpu-server` enable the NVIDIA persistence daemon. Run `rootfiles check` before the next `apply` to review these changes. (#2)
- After an OS reinstall, run `sudo rootfiles user restore` before `apply`, so SSH keys exist before password login is disabled. (#2)
- `rootfiles user passwd` now generates random passwords unless `--suffix` is given. (#2)

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.10.1...v0.11.0

## v0.10.1

`rootfiles update` now creates the `root` alias, but only from this version on.

### Fixed
- `rootfiles update` creates the `root` symlink after updating a binary in `/usr/local/bin` or `/usr/bin`, so hosts that never re-ran the installer get the alias on their next update. Custom install locations are left alone.

### Action needed
- A host on v0.10.0 that updates with `rootfiles update` still has no `root` alias, because the v0.10.0 updater exits after replacing the binary. Re-run the installer once: `curl -fsSL https://raw.githubusercontent.com/STAIxBWLB/rootfiles/main/scripts/install.sh | sudo bash`.

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.10.0...v0.10.1

## v0.10.0

Adds version-pinned Cloudflare tunnel updates and a short `root` command alias.

### New
- `rootfiles tunnel update` updates only `cloudflared`: it shows the current and target versions, installs the release and restarts the service only when it is enabled. `--check` reports versions without changing anything; `--version` pins a specific upstream release.
- The installer creates a `root` symlink next to `rootfiles`, so `sudo root apply` and `sudo rootfiles apply` are the same command.

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.9.1...v0.10.0

## v0.9.1

Adds the `rootfiles status` dashboard, renames `upgrade` to `update`, and makes self-update failures visible.

### New
- `rootfiles status` shows system, profile, modules, GPU allocations, tunnel and users in one view. `--profile` evaluates against another profile; color is dropped for non-terminal output and with `NO_COLOR=1`.

### Improved
- `rootfiles update` is the new name for self-update (`--check`, `--version`, `--dry-run`); `rootfiles upgrade` keeps working as an alias.
- `rootfiles check`, `gpu list`, `gpu status` and `tunnel status` share the same styled output.

### Fixed
- A failed self-update now prints the error instead of exiting silently.
- Self-update replaces the binary atomically, so a `rootfiles` run during the update never finds a missing or partial file, and no `.bak` file is left behind.

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.9.0...v0.9.1

## v0.9.0

Adds the `rootfiles status` dashboard and makes self-update failures visible.

### New
- `rootfiles status` shows system, profile, modules, GPU allocations, tunnel and users in one view. `--profile` and `--config` evaluate the host against another configuration; color is dropped for non-terminal output and with `NO_COLOR=1`.

### Improved
- `rootfiles check`, `gpu list`, `gpu status` and `tunnel status` share the same styled output.

### Fixed
- A failed `rootfiles upgrade` now prints the error instead of exiting silently.
- `rootfiles upgrade` replaces the binary atomically, so a `rootfiles` run during the upgrade never finds a missing or partial file, and no `.bak` file is left behind.

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.8.0...v0.9.0

## v0.8.0

Fixes lost GPU allocations when two assignments run at once and reports config load problems instead of hiding them.

### Fixed
- Two `rootfiles gpu assign` or `revoke` calls running at the same time no longer overwrite each other's allocations; the allocation file is locked and written atomically.
- A corrupt config file or missing profile now prints a warning instead of silently falling back to an empty configuration, which made `tunnel`, `gpu` and `user` commands behave as if nothing was configured.

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.7.0...v0.8.0

## v0.7.0

Per-user GPU allocations now also apply inside Docker containers, and the legacy `docker-compose` command works after `apply`.

### New
- `rootfiles gpu assign` installs a wrapper at `/usr/local/bin/docker` that limits containers to the user's allocated GPUs, replacing `--gpus` with the matching visible-device variables. The wrapper is removed when the last allocation is revoked.

### Fixed
- `rootfiles apply` creates a `docker-compose` symlink to the Compose plugin, so scripts that call `docker-compose` instead of `docker compose` no longer fail.

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.6.1...v0.7.0

## v0.6.1

GPU allocations made with the cgroup method are now actually enforced.

### Fixed
- `rootfiles gpu assign` with cgroup isolation no longer leaves every GPU reachable; the user slice now uses a strict device policy while keeping the standard terminal and null devices working. The legacy user metadata file `<home_base>/.rootfiles` is moved to `<home_base>/.rootfiles/users.json` automatically.

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.6.0...v0.6.1

## v0.6.0

Adds per-user GPU allocation and more user and group management commands.

### New
- `rootfiles gpu assign`, `revoke`, `list` and `status` allocate GPUs to users, enforced through `CUDA_VISIBLE_DEVICES` or cgroup device isolation.
- New `rootfiles user` subcommands: `list --system` lists system users, `id` shows a user's UID, GID and groups, `groups` lists groups, `group-add` and `group-del` manage membership, and `passwd` sets passwords in batch (generated or read from a file).

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.5.0...v0.6.0

## v0.5.0

`rootfiles check` can now show the exact commands `apply` would run.

### New
- `rootfiles check --verbose` (`-v`) lists the command behind each pending change, now filled in for the users, docker, nvidia and storage modules as well.

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.4.1...v0.5.0

## v0.4.1

Fixes `rootfiles check` reporting changes as pending on a host that is already configured.

### Fixed
- `rootfiles check` no longer reports modules as PENDING after a successful `apply`; its status queries (installed packages, UFW state) were skipped in check mode and returned empty results.

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.4.0...v0.4.1

## v0.4.0

`rootfiles` can now update itself from GitHub releases.

### New
- `rootfiles upgrade` downloads the latest (or a given) release, verifies its SHA256 checksum and replaces the installed binary. `--check` only reports whether a newer version exists; `--dry-run` shows what would happen.

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.3.0...v0.4.0

## v0.3.0

User backups now include every regular account on the host, and restores bring back SSH public keys.

### Improved
- `rootfiles user backup` scans `/etc/passwd` for all regular users (UID 1000-65533), so accounts not created by `rootfiles` are captured too.
- `rootfiles user restore` writes each user's SSH public keys to `authorized_keys` when the home directory has none.

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.2.0...v0.3.0

## v0.2.0

Adds a full-system backup command and lets you review profile settings before `apply` runs.

### New
- `rootfiles backup` saves system info, users, `/etc` configs, root's crontab and SSH directory, `/usr/local/bin`, the Docker image list and a `config-snapshot.yaml` before an OS upgrade. Restore the snapshot with `rootfiles apply --config`. Use `--output`, `--skip-docker` and `--skip-etc` to adjust it.
- `rootfiles user list --names` prints bare usernames for scripts.
- Interactive `rootfiles apply` walks through the SSH, users, docker, cloudflared, network and storage settings so you can review or change them before anything runs. `--yes` skips the prompts.

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.1.1...v0.2.0

## v0.1.1

Maintenance release: no user-facing changes.

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/v0.1.0...v0.1.1

## v0.1.0

First release of `rootfiles`, a single binary that bootstraps and configures Ubuntu and NVIDIA DGX OS servers from YAML profiles.

### New
- `rootfiles apply` configures a server from a profile (`base`, `minimal`, `dgx`, `gpu-server`, `full`); profiles inherit from each other and the profile is suggested from detected OS, DGX and GPU hardware.
- `rootfiles check` shows what `apply` would change without touching the system, and `--dry-run` previews any write operation.
- Nine modules: locale, packages, ssh, users, docker, nvidia, cloudflared, storage and network (UFW). A failing module is reported and the run continues with the rest.
- `rootfiles user` creates users under a custom home base such as `/raid/home`, backs them up to JSON, restores them after an OS reinstall, and moves homes with `user rehome`.
- `rootfiles tunnel` installs the Cloudflare tunnel binary and service and sets up a VLAN private network.
- Interactive prompts for missing values; `--yes` runs unattended.
- `scripts/install.sh` installs a checksum-verified release binary (stable channel) or builds from source (dev channel).

**Full changelog**: https://github.com/STAIxBWLB/rootfiles/commits/v0.1.0
