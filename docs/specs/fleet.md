---
id: fleet
type: module
title: Fleet controller
status: active
parent: architecture
paths:
  - internal/fleet/**
  - internal/cli/fleet.go
  - internal/module/fleet_sudo.go
---

# Fleet controller

History: designed in #13 and shipped in #19 (v0.12.0); later changes in #24, #48, #53, #70, #74 and #85 (see `docs/releases/`).

## Purpose

- `rootfiles fleet` runs on an operator machine (Linux or macOS) and drives the existing per-host commands (`status`, `check`, `doctor`, `apply`, `update`, `schedule`) over the system `ssh` client against hosts listed in an operator-owned inventory.
- Hosts keep running rootfiles as a single-host tool. The controller adds no agent or daemon to them; the only host-side piece is the read-only sudoers rule written by the `users` module (`internal/module/fleet_sudo.go`).
- Monitoring exporters and the hub are separate specs: [monitoring-exporters](monitoring-exporters.md), [monitoring-hub](monitoring-hub.md). This spec owns only `fleet targets`, which renders and pushes the hub's scrape targets.

## Contracts and invariants

Decisions (still in force):

| Decision | Owner | Limits | Revisit when |
|---|---|---|---|
| Fleet features live in a controller subcommand on the operator machine that drives the existing per-host commands over SSH | user | No agent or daemon on hosts beyond what modules install | A fleet outgrows SSH fan-out (hundreds of hosts) |
| The controller shells out to the system `ssh` client | AI proposes, user picks | Inherits `~/.ssh/config` (`ProxyJump`, agents, `known_hosts`); host key checking is never disabled | A platform without OpenSSH must be supported |
| The inventory is an operator-owned YAML file kept outside this repository | user | The repo ships `docs/fleet.example.yaml` and the validator only | - |
| The controller never asks for, stores or forwards sudo passwords; remote privilege is `sudo -n` or a root login, set per host | user | A host whose sudo needs a password is reported `needs-privilege` | - |
| Mutating fleet commands run one host at a time and stop at the first failure | user | Parallelism only for read-only commands | Rollout time becomes the bottleneck |

- 2026-10-03: read-only fleet commands get passwordless privilege through a sudoers drop-in that allows only the exact `status`, `check` and `doctor` command lines, managed by the `users` module. Mutating commands keep requiring a root login or full sudo. Basis: owner decision on #13.
- Rejected: Ansible (a second source of truth without `check`/`doctor` semantics), `x/crypto/ssh` (would re-implement `ProxyJump`, agents and `known_hosts`), a pull-based agent (a new privileged daemon per host), a metrics collector in the controller (Prometheus already does it).

Invariants an agent must keep:

- **Fixed remote command lines.** OpenSSH passes the remote command to a shell as one string, so it may contain only fixed tokens and validated values, shell-quoted (shared `fleet.ShellQuote` in `internal/fleet/ssh.go`, also used by CLI scripts and backup). The destination follows `--` (`runWithTimeout`), so an inventory value cannot become an ssh option. Site configs travel on stdin, never as arguments.
- **Sudoers and remote command must match byte for byte.** `fleetSudoCommands` in `internal/module/fleet_sudo.go` lists `/usr/local/bin/rootfiles status|check|doctor -o json`; `SSHRunner.ReadOnly` in `internal/fleet/ssh.go` builds exactly `remoteBinary + " " + op + " -o json"`. Changing either side without the other breaks every `sudo: nopasswd` host. No wildcard and no `--config` is ever allowed in the rule.
- **Inventory is strict.** `LoadInventory` (`internal/fleet/inventory.go`) uses `KnownFields(true)`, accepts one YAML document, defaults `sudo: nopasswd` and `parallel: 2` (range 1-128), and rejects unsafe host names, groups, `ssh` destinations (leading `-`, whitespace, shell metacharacters, malformed `user@`/IPv6) and addresses.
- **Site configs resolve on the controller.** `config` paths resolve relative to the inventory file and load through `config.LoadSite`, which ignores the controller's `ROOTFILES_*` environment, requires `users.home_base` (#24) and runs `Validate()`. A config that resolves an inline `tunnel_token` is refused; use `tunnel_token_file`. The resolved YAML (`Host.EffectiveConfig`) and `cfg.Fingerprint()` are computed once at load.
- **Applied provenance.** Read-only results compare the remote `applied_config_sha256` with the inventory fingerprint (`compareFingerprint`). A mismatch is `drift`; a missing fingerprint is `error` with "inventory configuration is unverified", never success. The original health state is kept in `health_state`. Any change to what `config.Fingerprint` hashes shows up here (#85); see [config](config.md).
- **Selection is explicit.** `Inventory.Select` returns hosts in inventory order; `--host` and `--group` form a union; `--all` cannot be combined with them; an empty selection is an error for every fleet command, read-only included.
- **Mutations need `--yes` and a selection** (`authorizeFleetMutation`), and run through `serial` in `internal/fleet/rollout.go`. After the first host whose state is not `ok`, `apply-pending` or `skipped-unreachable`, the remaining hosts are reported `skipped`.
- **Timeouts are bounded.** Read-only SSH calls use at most 45s; mutations default to 15m and `--timeout` must be positive and at most 24h (`ValidateMutationTimeout`), checked before any SSH call. SSH runs with `BatchMode=yes` and `ConnectTimeout=15`.
- **Versions are pinned.** `update` and `bootstrap` require `--version vX.Y.Z` (`ValidateVersion`).
- **The controller writes only operator-owned files**: the run log (`AppendRunLog`, `$XDG_STATE_HOME/rootfiles/fleet.jsonl` or `~/.local/state/rootfiles/fleet.jsonl`, file 0600, directory 0700). It takes no host lock and is not in `mutatingCommands`; remote native commands keep their own root and lock preflight.

## Behaviour

- `fleet targets` honors explicit host/group/all selectors; an omitted selector retains whole-inventory rendering. Explicit empty/unmatched selectors never fall back to all hosts.
- `fleet schedule enable --on-calendar EXPR` passes a validated, shell-quoted expression; omitted calendar keeps the prior remote enable/disable command bytes. Disable refuses a calendar. Read-only/native report and apply/update wire flags remain accepted.
- Fleet dry-run skips the real-rollout yes requirement but retains explicit rollout selection; real mutations still require confirmation.

Inventory example (default path `$XDG_CONFIG_HOME/rootfiles/fleet.yaml`, else `~/.config/rootfiles/fleet.yaml`):

```yaml
defaults:
  sudo: nopasswd          # nopasswd | root | none
  parallel: 2
hosts:
  gpu01:
    ssh: gpu01            # ssh destination: config alias or user@host
    address: 203.0.113.11 # scrape address as seen from the monitoring hub
    groups: [gpu]
    config: sites/gpu01.yaml
```

- **Read-only** `fleet status|check|doctor [selection] [--parallel N] [-o text|json]`: runs `[sudo -n] /usr/local/bin/rootfiles <op> -o json` on up to `--parallel` hosts at once (`runReadPool`). Host states: `ok`, `drift` (check exit 2, a status listing unsatisfied modules, or a fingerprint mismatch), `findings` (doctor exit 2), `needs-privilege` (sudo refused), `unreachable` (ssh exit 255 or timeout), `missing` (status only: the fixed probe of hostname, `/etc/os-release`, uptime and `rootfiles --version` shows no binary), `error`. Remote status JSON with `config_error` or `module_check_error` is `error` (`inspectStatusHealth`). Text output is one row per host; JSON embeds the remote JSON verbatim. Exit 0 when every host is `ok`, 2 otherwise, 1 for controller or inventory errors.
- **`fleet apply`**: requires a site config per host. Runs remote `status` first and proceeds only on `ok`, `drift`, an unverified fingerprint or an ambiguous home base, with remote OS evidence. It then validates module capabilities locally against the remote `SystemInfo` (`module.NewRegistry().Resolve` + `config.ValidateCapabilities`), so an unsupported module on a Rocky host fails before any write. `--dry-run` pipes the config to `rootfiles apply --yes --dry-run --config -`. A real run stores the config at `/etc/rootfiles/fleet/<host>-<sha256>.yaml` (root, 0600, immutable: an existing file with other content is refused), verifies the checksum on the host, restores `umask 022` (#74) and execs `rootfiles apply --yes --config <path>` (`persistAndApplyScript`). Later `check`/`status`/`doctor` then use the applied config.
- **`fleet update --version vX.Y.Z`**: runs the remote `rootfiles update --version`, then a read-only `check` with the new binary (`postUpdateCheck`). Drift becomes `apply-pending` with the unsatisfied module names; nothing is applied. `apply-pending` does not stop the rollout, and the run ends with a hint to run `fleet check` and `fleet apply` for those hosts (#70).
- **`fleet bootstrap --version vX.Y.Z`**: skips hosts where status proves rootfiles is installed; installs only on `missing` (or unverified with OS evidence) hosts by downloading the pinned linux amd64/arm64 release and `checksums.txt` from GitHub, verifying with `sha256sum -c`, and installing `/usr/local/bin/rootfiles` root 0755.
- **`fleet schedule enable|disable`**: runs the remote `rootfiles schedule <action>`.
- **`--skip-unreachable`** (apply, update, bootstrap, schedule; #70): a host whose ssh connection fails (exit 255, not a timeout) becomes `skipped-unreachable` and the rollout continues; any other failure still stops it. The run prints a rerun command for the skipped hosts (`fleetRerunCommand`) and logs the op with the flag.
- **`fleet targets [--push <hub> --yes]`**: renders Prometheus `file_sd` JSON from the whole inventory (`RenderTargets`): one entry per host with `address` and an enabled exporter in its site config, port from `NodePort()`/`DCGMPort()`, labels `host`, `exporter` (`node`|`dcgm`) and `group_<name>=true`. `--push` requires `--yes`, a hub host whose site config enables `modules.monitoring.hub`, and a `targets_file` that passes `config.ValidateMonitoringDiscoveryPath`. The push script (`fleetTargetsPushScript`) refuses symlinks and untrusted parents, creates missing parents root 0755, and replaces the file atomically (temp file, `mv`). Prometheus re-reads `file_sd` itself; the push restarts nothing. `--dry-run` reports the path only.
- **Every mutating run and push** appends one JSON line to the run log (time, operator, command, per-host results). Remote failure reasons are the last `Error:` line or last stderr line, at most 240 characters (`shortReason`).
- **Read-only sudoers** (`users.fleet_sudo_users`, applied by the `users` module, see [users](users.md)): writes `/etc/sudoers.d/rootfiles-fleet` with one `Cmnd_Alias ROOTFILES_FLEET_READONLY` granted `NOPASSWD` to the sorted user list. Check and Apply refuse when `/usr/local/bin/rootfiles` or any ancestor is a symlink, not root-owned, or group/world-writable (`validateFleetSudoBinary`). Users must exist (Check also accepts users declared in `users.accounts`). The file goes through `installSudoersFile` (temp file 0440, `visudo -cf`, rename), and Apply verifies the effect with `sudo -l -U <user>`. An empty list removes the drop-in. Config validation of the list lives in `internal/config/monitoring_validate.go` (users module must be enabled; valid, unique names).

## Distro support

- Controller: Linux and macOS (darwin/arm64 is built in CI and released by `.goreleaser.yaml`). It needs only OpenSSH locally.
- Remote scripts assume GNU coreutils (`stat -c`, `sha256sum`, `install`, `mktemp`) plus `curl` and `tar` for bootstrap; bootstrap supports linux amd64 and arm64.
- Rocky hosts: reporting works; `fleet apply` refuses modules unsupported on the remote distro before sending anything (capability check above, see [distro-rocky](distro-rocky.md)). The sudoers drop-in works on Ubuntu and Rocky (`users` is supported on both).

## Safety

- Generated-file comparisons preserve legacy headers using the [shared managed-header rule](exec-packages.md#contracts-and-invariants); changing the marker alone never rewrites a converged file or reloads its service.

- Host key verification is inherited from OpenSSH; there is no flag to disable it.
- The only passwordless privilege the feature adds is the three-command read-only rule. `installSudoersFile` is shared with the tenant operator rule (`internal/module/tenant.go`); keep its temp-file, `visudo -cf` and rename order.
- Secrets reach hosts only as files on the host (`tunnel_token_file`, `alert_receiver_file`); inline secrets in site configs are refused at inventory load.
- Rollout practice (from the 2026-10-03 rollout): publish and download a checksummed release; keep prior binaries, site configs, state and service configs for recovery; preserve GPU drivers, running containers and existing web ports; report unreachable hosts and unsupported modules explicitly. An unreachable host is never a successful deployment.

## Tests that pin it

- `internal/fleet/fleet_test.go`: inventory strictness and selection union, unsafe endpoint values, fingerprint comparison, status health, immutable config path and script, fake-ssh exit classification, serial stop and `skipped`, `--skip-unreachable` continuing only past connection failures, `apply-pending` from the post-update check, default umask on apply, timeouts, version validation, bootstrap skip logic.
- `internal/cli/fleet_test.go`: bounded read concurrency, invalid `--timeout` failing before SSH, `targets --push` path validation and parent guards, rerun command printing.
- `internal/module/fleet_sudo_test.go`: exact sudoers content, binary ancestor trust, install/verify/idempotency, missing users, symlinked drop-in.
- `tests/scenarios/fleet.sh` (CI job `fleet-scenario` in `.github/workflows/test.yaml`): two real SSH containers with verified host keys.

Verification evidence (folded from the 2026-10-03 rollout checklist):

| Contract | Evidence |
|---|---|
| Fleet reporting and stop behaviour | Fake-ssh unit tests in `internal/fleet/fleet_test.go`; `tests/scenarios/fleet.sh` with two real SSH containers (CI `fleet-scenario`) |
| Read-only sudo boundary | Exact-command rendering in `internal/module/fleet_sudo_test.go`; `tests/scenarios/fleet.sh` runs the allowed commands and asserts sudo refuses a native `apply` for the read-only account |
| Operator support | `darwin build` step of the CI `lint` job (`GOOS=darwin GOARCH=arm64 go vet/build`); darwin targets in `.goreleaser.yaml` |
| Secrets and remote access | Inline `tunnel_token` refusal in `LoadInventory` (no dedicated test found; unverified by tests) |

- `internal/fleet/shell_quote_test.go` pins POSIX quoting bytes and synthetic shell round trips (empty, apostrophe, whitespace and metacharacters). Sharing the helper changes no remote command or archive script bytes.

## Open issues

- [#91](https://github.com/STAIxBWLB/rootfiles/issues/91): fleet commands accept and ignore `--module`, `--profile`, `--config`, `--force`, `--tunnel-token`; `-o` is validated only after the SSH work; sudoers and remote command strings must stay unchanged.
- [#94](https://github.com/STAIxBWLB/rootfiles/issues/94): remaining JSON encoder and hardcoded exit 2. Quoting is shared by `fleet.ShellQuote`; run logs intentionally use XDG state storage while inventory uses XDG config storage.
