---
id: self-update
type: module
title: Self-update, install and release pipeline
status: active
parent: architecture
paths:
  - internal/cli/upgrade.go
  - scripts/**
  - .goreleaser.yaml
  - .github/workflows/**
---

# Self-update, install and release pipeline

## Purpose

- Get a verified `rootfiles` binary onto a host and keep it current: the release build (goreleaser), the installer (`scripts/install.sh`), and `rootfiles update` (alias `upgrade`).
- Gate every release on a passing Test run of the tagged commit and on release notes in the AGENTS.md format.
- Fleet-wide rollout of updates (`rootfiles fleet update`, `bootstrap`) belongs to [fleet](fleet.md); cloudflared updates (`tunnel update`) to [cloudflared](cloudflared.md).

## Contracts and invariants

Release artifacts (`.goreleaser.yaml`)
- Builds `./cmd/rootfiles` for linux and darwin, amd64 and arm64, `CGO_ENABLED=0`, with `main.version` and `main.commit` set by ldflags.
- Archive name `rootfiles_<version without v>_<os>_<arch>.tar.gz`, checksums in `checksums.txt`, `scripts/install.sh` attached to the release. The updater and installer build these names themselves: changing a template breaks every deployed updater.

`rootfiles update` (`internal/cli/upgrade.go`)
- Latest-tag HTTP plumbing is shared with cloudflared through `module.FetchLatestGitHubTag`; rootfiles retains `http.DefaultClient`, caller cancellation, raw tags and legacy error strings. The cloudflared wrapper retains its separate 15-second client.
- Target is `--version` or the GitHub `releases/latest` tag of `STAIxBWLB/rootfiles`. Versions are compared for equality only (`normalizeVersion` strips `v` and anything after a space); an equal version is a no-op, any other `--version` is installed, downgrades included.
- Linux only. The archive is downloaded to a temp dir and its SHA-256 must match the line for that exact archive name in the release's `checksums.txt`; a missing line or mismatch aborts before anything is replaced.
- `replaceBinary` stages the new binary as a sibling temp file in the target directory, chmods 0755, and `os.Rename`s it over the resolved (`EvalSymlinks`) current binary. Never write over the running executable (ETXTBSY) or stage on another filesystem (rename must be atomic). The staging file is removed on any failure.
- After a replace, `ensureRootAlias` creates or repairs the `root` symlink only when the binary is `/usr/local/bin/rootfiles` or `/usr/bin/rootfiles`; errors there never fail the update.
- Updating replaces only the binary. It then runs the new binary's `check`; exit code 2 prints the "Apply pending" hint. Nothing is applied automatically. Release notes must carry an `### Action needed` apply step for changes to generated files (AGENTS.md).
- `update` is in `mutatingCommands` (root plus global lock); `--dry-run` is exempt and stops after download, checksum and extraction.
- The download, verify and replace path uses `os.*` and `net/http` directly, not `rc.Runner`: it replaces the tool itself, not host configuration.

Installer (`scripts/install.sh`)
- Stable channel: resolves `--version` or the latest release tag, downloads the archive and `checksums.txt`, verifies with `sha256sum` or `shasum -a 256`, installs to `/usr/local/bin/rootfiles` with mode 755. Supports linux and darwin, amd64 and arm64.
- Dev channel builds `main` from source with Go and sets `main.version=dev-<commit>`.
- On Linux, `finish_install` creates `/usr/local/bin/root` and verifies that `sudo rootfiles` resolves to this binary for the invoking account (sudo `secure_path` omits `/usr/local/bin` on Rocky); only then does it add a previously absent `/usr/bin/rootfiles` link. `ensure_compat_symlink` never replaces an unrelated file or symlink.

CI (`.github/workflows/test.yaml`, `scripts/ci-decision.sh`)
- Test runs on every main push and PR, with no `paths` filter, so every main commit gets a Test result that Release can check. Main pushes are never cancelled.
- The `decision` job sets `run_full=false` only when no changed file matches `TEST_INPUTS` (minus `TEST_IGNORE`, `docs/releases/`), or when a main push has the exact tree of a merged PR head whose latest Test run passed and that contained the pre-merge main. Anything unknown (API failure, new ref, 300 or more files) runs full. A rename counts by both paths. `ci-decision.sh --self-test` pins the classification.
- `release-notes` runs regardless of the decision. Expensive jobs (`lint`, `vuln`, `unit`, then `integration`, `module`, `scenario`, `fleet-scenario`, `tenant-scenario`, `exporter-firewall-scenario`, `rocky`, `monitoring-hub`) run only when `run_full` is true; the feature specs own what those scenarios assert.
- `lint` also vets and builds for darwin/arm64 (read-only commands on operator laptops) and checks `go mod tidy` drift.

Release (`.github/workflows/release.yaml`)
- Triggered by a `v*` tag push; the tag must match `^v[0-9]+\.[0-9]+\.[0-9]+$`.
- Requires a successful main-push Test run for the tagged commit (waits up to 30 minutes; fails when the commit has no main-push run), so only commits on main are released.
- Requires `docs/releases/<tag>.md` to pass `scripts/check-release-notes.sh`, then runs goreleaser with that file as the release body.

Release notes (`scripts/check-release-notes.sh`)
- The format rules live in AGENTS.md "Release notes"; this script is their executable form and must stay in step with them. It checks format only: plain first sentence, only the allowed `###` sections in order and non-empty, no SHA or conventional-commit prefix, a PR reference on bullets from v0.11.0 on, and the changelog link to the previous note in version order as the last line.

## Behaviour

- Update progress/check text uses the command writer through UI plain-text helpers. Exit codes, error printing, download/replace behavior and message bytes are unchanged.

- Test workflow input prefixes refer to existing source/build/test paths; stale root-level `profiles/` and `templates/` prefixes are absent. Documentation inputs added by the docs workflow remain independently tracked.

- `rootfiles update [--check] [--version vX.Y.Z] [--dry-run]`: prints current and target, downloads, verifies, extracts the regular file named `rootfiles` from the archive, replaces, ensures the alias, then reports apply-pending. `--check` prints whether an update exists and exits 0 either way.
- `install.sh [--version vX.Y.Z] [--channel stable|dev]`, piped to `sudo bash` from the raw main-branch URL.
- `bash scripts/check-release-notes.sh [file...]` (default: every `docs/releases/v*.md`) and `--self-test`.

## Distro support

- `update` replaces the binary on any Linux host, Ubuntu, DGX OS and Rocky alike; it refuses on darwin. A macOS controller is updated by installing the darwin archive (installer or by hand after checking `checksums.txt`).
- The installer's sudo path check exists for Rocky's `secure_path`.

## Safety

- Every downloaded artifact is checked against `checksums.txt` before it is installed. Exception (unverified as intended): `install.sh` only warns and skips verification when neither `sha256sum` nor `shasum` exists.
- `checksums.txt` comes from the same release as the archive, so verification protects against corruption and partial downloads, not against a compromised release.
- Never weaken the Release gate (Test success for the tagged commit, release-notes check).

## Tests that pin it

- `internal/cli/upgrade_test.go`: `TestNormalizeVersion`, `TestVerifyChecksum_Valid`, `TestReplaceBinary`, `TestReplaceBinary_CleansStagingOnCopyFailure`, `TestEnsureRootAlias_SkipsCustomLocations`, `TestEnsureRootAlias_IgnoresNonRootfilesBinary`.
- `scripts/ci-decision.sh --self-test` (run by `lint`) and `scripts/check-release-notes.sh --self-test` (run by `release-notes`).
- The `unit` job's `./bin/rootfiles --help` smoke check. The installer runs in the Rocky fixture (`tests/run-rocky.sh`): sudo `secure_path` resolution through the `/usr/bin/rootfiles` link and refusal to replace unrelated `/usr/local/bin/root` or `/usr/bin/rootfiles` paths. Its Ubuntu path, checksum fallback and download failures have no automated test.

## Open issues

- [#87](https://github.com/STAIxBWLB/rootfiles/issues/87): Go toolchain and action pinning, required checks (workflows use `go-version: stable` and `govulncheck@latest`).
