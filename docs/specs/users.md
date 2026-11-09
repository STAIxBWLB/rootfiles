---
id: users
type: module
title: Users and accounts
status: active
parent: architecture
depends-on:
  - engine
paths:
  - internal/module/users.go
  - internal/module/accounts.go
  - internal/module/lifecycle.go
  - internal/module/quota.go
  - internal/cli/user.go
  - internal/cli/userlife.go
---

# Users and accounts

## Purpose

The `users` module and the `rootfiles user` command group manage human accounts on a host: the custom home base, declarative accounts, ad-hoc creation, passwords, keys, lock/expiry, deletion, rehoming, backup/restore of account metadata and per-user disk quota. Home-base detection itself lives in [config](config.md) (`internal/config/detector.go`); the read-only fleet sudoers rule that Check/Apply also call (`checkFleetSudo`, `applyFleetSudo` in `internal/module/fleet_sudo.go`) belongs to [fleet](fleet.md).

## Contracts and invariants

- **Metadata location.** Managed-user metadata is `<home_base>/.rootfiles/users.json` (`UsersDB`, `UserMeta`); `/home` when `users.home_base` is unset (`metaHomeBase`, `usersDBPath`). A missing file means "no managed users", never an error (`LoadUsersDB`, `updateUserMeta`); unreadable files and parse errors are surfaced with repair guidance.
- **Trusted metadata.** Every command that writes metadata or acts on what it grants calls `requireTrustedMetadata` first: the home base must pass `checkHomeBase` (`config.RootOnlyHomeBase`: `/` and every existing directory down to the base root-owned and not group/other-writable; `/home` is exempt), and everything under `<base>/.rootfiles` must be a root-owned, non-symlink regular file or directory without group/other write (`rootOnlyTree`). Covered: module Check/Apply, `AddUser`, `DeleteUser`, `AddKey`, `RemoveKey`, `SetQuota`, `RestoreUsers` without a path. Read-only reports (`ListUsers`, `AuditUsers`) only warn through `WarnUntrustedMetadata`; `BackupUsers` leaves an untrusted database out of the backup. New metadata consumers follow the same split: refuse when acting, warn when reporting.
- **Atomic metadata writes.** Database writes call shared `writeJSONDB`, refuse target symlinks, stage a fresh sibling at mode 0600 and replace the destination through Runner.Rename, preserving the prior ledger for rollback. Missing DB reads are empty; corrupt/unreadable databases are refused rather than overwritten. Lifecycle metadata updates use the same helper. Dry-run stages or renames nothing.
- **Legacy metadata.** A file at `<home_base>/.rootfiles` is refused, including during dry-run. Move it to a safe temporary location, create `.rootfiles` as a root-owned directory, then place the preserved file at `.rootfiles/users.json` with mode 0600 before retrying. There is no automatic migration.
- **Usernames.** `checkUsername` (Debian `NAME_REGEX`, max 32 chars) gates every path that embeds a name in sudoers lines or paths (`AddUser`, `writeSudoers`, `lookupManaged`, `RestoreUsers`).
- **System accounts are off limits.** Lifecycle commands resolve the account through `lookupManaged`, which refuses uid < 1000 and uid 65534.
- **Additive membership.** Group changes only add (`usermod -aG`); the only removal is the explicit `user group-del` (`gpasswd -d`). The conventional `sudo` group maps to the distro admin group (`accountGroupsForSystem`, `config.AdminGroup`), and `requireNativeAdminGroup` fails rather than create an account without the requested admin group. Groups that do not exist yet (for example `docker` before Docker) are skipped with a warning, not created.
- **Sudoers.** `writeSudoers` writes `<user> ALL=(ALL) NOPASSWD:ALL` to a dot-prefixed temp name sudo ignores, validates with `visudo -cf`, then renames into `/etc/sudoers.d/<user>` (dots replaced by `_`, `sudoersPath`). A failed validation removes the temp file. Never write a sudoers file in place.
- **Passwords never in argv or logs.** `setPassword` pipes `user:password` to `chpasswd` on stdin and rejects `:` or newlines. Generated passwords use `generatePassword` (crypto/rand, look-alike-free alphabet, 16 chars) and are printed once; dry-run prints `****`. `--expire` runs `chage -d 0`. `--suffix` (username+suffix) is deprecated, accepted for this release and removed next minor; use `--password` or `--file`.
- **Keys.** `authorized_keys` keys are compared by type + blob (`keyID`), ignoring options and comments; existing lines, including comments, are kept verbatim when appending. `installAuthorizedKeys` writes `~/.ssh` 0700 and the file 0600, then `chown -R` to the user.
- **Declarative accounts** (`users.accounts`): Check reports a missing account, missing keys and missing groups (`accountDrift`); Apply creates or converges exactly those (`convergeAccount`). Neither removes keys, groups or accounts.
- **useradd default.** When `users.home_base` is set (including `/home`), Check/Apply keep `HOME=` in `/etc/default/useradd` equal to it; Apply refuses to rewrite the file when it exists but cannot be read.
- **Data preservation.** Deletion keeps the home by default (`KeepHome`); `--archive` tars it under `<home_base>/.rootfiles/archive` before removal and aborts with the account intact if the archive fails; removal uses `rm -rf --one-file-system`. `DeleteUser` also revokes the user's GPU allocation ([gpu-allocation](gpu-allocation.md)), removes the sudoers drop-in and the metadata entry.
- **Rehome** (`RehomeUser`) refuses a non-empty destination, copies with `rsync -aH`, verifies with a checksum dry-run before touching the original, then switches the account, moves the original to `<old>.rootfiles-bak-<ts>` and leaves a symlink at the old path. The backup is removed only with `--remove-old`.
- **Restore** (`RestoreUsers`) skips existing accounts, keeps the recorded uid, reuses an existing home (`--no-create-home` plus `chown -R`), re-adds groups and sudoers, restores keys only when the home has none, re-applies recorded quota, and reports every failed user in one error at the end.

