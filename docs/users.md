# Users

Where user homes live, the rules that protect them, and the `rootfiles user` commands. Contract: [docs/specs/users.md](specs/users.md).

## Home base

Users are created under a home base such as `/raid/home`, which can survive an OS reinstall. `users.home_base` sets it. The `dgx` profile sets `/raid/home`; the other built-in profiles leave it unset, and it is detected on the host.

Detection, in order:

1. An uncommented `HOME=` in `/etc/default/useradd` is kept. Stock Ubuntu and DGX OS leave it commented out.
2. A base where rootfiles already manages users (`.rootfiles` under `/home`, `/raid/home`, `/data/home` or `/nvme/home`) is kept.
3. On Ubuntu and DGX OS, the first separate, read-write local data drive mounted at `/raid`, `/data` or `/nvme` (ext4, xfs, btrfs or zfs) gives `<mount>/home`.
4. Otherwise the home base is `/home`.

What does not count as a data drive in step 3:

- A mount on the same device as `/` (such as a bind mount of the root filesystem), a btrfs subvolume of the root filesystem, a ZFS dataset in the root pool, or a read-only mount. When `<mount>/home` is itself a mount, that mount decides.
- `/mnt` and network filesystems.
- A base where `/` or any existing directory down to it is a symlink, not owned by root, or writable by group or others. A world-writable scratch mount therefore keeps `/home`.

Other rules:

