# Tenant containers design

Source: STAIxBWLB/rootfiles#78 (the owner authorized implementation and deployment on 2026-10-20). User guide: [docs/tenant.md](../tenant.md).

## Context

GPU hosts give external organizations a long-lived Docker container each. Tenants SSH into their container through a published host port. Today this is hand-managed with docker compose files, and it fails in ways rootfiles can prevent.

- verified 2026-10-20: every container started with `--gpus` on the runc runtime lost GPU access after a `systemctl daemon-reload`. New CUDA processes failed with `Failed to initialize NVML: Unknown Error`. The reload came from an unattended systemd upgrade. A container started with `--runtime=nvidia` kept access.
- verified 2026-10-20: in the OCI spec, `--runtime=nvidia` lists `/dev/nvidia*` under `linux.devices` with cgroup allow rules. The runc + `--gpus` spec has no device entries, because the prestart hook injects the devices.
- verified 2026-10-20: `systemctl set-property --runtime docker-<id>.scope DeviceAllow=...` restores access without a restart. It survives later reloads and goes away with the container. A host-local guard timer built on this healed four containers.
- verified 2026-10-20: one tenant holds about 0.9 TiB in the container writable layer (`/home`, `/tmp`). Recreating the container to change its image or GPUs would lose that data.
- verified 2026-10-20: the tenant user's password and the sshd host keys live only in the container. A new image changes both.
- verified 2026-10-20: the data volume is ext4 without the `quota`/`project` features, and Docker uses overlay2. So `--storage-opt size` is unavailable, and directory project quota needs an unmount.
- verified 2026-10-20: the Docker `local` volume driver rejects `o=loop` (`loop: invalid argument`). A volume on a pre-attached loop device (`losetup` + `--opt device=/dev/loopN`) stopped writes at its 1 GiB size and grew to 2 GiB online with `losetup -c` and `resize2fs`.
- verified 2026-10-20 (kernel 6.8, loop-mounted image bound as a Docker volume, writer running): growing ext4 and xfs from 1 to 2 GiB took effect in the container at once, and the writer kept running. Online shrink failed on both (`On-line shrinking not supported`; xfs experimental shrink returned `Invalid argument`). ext4 shrank offline (unmount, `e2fsck`, `resize2fs`, `truncate`) with data intact. xfs cannot shrink. With `discard`, deleted files returned space to the image at once.
- verified 2026-10-20: no open-source tool fits a single-host Docker setup with long-lived SSH tenant containers, in-container accounts and per-volume quota. Incus needs a runtime change. Backend.AI and Coder are session or workspace platforms without ext4 volume quota. The loopback and xfs volume plugins were last updated in 2019 and 2018.
- rootfiles already has a GPU allocation ledger (`internal/module/gpu.go`), password issuance with `--expire` (`internal/module/users.go`), a precedent for limited sudo (`internal/module/fleet_sudo.go`), systemd unit writing, and a lock and dry-run Runner.

Implementation starts only on the owner's go-ahead.

## Decisions

| Decision | Owner | Limits | Revisit when |
|---|---|---|---|
| Build it inside rootfiles as `rootfiles tenant`, plus a host-prep module, not as a separate tool | AI proposes, user picks | No portal, SSO, reservations or scheduling. One host per invocation, the same as `user` and `gpu assign` | A web portal or self-service sign-up becomes a requirement |
| Quota is per tenant Docker volume, backed by a sparse image file on a loop device. Host user quota is not used | user | Writable layer is unbounded on ext4 hosts and gets a usage warning only | Hosts move to xfs+pquota (then add `--storage-opt size`) |
| GPU containers run with `runtime: nvidia` plus device IDs | AI proposes, user picks | The guard timer stays as a fallback for other containers | A future toolkit or Docker release makes runc + `--gpus` reload-safe |
| The NVIDIA attach method is declared per tenant (`gpu.runtime`, required) and set per container with `--runtime=nvidia`. The daemon `default-runtime` is not changed | user | Accepted value today: `nvidia`. Containers on runc + `--gpus` are reported as drift and converted on the next `apply` | CDI device names (`nvidia.com/gpu=<uuid>`) become the preferred path in Docker and the toolkit |
| Existing tenants are migrated by deleting data after their external backup and cleanup, then copying the remainder into a new quota volume | user | A tenant's remaining data must fit the host's free space with margin | The cleanup does not get the data under the agreed size |

