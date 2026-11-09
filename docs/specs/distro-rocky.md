---
id: distro-rocky
type: module
title: Distro capabilities and Rocky Linux
status: active
parent: architecture
paths:
  - internal/config/distro.go
  - internal/module/ssh_rocky.go
  - internal/module/security_rocky.go
  - internal/exec/rpm.go
---

# Distro capabilities and Rocky Linux

## Purpose

- Decide which operating systems rootfiles may change, and which modules and options each one supports, before any module runs.
- Own the native Rocky Linux paths: DNF/RPM package backend, Rocky sshd main-config include, SELinux SSH port labels, firewall verification for SSH port changes, and DNF security-only updates.
- This spec owns the contract behind the operator matrix in [docs/rocky.md](../rocky.md) and the Rocky lines of [docs/modules.md](../modules.md). A change to what the code accepts on Rocky updates them in the same PR.

## Contracts and invariants

- `ResolveDistro` (`internal/config/distro.go`) is the only place that maps a detected OS to a `Distro`. Supported: `ubuntu`, `dgx-os` (APT, admin group `sudo`, `/etc/default/locale`, service `ssh`) and Rocky `8.9`, `8.10`, `9.x` (DNF, admin group `wheel`, `/etc/locale.conf`, service `sshd`). Any other OS or Rocky release resolves with `Supported: false` and a `Reason`. Never infer support for another RPM distribution (RHEL, Alma, CentOS) from Rocky support.
- `ValidateCapabilities(cfg, system, enabledModules)` fails for an unsupported OS, and on Rocky fails when an enabled module is outside `SupportedModules` (Rocky core: `locale`, `system`, `packages`, `users`, `ssh`, `security`), when `modules.system.apt_mirror` is set with `system` enabled, or when `security.fail2ban` is set with `security` enabled. It runs before module execution so a mixed profile makes no change. Ubuntu and DGX OS get every module.
- Callers that must validate before mutating: `apply` (`internal/cli/apply.go`), `check`, `status`, `doctor`, the config commands on Rocky (`internal/cli/configcmd.go`) and fleet rollouts (`internal/fleet/rollout.go`). `rootfiles tenant` refuses on Rocky in `tenantEnv` (`internal/cli/tenant.go`). Adding a module to the Rocky set requires Rocky-specific evidence (unit tests plus the `rocky` CI job), not only a code path.
- `SupportedModules` derives the Ubuntu/DGX set from `config.ModuleNames()`. Rocky retains its explicit six-module core subset; sharing names does not expand support.
- `exec.NewPackageManager` (`internal/exec/package_manager.go`) picks APT or `RPM` from `ResolveDistro(...).PackageBackend` and refuses unsupported systems.
- `RPM` (`internal/exec/rpm.go`): `Update` is `dnf -q makecache` (metadata only, never an upgrade); `Install` runs `dnf -y --setopt=install_weak_deps=False install` without `--allowerasing`; every package name must match `validRPMName`. `Installed` is one read-only `rpm -q` through `Runner.Query`; `curl-minimal` satisfies `curl` only (`rpmInstalledAlternatives`). `AddKeyring`/`AddSourceList` always return an error; APT repository operations never run on Rocky.
- Rocky Check paths make no network calls and no metadata refresh. Package presence uses `rpm -q`; updateinfo verification runs the embedded DNF script with `cacheonly = True`. Only Apply calls `dnf makecache`.

## Behaviour

- Rocky automatic-file writes and service enables use the shared ordinary-file/systemd primitives without changing RPM package selection, metadata queries, file bytes or service predicates.

- SSH main config (`rockySSHDMainState`, `addRockySSHDInclude`): when `/etc/ssh/sshd_config` lacks an `Include /etc/ssh/sshd_config.d/*.conf` (or `/*`), Apply prepends one and keeps the file mode. A managed include inside a `Match` block is refused; a non-regular main config is refused. Check reports the pending include (marked blocked when the lockout guard would refuse).
- SSH port on Rocky (`ssh.port > 0`), in `SSHModule.Check` and `Apply` (`internal/module/ssh.go`), using shared current-port discovery owned by [ssh-access](ssh-access.md):
  - Only an effective port change calls `rockyFirewallAllowsSSH`; unchanged ports retain their existing restrictive policy. A pending managed Include excludes the inactive drop-in from current-port input, so activating its custom port cannot skip verification. For a change, `rockyFirewallAllowsSSH` must return true. UFW, when installed and active, must show no DENY/REJECT rule and an unrestricted `ALLOW` for `<port>/tcp` from `Anywhere` in both IPv4 and IPv6. firewalld, when running, must have no rich rules in any active zone (default zone if none active) and must allow the port (or service `ssh` for port 22) in every one. Shared inspection in `internal/module/firewall.go` treats query failure or unrecognised output as unknown; Check reports it as a blocked change and Apply refuses before writes. `not running` with exit 252 counts as no firewalld. The parsed model is owned by [ssh-access](ssh-access.md).
  - SELinux remains checked for every explicit custom port, even unchanged: `rockySSHSELinuxPortLabeled` requires `semanage`; a port already labeled with another type is refused. Apply labels the port `ssh_port_t` (`ensureRockySSHSELinuxPort`) and removes the label it added if a later step fails. Port 22 needs nothing.
  - rootfiles never opens or broadens a firewall on Rocky.