- Rocky Linux keeps `/home`, because rootfiles does not label homes outside `/home` for SELinux.
- The `users` module writes the applied home base, `/home` included, as `HOME=` in `/etc/default/useradd`, so later runs keep it.
- If another candidate base already holds `.rootfiles/users.json` or `gpu-allocations.json` (for example after `user` or `gpu` commands ran without `--profile` on a `dgx` host), detection stops with an error naming the file. Choose with `users.home_base`, `ROOTFILES_HOME_BASE` or `--home-base`.
- Fleet site configs must set `users.home_base` explicitly ([fleet.md](fleet.md#inventory)), so a fleet rollout is not blocked by this.
- Only new users get the new base. Existing homes are not moved; `rootfiles user rehome` moves them.

## Root-only trust rules

A home base other than `/home`, set explicitly or kept from `/etc/default/useradd`, must be controlled by root alone. A symlinked `/home` is followed once when its target passes the same rule.

- The base fails the rule when `/` or any existing directory down to it is a symlink, not owned by root, or writable by group or others.
- The `users` and `storage` modules, `user add` and `user rehome` refuse such a base and change nothing until it is fixed.
- `user restore` applies the rule to each restored home's parent directory, skips a user whose parent fails it and reports the skip. Without a backup file it refuses to read the users database from such a base.

The users database (`<home_base>/.rootfiles/users.json`) and the GPU ledger (`<home_base>/.rootfiles/gpu-allocations.json`) need the same rule plus a `<home_base>/.rootfiles` in which every entry is a root-owned regular file or directory that group and others cannot write (no symlinks).

- The `users`, `storage` and `gpu` modules, `user del`, `user key add` and `rm`, `user quota`, and `gpu assign` and `revoke` refuse before any change.
- `backup` leaves the users database out with a warning.
- `status`, `user list`, `user audit`, `gpu list` and `gpu status` warn on stderr and still report.
- No database write follows a symlink.

A file (not a directory) at `<home_base>/.rootfiles`, left by versions before v0.1, is refused, dry runs included; rootfiles no longer moves it. To keep its records: move the file aside, create `<home_base>/.rootfiles` as a root-owned directory, put the file back as `<home_base>/.rootfiles/users.json` with mode 0600, then retry.

## Accounts

```bash
sudo rootfiles user add alice --ssh-pubkey "ssh-ed25519 AAAA..."   # --groups dev,ops, --no-docker
sudo rootfiles user list                  # managed users; --names prints names only
sudo rootfiles user list --system         # all accounts with UID 1000-65533
sudo rootfiles user id alice              # UID, GID and groups
sudo rootfiles user groups [alice]        # all groups, or one user's
sudo rootfiles user group-add alice --docker --sudo
sudo rootfiles user group-add alice --groups dev,ops
sudo rootfiles user group-del alice --docker
```

`user add` takes the name from the argument, `--user` or `ROOTFILES_USER`, and the key from `--ssh-pubkey` or `ROOTFILES_SSH_PUBKEY`. `--pubkey` still works as a deprecated alias of `--ssh-pubkey`.

Accounts can also be declared in the config under `users.accounts`; the `users` module creates them and adds missing keys and groups, and never removes anything.

## Passwords

```bash
sudo rootfiles user passwd alice bob --expire          # random password per user, printed once
sudo rootfiles user passwd alice bob --password 'shared-pass'
sudo rootfiles user passwd --file users.txt            # usernames, or username,password per line
sudo rootfiles user passwd --all --dry-run
```

Passwords reach `chpasswd` on stdin, never on a command line. `--expire` forces a change at the next login. `--password` values are redacted in the audit log ([operations.md](operations.md#lock-and-audit-log)). `--suffix` (password = username + suffix) is deprecated because it makes predictable passwords, and goes away in the next minor release.

## Lifecycle

```bash
sudo rootfiles user key add alice "ssh-ed25519 AAAA... alice@laptop"
sudo rootfiles user key list alice
sudo rootfiles user key rm alice alice@laptop      # by list index, key or comment
sudo rootfiles user lock alice                     # blocks password and SSH key logins
sudo rootfiles user unlock alice
sudo rootfiles user expire alice 2026-12-31        # or: never
sudo rootfiles user del alice                      # home kept
sudo rootfiles user del alice --archive            # tar.gz of the home under <home_base>/.rootfiles/archive, then removed
sudo rootfiles user del alice --remove-home        # removed without an archive
sudo rootfiles user du                             # home usage, largest first
sudo rootfiles user audit                          # users.json vs. accounts on the host; exit 2 on drift
sudo rootfiles user rehome alice                   # verified copy to <home_base>/alice
sudo rootfiles user rehome alice --remove-old
```

- `user del` also removes the user's sudoers drop-in, GPU allocation and database entry.
- `user rehome` copies the home, verifies the copy, switches the account and leaves a symlink at the old path. The original is kept as `<old>.rootfiles-bak-<timestamp>` unless `--remove-old` is given.

## Quotas

Hard limits on the home base filesystem: XFS project quota (mounted with `prjquota`) or ext4 user quota (mounted with `usrquota`, then `quotacheck` and `quotaon`). The command says which mount option or tool is missing.

```bash
sudo rootfiles user quota set alice 500G
sudo rootfiles user quota show
sudo rootfiles user quota rm alice
```

The limit is recorded and applied again by `user restore`.

## Backup and restore

```bash
sudo rootfiles user backup                   # rootfiles-users-<hostname>-<YYYYMMDD>.json in the current directory
sudo rootfiles user backup --file users.json
sudo rootfiles user restore                  # reads <home_base>/.rootfiles/users.json
sudo rootfiles user restore users.json
```

The backup holds the users database merged with the accounts found on the host. `restore` skips accounts that already exist, recreates the others with their UID and shell, reuses a home directory that survived (fixing its ownership), restores group memberships, sudo rules and recorded SSH keys (when the home has no `authorized_keys` of its own), and applies recorded quotas again.

## OS reinstall recovery

After reinstalling the OS on a host whose homes live on a separate data drive:

1. Install rootfiles.
2. Restore the accounts first. Their SSH keys are what lets the SSH hardening disable password login safely.

   ```bash
   sudo rootfiles user restore
   ```

3. Preview, then apply the config snapshot from the system backup, here one written with `rootfiles backup --dir /raid/backup` before the reinstall ([operations.md](operations.md#system-backup)):

   ```bash
   sudo rootfiles apply --config /raid/backup/rootfiles-backup-*/config-snapshot.yaml --dry-run
   sudo rootfiles apply --config /raid/backup/rootfiles-backup-*/config-snapshot.yaml --yes
   ```
