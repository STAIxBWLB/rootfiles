# GPU allocation

Assign GPUs to individual users so jobs on a shared GPU server do not collide. Contract: [docs/specs/gpu-allocation.md](specs/gpu-allocation.md). Not supported on Rocky Linux ([rocky.md](rocky.md)).

## Enable

The `gpu` module is switched on by `modules.nvidia.gpu_allocation.enabled`, not by a `modules.gpu` key:

```yaml
modules:
  nvidia:
    enabled: true
    gpu_allocation:
      enabled: true
      method: both     # env (default), cgroup or both
```

The `gpu-server` profile enables it with `env`, the `dgx` profile with `both`. On each `apply` the module rewrites the per-user files below from the ledger, so a reinstalled host gets its allocations back.

## Methods

| Method | Mechanism | Scope |
|---|---|---|
| `env` | `/etc/profile.d/rootfiles-gpu-<user>.sh` sets `CUDA_VISIBLE_DEVICES` and `NVIDIA_VISIBLE_DEVICES` | Login shells |
| `cgroup` | `/etc/systemd/system/user-<uid>.slice.d/gpu-access.conf` with `DeviceAllow` rules for the assigned `/dev/nvidia*` devices | Every process in the user's session |
| `both` | `env` and `cgroup` together | Full isolation |

While any user allocation exists, `/usr/local/bin/docker` is a wrapper that limits a non-root user's `docker run` and `docker create` to that user's GPUs. It is removed when the last allocation is revoked.

## Ledger

- Allocations live in `<home_base>/.rootfiles/gpu-allocations.json`. The file must pass the trust rules in [users.md](users.md#root-only-trust-rules).
- Writes take a file lock and replace the file atomically, so concurrent `gpu assign` and `revoke` calls never lose an allocation.
- Each GPU belongs to one owner. Assigning a GPU held by someone else is refused, and so is an index beyond the detected GPU count.
- Tenant containers record their GPUs in the same ledger. `gpu revoke` refuses a tenant entry; change it with `rootfiles tenant gpu` ([tenant.md](tenant.md#change-a-tenant)).

## Commands

```bash
sudo rootfiles gpu assign alice --gpus 0,1,2,3 --method env
sudo rootfiles gpu assign bob --gpus 4,5,6,7          # method from gpu_allocation.method
sudo rootfiles gpu revoke alice
sudo rootfiles gpu list                               # allocations from the ledger
sudo rootfiles gpu status                             # GPUs with their owners
```

`gpu assign` needs `--gpus`. Assigning a user again replaces that user's allocation.

## MIG

```bash
rootfiles gpu mig status
```

Shows the current and pending MIG mode and the instances of each GPU. It is read-only; repartition with `nvidia-smi mig`, which may need a GPU reset.
