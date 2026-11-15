# Fleet controller

`rootfiles fleet` runs on an operator's Linux or macOS machine and drives the per-host commands over the system `ssh` client. Hosts keep running rootfiles as a single-host tool; the controller adds no agent to them. Every flag: [commands.md](commands.md#rootfiles-fleet). Contract: [docs/specs/fleet.md](specs/fleet.md).

## Install and update

A macOS controller installs without root, into its own home:

```bash
curl -fsSL https://raw.githubusercontent.com/STAIxBWLB/rootfiles/main/scripts/install.sh | bash
rootfiles update            # replaces that binary; no sudo
rootfiles update --check    # exit 2 when an update exists
```

- The default destination is `$HOME/.local/bin`; `--prefix DIR` installs to `DIR/bin` instead. The installer prints the destination and warns when it is not on `PATH`.
- A binary installed as root in `/usr/local/bin` is refused by `rootfiles update` without being touched, because replacing it would need sudo. Reinstall under your own prefix: `curl ... | bash -s -- --prefix "$HOME/.local"`.
- Controller installs and updates take no host lock and need no sudoers entry; those belong to the managed Linux hosts ([self-update](specs/self-update.md)).

## Inventory

The inventory is an operator-owned YAML file kept outside this repository. Create one with `rootfiles fleet init`, then replace the example host with your own. Alternatively, use `rootfiles fleet init --from-ssh-config` to import concrete aliases. `--match` filters aliases and `--group` assigns a group. Existing files require `--overwrite`, which saves a backup first. Use `--dry-run` to print the YAML without writing. The longer example is [fleet.example.yaml](fleet.example.yaml). The default path is `$XDG_CONFIG_HOME/rootfiles/fleet.yaml`, else `~/.config/rootfiles/fleet.yaml`; `--inventory` picks another.

```yaml
defaults:
  sudo: nopasswd          # nopasswd (default), root or none
  parallel: 2             # read-only concurrency, 1-128 (default 2)
hosts:
  gpu01:
    ssh: gpu01                    # ssh destination: config alias or user@host
    address: 203.0.113.11         # scrape address as seen from the monitoring hub
    groups: [gpu, dgx]
    config: sites/gpu01.yaml      # relative to the inventory file
  workstation01:
    ssh: root@workstation01
    sudo: root                    # per-host override
```

