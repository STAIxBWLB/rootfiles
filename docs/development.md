# Development

Building, testing and releasing rootfiles. Agent rules for this repository are in [AGENTS.md](../AGENTS.md); the CI and release contracts are in [docs/specs/self-update.md](specs/self-update.md).

## Build

Requires the Go version in `go.mod`.

```bash
make build    # bin/rootfiles, version and commit set through -ldflags
make test     # go test ./... -race -count=1
make lint     # golangci-lint run ./...
```

## Test layers

| Layer | Where | What it covers |
|---|---|---|
| Unit | `*_test.go` next to the code | Module `Check`/`Apply` logic, config loading and validation, CLI wiring. Tests never touch the host: they override package path variables and put stub commands on a private `PATH`. |
| Integration | `tests/integration/` | Each built-in profile applied in a privileged container (`run-profile.sh`), and single modules in isolation (`run-module.sh`), on Ubuntu 22.04, Ubuntu 24.04 and a DGX OS mock. The Rocky images also live here. |
| Scenario | `tests/scenarios/` | End-to-end flows against a built binary, such as backup and restore, rollback and lock, fleet over SSH, tenant lifecycle, exporter firewall and the monitoring hub. |
| Rocky | `tests/run-rocky.sh` | Native Rocky core profile, SSH and installer. |

Documentation drift tests run with the unit tests: `go test ./internal/cli ./internal/config -run Docs` fails when a command, flag or config key is missing from [commands.md](commands.md) or [configuration.md](configuration.md). Add the entry in the same change.

## CI jobs

The Test workflow (`.github/workflows/test.yaml`) runs on every push to `main` and every pull request.

| Job | Purpose |
|---|---|
| `decision` | Decides whether the expensive jobs run: they are skipped when only non-test files changed, or when a `main` push carries a tree its pull request already validated (`scripts/ci-decision.sh`). |
| `release-notes` | Always runs: self-test of `scripts/check-release-notes.sh`, then checks every `docs/releases/*.md`. |
| `lint` | `gofmt`, `go vet`, a darwin/arm64 build, and `go mod tidy` drift. |
| `vuln` | `govulncheck ./...`. |
| `unit` | `go test ./... -race -count=1` with a coverage summary, then builds the binary and runs `--help`. |
| `integration` | 3 OS images by up to 4 profiles (11 combinations). |
| `module` | 9 modules on Ubuntu 22.04 and 24.04, plus `gpu` on the DGX mock. |
| `scenario` | 14 flows: dry run of all profiles, user backup/restore, rehome, list names, lifecycle and quota, declarative accounts, tunnel setup/teardown, OS reinstall recovery, system backup, GPU allocation, status, SSH lockout guard, state/rollback/lock. |
| `fleet-scenario` | Fleet controller against two SSH containers (`tests/scenarios/fleet.sh`). |
| `tenant-scenario` | Tenant lifecycle on the runner's Docker and systemd (`tests/scenarios/tenant-lifecycle.sh`). |
| `exporter-firewall-scenario` | Exporter firewall guard on the runner's Docker, iptables and systemd (`tests/scenarios/exporter-firewall.sh`). |
| `rocky` | Rocky 8.9, 8.10 and 9 images (`tests/run-rocky.sh`). |
| `monitoring-hub` | Generated hub configs, targets and alert delivery (`tests/scenarios/monitoring-hub.sh`). |
| `ci-ok` | Single required gate: passes only when every job above succeeded, or was skipped because `decision` allowed it (`scripts/ci-ok.sh`). |

The monitoring hub scenario needs Docker with the Compose and Buildx plugins, SSH, curl and Python 3. Check `docker compose version` and `docker buildx version` before running it locally.

## Releases

- The Release workflow (`.github/workflows/release.yaml`) runs on every `v*` tag push. Test does not run for tags, so it waits (up to 30 minutes) for the `main`-push Test run of the tagged commit and stops unless that run succeeded. It then checks the release notes and runs GoReleaser.
- Every tag `vX.Y.Z` needs `docs/releases/vX.Y.Z.md` in the tagged commit. The workflow publishes it as the GitHub release body and fails when it is missing or fails `bash scripts/check-release-notes.sh`.
- Write the notes by the rules in [AGENTS.md, "Release notes"](../AGENTS.md#release-notes) and run the checker before pushing.
