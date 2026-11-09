---
id: state-rollback
type: module
title: State, Backups and Rollback
status: active
parent: architecture
paths:
  - internal/state/**
  - internal/exec/backup.go
  - internal/cli/rollback.go
  - internal/cli/backup.go
---

# State, Backups and Rollback

## Purpose

What rootfiles remembers on a host and how an operator undoes it: the recorded apply state and history, the applied-config copy, the global lock, per-run file backups with `rootfiles rollback`, and the full-host `rootfiles backup` archive for OS reinstall.

## Contracts and invariants

- **Locations** (`internal/state/state.go`): state dir `/var/lib/rootfiles` (`ROOTFILES_STATE_DIR`), audit log `/var/log/rootfiles.log` (`ROOTFILES_LOG_FILE`), lock `/run/rootfiles.lock` (`ROOTFILES_LOCK_FILE`), per-run backups under `<state>/backups`. The env overrides exist for tests and non-standard hosts; tests must set them instead of touching real paths.
- **Recorded run** (`Record`, `Run`): `state.json` is replaced atomically (temp file plus rename) and the same record is appended to `history.jsonl`. Only `apply` records runs (see [engine](engine.md)). Field names (`config_sha256`, `profile`, `config_path`, `success`, `backup_id`, `modules`) are read by status, provenance and fleet; renaming one is a contract change.
- **Applied copy** (`AppliedConfigPath`, `SaveAppliedConfig`): `<state>/applied-config.yaml`, written atomically with mode 0600; `nil` removes it. Its trust and reuse rules live in [cli-runtime](cli-runtime.md).
- **Global lock** (`Acquire`, `Release`): non-blocking exclusive `flock`; the holder writes its PID into the file and a second mutating command fails at once with a message naming that PID. There is no waiting and no stale-lock breaking; the kernel drops the lock when the process exits.
- **Per-run backup** (`internal/exec/backup.go`): a session id is the start time (`20060102-150405`). `Preserve` records each path the first time the run touches it: regular files are copied (0600, under a 0700 session dir) with mode, uid and gid; symlinks keep their target; a missing path is recorded as created so rollback can remove it; directories are never preserved. The session dir is created lazily, so a run that changes nothing leaves no backup. `manifest.json` is rewritten after every entry.
- The Runner calls `Preserve` before `WriteFile`, `Symlink`, `Remove` and the target of `Rename` (see [exec-packages](exec-packages.md)). A write that bypasses the Runner is not rolled back.
- **Restore** (`RestoreBackup`): walks entries in reverse; restores files (mode, then best-effort owner) and symlinks; removes paths the run created; keeps any directory the run created, because it may hold data. `LoadBackup` rejects ids that are not a single path element. With `dryRun` it only lists actions.
- **rollback** (`internal/cli/rollback.go`): without an id lists sessions (read-only, no root). With an id it previews, then calls `requireRootAndLock` itself (it is not in `mutatingCommands`), asks for confirmation (`--yes` skips), restores, removes the applied copy (`forgetRolledBackRun`, since the host no longer matches that config) and writes an audit record. Packages, users and services are not reverted, and affected services are not restarted.
- **backup** (`internal/cli/backup.go`): builds its context with `buildRunContext`; writes `rootfiles-backup-<host>-<YYYYMMDD>` under `--dir` (default `<state dir>/system-backup`, with deprecated `-o/--output` aliases) with system info, users DB, an `/etc` tarball, root crontab, `/root/.ssh` and `/usr/local/bin` tarballs, the Docker image list and a config snapshot (`config.SnapshotCurrent`, inline tunnel token cleared). The output path must pass `config.RootOnlyBase`; tarballs are created under `umask 077`; the directory is forced to 0700 and every artifact to 0600 after writing, also on a same-day re-run. Individual artifact failures are warnings, not errors.

## Behaviour

- Backup/rollback text uses the command writer. Rollback symlink previews use the ASCII ` -> ` separator; restore actions and manifest fields are unchanged.

- `rootfiles rollback [BACKUP_ID] [--dry-run] [--yes]`.
- `rootfiles backup [--dir DIR] [--skip-docker] [--skip-etc] [--dry-run]`: all writes go through the Runner, so a dry run writes nothing. Restore is manual: `rootfiles apply --config <dir>/config-snapshot.yaml`, plus `rootfiles user restore` for accounts.

## Distro support

- Distro-neutral. The backup's `/etc` file list and tar invocations assume standard Linux paths; on paths that do not exist the artifact is skipped.

## Safety

- Backups and the archive hold private keys and `/etc`: keep them root-only (0700 directories, 0600 files) and refuse output directories another user could control.
- Rollback never removes directories and never recurses.
- `backup` is a mutating command with the native preflight, lock/audit and backup session. Session notes redact secret inputs before rollback lists them.

## Tests that pin it

- `internal/state/state_test.go`: `TestRecordAndLast`, `TestAcquireIsExclusive`.
- `internal/exec/backup_test.go`: `TestBackupPreserveAndRestore`, `TestRestoreBackupKeepsCreatedDirectories`, `TestLoadBackupRejectsTraversal`.
- `internal/cli/tunnel_test.go`: `TestForgetRolledBackRun`.
- `internal/cli/backup_test.go`: `TestBackupCmd_DryRunWritesNothing`, `TestBackupCmd_RealRunIsRootOnly`, `TestBackupCmd_RefusesUntrustedOutputDir`, `TestBackupCmd_RerunHardensExistingDir`, `TestBackupCmd_RelativeOutputDir`.
- Scenarios (CI job `scenario`): `tests/scenarios/state-rollback-lock.sh`, `tests/scenarios/system-backup.sh`, `tests/scenarios/os-reinstall-recovery.sh`.

## Open issues

- [#88](https://github.com/STAIxBWLB/rootfiles/issues/88): redact secrets from the backup-session note that `rollback` prints.
- [#91](https://github.com/STAIxBWLB/rootfiles/issues/91): `backup` becomes a mutating command (lock and audit); `rollback` honours `ROOTFILES_YES`.
