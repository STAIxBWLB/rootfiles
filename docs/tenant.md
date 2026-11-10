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
      # failtoban_ignore: [203.0.113.33/32]   # shared VPN gateway, never banned
```

`sudo rootfiles apply --module tenant` installs sshpiperd with its yaml and failtoban plugins under `/usr/local/lib/rootfiles/sshpiper/<version>` and runs `rootfiles-sshpiper.service`. Users then reach any tenant through one port by naming the tenant in the user name:

```bash
ssh -p 2222 lab.dev@gpu01      # user dev of tenant lab
```

- Passwords go to the tenant's own sshd, which checks them and enforces the change of a temporary password.
- Keys: sshpiper checks the client key against the user's `~/.ssh/authorized_keys` inside the container (copied by the 2-minute sync; only plain key lines, options are dropped), then logs in to the container with a host-only key. A tenant created before sshpiper was enabled needs `rootfiles tenant apply <t>` and `rootfiles tenant restart <t>` once before key logins work.
- sshpiper checks each container's host key against the keys rootfiles stores. Clients see sshpiper's own host key on port 2222.
- failtoban bans an address for an hour after 5 failed logins. Users behind one VPN or NAT gateway share its address, so one user's typos would lock out all of them: list such gateways in `failtoban_ignore` (addresses or CIDRs). Applying a changed list restarts sshpiper, which also clears current bans and drops sessions open through it.
- Firewall: with `modules.network.ufw`, the network module admits the port. If UFW is active without that, or its state cannot be read, `check` reports the port as blocked and `apply` stops before starting sshpiper. A port held by another process is refused the same way, also when changing the port of a running sshpiper.
- The direct per-tenant ports keep working. Disabling sshpiper removes the service and the route files.
- `rootfiles tenant status` and `create` print the routed command for each account.

## Create a tenant

```bash
sudo rootfiles tenant create lab \
  --image registry.example/lab:2026.10 --ssh-port 3889 --gpus 2,3 --size 2T \
  --account lab:1000 --sudo --operator alice
```

| Flag | Meaning |
|---|---|
| `--image` | Container image; it needs `openssh-server` |
| `--ssh-port` | Host port published to the container's sshd |
| `--size` | Volume size, such as `500G` or `2T` |
| `--fs` | Volume filesystem: `ext4` (default; can shrink offline) or `xfs` |
| `--gpus` | Comma-separated GPU indices |
| `--cpus` | Host CPU list such as `0-3,8-11` (pinning, not a CPU quota) |
| `--mems` | NUMA memory-node list such as `0` or `0-1` |
| `--account NAME[:UID[:GID]]` | Account to create (repeatable); UIDs default to 1000, 1001, ... and the GID to the UID |
| `--sudo` | Give the accounts sudo inside the container |
| `--operator` | Host account allowed `sudo rootfiles tenant ops` on this tenant (repeatable) |
| `--port HOST:CONTAINER[/udp]` | Extra published port (repeatable) |
| `--bind SRC:DST[:ro\|rw]` | Extra bind mount, outside the volume quota (repeatable); stored as `extra_binds` |

This writes `<home_base>/.rootfiles/tenants/lab.yaml`, creates the volume, starts `tenant-lab`, checks sshd, the host key and the GPUs, and prints a temporary password per account. The password must be changed at first login. It is printed once and never written to a log or passed on a command line.

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
| Image | `rootfiles tenant image lab IMAGE` | recreated |
| GPUs | `rootfiles tenant gpu lab --gpus 0,1` (or `none`) | recreated |
| SSH port | `rootfiles tenant port lab 3890` | recreated |
| Account | `rootfiles tenant user add lab bob [--sudo]` | kept, account added live |
| Password | `rootfiles tenant passwd lab bob` (`reset` is the same) | kept |
| Volume size | `rootfiles tenant volume resize lab 3T` | kept (grows online) |
| Anything in the spec | edit the YAML, then `rootfiles tenant apply lab` (`--all` for every tenant) | as needed |

A recreate saves the current password hashes, prints the size of the old writable layer, renames the old container to `tenant-lab-prev` (restart policy `no`), starts the new one and verifies sshd, the stored host key and the GPUs. On failure it removes the new container and restarts the old one. `tenant-lab-prev` stays until you remove it; the next recreate refuses while it exists.

Shrinking a volume needs `--offline` and ext4: it stops the container, checks the filesystem, shrinks it and starts the container again. xfs volumes cannot shrink.

## Inspect and restart

```bash
sudo rootfiles tenant list                  # tenant names
sudo rootfiles tenant status [lab]          # state, spec drift, volume use
sudo rootfiles tenant logs lab --tail 500   # docker logs; last 200 lines by default
sudo rootfiles tenant restart lab           # docker restart; the container is kept
```

`rootfiles tenant status` shows each tenant's state, spec drift, volume use and the sum of volume sizes against the free space of the tenant root (sparse images can be overcommitted). `rootfiles tenant status lab` adds the writable-layer size.

## Operators

Host accounts in `ops_group` that a spec lists under `operators` can, without other sudo rights:

```bash
sudo rootfiles tenant ops lab status|logs|gpu-heal|restart
sudo rootfiles tenant ops lab reset USER
```

`tenant ops` takes no flags, ignores `ROOTFILES_*` variables and reads only the host's applied configuration. Creating tenants and changing images, GPUs, ports or volumes stay with root.

## Audit

Every real run of a changing tenant command appends one JSON line to `/var/log/rootfiles-tenant.jsonl` (time, actor, tenant, action, result). Refused operator attempts are recorded too. `--dry-run` changes nothing and records nothing.

## Import a hand-made container

```bash
sudo rootfiles tenant import lab --from lab-workspace --size 2T \
  --data /raid/data/lab --keep /home/lab --dry-run
