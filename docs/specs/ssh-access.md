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
---

# SSH hardening, remote access and UFW

## Purpose

- `ssh` module: write the managed sshd drop-in (root login, password auth, max auth tries, port, per-user password exceptions), validate it and reload sshd without cutting off the operator.
- `network` module: install and enable UFW with the SSH port and configured ports admitted.
- `access.go`: the shared view both modules (and `security`, `doctor`, `monitoring`) use for "which port is sshd on" and "who can still log in with a key".

## Contracts and invariants

- AGENTS.md rule: verify installed firewall state and port allowances before changing SSH ports; unknown state blocks the change. Rocky meets it (see [distro-rocky](distro-rocky.md)). **Ubuntu does not yet**: a known gap, tracked in #89 (see Open issues). New code must not widen the gap.
- The managed drop-in is `/etc/ssh/sshd_config.d/00-rootfiles.conf` (`sshdDropInPath`), built only by `SSHModule.buildConfig`. A `Match User` block for `ssh.password_auth_users` is always last, because a Match block runs to the end of the file. Disabling password auth also writes `KbdInteractiveAuthentication no`.
- Lockout guard (`SSHModule.lockoutGuard`): with `disable_password_auth`, refuse when no login account has a non-empty `authorized_keys` (root counts only while root login stays permitted) and no declared `users.accounts` entry with keys counts, or when a password-only account would be stranded (not keyed, not in `password_auth_users`). Declared accounts count only while the `users` module is in the run, has not failed and the home base is accepted (`declaredKeyAccounts`). An unreadable shadow file lists every keyless account. Check marks the change `(blocked: ...)`; Apply refuses unless `--force` (`rc.Force`).
- Apply never leaves a broken config: every write is followed by `sshd -t` (skipped in dry-run or when sshd is absent); on failure the previous drop-in (and on Rocky the main config) is restored and the error says the change was reverted. A non-regular drop-in is refused.
- `sshPorts` returns the configured `ssh.port`, else effective ports from `sshd -T`, else every `Port` directive in `sshd_config` and `sshd_config.d/*.conf`, else 22. It errs towards more ports because the result decides what the firewall keeps open.
- `parseUFWStatus` counts only exact-port `ALLOW` rules on tcp (or protocol-less) specs; `2222` does not satisfy `22`, udp-only rules do not admit SSH. `queryUFW` returns `ok=false` both when ufw is absent and when `ufw status` fails.
- `network.requiredPorts` is `allowed_ports` plus `sshPorts` plus the sshpiper listen port when `modules.tenant.sshpiper.enabled`. Apply adds rules first and enables UFW last, so the SSH port is admitted before default-deny takes effect. It never removes rules and never disables UFW.

## Behaviour

- `ssh` Check: on Rocky, pending main-config include, firewall and SELinux checks (owned by [distro-rocky](distro-rocky.md)); on every distro, a drop-in diff against `buildConfig`.
- `ssh` Apply on Ubuntu with `ssh.port` set: when `queryUFW` succeeds and UFW is active without the port, runs `ufw allow <port>/tcp` before writing the drop-in. This step is not reported by Check, and a failed status query is treated as "no UFW" (gap, #89).
- Reload (`SSHModule.reload`): on socket-activated systems (`ssh.socket` active) a port change runs `systemctl daemon-reload` and restarts `ssh.socket`, then `try-reload-or-restart ssh.service`; otherwise `systemctl reload` of `ssh`/`sshd` (`sshd` first on Rocky). A reload failure is an Apply warning ("restart sshd manually"), not an error, because the validated config is already in place.
- `network` Check: install UFW if missing, one change per required port not allowed, enable if inactive. Apply: installs `ufw` through the package manager, allows missing ports, then `ufw --force enable`. With `ufw: false` both are no-ops.

## Distro support

- `ssh`: Ubuntu, DGX OS and Rocky (Rocky paths in `ssh_rocky.go`, spec [distro-rocky](distro-rocky.md)).
- `network`: Ubuntu and DGX OS only; `ValidateCapabilities` rejects it on Rocky.

## Safety

- Any change that can alter SSH reachability (port, password auth, firewall enable) must keep the lockout guard and the rules-before-enable order.
- Do not change a port before the firewall admits it, and do not treat an unknown firewall state as inactive in new code.
- `access.go` reads `/etc/passwd`, `/etc/shadow` and `authorized_keys` directly (`passwdPath`, `shadowPath` are test overrides); keep these read-only.

## Tests that pin it

- `internal/module/ssh_test.go`: `buildConfig` content and Match-last ordering, dry-run apply, lockout guard (keys, password-only accounts, unreadable shadow, refused home base, users-module outcome), refusal without `--force`, no-op when unchanged, `portLine`.
- `internal/module/network_test.go`: disabled module no-op, `parseUFWStatus` (active, inactive, exact port), required ports include SSH, `parsePortDirectives`, `sshPorts` config-file fallback.
- `internal/module/security_test.go`: `TestSSHBuildConfigHardening`.
- `internal/module/rocky_test.go`: Rocky SSH paths, including unknown firewall blocking Check and Apply before writes.
- `internal/module/doctor_test.go`: `TestDoctorSSHWarnsPasswordOnlyAccounts` (shared access helpers).
- `tests/scenarios/ssh-lockout-guard.sh` (CI `scenario` job): refusal without key logins, hardening with a key-holding operator, password-only accounts, UFW admits port 22, idempotent second run.
- CI `module` job: `ssh` and `network` on `ubuntu-22.04` and `ubuntu-24.04` via `tests/integration/run-module.sh`.

## Open issues

- [#89](https://github.com/STAIxBWLB/rootfiles/issues/89): Ubuntu SSH port change proceeds on a failed `ufw status`; Check does not report the `ufw allow`; `network` Apply discards the `queryUFW` error and may run `ufw --force enable` on unknown state; three UFW parsers (`access.go`, `ssh_rocky.go`, `monitoring_firewall.go`).
- [#104](https://github.com/STAIxBWLB/rootfiles/issues/104): Rocky firewall verification in `SSHModule.Check`/`Apply` should run only when the effective port changes.
- [#93](https://github.com/STAIxBWLB/rootfiles/issues/93): `sshPorts` creates `/run/sshd` with `os.MkdirAll` on a read path; `network` Check hard-codes `apt-get install ufw`; module tests still run real `ufw`, `sshd -t`, `systemctl`.
- [#94](https://github.com/STAIxBWLB/rootfiles/issues/94): `ssh.go` prints the `--force` warning to stdout directly; daemon-reload failure policy to unify.
- [#95](https://github.com/STAIxBWLB/rootfiles/issues/95): `# Managed by rootfiles-v2` header in the drop-in.