## Design

- **Tenant spec** `<home_base>/.rootfiles/tenants/<t>.yaml`. It is root-owned and passes the trusted metadata check. Fields:
  - `image`
  - `gpu.runtime` (required when `gpu.devices` is set; only `nvidia` is accepted)
  - `gpu.devices: [uuids]` (an index given at `create` is resolved to the GPU UUID and stored as the UUID, because indices can change)
  - `ssh_port`
  - `ports` (extra)
  - `volume.size`
  - `volume.fs` (ext4 by default, xfs optional)
  - `users`: name, uid, gid, sudo
  - `operators` (host accounts allowed limited actions)
  - `extra_binds` (legacy paths)
- **Layout**:
  - `/raid/tenants/<t>/volume.img` (sparse)
  - `/raid/tenants/<t>/vol` (loop mount through a generated systemd `.mount` unit that starts at boot before docker)
  - `/raid/tenants/<t>/system` (root 0700: host keys, password hashes, user list)
  - Docker named volume `tenant-<t>` (`type=none,o=bind,device=/raid/tenants/<t>/vol`). Its subdirectories `data`, `home` and `tmp` are mounted at `/data`, `/home` and `/tmp`. `system` is mounted read-only at `/etc/tenant-system`.
- **Image-agnostic boot contract**: rootfiles bind-mounts a read-only entrypoint. On every start it:
  - creates the users and groups;
  - applies the password hashes with `chpasswd -e`;
  - restores the host keys;
  - starts sshd;
  - runs `/data/.tenant/autostart.sh` as the tenant user if it exists;
  - then sleeps.

  The only requirement on an image is `openssh-server`.
- **Containers** are created by `docker run`, not compose. Settings: `--runtime=nvidia --gpus device=<uuid,...>`, `-e NVIDIA_DRIVER_CAPABILITIES=compute,utility`, `--restart=always`, `--ipc=host`, labels `rootfiles.tenant=<t>`, `rootfiles.tenant.spec=<sha>` and `rootfiles.tenant.gpu.runtime=nvidia`. Check compares the label sha with the spec and reports drift.
- **GPU attach check** (Check, and after every create, recreate and restart):
  - `HostConfig.Runtime` must be `nvidia`;
  - `HostConfig.DeviceRequests` device IDs must equal the spec UUIDs;
  - the OCI spec `linux.devices` must list `/dev/nvidiactl`, `/dev/nvidia-uvm` and one `/dev/nvidia<N>` per assigned GPU.

  A mismatch is drift: Check reports it, and `apply` recreates the container. A tenant container on runc + `--gpus` fails the check, because its devices come only from the prestart hook and are lost on `daemon-reload` (verified 2026-10-20). `tenant import` converts such containers.
- **Passwords**:
  - `tenant passwd <t> <user>` generates a password, pipes it to `chpasswd` inside the container, runs `chage -d 0` and stores the hash in `system`. The temporary password is printed once and never logged or put on a command line.
  - `tenant reset` is the same flow.
  - Before every recreate, and once a day, the in-container shadow hashes are synced back to `system`. A direct bind of `/etc/shadow` is rejected because `passwd` renames the file.
- **Recreate** on an image, GPU or port change:
  - sync hashes, then stop;
  - rename the old container to `<name>-prev` with restart=no;
  - run the new container, then verify GPU (`nvidia-smi -L`), sshd banner and host key fingerprint;
  - on failure, roll back automatically.

  Before acting, it prints the old writable-layer size as a loss warning.
- **Volume quota**:
  - `tenant volume resize <t> <size>` grows the volume online (`truncate`, `losetup -c`, then `resize2fs` or `xfs_growfs`). Shrinking while running is refused. `--offline` (ext4 only) stops the container, then runs `e2fsck`, `resize2fs` and `truncate`.
  - Check reports used/size per volume and the sum of committed sizes against free space on the backing filesystem, and warns on overcommit.
  - A weekly `fstrim` gives deleted space back to the backing file.
