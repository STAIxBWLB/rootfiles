---
id: ssh-access
type: module
title: SSH hardening, remote access and UFW
status: active
parent: architecture
depends-on:
  - engine
paths:
  - internal/module/ssh.go
  - internal/module/access.go
  - internal/module/network.go
  - internal/module/firewall.go
---

# SSH hardening, remote access and UFW

## Purpose

- `ssh` module: write the managed sshd drop-in (root login, password auth, max auth tries, port, per-user password exceptions), validate it and reload sshd without cutting off the operator.
- `network` module: install and enable UFW with the SSH port and configured ports admitted.
- `access.go`: the shared view of SSH ports and key-capable login accounts. `firewall.go` owns the shared UFW parser, explicit known-state inspection and UFW/firewalld reachability policy used by SSH, network, monitoring, doctor and tenant SSH.

## Contracts and invariants

- Verify installed UFW/firewalld state and port allowances before changing SSH ports. Unknown inspection never permits an Apply; a read-only Check returns a pending `ChangeBlocked` result instead of aborting the module report. `--force` cannot bypass this firewall refusal. Rocky policy is described in [distro-rocky](distro-rocky.md).
- The managed drop-in is `/etc/ssh/sshd_config.d/00-rootfiles.conf` (`sshdDropInPath`), built only by `SSHModule.buildConfig`. A `Match User` block for `ssh.password_auth_users` is always last, because a Match block runs to the end of the file. Disabling password auth also writes `KbdInteractiveAuthentication no`.
- Lockout guard (`SSHModule.lockoutGuard`): with `disable_password_auth`, refuse when no login account has a non-empty `authorized_keys` (root counts only while root login stays permitted) and no declared `users.accounts` entry with keys counts, or when a password-only account would be stranded (not keyed, not in `password_auth_users`). Declared accounts count only while the `users` module is in the run, has not failed and the home base is accepted (`declaredKeyAccounts`). An unreadable shadow file lists every keyless account. Check marks the change `(blocked: ...)`; Apply refuses unless `--force` (`rc.Force`).
- Apply never leaves a broken config: every write is followed by `sshd -t` (skipped in dry-run or when sshd is absent); on failure the previous drop-in (and on Rocky the main config) is restored and the error says the change was reverted. A non-regular drop-in is refused.
- `sshPorts` returns the configured `ssh.port`, else effective ports from `sshd -T`, else every `Port` directive in `sshd_config` and `sshd_config.d/*.conf`, else 22. It errs towards more ports because the result decides what the firewall keeps open.
- `FirewallState` distinguishes missing/inactive firewalls from failed or unrecognized probes with `Known`, `Installed` and parsed rules. `parseUFWStatus` is the only normal/verbose UFW row parser; rules retain target, source, action, direction and address family. Numeric inbound TCP allowances never use partial port matches or UDP/OUT rules. Consumers apply their own reachability/source policy to this shared representation.
- `inspectFirewall` reuses the existing Rocky UFW/firewalld probes. An installed query failure or unrecognized status is an inspection error; no direct iptables or nftables probes are added. With neither manager active, existing behavior is retained.
- A blocked firewall report is the only change returned for that module: other pending diffs are omitted until inspection/policy is repaired. This permits check/status to report the blocker without presenting writes Apply cannot safely perform.
- `network.requiredPorts` is `allowed_ports` plus `sshPorts` plus the sshpiper listen port when `modules.tenant.sshpiper.enabled`. Apply adds rules first and enables UFW last, so the SSH port is admitted before default-deny takes effect. It never removes rules and never disables UFW.

## Behaviour

