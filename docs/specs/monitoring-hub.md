---
id: monitoring-hub
type: submodule
title: Monitoring hub
status: active
parent: monitoring-exporters
paths:
  - internal/module/monitoring_hub.go
  - internal/config/monitoring_discovery.go
---

# Monitoring hub

History: designed in #13 (phase 4) and shipped in #19 (v0.12.0); alert annotations and `alertmanager_retention` in #50, filesystem threshold in #51, reload on change in #68, start with Docker in #84.

## Purpose

- An opt-in part of the `monitoring` module (`modules.monitoring.hub.enabled`) that runs Prometheus, Alertmanager and Grafana on one Docker host from a generated Compose file under one systemd unit, scrapes the fleet's exporters through Prometheus `file_sd`, and evaluates a small embedded rule set.
- Scrape targets are produced by `rootfiles fleet targets --push` ([fleet](fleet.md)); exporters are [monitoring-exporters](monitoring-exporters.md).

## Contracts and invariants

| Decision | Owner | Limits | Revisit when |
|---|---|---|---|
| Monitoring uses Prometheus, Alertmanager, Grafana and NVIDIA DCGM Exporter; rootfiles installs and configures them and does not reimplement metrics or alerting | user | Images pinned in config defaults | - |

- 2026-10-03: hub web UIs bind to loopback by default. External access goes only through a Cloudflare Tunnel on the hub, never through an opened port. Basis: owner decision on #13.
- **Defaults** (`MonitoringHubConfig.WithDefaults` in `internal/config/monitoring.go`): `data_dir` `/data/monitoring`, retention `30d`, `targets_file` `/etc/rootfiles/monitoring/discovery/targets.json`, `listen_address` `127.0.0.1`, ports 3000/9090/9093, pinned Prometheus, Alertmanager and Grafana images, Grafana password file `/etc/rootfiles/monitoring/grafana-admin-password`.
- **Validation before mutation.** `checkMonitoringHub` is read-only and runs inside the module preflight and again at the start of `applyMonitoringHub`. It requires the `docker` command and the Compose plugin, a valid config (`validateMonitoringHubConfig`: absolute non-root `data_dir`, IP `listen_address`, distinct ports in range, pinned images, valid retentions), and safe secret files.
- **Discovery directory isolation.** Prometheus mounts the directory of `targets_file` read-only, so an atomic replacement is visible inside the container. `config.ValidateMonitoringDiscoveryPath` (`internal/config/monitoring_discovery.go`) is the single path rule, used by both the module and `fleet targets --push`: canonical absolute `.json`; not a broad system directory; under `/etc` only inside the managed discovery directory; never inside `/root`, `/usr`, `/proc`, `/sys`, `/dev`, `/boot`, `/home`; a custom directory must be named `discovery`; must not contain a configured secret (receiver, Telegram token, Grafana password, tunnel token) and must not overlap `data_dir`. On the host, `validateMonitoringDiscoveryFilesystem` requires every existing ancestor to be a root-owned, non-symlink directory not writable by group or others, and the directory to hold only the targets file.
- **Secrets stay files.** `alert_receiver_file` and `telegram_bot_token_file` must be regular, root-owned, mode 0600 and are opened with `O_NOFOLLOW` (`validateOptionalRootSecretFile`). The receiver file is bind-mounted read-only as the full Alertmanager config; without it Alertmanager gets a no-op default receiver. The Telegram token is mounted at `/run/secrets/telegram-bot-token`. A configured Grafana password file must already exist; otherwise Apply generates a 32-byte hex password into the default root 0600 file. Secret contents are never rendered into generated files or printed.
- **Generated files** under `/etc/rootfiles/monitoring` (dir 0700): `compose.yaml`, `prometheus.yaml`, `alertmanager.yaml`, `alert-rules.yaml`, `grafana-datasource.yaml` (datasource only; dashboards are not shipped), plus `/etc/systemd/system/rootfiles-monitoring-hub.service`. Each is compared byte for byte; Apply writes only differences.
- **Container hardening**: every service runs `user: 0:0` with `cap_drop: [ALL]`, `no-new-privileges`, `restart: unless-stopped`, and publishes only `<listen_address>:<port>`. Grafana adds `CHOWN` and disables sign-up. Prometheus runs with `--web.enable-lifecycle` for reloads.
- **Reload on change** (#68): when the stack is active and something changed, Apply runs `docker compose up -d`, then POSTs `/-/reload` to Prometheus (if `prometheus.yaml` or `alert-rules.yaml` changed) and Alertmanager (if `alertmanager.yaml` changed) on the published port, retrying up to 30s (`reloadMonitoringHubService`). A failed reload restores the previous file contents, so the next apply sees the change and retries instead of passing on stale config.
- **Start with Docker** (#84): the unit is `WantedBy=multi-user.target docker.service`; Check requires the `docker.service.wants` link and Apply runs `systemctl reenable` when it is missing.
- **Alert rules** (`monitoringHubRules`): `FleetTargetDown` (5m), `FleetFilesystemNearlyFull` (above 99% for 10m, #51), `RootfilesConfigurationDrift`, `RootfilesDoctorFailure`, `RootfilesReportStale` (48h or missing timestamp), `GPUXIDError`, `GPUUncorrectableECCError`, `GPUHighTemperature` (above 85 C for 10m). Every rule carries a summary and a diagnostic description naming the host and GPU (#50). `rootfiles update` replaces only the binary, so a rule change needs `rootfiles apply` on the hub and an `### Action needed` line in the release notes.
- **Port ownership.** The exporter preflight exempts a hub port only when Compose reports that exact service running with that host-to-container binding on `listen_address` (`monitoringHubOwnsListener`).

## Behaviour

- Check: config and filesystem validation; one change for any drift in generated files or unit; missing password file, data directories (`prometheus`, `alertmanager`, `grafana` under `data_dir`, root-owned real directories) and targets file; each Compose service not running; unit not active.
- Apply: creates directories, an empty `[]` targets file when absent, the Grafana password when needed, writes changed files and the unit. Dry-run stops after the Runner-gated writes. Otherwise it reloads systemd for a changed unit, reenables, runs `compose up -d` plus reloads when active, or `systemctl enable --now` when inactive, and fails when any service does not reach running state (`verifyMonitoringHubServices`).
- Scrape config: one static `prometheus` job and a `fleet` `file_sd` job refreshed every 30s; labels come from the targets file (`host`, `exporter`, `group_*`).

## Distro support

- Ubuntu and DGX OS hosts with Docker and the Compose plugin. Refused on Rocky by capability validation (`monitoring` is not in the Rocky set in `internal/config/distro.go`).

## Safety

- UIs are loopback-only unless `listen_address` says otherwise; publish Grafana through the `cloudflared` module with the origin `http://127.0.0.1:<grafana_port>`. Scraping is outbound, so the loopback binding does not affect collection.
- The hub never deletes `data_dir` contents; `data_dir` cannot be `/`.
- On hub hosts with the exporter firewall guard, the guard admits the hub's Compose bridge so Prometheus can scrape the hub host itself (see [monitoring-exporters](monitoring-exporters.md)).

## Tests that pin it

- `internal/module/monitoring_hub_test.go`: pinned images and loopback ports, configured listen address, optional `alertmanager_retention`, alert rule contents and promtool checks, receiver template, secret contents never rendered, unsafe secret files, configured password file required before mutation, lifecycle and startup failure with fake commands, data and discovery directory trust, discovery directory holding only the targets file, directory mount for atomic replacement, broad mount refusal, exact Compose listener ownership, Prometheus reload when only rules change.
- `internal/config/monitoring_discovery_test.go`: discovery path rules and `alertmanager_retention` validation.
- `internal/module/monitoring_scenario_test.go` (`TestWriteMonitoringScenarioFixtures`) generates fixtures for `tests/scenarios/monitoring-hub.sh` (CI job `monitoring-hub`).

Verification evidence (folded from the 2026-10-03 rollout checklist):

| Contract | Evidence |
|---|---|
| Hub configs, readiness, target push, alerting | `tests/scenarios/monitoring-hub.sh` in CI `monitoring-hub`: real Prometheus, Alertmanager and Grafana from generated configs, promtool and amtool validation, a `file_sd` target becoming healthy with labels, `fleet targets --push` through a real SSH fixture without a Prometheus container restart, and `FleetTargetDown` firing after a simulated five-minute outage |
| Root-owned 0600 secrets, loopback web ports | `TestMonitoringHubRejectsUnsafeSecretFilesWithoutEchoingContents`, `TestRenderMonitoringHubFilesPinsImagesAndLoopbackPorts`; the scenario asserts loopback-only bindings |
| Cloudflare Access policy and live receiver delivery | Operator-side, not in CI (unverified by tests) |

## Open issues

- [#94](https://github.com/STAIxBWLB/rootfiles/issues/94): the hub has its own write-if-changed loop and calls `daemon-reload` directly with failure as error.