- Unknown keys, a second YAML document, unsafe host or group names, and `ssh` or `address` values with options, whitespace or shell characters are refused.
- `ssh` is passed to OpenSSH as is, so `~/.ssh/config` aliases, `ProxyJump`, agents and `known_hosts` apply. Host key checking is never disabled.
- `config` is resolved on the controller, including file `extends`, and validated before any SSH connection. Each site config must set `users.home_base` explicitly, because the controller fingerprints it without seeing the host ([users.md](users.md#home-base)).
- A site config that resolves an inline `tunnel_token` is refused; use `tunnel_token_file` ([tunnel.md](tunnel.md#token-file)).
- `address` is needed only for [scrape targets](#scrape-targets).

## Controller health

On non-Linux controllers, `rootfiles doctor` checks the local SSH client, inventory, identity, run log directory and latest release. Offline release checks are skipped. `rootfiles status` shows inventory counts and each host's most recent logged rollout. Both accept `--inventory` and `--json`; controller JSON has `mode: "controller"`. Neither command connects to fleet hosts.

## Privilege

The controller never asks for, stores or forwards a sudo password. `sudo` sets how it runs remote commands:

| `sudo` | Remote command |
|---|---|
| `nopasswd` | Prefixed with `sudo -n`. A host whose sudo wants a password is reported `needs-privilege`. |
| `root` | Run as is; `ssh` must log in as root. |
| `none` | Run as is, as the SSH user. |

For `nopasswd` hosts whose operators should not have full sudo, `users.fleet_sudo_users` in the host's config gives the listed accounts passwordless sudo for exactly `/usr/local/bin/rootfiles status -o json`, `check -o json` and `doctor -o json`:

```yaml
users:
  fleet_sudo_users: [ops]
```

The `users` module writes it to `/etc/sudoers.d/rootfiles-fleet` and refuses when `/usr/local/bin/rootfiles` or a directory above it is a symlink, not owned by root, or writable by group or others. The accounts must exist or be declared in `users.accounts`. An empty list removes the file. Rollouts still need a root login or full passwordless sudo.

## Selection

Use `rootfiles fleet hosts` to validate the inventory and list all hosts without connecting; `--host`, `--group`, or `--all` narrows the list. `--json` outputs an array with `name`, `groups`, `ssh`, `sudo` (effective mode), and `config` fields. Every fleet report and rollout needs a selection; `fleet targets` uses the whole inventory unless one is given:

- `--host gpu01,gpu02` (comma-separated or repeated) and `--group gpu` select a union, in inventory order.
- `--all` selects every host and cannot be combined with `--host` or `--group`.
- An unknown host name, or a selection that matches nothing, is an error.

Fleet commands refuse native-host flags such as `--module`, `--profile`, `--config`, `--force`, `--home-base`, `--user`, `--ssh-pubkey`, `--tunnel-token` and `--vlan-address` before connecting; put those settings in the inventory or the site configs. `-o` must be `text` or `json`.

## Reports

```bash
rootfiles fleet status --all
rootfiles fleet check --group gpu -o json
rootfiles fleet doctor --host gpu01 --parallel 4
```

- Each host runs `rootfiles <status|check|doctor> -o json`. Up to `--parallel` hosts run at once (default from `defaults.parallel`).
- Host states: `ok`, `drift` (pending changes, or a host that applied a different config than the inventory resolves), `findings` (doctor), `needs-privilege`, `unreachable`, `missing` (status only: rootfiles is not installed), and `error`. A host that never recorded the inventory's config is `error` ("inventory configuration is unverified"), never `ok`.
- Exit codes: 0 when every host is `ok`, 2 otherwise, 1 for controller or inventory errors.
- `--view gpu` or `--view users` adds a fleet-wide section rendered from that same `status` report: GPU allocations, or managed and system account counts, per host. No extra remote command is sent, so the sudoers rule is unchanged, and a host that did not report the section shows `-` instead of failing the run. `--json` adds the section beside `hosts` under the view's own key; without `--view` the report is unchanged.
- The text report gains a `SHUTDOWN` column when any selected host has a [scheduled shutdown](#shutdown). A fleet with nothing scheduled prints exactly the table it printed before, and the JSON report is unchanged either way: each host's schedule already travels inside its own `status -o json` payload.

## Shutdown

```bash
rootfiles fleet shutdown schedule --group gpu --at 2026-11-21T18:00:00+09:00 \
  --message 'Building power work, back at 22:00' --yes
rootfiles fleet shutdown now --host gpu01 --in 2m --message 'PSU replacement' --yes
rootfiles fleet shutdown status --all
rootfiles fleet shutdown cancel --group gpu --yes
```

- One absolute UTC instant goes to every host, so the hosts stop together however long the SSH fan-out took. Scheduling itself stays serial and stops at the first failure, per the rollout contract above; only the timers fire together. `now` is the same schedule with `--in` (default `2m`, minimum `1m`).
- Every host is pre-flighted before anything is written: the remote clock must be within 30 seconds of the controller's (`date -u +%s` over plain ssh, never sudo), rootfiles there must have the `shutdown` command, and the host must have no schedule unless `--replace` is given. One failure leaves every host unchanged.
- If a host fails while being scheduled, the controller cancels every host it touched in this run, the failing one included, because that host may already have its poweroff timer enabled. Each host is then reported `cancelled`, `cancel-failed`, `error` or `skipped`; a failed cancellation prints the exact `rootfiles fleet shutdown cancel --host ... --yes` command to finish with, and a host whose follow-up cancel also failed is reported as possibly still stopping.
- `fleet shutdown cancel` is the one mutating fleet command that continues past failures, because a cancellation has to reach every host it can. It exits 2 when any host failed.
- `fleet shutdown status` uses the same host states and exit codes as `fleet status`: 0 when every host is `ok`, 2 when a host reports drift or another finding, and 1 for controller or inventory errors.
- The controller's own host is refused unless `--include-self` is given: a controller that powers off mid-rollout leaves the run unfinished. The check follows what ssh would connect to, so an alias pointed back at this machine is caught too; a host reached through a loopback port forward stays selectable, because a forwarded port is a different machine.
- Privilege is a root login or full sudo, like `apply`. The read-only sudoers rule is unchanged, and `fleet shutdown status` reads the same `status -o json` the reports already run.
- Five minutes before shutdown, new logins are blocked in applicable tenant containers only. The host remains reachable by SSH so an operator can cancel; a marked host login block left by an older version is removed on cancel.
- Host poweroffs are scheduled, not run by the controller: each host gets its own systemd timer ([specs/shutdown.md](specs/shutdown.md)). Powering hosts back on is out of scope.

A maintenance window looks like this: `fleet shutdown schedule --all --at ... --message ... --yes` a day ahead, `fleet shutdown status --all` to confirm, `fleet check --all` before the window, then the hosts stop on their own. Every `schedule`, `now` and `cancel` run appends to the [run log](#run-log) with the instant, the action and the operator's reason.

## Connectivity preflight

Run `rootfiles fleet ping --all` before a rollout to check SSH, the remote rootfiles version and the configured privilege mode. `--host`, `--group`, `--parallel` and `--json` work as for reports. Unknown or changed host keys require out-of-band verification before updating `known_hosts`; ping never accepts keys automatically. A missing binary can be installed with `fleet bootstrap` and a pinned version. For passwordless read-only access, configure `users.fleet_sudo_users`. A successful sudo probe confirms the command worked in that SSH session; cached credentials may allow it even when the rule is not NOPASSWD.

## Rollouts

```bash
rootfiles fleet apply --host gpu01 --dry-run
rootfiles fleet apply --group gpu --yes
rootfiles fleet update --group gpu --version v1.2.3 --yes --timeout 25m
rootfiles fleet bootstrap --host gpu03 --version v1.2.3 --yes
rootfiles fleet schedule enable --all --yes --on-calendar hourly
```

- Rollouts need a selection and `--yes`; a `--dry-run` preview needs no `--yes`. They run one host at a time and stop at the first failure; the remaining hosts are reported `skipped`. Try one host as a canary before widening a rollout.
- `apply` needs a site config per host. It checks the remote OS first and refuses modules that host does not support before sending anything. A real run stores the config on the host as `/etc/rootfiles/fleet/<host>-<sha256>.yaml` (root, 0600) and applies it; `--dry-run` streams it to `rootfiles apply --dry-run --config -`.
- `update` and `bootstrap` need `--version vX.Y.Z`. `bootstrap` installs a checksum-verified release only on hosts where rootfiles is missing.
- After `update`, each host runs a read-only `check` with the new binary. A host that plans changes is reported `apply-pending` with the modules affected; the rollout continues, nothing is applied, and the run exits 2 with a hint to run `fleet check` and `fleet apply` for those hosts.
- `schedule enable|disable` runs `rootfiles schedule enable` or `disable` on each host. `--on-calendar` passes an OnCalendar expression to `enable` (default `daily` on the host) and is refused with `disable`.

## Timeouts and unreachable hosts

- Read-only reports and rollout preflight checks wait at most 45 seconds per host. SSH runs with `BatchMode=yes` and `ConnectTimeout=15`.
- Each mutating remote command gets 15 minutes by default. `--timeout` sets another positive duration up to 24 hours. The limit covers one host's command, not the whole rollout.
- A timeout or cancellation can leave the current host partly changed or its remote process still running, while later hosts stay skipped. Inspect that host with `fleet status`, `fleet check` and its lock state before retrying.
- `--skip-unreachable` (on `apply`, `update`, `bootstrap` and `schedule`) continues past a host whose SSH connection fails; it is reported `skipped-unreachable`. Any other failure, a timeout included, still stops the run. The run then exits 2 and prints a command that reruns only the skipped hosts.
- A failed host's reason is the last remote `Error:` line, else the last stderr line; the full output is in the run log.

## Run log

Every rollout and target push appends one JSON line (time, operator, command, per-host results) to `$XDG_STATE_HOME/rootfiles/fleet.jsonl`, else `~/.local/state/rootfiles/fleet.jsonl` (file 0600, directory 0700). The controller takes no host lock; each remote command takes its own.

Read it back with `rootfiles fleet log`, newest first, without logging in to a host:

```bash
rootfiles fleet log --limit 20
rootfiles fleet log --host gpu01 --since 7d
rootfiles fleet log --op update --json
```

- The text report has one row per run and host: time, operation, host, state, exit code and the short reason.
- `--host` keeps runs that touched that host, `--op` takes the operation (the recorded command's first word, so `targets` also matches `targets --push`), and `--since` takes a window (`24h`, `7d`), a UTC date (`2026-11-01`) or an RFC 3339 timestamp.
- `--limit` caps the number of runs, not the rows, so a rollout keeps all its host rows together.
- `--json` reprints the stored records unchanged, one per line, so `jq` reads them directly.
- It reads a local file: no inventory, no SSH, no root. A missing log prints nothing and exits 0, and a corrupt line is named with its line number on stderr and skipped.

## Scrape targets

```bash
rootfiles fleet targets                                   # print file_sd JSON for the whole inventory
rootfiles fleet targets --group gpu                       # only the selected hosts
rootfiles fleet targets --push monitor01 --yes            # replace the hub's targets file
rootfiles fleet targets --push monitor01 --dry-run        # show the path only
```

- One target per host with an `address` and an enabled exporter in its site config; labels are described in [monitoring.md](monitoring.md#targets-and-labels).
- `--host`, `--group` and `--all` narrow the rendered targets; without them every host is rendered. The hub of `--push` is looked up in the whole inventory.
- `--push` needs `--yes` (or `--dry-run`, which prints the path only) and a hub host whose site config enables `modules.monitoring.hub`. It replaces the targets file atomically; Prometheus re-reads it without a restart. `--timeout` applies to the push.
