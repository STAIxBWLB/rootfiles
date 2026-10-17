# rootfiles

Go-based server bootstrapping for Ubuntu, NVIDIA DGX OS and the documented Rocky core matrix, with an unprivileged operator-side SSH fleet controller.

## Build & Test

```bash
make build        # → bin/rootfiles
make test         # go test ./... -race
```

## Architecture

- `cmd/rootfiles/` — entry point
- `internal/cli/` — cobra commands (apply, backup, check, config, doctor, gpu, rollback, schedule, status, tunnel, update, user); `update` keeps `upgrade` as an alias. `fleet` is the operator-side controller; `runtime.go` holds the native-host root/lock preflight (native mutating commands are listed in `mutatingCommands`), audit logger and `newRunner`
- `internal/config/` — config structs, YAML profile loader (profiles merged as YAML trees, strict keys), `Validate()`, system detector
- `internal/module/` — Module interface + 13 implementations (locale, system, packages, users, ssh, security, docker, nvidia, gpu, cloudflared, storage, network, monitoring), plus `doctor.go`
- `internal/exec/` — shell runner (dry-run aware; `Runner.Backup` preserves files before WriteFile/Remove/Rename/Symlink), APT and RPM/DNF package-manager backends
- `internal/state/` — `/var/lib/rootfiles/state.json` + history, global flock
- `internal/ui/` — interactive prompts (Charm huh) + shared output styling: `styles.go` (lipgloss palette), `markers.go` (✓ ✗ → ⚠), `format.go` (WriteHeader/Section/KV/Hint/Bullet). lipgloss auto-detects TTY and honours `NO_COLOR`; all status-style reports must go through these helpers.

## Module Interface

Every module implements three methods with identical signatures:

```go
type Module interface {
    Name() string
    Check(ctx context.Context, rc *RunContext) (*CheckResult, error)
    Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error)
}
```

- `Name()` returns the module id used in `defaultOrder` and `--module` filters.
- `Check()` reports pending changes without side effects; `CheckResult.Satisfied` gates whether `Apply()` runs.
- `Apply()` performs idempotent changes; returns `ApplyResult.Changed` so callers can summarise diffs.

`NewRegistry()` in `internal/module/module.go` wires all 13 implementations. `defaultOrder` in the same file defines execution sequence. These two lists MUST stay in sync — `TestRegistryDefaultOrderSync` in `module_test.go` enforces this.

## Conventions

- Go 1.23+, conventional commits
- Profiles embedded via go:embed in `internal/config/profiles/`
- Module execution order is static (defined in `module.go defaultOrder`)
- `--yes` propagates via RunContext, bypasses all prompts
- `--dry-run` logs commands without executing (write ops gated)
- GPU allocation DB writes go through `withGPUDBLock` (flock + atomic rename) to protect concurrent `gpu assign`/`revoke` calls
- File writes/removals go through `rc.Runner` (never `os.*` directly) so dry-run and backups apply; never `rm -rf` user data
- `Check()` and `Apply()` must agree: Apply only acts on what Check reports and returns `Changed` only for real work; non-fatal problems go in `ApplyResult.Warnings`
- Tests must not touch the host: override package-level path vars and stub commands on PATH (`fakeBin`) instead of calling real systemctl/apt
- Discovery directory mounts must contain only the configured targets file. Validate real root-owned ancestors and directory contents before launch to prevent sibling-secret exposure.
- Verify installed firewall state and port allowances before changing SSH ports; unknown state blocks the change.
- Root-mutating subcommands other than `apply` that load a config build their run context through `buildRunContext` or `buildConfigFreeRunContext` (`internal/cli/tunnel.go`), which act on the config they are given (`--config`, `--profile`, `ROOTFILES_PROFILE`) or on the applied copy `apply` keeps in the state directory when root alone controls it and it matches the recorded run. Without either they use `minimal` with home-base detection; they never re-read a recorded path, re-resolve a recorded profile or take the profile detection suggests. Someone other than root can control a recorded file or its `extends`, re-resolving a profile drops what the apply added on top of it (such as `--home-base`), and a suggested profile pins values the host never chose.

## Fleet and distro boundaries

- Fleet commands run as the operator and inherit OpenSSH host-key verification. They do not take the local native-host root lock; remote native mutations retain their own root/lock preflight. Explicit selection and confirmation guard controller rollouts, and their results are written to the controller audit log.
- A successful native apply records the effective configuration digest. Read-only reports must not claim applied provenance for legacy, failed, changed or unrelated targets.
- Check uses cached, read-only package queries. Metadata refresh and package transactions belong to Apply, through the runner; unavailable required advisory sources must not be reported as working protection.
- Rocky support is per module, as documented in docs/rocky-support.md. Reject unsupported requested capabilities before host configuration changes; do not infer support for another RPM distribution from Rocky support.

## Release notes

- Every published tag `vX.Y.Z` needs `docs/releases/vX.Y.Z.md` in the tagged commit. Add it in the release PR; the Release workflow publishes it as the GitHub release body and fails before goreleaser when it is missing or empty.
- Readers are operators who run `rootfiles` on servers (and the fleet controller). Write what changes for them, not how the code changed. Cover every PR since the previous published release.
- Format:
  - First line: one sentence on what this release changes for the user. No heading before it.
  - Then only the sections that have content, in this order: `### New`, `### Improved`, `### Fixed`, `### Action needed`.
  - One bullet per change in plain language, naming the command, flag or file the user touches (`rootfiles apply`, `rootfiles fleet check`). Describe fixes by the symptom the user saw. End the bullet with the PR number, e.g. `(#123)`.
  - `### Action needed`: steps required after upgrading (config edits, renamed commands, manual migrations). Omit it when there are none.
  - Leave out tests, CI, refactors, docs, agent instructions and release plumbing. A release with nothing else is the single line `Maintenance release: no user-facing changes.` plus the changelog line.
  - No commit SHAs, conventional-commit prefixes or internal package paths.
  - Last line: `**Full changelog**: https://github.com/STAIxBWLB/rootfiles/compare/<previous release>...vX.Y.Z` (`.../commits/vX.Y.Z` for the first release).
- Upgrade command to mention when users must act: `sudo rootfiles update` or `rootfiles fleet update --version vX.Y.Z`.

