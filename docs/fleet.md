# Fleet controller

`rootfiles fleet` runs on an operator's Linux or macOS machine and drives the per-host commands over the system `ssh` client. Hosts keep running rootfiles as a single-host tool; the controller adds no agent to them. Contract: [docs/specs/fleet.md](specs/fleet.md).

## Inventory

The inventory is an operator-owned YAML file kept outside this repository. Start from [fleet.example.yaml](fleet.example.yaml). The default path is `$XDG_CONFIG_HOME/rootfiles/fleet.yaml`, else `~/.config/rootfiles/fleet.yaml`; `--inventory` picks another.

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

Every fleet command except `fleet targets` needs a selection, the read-only ones included:

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

## Rollouts

```bash
rootfiles fleet apply --host gpu01 --yes --dry-run
rootfiles fleet apply --group gpu --yes
rootfiles fleet update --group gpu --version v1.2.3 --yes --timeout 25m
rootfiles fleet bootstrap --host gpu03 --version v1.2.3 --yes
rootfiles fleet schedule enable --all --yes
```

- Rollouts need a selection and `--yes`. They run one host at a time and stop at the first failure; the remaining hosts are reported `skipped`. Try one host as a canary before widening a rollout.
- `apply` needs a site config per host. It checks the remote OS first and refuses modules that host does not support before sending anything. A real run stores the config on the host as `/etc/rootfiles/fleet/<host>-<sha256>.yaml` (root, 0600) and applies it; `--dry-run` streams it to `rootfiles apply --dry-run --config -`.
- `update` and `bootstrap` need `--version vX.Y.Z`. `bootstrap` installs a checksum-verified release only on hosts where rootfiles is missing.
- After `update`, each host runs a read-only `check` with the new binary. A host that plans changes is reported `apply-pending` with the modules affected; the rollout continues, nothing is applied, and the run exits 2 with a hint to run `fleet check` and `fleet apply` for those hosts.
- `schedule enable|disable` runs `rootfiles schedule` on each host with its default calendar.

## Timeouts and unreachable hosts

- Read-only reports and rollout preflight checks wait at most 45 seconds per host. SSH runs with `BatchMode=yes` and `ConnectTimeout=15`.
- Each mutating remote command gets 15 minutes by default. `--timeout` sets another positive duration up to 24 hours. The limit covers one host's command, not the whole rollout.
- A timeout or cancellation can leave the current host partly changed or its remote process still running, while later hosts stay skipped. Inspect that host with `fleet status`, `fleet check` and its lock state before retrying.
- `--skip-unreachable` (on `apply`, `update`, `bootstrap` and `schedule`) continues past a host whose SSH connection fails; it is reported `skipped-unreachable`. Any other failure, a timeout included, still stops the run. The run then exits 2 and prints a command that reruns only the skipped hosts.
- A failed host's reason is the last remote `Error:` line, else the last stderr line; the full output is in the run log.

## Run log

Every rollout and target push appends one JSON line (time, operator, command, per-host results) to `$XDG_STATE_HOME/rootfiles/fleet.jsonl`, else `~/.local/state/rootfiles/fleet.jsonl` (file 0600, directory 0700). The controller takes no host lock; each remote command takes its own.

## Scrape targets

```bash
rootfiles fleet targets                                   # print file_sd JSON for the whole inventory
rootfiles fleet targets --push monitor01 --yes            # replace the hub's targets file
rootfiles fleet targets --push monitor01 --yes --dry-run  # show the path only
```

- One target per host with an `address` and an enabled exporter in its site config; labels are described in [monitoring.md](monitoring.md#targets-and-labels).
- `--push` needs `--yes` and a hub host whose site config enables `modules.monitoring.hub`. It replaces the targets file atomically; Prometheus re-reads it without a restart. `--timeout` applies to the push.