## Behaviour

- New metadata databases use `CreatedBy: rootfiles`. Existing databases retain their recorded value, including `rootfiles-v2`; marker cleanup alone never rewrites a ledger.
- `user add` uses the inherited `--ssh-pubkey`; deprecated `--pubkey` remains an alias, including username fallback via `--user` or environment. Prefix root flags retain their values. `user backup --file` keeps deprecated `-o/--output` aliases with a replacement notice.

- Module Check/Apply: custom home base and metadata directory, useradd `HOME`, declarative accounts, then fleet sudoers. Check and Apply cover the same items.
- `rootfiles user`: `add`, `list [--system] [--names]`, `backup [--file FILE]`, `restore [FILE]`, `rehome USER [--remove-old]`, `id`, `groups`, `group-add`, `group-del`, `passwd [USER...|--all|--file] [--password|--suffix] [--expire]`, `del [--archive|--remove-home]`, `lock`, `unlock`, `expire`, `key list|add|rm`, `du`, `audit`, `quota set|rm|show`. Mutating subcommands are listed in `mutatingCommands` (`internal/cli/runtime.go`); `backup`, `list`, `id`, `groups`, `du`, `audit` are not.
- `lock` runs `usermod -L -e 1` so SSH key logins stop too; `unlock` reverses it.
- `user audit` compares `users.json` with `/etc/passwd`: missing accounts (restore candidates), uid drift, home drift, and untracked accounts under `home_base`.
- Quota (`SetQuota`): xfs uses project quota keyed by uid on the home directory (requires `prjquota`), ext4/ext3 uses user quota (requires `usrquota`); other filesystems and missing mount options are errors naming the fix. rootfiles never changes mount options. Size `0` removes the limit; the size is recorded in `UserMeta.Quota`.
- System user scans (`scanSystemUsers`) cover uid 1000-65533 and skip `nologin`/`false` shells.

## Distro support

- Ubuntu/DGX and Rocky. On Rocky the admin group is `wheel` (`config.AdminGroup`); the `sudo` name is translated, never created.
- Quota hint messages name APT packages (`xfsprogs`, `quota`) on every distro.

## Safety

- Root-mutating subcommands build their context with `buildRunContext` ([cli-runtime](cli-runtime.md)).
- Never follow symlinks or trust non-root-owned paths under the home base; never remove a home outside `--archive`/`--remove-home`/`--remove-old`; never put a password on a command line.
- `user del --remove-home` asks for confirmation unless `--yes`.

## Tests that pin it

- `internal/module/users_test.go`: home base Check/Apply, useradd `HOME`, untrusted base refusal, backup merge, restore keys, passwd parsing, `keyID`.
- `internal/module/metadata_test.go`: refuse/warn split for untrusted metadata, symlink refusal, backup without an untrusted DB.
- `internal/module/homelink_test.go`, `internal/module/accounts_test.go`, `internal/module/rocky_accounts_test.go`, `internal/module/quota_test.go`, `internal/cli/user_test.go`.
- Scenarios (CI `scenario` job): `tests/scenarios/user-lifecycle.sh`, `user-backup-restore.sh`, `user-rehome.sh`, `user-quota.sh`, `user-list-names.sh`, `declarative-accounts.sh`, `os-reinstall-recovery.sh`.

## Open issues

- [#88](https://github.com/STAIxBWLB/rootfiles/issues/88): `user passwd --password` lands in the audit log and backup note.
- [#94](https://github.com/STAIxBWLB/rootfiles/issues/94): duplicated `generatePassword` and home-base defaulting, direct stdout prints in `users.go`, `lifecycle.go`, `quota.go`.
