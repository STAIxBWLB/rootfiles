# Rocky Linux compatibility

The Rocky profile targets Rocky Linux 8.9, 8.10, and Rocky Linux 9. Other Rocky minor releases and RHEL derivatives are outside this compatibility claim. The profile is selected as `rocky` and uses DNF/RPM package names.

| Module | Rocky status | Conditions |
| --- | --- | --- |
| `locale` | Supported | Uses `/etc/locale.conf`, `localectl`, and installed language packs. A requested locale must already be available through the installed glibc language packs. Timezone data comes from `tzdata`. |
| `system` | Supported | Hostname, sysctl, swap, and journald operations use existing system tools. `apt_mirror` is rejected; configure DNF repositories separately. |
| `packages` | Supported | Package names must be native RPM names. |
| `users` | Supported | Administrator membership uses `wheel`; existing homes, keys, passwords, and other groups are preserved. |
| `ssh` | Supported | If the main sshd config lacks `/etc/ssh/sshd_config.d/*.conf`, Apply backs it up and adds a global Include at the top while preserving its mode. It validates `sshd -t` and effective settings before reload, restoring the main config and managed drop-in on validation failure. SSH port changes require a compatible SELinux `ssh_port_t` label and unrestricted TCP allowances for both address families in UFW, or an allowance in every active firewalld zone. Source-, family-, zone-limited, or complex deny/rich-rule policies need operator preparation and console verification; rootfiles never opens or broadens firewall policy. |
| `security` | Supported with limits | `dnf-automatic-install.timer` applies security updates only from Rocky `baseos` and `appstream`, excludes NVIDIA/CUDA packages, and sets reboot to `never`. Check reads and parses the DNF cache without refreshing it; expired but parseable metadata does not make a correct policy appear drifted, while absent or malformed required metadata reports pending refresh. Apply refreshes metadata and strictly verifies fresh `updateinfo` for both required repositories before writing policy. This Check behavior does not promise current patches. Other enabled repositories without usable `updateinfo` produce an explicit coverage warning on Apply; they are not covered by the security-only guarantee. `chronyd` provides time synchronization. `fail2ban` is rejected unless a later release verifies an explicit EPEL configuration. |
| `docker`, `nvidia`, `gpu`, `cloudflared`, `storage`, `network`, `monitoring`, `tenant` | Unsupported | Rocky capability validation rejects these enabled modules before module execution, and `rootfiles tenant` refuses to run. No firewall, container, GPU, tunnel, storage, or monitoring vendor stack is changed. |

Use `--profile rocky` as the starting point for a Rocky host. Review package names and SSH settings for the target before applying. A reduced configuration does not enable unsupported modules; requesting one produces an explicit capability error.

The Rocky 8.9/8.10 deployment evidence covered locale time zone, journald limits, and users. Rocky 8 and Rocky 9 CI plus effective SSH, DNF security policy, installer path, idempotency, and rollback checks remain required before treating this compatibility scope as release verified.

## Native subcommands

Native GPU, tunnel, account and tenant mutations validate the modules they touch before running commands, including `--dry-run`. In particular, `gpu assign`/`revoke` and tunnel install/setup/restart/update/uninstall refuse Rocky's unsupported GPU/cloudflared capabilities. Read-only reports and `tunnel update --check` can still inspect the host. Contexts use the detected distribution's package backend (DNF on supported Rocky), rather than assuming APT.

## Shared firewall inspection

Ubuntu SSH port changes reuse Rocky's UFW/firewalld inspection and refusal behavior. A failed or unrecognized installed-manager query blocks changes before sshd configuration is written. For an actual SSH port change, Rocky retains its requirement for verified existing TCP allowance (both UFW address families, no conflicting deny/reject rules, and allowance in every active firewalld zone); rootfiles does not add UFW rules for Rocky. With neither UFW nor firewalld active, the existing Rocky behavior remains unchanged. No direct iptables or nftables probes are introduced.

Rocky Check and Apply skip firewall verification when the requested SSH port is unchanged, so existing source-limited or rich-rule policies remain valid. Compatible SELinux port labels are still checked and added for an unchanged custom port when needed. Ubuntu and Rocky share current-port detection: use the managed drop-in Port when its Include is already active, otherwise inspect `sshd -T` read-only or the main config's explicit Port. When `sshd -T` cannot read host keys or the socket-activated daemon has not yet created `/run/sshd`, discovery follows Include patterns read-only, in lexical order, resolving relative paths from the SSH config directory. It uses the first Port found, or 22 when none is declared. An unreadable included path or invalid glob names the path to repair before retrying. Discovery never creates the sshd privilege-separation directory.

When Rocky must add the managed Include, current-port detection ignores an inactive drop-in and inspects the existing main/effective configuration before firewall verification. Activating a pre-existing custom Port therefore cannot bypass the firewall guard.

Read-only module checks represent unknown firewall inspection as a blocked change, so check/status reports stay readable without firewall-query privileges. Apply and its mutation preflight retain the refusal before any affected component changes.