- **GPU ledger**: tenant allocations go into the existing `gpu-allocations.json` with a `tenant:<t>` owner. Overlaps with users or other tenants are rejected.
- **Host-prep module** `tenant` (off by default):
  - installs the gpu device guard timer (`systemctl set-property --runtime` DeviceAllow heal, every minute);
  - writes the volume mount units;
  - writes one sudoers rule, `%rootfiles-tenant-ops ALL=(root) NOPASSWD: /usr/local/bin/rootfiles tenant ops *`.

  `tenant ops` checks `SUDO_USER` against the spec's `operators` and allows only `status|logs|gpu-heal|restart|reset`.
- **Audit**: one JSONL line per mutating tenant command in `/var/log/rootfiles-tenant.jsonl` (time, actor, tenant, action, result).
- **Legacy import** `tenant import <t> --from <container>`:
  - writes the spec from `docker inspect`;
  - copies the host keys and user hashes;
  - copies the data the tenant kept after cleanup into the new volume, with `rsync -aHAX` and a size check before the copy;
  - copies the writable-layer `/home` and `/tmp` keep-list.

  The old container is kept stopped as a rollback until the operator removes it.
- **Rejected alternatives**:
  - A separate tool would re-implement the ledger, passwords, lock, release and tests, and would split GPU allocation across two ledgers.
  - Incus would replace Docker images and compose assets, and would add a second runtime next to existing Docker services.
  - Direct per-directory ext4 project quota needs a host-wide unmount.

## Changes

- `internal/tenant/` (new): spec load/validate, container create/recreate/rollback, entrypoint asset, password sync, volume image and mount units, ledger access, audit.
- `internal/cli/tenant.go` (new): `tenant create|apply|status|list|user add|passwd|reset|gpu|image|port|volume resize|restart|logs|import|ops`. Register the mutating ones in `mutatingCommands`.
- `internal/module/tenant.go` (new): host-prep module (guard timer, mount units, sudoers). Add it to `defaultOrder` and `NewRegistry`, disabled unless `tenant.enabled`.
- `internal/module/gpu.go`: allow a `tenant:<t>` owner in the ledger, with overlap checks across users and tenants.
- `internal/config/config.go`, `validate.go`: `tenant.enabled`, `tenant.root` (default `/raid/tenants`), `tenant.ops_group`.
- `tests/scenarios/`: a tenant lifecycle scenario on the Docker test image (GPU steps stubbed).
- `docs/`: tenant guide, config reference, release notes.

## Acceptance criteria

- [ ] AC1: `tenant create` with image, 1 GPU, SSH port, one user and a 10 GiB volume gives a running container. A temporary password logs in over SSH and is forced to change at first login.
- [ ] AC2: changing `image` and running `tenant apply` recreates the container. Volume data, the user's changed password, the host key fingerprint and GPU access all survive.
- [ ] AC3: writes past the volume size fail with ENOSPC. `tenant volume resize` raises the limit while the container runs.
- [ ] AC4: after `systemctl daemon-reload`, `nvidia-smi` in a tenant container still works.
- [ ] AC4a: `tenant status` shows the attach method (`runtime=nvidia`, device UUIDs, devices found in the OCI spec). A container started by hand with runc + `--gpus` is reported as drift, and `apply` recreates it with `--runtime=nvidia`.
- [ ] AC4b: `create` without `gpu.runtime` when `gpu.devices` is set, or with any value other than `nvidia`, fails validation.
- [ ] AC5: a GPU already assigned to a user or another tenant is rejected at `create` and `gpu` time.
- [ ] AC6: an account in the ops group without sudo can run `rootfiles tenant ops <t> status|restart|reset` only for tenants that list it. Any other action or tenant is refused, and every attempt is in the audit log.
- [ ] AC7: no temporary password appears in the audit log, `/var/log/rootfiles.log`, or process arguments.
- [ ] AC8: after a reboot the volume mount units bring every tenant volume back before docker starts its containers.
- [ ] AC9: `tenant import` refuses to start when the data to copy exceeds the new volume size or the free space. A dry-run prints the plan.
- [ ] AC10: `--dry-run` on every mutating tenant command changes nothing.

