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
