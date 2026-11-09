---
id: host-base
type: module
title: Host base (locale, system, storage)
status: active
parent: architecture
depends-on:
  - engine
paths:
  - internal/module/locale.go
  - internal/module/system.go
  - internal/module/storage.go
---

# Host base (locale, system, storage)

## Purpose

- `locale`: system `LANG` and timezone.
- `system`: hostname, swapfile, sysctl, journald size cap and the Ubuntu APT mirror. Runs before `packages` in `defaultOrder` so a mirror change applies to the first install.
- `storage`: data directory, home base and its `.rootfiles` metadata directory, managed symlinks and the Docker storage directory.

## Contracts and invariants

- All writes go through `rc.Runner` (`WriteFile`, `MkdirAll`, `Remove`, `Symlink`, `Run`) so dry-run and backups apply. Path variables (`defaultLocalePath`, `hostnamePath`, `sysctlPath`, ...) are package vars only so tests can redirect them.
- Locale sets only `LANG` (`setLocaleConfig`): it rewrites the distro locale file with `LANG=` first, keeps other lines and drops a stale `LC_ALL`. Forcing `LC_ALL` system-wide would override every per-user choice.
- Locale file: `/etc/default/locale` on Ubuntu/DGX OS, `/etc/locale.conf` on Rocky (`localeConfigPath`). Check reports the actual `write` to that path. On Ubuntu it separately reports `locale-gen` and, when that command is absent, installation of `locales` via `packageInstallCommand`; it never claims Apply runs `update-locale`.
- Current timezone comes from the `/etc/localtime` symlink, falling back to `/etc/timezone` (`currentTimezone`). Apply refuses an unknown zone (not under `/usr/share/zoneinfo`, after installing `tzdata` when missing).
- Rocky locale (`rockyLocaleLanguagePack`): a locale must already be listed by `localectl list-locales` or map to a safe `glibc-langpack-<lang>` name; `C`/`POSIX` variants never install a package. Apply installs the pack, re-lists and refuses to write the config when the locale is still unavailable. An installed pack that does not provide the locale is an error.
- System:
  - hostname: `hostnamectl set-hostname`, falling back to writing `/etc/hostname` with a warning; `ensureHostsEntry` keeps exactly one `127.0.1.1 <hostname>` line in `/etc/hosts`.
  - swap is created only when `/proc/swaps` lists no device. An existing `/swapfile` without active swap is an error (operator decides); a failed create step removes the partial file. The fstab entry is appended once.
  - sysctl goes to `/etc/sysctl.d/90-rootfiles.conf` (sorted keys), journald to `/etc/systemd/journald.conf.d/90-rootfiles.conf`. Reload failures (`sysctl -p`, journald restart) are warnings.
  - `apt_mirror` rewrites only `archive.ubuntu.com` (and country mirrors) URLs in `ubuntu.sources`/`sources.list` (`mirroredSources`). Removing the key does not revert sources.
- Storage:
  - `requireTrustedMetadata` runs at the start of both Check and Apply; a home base or `.rootfiles` tree that a non-root user could control is refused (owned by [users](users.md)).
  - A symlink replaces an existing path only when no data can be lost: missing path, an existing symlink, or an empty directory (`symlinkBlocker`). A regular file or populated directory is an error in Apply and `(blocked: ...)` in Check.
  - The home base and metadata directory are created only for a non-default base (`homeBase != "/home"`) through `ensureMetaDir`. The Docker storage directory is created `0710`.

## Behaviour

- Check of each module lists one change per unmet item; Apply acts only on those items and sets `Changed` only when it acted.
- Locale on Ubuntu: installs `locales` when `locale-gen` is missing, runs `locale-gen` (failure is a warning), writes the locale file. Timezone: `timedatectl set-timezone`, or without systemd replaces the `/etc/localtime` symlink directly; `/etc/timezone` is written on non-Rocky systems only.

## Distro support

- `locale` and `system`: Ubuntu, DGX OS and Rocky. On Rocky, `system.apt_mirror` is rejected by `ValidateCapabilities` ([distro-rocky](distro-rocky.md)).
- `storage`: Ubuntu and DGX OS only; rejected on Rocky.

## Safety

- Generated-file comparisons preserve legacy headers using the [shared managed-header rule](exec-packages.md#contracts-and-invariants); changing the marker alone never rewrites a converged file or reloads its service.

- Never delete or overwrite operator data to place a symlink or swapfile; refuse with a message that says what to move.
- Keep `requireTrustedMetadata` before any metadata directory work.

## Tests that pin it

- `internal/module/agreement_test.go`: Ubuntu/Rocky locale plans name the actual file write, Apply preserves other settings and removes `LC_ALL`, and a second Check/Apply reports no locale work.
- `internal/module/locale_test.go`: empty config satisfied, Rocky native paths, missing langpack reported, install-then-recheck, unknown Rocky locale not written, langpack mapping, built-in aliases, timezone from symlink, apply without systemd, unknown timezone, `LC_ALL` dropped.
- `internal/module/system_test.go`: full converge (second Check satisfied) and existing swap untouched.
- `internal/module/storage_test.go`: missing data dir, default home base satisfied, dry-run, `isSymlinkTo`, refusal for non-empty directory, replacing empty dir and stale symlink.
- `internal/module/metadata_test.go`: `TestMetadataCommandsRefuseUntrustedBase` covers storage Check refusing an untrusted base.
- CI `module` job: `locale`, `system`, `storage` on `ubuntu-22.04` and `ubuntu-24.04` (`tests/integration/run-module.sh`); CI `rocky` job for Rocky locale and journald.
- `tests/scenarios/os-reinstall-recovery.sh` applies `storage` with the `dgx` profile.

## Open issues

- [#94](https://github.com/STAIxBWLB/rootfiles/issues/94): remaining repeated home-base defaults and output routing; ordinary sysctl/journald/APT-mirror writes already use the shared helper.