- `ssh` Check: on Rocky, pending main-config include, firewall and SELinux checks (owned by [distro-rocky](distro-rocky.md)); on every distro, a drop-in diff against `buildConfig`.
- Ubuntu and Rocky share `managedSSHPortChanges`: trust an active managed Port, otherwise query `sshd -T -f sshd_config` read-only and resolve main/Include configuration when that probe fails. Relative patterns are rooted at the SSH config directory; each glob is read in lexical order, the first Port is used, and no declared Port means 22. Unreadable paths, invalid globs and Include cycles name the repair target; discovery never creates `/run/sshd`.
- When Rocky must add the managed Include, existing drop-in bytes are not active input to current-port discovery. A 22-to-custom activation therefore receives firewall verification before Include/drop-in writes, labels or reload. An already-current main port permits an Include-only repair without an unnecessary firewall query.
- Both distributions leave firewall policy alone when the port is unchanged; Rocky still verifies/adds the SELinux label for an unchanged custom port. For a changed Ubuntu port, Check reports `ufw allow <port>/tcp` only for a missing unrestricted rule under known state; restricted/family-limited rules and DENY/REJECT need operator reconciliation. Apply repeats verification, adds and confirms the planned allowance, then writes sshd. Firewall refusal is blocked Check data and a pre-write Apply error.
- Reload (`SSHModule.reload`): on socket-activated systems (`ssh.socket` active) a port change runs `systemctl daemon-reload` and restarts `ssh.socket`, then `try-reload-or-restart ssh.service`; otherwise `systemctl reload` of `ssh`/`sshd` (`sshd` first on Rocky). A reload failure is an Apply warning ("restart sshd manually"), not an error, because the validated config is already in place.
- `network` Check: under known state, report install UFW if missing, one change per missing required port, and enable if inactive; unknown state or another active manager yields a blocked change. Apply refuses those blockers before package/rule changes, otherwise installs UFW, allows missing ports and enables last. With `ufw: false` both are no-ops.

## Distro support

- `ssh`: Ubuntu, DGX OS and Rocky (Rocky paths in `ssh_rocky.go`, spec [distro-rocky](distro-rocky.md)).
- `network`: Ubuntu and DGX OS only; `ValidateCapabilities` rejects it on Rocky.

## Safety

- Read-only port discovery never creates `/run/sshd`; a failed effective-port query uses existing config/default fallback. Module fixtures redirect native config paths and reject Query executables outside the private fixture tree.

- Any change that can alter SSH reachability (port, password auth, firewall enable) must keep the lockout guard and the rules-before-enable order.
- Do not change a port before the firewall admits it, and do not treat an unknown firewall state as inactive in new code.
- `access.go` reads `/etc/passwd`, `/etc/shadow` and `authorized_keys` directly (`passwdPath`, `shadowPath` are test overrides); keep these read-only.

## Tests that pin it

- `internal/module/ssh_test.go`: `buildConfig` content and Match-last ordering, dry-run apply, lockout guard (keys, password-only accounts, unreadable shadow, refused home base, users-module outcome), refusal without `--force`, no-op when unchanged, `portLine`.
- `internal/module/network_test.go`: disabled module no-op, `parseUFWStatus` (active, inactive, exact port), required ports include SSH, `parsePortDirectives`, `sshPorts` config-file fallback.
- `internal/module/security_test.go`: `TestSSHBuildConfigHardening`.
- `internal/module/firewall_test.go`, `firewall_consumers_test.go`, `firewall_fixture_test.go`: parser fields, unknown-state blocked reports, no-write/no-enable Apply refusals, policy preservation and private fixture precedence over failing ambient UFW.
- `internal/cli/firewall_report_test.go`: non-root network/monitoring check exits 2 with blocked JSON and status returns readable JSON without root lock/audit.
- `internal/module/rocky_test.go`: Rocky SSH paths, including blocked Check data and Apply refusal before writes.
- `internal/module/doctor_test.go`: `TestDoctorSSHWarnsPasswordOnlyAccounts` (shared access helpers).
- `tests/scenarios/ssh-lockout-guard.sh` (CI `scenario` job): refusal without key logins, hardening with a key-holding operator, password-only accounts, UFW admits port 22, idempotent second run.
- CI `module` job: `ssh` and `network` on `ubuntu-22.04` and `ubuntu-24.04` via `tests/integration/run-module.sh`.

## Open issues

- [#94](https://github.com/STAIxBWLB/rootfiles/issues/94): `ssh.go` prints the `--force` warning to stdout directly; daemon-reload failure policy to unify.
- [#95](https://github.com/STAIxBWLB/rootfiles/issues/95): `# Managed by rootfiles-v2` header in the drop-in.