## Tasks

- [ ] T1: promote this spec to `docs/specs/YYYY-MM-DD-tenant-containers-design.md` on the implementation branch
  - Done when: the spec file is in the first PR and this issue links it
- [ ] T2: spec, config and ledger owner type
  - Done when: `go test ./internal/config ./internal/module -run 'Tenant|GPU'` passes (AC5)
- [ ] T3: volume image, mount unit, resize and overcommit check
  - Done when: scenario test fills the volume to ENOSPC, resizes, and survives a unit restart (AC3, AC8)
- [ ] T4: entrypoint contract, create and recreate with rollback
  - Done when: scenario covers AC1 and AC2 with a stubbed GPU check
- [ ] T5: passwords and shadow sync
  - Done when: AC1, AC2 and AC7 pass, and grep finds no secret in logs or `ps` captures
- [ ] T5a: explicit GPU attach (spec field, UUID resolution, attach check, drift handling)
  - Done when: unit tests for validation (AC4b) and an OCI spec fixture check pass; AC4a scenario passes with a stubbed runtime
- [ ] T6: host-prep module (guard timer, sudoers) and `tenant ops`
  - Done when: AC4 is verified on a GPU host, and the AC6 scenario passes
- [ ] T7: `tenant import`
  - Done when: AC9 dry-run and refusal tests pass
  - Needs judgment: the owner confirms each tenant's external backup and cleanup before the real import
- [ ] T8: docs and release notes, then a pilot on one GPU host
  - Needs judgment: the owner picks the pilot host and tenant

## Out of scope

- Web portal, SSO, reservations, GPU scheduling or fractional GPU. These follow the separate Kubernetes plan.
- Multi-host placement. `fleet` only ships the declarative `tenant` module config.
- A writable-layer size limit on ext4 hosts.
- Single-port SSH routing (for example sshpiper). It is a follow-up issue if the per-tenant ports become a problem.
- Host-specific migration details. They live in the operator workspace (`admin/computing/infra/261020-plan-tenant-container-manager.md`).




## Implementation notes (changes from the issue)

- Config lives under `modules.tenant` (`enabled`, `root`, `ops_group`). The default operator group is `rootfiles-tenant-ops`.
- The container mounts the volume subdirectories with `--mount type=bind` instead of a named Docker volume `tenant-<t>`. A bind mount fails when its source is missing, so a container cannot start on the bare mount point of an unmounted volume and write past the quota. The named volume added nothing else.
- The volume image is mounted by a systemd `.mount` unit with `loop,discard,nodev,nosuid`, `Before=docker.service` and `WantedBy=docker.service`. `nosuid,nodev` and a root-only `<root>/<t>` keep host users away from setuid files a tenant leaves in its volume.
- On a container's first boot the stored password hashes replace whatever the image ships. On later boots they only seed accounts without a usable password, so a password changed inside a container survives a plain restart even before the next sync.
- Tenant names cannot end in `-prev`, the suffix of rollback containers. Containers carry a `rootfiles.tenant.gpus` label; a failed apply returns the GPU ledger to the GPUs of the container left running.
- The sync is `rootfiles tenant sync`, run by `rootfiles-tenant-sync.timer` (every 2 minutes since #80, without the global lock). `tenant list` prints tenant names.
- Single-port SSH through sshpiper followed in #80; its design is in that issue and in `docs/tenant.md`.
- A recreate refuses while `tenant-<t>-prev` still exists, so an earlier rollback copy is never discarded silently.
- `tenant ops` refuses every flag and clears `ROOTFILES_*` variables before loading the host's applied config, because the sudoers wildcard would otherwise let an operator point `--config` at a spec they control.
- Import requires the legacy container to be running (accounts, hashes and the overlay merged directory are read from it) and keeps the tenant's passwords by copying the hashes.
- Space return uses the `discard` mount option plus the stock `fstrim.timer`.
- The module installs the GPU device guard verified on dgx02 as `/usr/local/sbin/rootfiles-gpu-guard`.
- AC4 rests on the nvidia runtime behavior verified on 2026-10-20; CI has no GPU, so the tenant scenario covers AC1-AC3 and AC6-AC10 and unit tests cover AC5.
