---
id: gpu-allocation
type: module
title: GPU allocation
status: active
parent: architecture
depends-on:
  - engine
paths:
  - internal/module/gpu.go
  - internal/module/mig.go
  - internal/cli/gpu.go
---

# GPU allocation

## Purpose

Per-user GPU assignment on shared GPU hosts. One ledger records which physical GPUs belong to which host account or tenant container; the `gpu` module and `rootfiles gpu assign|revoke` turn user entries into enforcement (shell environment, systemd cgroup device policy, a Docker wrapper). MIG state is reported read-only. The module is toggled by `modules.nvidia.gpu_allocation.enabled` (`Config.IsModuleEnabled`, [config](config.md)).

## Contracts and invariants

- **One ledger.** `<home_base>/.rootfiles/gpu-allocations.json` (`gpuDBPath`, `GPUAllocationsDB`) holds both user and tenant entries. GPUs are physical indices (`/dev/nvidia<N>`). A GPU appears in at most one entry: `validateGPURequest` rejects a GPU held by any other owner (users and tenants alike) and indices outside `0..TotalGPUs-1` when the total is known. Reassigning to the same owner is allowed (`upsertAllocation` replaces).
- **Tenant owners.** Tenant entries use owner `tenant:<name>` (`tenantOwnerPrefix`) and method `tenant`; a colon never occurs in an account name, so owners cannot collide. They are written only through `ReserveTenantGPUs` (called by [tenant](tenant.md)); an empty GPU list drops the entry. `RevokeGPUs` refuses a tenant owner and points to `rootfiles tenant gpu`. User enforcement (`userAllocations`) ignores tenant entries.
- **Every read-modify-write goes through `withGPUDBLock`:** an exclusive `flock` on `gpu-allocations.json.lock`, reload under the lock, mutate, then `saveGPUDB`. Never read the DB, change it and save it outside this helper.
- **Atomic replace.** `saveGPUDB` uses shared `writeJSONDB`: a fresh mode-0644 sibling is written through Runner.WriteFile and replaced through Runner.Rename, preserving the previous ledger for rollback. The Docker wrapper must never see partial JSON and needs the readable ledger. Missing reads yield an empty database; malformed/unreadable reads stop a locked mutation instead of losing existing allocations. In dry-run the lock file is not created and Runner writes/renames are no-ops.
- **Trusted metadata.** Module Check/Apply, `AssignGPUs`, `RevokeGPUs` and `ReserveTenantGPUs` call `requireTrustedMetadata` (see [users](users.md)); `gpu list` and `gpu status` only warn.
- **GPU count from device nodes.** `detectGPUInfo` counts `/dev/nvidia[0-9]+` (`countGPUDevices`), because `nvidia-smi` only sees GPUs allowed by the caller's cgroup; `nvidia-smi` supplies the model name only.
- **Methods** (`effectiveMethod`: per-entry method, else `modules.nvidia.gpu_allocation.method`, else `env`):
  - `env`: `/etc/profile.d/rootfiles-gpu-<user>.sh` exports `CUDA_VISIBLE_DEVICES` and `NVIDIA_VISIBLE_DEVICES` for that user (advisory).
  - `cgroup`: `/etc/systemd/system/user-<uid>.slice.d/gpu-access.conf` with `DevicePolicy=strict`, the standard character devices, the assigned `/dev/nvidia<N>` and the shared NVIDIA control devices (`buildCgroupConf`).
  - `both`: the two above.
- **Docker wrapper.** While any user allocation exists, `/usr/local/bin/docker` is a managed bash wrapper (`buildDockerWrapper`) in front of `/usr/bin/docker`: root bypasses it; for a user with an allocation, `run`/`create` drop any `--gpus` argument and inject `NVIDIA_VISIBLE_DEVICES`/`CUDA_VISIBLE_DEVICES`, and `compose` gets them exported. Without allocations the wrapper is removed (module Apply, `RevokeGPUs`).
- **Check and Apply agree** on each user's env script, cgroup drop-in and the wrapper content; file content is compared exactly except for the shared legacy management marker. A missing account skips its cgroup drop-in (warning in Apply).
- **daemon-reload is a warning.** `systemdReload` is shared from `module/files.go`; it warns with the manual daemon-reload command instead of failing. Existing drift gates remain, so a converged later apply does not perform an invented retry.
- **Revoke removes only rootfiles files:** the env script, the `gpu-access.conf` drop-in, and the slice directory only when empty.
- **MIG is read-only.** `MIGStatus` reads `nvidia-smi` mode and `nvidia-smi -L` instances; repartitioning is left to the operator because it destroys running instances. Declarative MIG partitioning (carried over from the 2026-09 improvement plan) is not planned; revisit only with an owner decision and a drain procedure for running instances.

## Behaviour

- `rootfiles gpu assign USER --gpus 0,1 [--method env|cgroup|both]`, `gpu revoke USER` (both in `mutatingCommands`), `gpu list`, `gpu status` (all physical GPUs with `nvidia-smi` data where visible, `(restricted)` otherwise), `gpu mig status`.
- `parseGPUList` requires at least one non-negative integer index.
- Assign and revoke apply their method side effects immediately; `apply --module gpu` re-converges every user entry.
- `user del` revokes the account's allocation ([users](users.md)); `status` reads the ledger through `LoadGPUDB`.

## Distro support

- Ubuntu and DGX OS. Rocky rejects `gpu` in `config.ValidateCapabilities` for `apply`/`check`; the `gpu` subcommands do not run that validation yet (#91).

## Safety

- Generated-file comparisons preserve legacy headers using the [shared managed-header rule](exec-packages.md#contracts-and-invariants); changing the marker alone never rewrites a converged file or reloads its service.

- Root-mutating subcommands build their context with `buildRunContext` ([cli-runtime](cli-runtime.md)).
- The wrapper replaces `/usr/local/bin/docker`; never write it when no user allocation exists, and never point it at anything but `/usr/bin/docker`.
- `env` alone is not isolation; `cgroup` is the enforced method.

## Tests that pin it

- `internal/module/gpu_test.go`: conflicts and range checks, self-reassignment, revoke keeps other entries, `TestWithGPUDBLock_NoLostUpdatesUnderConcurrency`, generated script/drop-in/wrapper content, `TestBuildDockerWrapper_SyntaxCheck`, Check/Apply wrapper agreement.
- `internal/module/mig_test.go`, `internal/cli/gpu_test.go`.
- Tenant ledger overlap: `internal/module/tenant_test.go` (owned by [tenant](tenant.md)).
- Scenario `tests/scenarios/gpu-allocation.sh` (CI `scenario` job); `tests/integration/run-module.sh gpu` (CI `module` job).

## Open issues

- [#91](https://github.com/STAIxBWLB/rootfiles/issues/91): `gpu assign`/`revoke` skip capability validation and always build an APT backend.
- [#94](https://github.com/STAIxBWLB/rootfiles/issues/94): remaining output through `ui`; equivalent home-base defaults use `UsersConfig.Base()`, while unknown-base guards remain separate. Shared ordinary-file/systemd helpers are in place.
