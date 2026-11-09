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

Native GPU, tunnel, account and tenant mutations validate the modules they touch before running commands, including `--dry-run`. In particular, `gpu assign`/`revoke` and tunnel install/setup/restart/update/uninstall refuse Rocky's unsupported GPU/cloudflared capabilities. Read-only reports and `tunnel update --check` can still inspect the host. Contexts use the detected distribution's package backend (DNF on supported Rocky), rather than assuming APT.

## Shared firewall inspection

Ubuntu SSH port changes reuse Rocky's UFW/firewalld inspection and refusal behavior. A failed or unrecognized installed-manager query blocks changes before sshd configuration is written. For an actual SSH port change, Rocky retains its requirement for verified existing TCP allowance (both UFW address families, no conflicting deny/reject rules, and allowance in every active firewalld zone); rootfiles does not add UFW rules for Rocky. With neither UFW nor firewalld active, the existing Rocky behavior remains unchanged. No direct iptables or nftables probes are introduced.

Rocky Check and Apply skip firewall verification when the requested SSH port is unchanged, so existing source-limited or rich-rule policies remain valid. Compatible SELinux port labels are still checked and added for an unchanged custom port when needed. Ubuntu and Rocky share current-port detection: use the managed drop-in Port when its Include is already active, otherwise inspect `sshd -T` read-only or the main config's explicit Port. When `sshd -T` cannot read host keys or the socket-activated daemon has not yet created `/run/sshd`, discovery follows Include patterns read-only, in lexical order, resolving relative paths from the SSH config directory. It uses the first Port found, or 22 when none is declared. An unreadable included path or invalid glob names the path to repair before retrying. Discovery never creates the sshd privilege-separation directory.

When Rocky must add the managed Include, current-port detection ignores an inactive drop-in and inspects the existing main/effective configuration before firewall verification. Activating a pre-existing custom Port therefore cannot bypass the firewall guard.

Read-only module checks represent unknown firewall inspection as a blocked change, so check/status reports stay readable without firewall-query privileges. Apply and its mutation preflight retain the refusal before any affected component changes.
