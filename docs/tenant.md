# Tenant containers

`rootfiles tenant` runs long-lived containers for outside groups on a GPU host. Each tenant gets one Docker container with sshd on its own host port, a size-capped volume, accounts whose passwords and host keys survive image changes, and GPUs recorded in the same ledger as `rootfiles gpu assign`.

## Host preparation

Enable the module in the site config and apply it once per host:

```yaml
modules:
  tenant:
    enabled: true
    root: /raid/tenants          # default; volume images, mount points, system files
    ops_group: rootfiles-tenant-ops  # default; members may run `sudo rootfiles tenant ops`
```

```bash
sudo rootfiles apply --module tenant
```

The module installs:

- `rootfiles-gpu-guard.timer`: every minute, restores GPU access of running `--gpus` containers whose device filter a `systemctl daemon-reload` rebuilt without their `/dev/nvidia*` nodes (`systemctl set-property --runtime ... DeviceAllow`). Tenant containers use the nvidia runtime and do not need it; it covers the other GPU containers on the host.
- `rootfiles-tenant-sync.timer`: every 2 minutes, `rootfiles tenant sync` saves the password hashes of running tenants and refreshes their sshpiper keys and routes. It needs root but not the global lock, so it never fails because an `apply` is running.
- `fstrim.timer`, so space deleted inside a volume returns to the host.
- `/etc/sudoers.d/rootfiles-tenant-ops`: `%<ops_group> ALL=(root) NOPASSWD: /usr/local/bin/rootfiles tenant ops *`, validated with `visudo`.
- A mount unit per existing tenant volume (rewritten after an OS reinstall).

GPU tenants need the `nvidia` module (Docker's `nvidia` runtime).

## One SSH port for every tenant (sshpiper)

```yaml
modules:
  tenant:
    sshpiper:
      enabled: true
      port: 2222       # default; must not be the host's SSH port
      # address: 0.0.0.0, version: v1.6.1 (pinned; the archive SHA-256 is checked)
```

`sudo rootfiles apply --module tenant` installs sshpiperd with its yaml and failtoban plugins under `/usr/local/lib/rootfiles/sshpiper/<version>` and runs `rootfiles-sshpiper.service`. Users then reach any tenant through one port by naming the tenant in the user name:

```bash
ssh -p 2222 est.dev@dgx02      # user dev of tenant est
```

- Passwords go to the tenant's own sshd, which checks them and enforces the change of a temporary password.
- Keys: sshpiper checks the client key against the user's `~/.ssh/authorized_keys` inside the container (copied by the 2-minute sync; only plain key lines, options are dropped), then logs in to the container with a host-only key. A tenant created before sshpiper was enabled needs `rootfiles tenant apply <t>` and `rootfiles tenant restart <t>` once before key logins work.
- sshpiper checks each container's host key against the keys rootfiles stores. Clients see sshpiper's own host key on port 2222.
- failtoban bans an address for an hour after 5 failed logins.
- The direct per-tenant ports keep working. Disabling sshpiper removes the service and the route files.
- `rootfiles tenant status` and `create` print the routed command for each account.

## Create a tenant

```bash
sudo rootfiles tenant create est \
  --image registry.local/est:2026.10 --ssh-port 3889 --gpus 2,3 --size 2T \
  --account est:1000 --sudo --operator alice
```

This writes `<home_base>/.rootfiles/tenants/est.yaml`, creates the volume, starts `tenant-est`, checks sshd, the host key and the GPUs, and prints a temporary password per account. The password must be changed at first login. It is printed once and never written to a log or passed on a command line.

The image needs only `openssh-server` (and `useradd`, `chpasswd`, which Debian, Ubuntu and RHEL images have). rootfiles starts the container with its own read-only entrypoint, `/etc/tenant-system/init.sh`, which on every start creates the accounts, applies the stored password hashes (on a new container to every account, later only to accounts without a usable password), restores the host keys, starts sshd and runs `/data/.tenant/autostart.sh` as the first account when that file exists.

| Inside the container | Host | Limit |
|---|---|---|
| `/data`, `/home`, `/tmp` | `<root>/<tenant>/vol/{data,home,tmp}` on a loop-mounted image | `volume.size` |
| `/etc/tenant-system` (read-only) | `<root>/<tenant>/system`: accounts, password hashes, host keys | root only |
| everything else | container writable layer | none on ext4 hosts; keep data in the volume |
| `extra_binds` | as given | none |

## Change a tenant

| Change | Command | Container |
|---|---|---|
| Image | `rootfiles tenant image est IMAGE` | recreated |
| GPUs | `rootfiles tenant gpu est --gpus 0,1` (or `none`) | recreated |
| SSH port | `rootfiles tenant port est 3890` | recreated |
| Account | `rootfiles tenant user add est bob [--sudo]` | kept, account added live |
| Password | `rootfiles tenant passwd est bob` (`reset` is the same) | kept |
| Volume size | `rootfiles tenant volume resize est 3T` | kept (grows online) |
| Anything in the spec | edit the YAML, then `rootfiles tenant apply est` | as needed |

A recreate saves the current password hashes, prints the size of the old writable layer, renames the old container to `tenant-est-prev` (restart policy `no`), starts the new one and verifies sshd, the stored host key and the GPUs. On failure it removes the new container and restarts the old one. `tenant-est-prev` stays until you remove it; the next recreate refuses while it exists.

Shrinking a volume needs `--offline` and ext4: it stops the container, checks the filesystem, shrinks it and starts the container again. xfs volumes cannot shrink.

`rootfiles tenant status` shows each tenant's state, spec drift, volume use and the sum of volume sizes against the free space of the tenant root (sparse images can be overcommitted). `rootfiles tenant status est` adds the writable-layer size.

## Operators

Host accounts in `ops_group` that a spec lists under `operators` can, without other sudo rights:

```bash
sudo rootfiles tenant ops est status|logs|gpu-heal|restart
sudo rootfiles tenant ops est reset USER
```

`tenant ops` takes no flags, ignores `ROOTFILES_*` variables and reads only the host's applied configuration. Creating tenants and changing images, GPUs, ports or volumes stay with root.

## Audit

Every real run of a changing tenant command appends one JSON line to `/var/log/rootfiles-tenant.jsonl` (time, actor, tenant, action, result). Refused operator attempts are recorded too. `--dry-run` changes nothing and records nothing.

## Import a hand-made container

```bash
sudo rootfiles tenant import est --from est-workspace --size 2T \
  --data /raid/data/devcon/est/data --keep /home/est --dry-run
```

Import reads the running container: image, published ports (22 becomes the SSH port), GPU IDs, bind mounts (kept as `extra_binds` unless they target `/data`, `/home` or `/tmp`), accounts with a login shell and uid 1000-65533, their sudo membership, password hashes and host keys. It copies `--data` and the `--keep` paths with `rsync -aHAX`, stops the old container with restart policy `no`, and starts the tenant container. Accounts keep their passwords. Before any change it refuses when the data does not fit the volume with 5% headroom or the free space; `--dry-run` prints the plan. Ask the tenant to stop writing first. Remove the old container once the tenant confirms.

## Limits

- One host per command; `fleet` only ships the module config.
- The writable layer has no size limit on ext4 hosts.
- Not supported on Rocky Linux.