```

Import reads the running container: image, published ports (22 becomes the SSH port), GPU IDs, bind mounts (kept as `extra_binds` unless they target `/data`, `/home` or `/tmp`), accounts with a login shell and uid 1000-65533, their sudo membership, password hashes and host keys. It copies `--data` and the `--keep` paths with `rsync -aHAX`, stops the old container with restart policy `no`, and starts the tenant container. Accounts keep their passwords. Before any change it refuses when the data does not fit the volume with 5% headroom or the free space; `--dry-run` prints the plan. Ask the tenant to stop writing first. Remove the old container once the tenant confirms.

## Limits

- One host per command; `fleet` only ships the module config.
- The writable layer has no size limit on ext4 hosts.
- Not supported on Rocky Linux.

## CPU and NUMA pinning

```yaml
cpuset:
  cpus: "0-3,8-11"
  mems: "0"
```

Either key may be set alone. Absent or empty spec keys are unmanaged: apply leaves any hand-set pin untouched. CPU indices must be online (`/sys/devices/system/cpu/online`); memory nodes must have memory (`/sys/devices/system/node/has_memory`). Invalid or unavailable values are refused before any container changes. Several tenants may share the same CPUs/nodes.

```bash
sudo rootfiles tenant cpuset lab --cpus 0-3 --mems 0
sudo rootfiles tenant status lab
sudo rootfiles tenant cpuset lab --clear
```

`tenant cpuset` saves the spec and uses `docker update` on the same running or stopped container, without a restart. Omitted flags retain their existing spec keys. An explicit `--cpus ""` or `--mems ""` makes that key unmanaged and leaves the live setting alone. `--clear` instead removes the field and resets both live settings to the host's full lists. A failed Docker update restores the previous spec. Dry-run writes nothing.

If the image, ports or other container settings have pending changes (including a container without a digest label), they remain pending. The command says when `rootfiles tenant apply lab` is still needed; apply may recreate for those other changes. Cpuset alone never changes the recreate digest. Create and recreate use the spec's managed pinning. Before recreating a container with unmanaged hand-set pins, rootfiles warns; adopt the values with `tenant cpuset` first if you want to keep them. `tenant import` carries existing pins into the new spec.

`tenant status` shows `update` for live-only cpuset drift and `drift` for settings requiring recreation. Detailed status shows the spec and container values, including unmanaged pins.

On cgroup v2, changing memory nodes migrates existing memory pages to the new nodes. This can take noticeable time for a large process.

Rollback: older binaries strictly reject specs containing `cpuset`. Before downgrading, remove the field from each spec; the container keeps its current pinning. The release notes for the version introducing this feature must carry this step.