- Validation: after writing, Apply runs `sshd -t` and `verifyRockySSHEffective`, which compares `sshd -T` with the configured root login, password auth, port and max auth tries, and checks each `password_auth_users` entry with `sshd -T -C user=...`. On failure the main config and the drop-in are restored. Reload tries `sshd` before `ssh`.
- Security (`rockySecurityCheck`, `rockySecurityApply`): packages `dnf-automatic` and `chrony`. With `unattended_upgrades`, Check verifies cached updateinfo for `baseos` and `appstream` (absent or unparseable metadata is a pending refresh, expired but parseable metadata is not drift); Apply runs `dnf makecache`, then verifies strictly (expired or missing required metadata is an error) before writing `/etc/dnf/automatic.conf` (`upgrade_type = security`, `reboot = never`, NVIDIA/CUDA/DCGM excludes) and enabling `dnf-automatic-install.timer`. Other enabled repositories without usable updateinfo give an Apply warning (`coverage gap`), never a claim of protection. No platform-python or python3 with the DNF API is an error. `time_sync` enables `chronyd`.

## Distro support

- Supported matrix: [docs/rocky.md](../rocky.md); per-module conditions: [docs/modules.md](../modules.md). Unsupported on Rocky: `docker`, `nvidia`, `gpu`, `cloudflared`, `storage`, `network`, `monitoring`, `tenant`, `system.apt_mirror`, `security.fail2ban`.
- `internal/config/profiles/rocky.yaml` enables only the core modules with native RPM package names and `fail2ban: false` (owned by [config](config.md)).

## Safety

- RPM implements package operations but not the separate APT repository capability. Docker/NVIDIA refuse that missing capability before mutations, in addition to existing distro validation.
- The shared managed-header comparison keeps a legacy `automatic.conf` unchanged when its only difference is the management marker.

- Capability errors happen before any host change; never move a capability check after a module's Apply.
- Unknown firewall state blocks an SSH port change. Do not add a path that changes `Port` on Rocky without `rockyFirewallAllowsSSH`.
- Never relabel an SELinux port that another type owns.
- Never claim security-only coverage for a repository whose updateinfo was not verified.

## Tests that pin it

- `internal/config/distro_test.go`: distro matrix, capability rejection before apply, `apt_mirror` and fail2ban rejection, unchanged Ubuntu module set.
- `internal/exec/rpm_test.go`: DNF selection on Rocky, `rpm -q` parsing, `curl-minimal`, DNF install args, APT repository refusals.
- `internal/module/rocky_test.go`: include placement and Match refusal, stock 8.9 check-then-apply convergence, restore on invalid candidate, firewall fail-closed cases (UFW and firewalld, rich rules, unrestricted allowance), SELinux labeling, effective-value verification, security-only policy, cache-only updateinfo check, Apply refresh-then-prove ordering, coverage gap not looping, Rocky profile module set.
- `internal/module/locale_test.go` (Rocky locale paths) and `internal/module/rocky_accounts_test.go` (`wheel` admin group).
- CI job `rocky` (`.github/workflows/test.yaml`, matrix `rocky-8.9`, `rocky-8.10`, `rocky-9`) runs `tests/run-rocky.sh` on the `tests/integration/Dockerfile.rocky-*` systemd fixtures. It is the evidence for:
  - native package, locale and security behaviour: traps on `apt`, `apt-get`, `dpkg` and `locale-gen` fail the run if dry-run, apply or repeat apply calls them;
  - security-only updates: readback of `/etc/dnf/automatic.conf` (security, `reboot = never`, NVIDIA/CUDA excludes) and an active `dnf-automatic-install.timer`, with `baseos` and `appstream` enabled;
  - identity and SSH access preservation: home, shell and password hash unchanged, managed and pre-existing key logins over real loopback sshd, root key login refused;
  - idempotency: a second apply reports every core module already satisfied; check exits 2 before apply; status and doctor read the host;
  - installer path: `scripts/install.sh` secure_path link and refusal to overwrite unrelated paths.
- Fixture caveats: the containers share the Ubuntu runner kernel, so SELinux enforcement is not exercised (SELinux command and refusal paths have unit coverage only; production non-default ports need the firewall and labeling prerequisites). When the runner's AppArmor `unix-chkpwd` profile denies Rocky's PAM helper, `tests/run-rocky.sh` relocates an identical, checksummed setuid helper inside the disposable fixture only; PAM, shadow permissions and host policy stay intact, and production code never does this.

## Open issues

- [#91](https://github.com/STAIxBWLB/rootfiles/issues/91): `tunnel` and `gpu` mutations skip `ValidateCapabilities` and always build an APT backend.
