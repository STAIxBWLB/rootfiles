# Rocky Linux

rootfiles supports a core profile on Rocky Linux 8.9, 8.10 and 9. Other Rocky releases and other RHEL derivatives are outside this support; Rocky support does not imply support for another RPM distribution. Contract: [docs/specs/distro-rocky.md](specs/distro-rocky.md).

## Supported matrix

- Supported: `locale`, `system` (without `apt_mirror`), `packages`, `users`, `ssh`, and `security` with limits (without `fail2ban`).
- Unsupported: `docker`, `nvidia`, `gpu`, `cloudflared`, `storage`, `network`, `monitoring`, `tenant`. Capability validation refuses these when enabled, before any module runs and in dry runs too, and `rootfiles tenant` refuses to run. No firewall, container, GPU, tunnel, storage or monitoring stack is changed.
- The conditions of each supported module (locale packs, the sshd Include, SELinux port labels, DNF security updates) are in the Rocky lines of [modules.md](modules.md).

## Getting started

- Start from `--profile rocky`. It uses DNF/RPM package names, `wheel` for administrators, and chronyd; it leaves `fail2ban` off.
- Review package names and SSH settings for the target before applying. A reduced configuration does not enable unsupported modules; requesting one gives an explicit capability error.
- Home base detection keeps `/home` ([users.md](users.md#home-base)).
- Backup and rollback restore managed files; they do not uninstall packages, reverse service effects or move user data. Verify SSH access after any change before ending the administrative session.

## Verification

- The `rocky` CI job ([development.md](development.md#ci-jobs)) runs the native core profile in Rocky 8.9, 8.10 and 9 containers: `check` before and after `apply`, the effective sshd settings, the DNF automatic security timer, a second idempotent `apply`, and the installer.
- It does not cover rollback or SELinux behaviour on the container host.
- Earlier deployments on Rocky 8.9 and 8.10 covered locale, timezone, journald limits and users.

## Native subcommands

GPU, tunnel, account and tenant commands that change the host check the capabilities of the modules they touch before running anything, `--dry-run` included. `gpu assign`, `gpu revoke` and the tunnel `setup`, `restart`, `update` and `uninstall` commands therefore refuse on Rocky. Read-only reports and `tunnel update --check` still inspect the host. Package work uses DNF on Rocky.

## Firewall and SSH port

The shared rules are in [modules.md](modules.md#firewall-state). On Rocky in addition:

- A port change needs an existing, verified TCP allowance for the new port: in UFW for both address families with no conflicting deny or reject rule, or in every active firewalld zone. rootfiles never adds or widens a rule; prepare source-, family- or zone-limited and rich-rule policies yourself and verify from the console.
- The port also needs a compatible SELinux `ssh_port_t` label. The label is checked, and added when needed, for an unchanged custom port too.
- With neither UFW nor firewalld active, no firewall check runs.
- When Apply must add the managed `Include` to the main sshd config, an inactive drop-in is ignored and the current port comes from the existing main and effective config, so activating a custom port in an old drop-in cannot skip the firewall check.
