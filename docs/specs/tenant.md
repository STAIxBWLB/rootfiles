---
id: tenant
type: module
title: Tenant containers
status: active
parent: architecture
paths:
  - internal/tenant/**
  - internal/module/tenant.go
  - internal/module/tenant_piper.go
  - internal/cli/tenant.go
---

# Tenant containers

History: designed in #78, shipped in #79 (v0.16.0); 2-minute lock-free sync and single-port SSH through sshpiper in #80/#81 (v0.17.0); `failtoban_ignore` in #82 (v0.17.1). User guide: [docs/tenant.md](../tenant.md).

## Purpose

- Give outside groups a long-lived Docker container each on a GPU host: SSH access, a size-capped volume, accounts and host keys that survive image changes, and GPUs from the shared allocation ledger.
- `rootfiles tenant` manages tenants on one host per invocation. The opt-in `tenant` module (`modules.tenant.enabled`) prepares the host: GPU device guard, sync timer, `fstrim.timer`, operator sudoers rule, volume mount units and, when enabled, sshpiper.
- Not in scope: web portal, SSO, reservations, scheduling, fractional GPUs, multi-host placement (fleet only ships the module config), a writable-layer size limit on ext4 hosts.
- Decisions in force (from #78):
  - Built inside rootfiles, not a separate tool, to share the GPU ledger, passwords, lock, dry-run Runner and release. Revisit when a portal or self-service sign-up is required.
  - Quota is a per-tenant volume on a sparse image file on a loop device. The Docker `local` driver rejects `o=loop`, and ext4 project quota needs an unmount (verified 2026-10-20). Revisit when hosts move to xfs with pquota (then `--storage-opt size`).
  - GPU containers run on the nvidia runtime; the guard timer remains the fallback for other containers. Revisit when CDI device names become the preferred path.
  - Legacy containers are migrated by `tenant import` after the operator's external backup and cleanup.
- Rejected: a separate tool (second ledger, duplicated lock and tests); Incus (second runtime beside Docker); per-directory ext4 project quota (host-wide unmount).

## Contracts and invariants

Package boundary
- `internal/tenant` must not import `internal/module`; the module builds a `tenant.Manager` through `NewTenantManager` (`internal/module/tenant.go`) and injects the runner, paths, `ReserveGPUs` and `Piper`.
- File writes go through `Manager.Runner`. Derived files are written with `writeIfChanged` (temp file plus rename, refuses non-regular targets), because sshpiperd reads route and key files on every connection and the lock-free sync may rewrite them during an apply. The audit log is the one direct `os.OpenFile` write (`tenant.Audit`).

Spec
- One spec per tenant at `<home_base>/.rootfiles/tenants/<t>.yaml` (`SpecDir`), mode 0600, under the root-only metadata tree checked by `RequireTrustedMetadata`. `LoadSpec` decodes strictly (`KnownFields`) and rejects a file whose `name` differs from its file name.
- Fields (`Spec` in `internal/tenant/spec.go`): `name`, `image`, `gpus` (GPU indices), `ssh_port`, `ports` (`HOST:CONTAINER[/tcp|udp]`), `volume.size` (min 64M), `volume.fs` (ext4 default, or xfs), `users` (name, uid/gid 1000-65533, sudo), `operators`, `extra_binds` (`SRC:DST[:ro|rw]`, absolute, not on `/data`, `/home`, `/tmp`, `/etc/tenant-system`).
- `Validate` reports every problem at once. Tenant names match `^[a-z][a-z0-9-]{0,30}$`, contain no `.` (so sshpiper route names `<tenant>.<user>` cannot collide) and never end in `-prev`.
- `Spec.Digest` covers only settings a new container can change: name, image, sorted GPUs, SSH port, ports, extra binds. Users, operators and volume size converge without a recreate. Adding a field that needs a new container means adding it to the digest.

Host layout (root `modules.tenant.root`, default `/raid/tenants`, must be a root-only path chain: `config.RootOnlyHomeBase` in `tenantEnv`)
- `<root>/<t>/` is 0700 so host users cannot reach setuid files a tenant leaves in its volume.
- `<root>/<t>/volume.img`: sparse image, ext4 (`mkfs.ext4 -m 0`) or xfs.
- `<root>/<t>/vol`: mounted by a generated systemd `.mount` unit (`MountUnit`) with `loop,discard,nodev,nosuid`, `Before=docker.service`, `WantedBy=multi-user.target docker.service`, so volumes are mounted before Docker starts containers.
- `<root>/<t>/system`: root 0700; `init.sh`, `users`, `shadow` (name:hash:lastchg), `ssh/` host keys, `piper.pub` and `piper/` route material.

Container (`Manager.RunArgs`)
- Created with `docker run`, not compose: name `tenant-<t>`, hostname `<t>`, `--restart=always --init --ipc=host`, labels `rootfiles.tenant`, `rootfiles.tenant.spec=<digest>`, `rootfiles.tenant.gpus=<indices>`, `-p <ssh_port>:22`.
- GPUs always attach with `--runtime=nvidia --gpus "device=<indices>"`. Evidence (verified 2026-10-20): runc plus `--gpus` loses device access on any `systemctl daemon-reload`, because its devices come only from the prestart hook; the nvidia runtime lists them in the OCI spec. The daemon `default-runtime` is not changed.
- `/data`, `/home`, `/tmp` are `--mount type=bind` from `vol/data|home|tmp`; `system` is bound read-only at `/etc/tenant-system`. A bind fails on a missing source, so a container never starts on an unmounted volume and writes past the quota. Keep it a bind, never a named volume.
- Entrypoint is `/bin/sh /etc/tenant-system/init.sh` (`InitScript`), the image-agnostic boot contract. The only image requirement is `openssh-server`. On every start it creates users and groups, restores password hashes and host keys, starts sshd (trusting `piper.pub` for every account when present), runs `/data/.tenant/autostart.sh` as the first user, then sleeps. `init.sh users` re-runs only the account step.
- On a container's first boot the stored hashes replace whatever the image ships; on later boots they only seed accounts without a usable password, so a password changed inside the container survives a restart.

Passwords
- `Manager.Passwd` generates a 16-character password, sends it only on stdin to `docker exec -i ... chpasswd`, runs `chage -d 0`, syncs the hash, and returns it once for printing. It is never logged, stored or put on a command line. `tenant passwd` and `tenant reset` are the same command.
- `SyncShadow` copies usable hashes of spec users from the running container to `system/shadow`; a stopped container keeps the stored hashes. A direct bind of `/etc/shadow` is not used because `passwd` renames the file.
- Reads from a tenant container go through `readInContainer`: `timeout 10 head -c 4194304`, inside the container so symlinks resolve against its root.

Recreate (`Manager.recreate`, on digest change)
- Refuses while `tenant-<t>-prev` exists, so an earlier rollback copy is never discarded.
- Syncs hashes, warns with the writable-layer size, stops, renames to `-prev` with `--restart=no`, runs the new container and verifies it; on failure removes it and restores the old one (name, restart policy, start). On success `-prev` is kept until the operator removes it.
- `Verify`: SSH banner on `127.0.0.1:<ssh_port>` within 60s, the stored ed25519 host key via `ssh-keyscan` (skipped with a warning when no stored key or no `ssh-keyscan`), and with GPUs `nvidia-smi -L` listing the expected count.

GPU ledger
- Tenant GPUs live in the shared `gpu-allocations.json` with owner `tenant:<t>` (`ReserveTenantGPUs` in `internal/module/gpu.go`, under `withGPUDBLock`). Overlaps with users or other tenants are rejected; an empty list releases the entry. See [gpu-allocation](gpu-allocation.md).
- `Manager.Apply` reserves before converging; when it fails (not in dry-run) the ledger is reset to the GPUs in the `rootfiles.tenant.gpus` label of the container left running.

Volume quota
- Quota is per tenant volume, not host user quota. The writable layer is unbounded on ext4/overlay2 hosts; `tenant status <t>` shows its size as a warning only.
- Grow is online: `truncate`, `losetup -c`, `resize2fs` or `xfs_growfs`. Shrink needs `--offline` and ext4 (stop container and mount unit, `e2fsck -f -p`, `resize2fs`, `truncate`, start). xfs never shrinks. A spec smaller than the image only warns during apply.
- `discard` plus the stock `fstrim.timer` returns deleted space to the backing file. `Status` reports the committed size not yet allocated against free space and flags overcommit.

Operators and audit
- The module writes `/etc/sudoers.d/rootfiles-tenant-ops`: `%<ops_group> ALL=(root) NOPASSWD: /usr/local/bin/rootfiles tenant ops *` (group default `rootfiles-tenant-ops`), through `installSudoersFile`, only after `validateFleetSudoBinary` confirms the binary is root-owned and not writable.
- `tenant ops TENANT ACTION [USER]` allows only `status|logs|gpu-heal|restart|reset` (`OpsActions`), only to accounts listed in the spec's `operators` when called via sudo (`AuthorizeOps`, actor from `SUDO_USER`). It refuses every flag and unsets all `ROOTFILES_*` variables before loading config, because the sudoers wildcard would otherwise let an operator point `--config` at a spec they control. Every attempt, refusals included, is audited.
- `/var/log/rootfiles-tenant.jsonl` (0640) gets one JSON line per real mutating tenant command: time, actor, tenant, action, result. Callers pass the action and an error summary only, never secrets. Dry runs are not audited.

Single-port SSH (sshpiper)
- `modules.tenant.sshpiper` (`enabled`, `port` default 2222, `address` default all, `version` default `v1.6.1`, `failtoban_ignore`). Config validation rejects the host SSH port, a non-IP address and a non-address/CIDR ignore entry.
- Install: only versions pinned in `piperSums` with a SHA-256 per arch (x86_64, arm64); the archive is downloaded to a temp dir, checksum-verified, unpacked with `--no-same-owner` and renamed into `/usr/local/lib/rootfiles/sshpiper/<version>`. An incomplete install directory is an error, never overwritten.
- `rootfiles-sshpiper.service` runs sshpiperd with the yaml plugin over `/etc/rootfiles/sshpiper/tenants/*.yaml` chained with the failtoban plugin, one `--ignore-ip` per `failtoban_ignore` entry. Config dir is 0700; the server host key is generated by sshpiperd on first start, the upstream key `upstream_ed25519` by the module.
- Routing (`WriteRoutes`): SSH user `<tenant>.<user>` on the shared port. Two pipes per user: a key login checked by sshpiper against the user's keys copied from the container (`cleanKeys` keeps only plain key lines whose blob matches its type; options are dropped) continues with the upstream key; a password login goes to the container's sshd, which checks it and enforces the temporary-password change. A password pipe never carries a private key. Upstream host is `127.0.0.1:<ssh_port>`, verified against the tenant's stored host keys.
- `piperBlock` stops Check (as a blocked change) and Apply (before the unit changes) when another process holds the port, when the UFW state cannot be read, or when UFW is active without an allowance and `modules.network.ufw` is not succeeding in this run. A bad port therefore never stops a working listener.
- A unit change restarts sshpiper, which clears current bans and drops open sessions. Disabling sshpiper removes the unit and all route files.

## Behaviour

- Module Check (`tenantChanges`) lists: managed files that differ (guard script `/usr/local/sbin/rootfiles-gpu-guard`, guard and sync units and timers), timers not enabled (`rootfiles-gpu-guard.timer` every minute, `rootfiles-tenant-sync.timer` every 2 minutes, `fstrim.timer`), missing operator group, sudoers rule content or file mode drift, stale or disabled mount units of existing tenants, and sshpiper changes. Apply does exactly these, reloads systemd once when a unit changed (reload failure is a warning), and reports `Changed=false` in dry-run.
- The GPU device guard heals running `--gpus` containers on runc whose device filter a reload rebuilt (`systemctl set-property --runtime docker-<id>.scope DeviceAllow=...`); it never touches a container on the nvidia runtime and reports one that lost access.
- `tenant sync` (the 2-minute timer, `ExecStart=/usr/local/bin/rootfiles tenant sync`) saves hashes, refreshes `piper.pub` and routes for every tenant. It requires root but is not in `mutatingCommands`, so it takes no global lock and cannot fail while an apply runs. It must keep writing only per-tenant derived files.
- Commands: `list`, `status [TENANT]`, `create`, `apply TENANT|--all`, `user add`, `passwd`/`reset`, `gpu --gpus 0,1|none`, `image`, `port`, `volume resize [--offline]`, `restart`, `logs [--tail]`, `import`, `ops`, `sync`. Every mutating one except `sync` is in `mutatingCommands` (root plus global lock; `--dry-run` exempt). `create` and `status` print the routed `ssh -p <port> <tenant>.<user>@HOST` commands when sshpiper is on.
- Apply of one tenant (`converge`): ensure volume and mount, write system files, then create, recreate on digest mismatch, start if stopped, or re-run `init.sh users` if current. A hand-made container without the digest label is recreated.
- `tenant import TENANT --from C --size S` requires `rsync` and a running legacy container. It derives the spec from `docker inspect` (image, ports, GPU indices, binds that pass `ParseBind`, users from the container) and prints the plan; before any change it refuses when the data to copy exceeds 95% of the volume or the free space. It copies host keys (`docker cp -L`, never a symlink), hashes, `--data` and `--keep` paths (only under `/home` or `/tmp`, resolved inside the container's layer), stops the old container with `--restart=no` and keeps it for rollback; a failed start restarts the old one.

## Distro support

- Ubuntu and DGX OS only. `tenantEnv` refuses every `rootfiles tenant` command on Rocky, and `config.ValidateCapabilities` rejects the `tenant` module there ([distro-rocky](distro-rocky.md)).
- Requires Docker and the NVIDIA container runtime for GPU tenants ([docker-nvidia](docker-nvidia.md)); the module runs after `docker` and `nvidia` in `defaultOrder`.

## Safety

- Never discard tenant data: no recreate while `-prev` exists, rollback on failed verification, import keeps the old container, shrink only offline on ext4 with `e2fsck` first.
- Temporary passwords appear only in the command's own output: not in the audit log, `/var/log/rootfiles.log`, process arguments or the spec.
- Operator sudo is limited to `tenant ops` with no flags and a scrubbed environment.
- sshpiper binaries are pinned by SHA-256; an unpinned version is refused.
- Tenant-controlled files are read with size and time bounds and resolved inside the container.

## Tests that pin it

- `internal/tenant/tenant_test.go`: `TestValidate`, `TestDigestCoversContainerSettingsOnly`, `TestRunArgs`, `TestMountUnitOrdersBeforeDocker`, `TestUnitName`, `TestShadowFileKeepsUsableHashesOfSpecUsers`, `TestInitScriptSyntax`, `TestAuthorizeOps`, `TestLoadSpecRejectsUnknownFieldsAndRename`, `TestParseLegacy`, `TestPasswdNeverLogsPassword`, `TestDryRunApplyChangesNothing`, `TestFailedApplyRestoresGPULedger`, `TestCleanKeys`, `TestWriteRoutes`.
- `internal/module/tenant_test.go`: `TestReserveTenantGPUsSharesTheLedger`, `TestGPUModuleIgnoresTenantEntries`, `TestTenantModuleCheck`, `TestInstallPiperRejectsWrongChecksum`, `TestPiperUnit`, `TestPiperBlock`, `TestPiperUnitFailtobanIgnore`.
- `tests/scenarios/tenant-lifecycle.sh`, run by the `tenant-scenario` job in `.github/workflows/test.yaml` (no GPU in CI; GPU steps are stubbed, the daemon-reload survival rests on the 2026-10-20 host evidence).

## Open issues

- [#89](https://github.com/STAIxBWLB/rootfiles/issues/89): one firewall parser with an explicit unknown state; `piperBlock` uses `parseUFWStatus` from `access.go`.
- [#92](https://github.com/STAIxBWLB/rootfiles/issues/92): `tenant reset` becomes an alias of `tenant passwd`; `Example` on `tenant create`.
- [#94](https://github.com/STAIxBWLB/rootfiles/issues/94): shared `writeIfChanged` and `generatePassword` helpers, output through `internal/ui`.
- [#95](https://github.com/STAIxBWLB/rootfiles/issues/95): remove dead `Manager.RemoveRoutes`.
- Not tracked: the 2026-10-20 design stored GPU UUIDs and checked OCI `linux.devices`; the code stores indices and verifies with `nvidia-smi -L`.
