---
id: monitoring-exporters
type: module
title: Monitoring exporters
status: active
parent: architecture
depends-on:
  - engine
paths:
  - internal/module/monitoring.go
  - internal/module/monitoring_exporters.go
  - internal/module/monitoring_firewall.go
  - internal/module/monitoring_guard.go
  - internal/module/assets/**
  - internal/config/monitoring.go
  - internal/config/monitoring_validate.go
---

# Monitoring exporters

History: exporters designed in #13 (phase 3) and shipped in #19 (v0.12.0); later #20, #68 and the exporter firewall guard in #84 (v0.18.0), fingerprint fix #85 (v0.18.1).

## Purpose

- The `monitoring` module (`MonitoringModule` in `internal/module/monitoring.go`) installs Prometheus node exporter and an NVIDIA DCGM exporter on a host, restricts who can reach them, and optionally manages the iptables exporter firewall guard. It also drives the opt-in hub, specified in [monitoring-hub](monitoring-hub.md).
- `internal/config/monitoring.go` holds the defaults (ports 9100/9400, pinned DCGM image, hub defaults) and `internal/config/monitoring_validate.go` validates every `modules.monitoring.*` key plus `users.fleet_sudo_users` (that rule belongs to [fleet](fleet.md)).

## Contracts and invariants

- 2026-10-03: a perimeter firewall with an open internal segment is a supported deployment; `perimeter_firewall: true` records it, so exporters need no host firewall rule there. Basis: owner decision on #13.
- 2026-10-03: all exporter and hub ports are configurable, keep upstream defaults, and are checked for conflicts before apply. Basis: owner decision on #13.
- **Preflight before any mutation.** `monitoringPreflight` runs in both Check and Apply (Apply may be called without Check): configured ports must be distinct; `listen_address` must parse; `preflightExporterFirewall` must pass; every configured port held by a listener that `monitoringListenerManaged` does not attribute to our unit or container (`ss -H -ltnp`, systemd `MainPID`, `docker inspect` of `rootfiles-dcgm-exporter`, Compose `ps` for hub ports) fails with the holder's name. An unrelated `docker-proxy` is never trusted.
- **Apply order** in `MonitoringModule.Apply`: preflight, hub check, exporter guard, exporters, hub, then the guard again on hub hosts so a first apply admits the hub's Compose bridge. The guard always goes before exporters so no exporter starts without its rules.
- **Missing DCGM prerequisites do not hold back node exporter.** `dcgmDependencies` checks `docker`, the Docker `nvidia` runtime, `nvidia-smi` and a visible GPU. Check reports a missing one as `ChangeBlocked` (so `apply --dry-run` fails), or as pending when an earlier module of this run (`docker`, `nvidia`) provides it (`rc.moduleSucceeding`). Apply sets up node exporter and the hub first, then returns the DCGM error with the fix command (#68). No DCGM unit is written while prerequisites are missing.
- **Images are pinned.** `validateMonitoring` requires a tag or digest and rejects `:latest` for `dcgm_exporter_image`; the default in `MonitoringConfig.ExporterImage` is `nvcr.io/nvidia/k8s/dcgm-exporter:4.6.1-4.8.4-distroless`. Explicit host tags/digests win unchanged, so managed hosts that pin an image retain it. The owner reports that this exact tag is already deployed on managed GPU hosts with the current collector CSV and dashboards. Release acceptance still includes the owner's doctor check; this source change does not claim a fresh live-host run. 4.9.0 metric renames/unit changes are outside this update.
- **Generated files**: `/etc/default/prometheus-node-exporter` (only the `--web.listen-address` argument of `ARGS` is replaced, other arguments kept, `nodeExporterDefaultsContent`), `/etc/systemd/system/rootfiles-dcgm-exporter.service` (`renderDCGMUnit`: `docker run --runtime=nvidia --gpus all`, publishes `<listen_address>:<port>:9400`, read-only collectors bind mount, `WantedBy=multi-user.target docker.service`), and `/etc/rootfiles/monitoring/dcgm-exporter/default-counters.csv` (`dcgmExporterCollectorsCSV`, the fields the hub's GPU rules use, including ECC and XID). Check compares each file byte for byte; Apply rewrites only on difference.
- **Exporters come back with Docker** (#84): the DCGM unit must have its `docker.service.wants` link (`dockerWantsLink`); Check reports a missing link and Apply runs `systemctl reenable`.
- **`allow_from` never turns a firewall on.** With UFW active, Apply adds `ufw allow from <cidr> to any port <p> proto tcp` per exporter port and source. `preflightExporterFirewall` refuses before any change when UFW state is unknown, the default incoming policy is not deny/reject, or an existing ALLOW rule (numeric, range or application profile, resolved read-only with `ufw app info`) admits an exporter port from a source outside `allow_from`.
- **Exporter firewall guard** (`exporter_firewall: true`, `internal/module/monitoring_guard.go`): config validation requires an IPv4 `listen_address`, at least one exporter, the default ports 9100 and 9400, and exactly one IPv4 `/32` in `allow_from`. The embedded helper `internal/module/assets/exporter-firewall.sh` is written to `/usr/local/sbin/rootfiles-exporter-firewall` (0750) with `/etc/rootfiles/exporter-firewall.conf` (0600, `BIND_IP`, `ALLOW_FROM`, and on hub hosts `WEB_INTERFACE=br-<id>` of the `monitoring_default` network). Both must be regular, root-owned, not group/world-writable and byte-identical (`exporterGuardFileCurrent`); otherwise Apply removes and rewrites them so ownership and mode are fresh. Then `helper check` must pass, or Apply runs `helper setup`.
- **Helper contract** (keep stable; hand-installed v1 guards are adopted in place): owner marker `rootfiles-exporter-firewall:v1`, chains `RFILES_EXPORTER_IN` (INPUT, node exporter on `BIND_IP:9100`) and `RFILES_DCGM_DU` (DOCKER-USER, conntrack original destination `BIND_IP:9400`), unit `rootfiles-exporter-firewall.service`, drop-ins for docker (`ExecStartPost=apply-docker`), ufw (`ExecStartPost=apply-host`), node exporter and DCGM (`After=` the guard plus `ExecStartPre=+helper verify-host|verify-docker`) and the hub (`After=` only). Exporters refuse to start only while their rules are missing and do not stop when the guard unit or Docker stops. `setup` rebuilds the owned chains behind a temporary DROP guard. Unowned chains with our names are refused, never replaced.
- **Rollback covers the guard's units.** Before `setup`, Apply preserves every path in `exporterGuardSetupFiles` through `Runner.Backup`, so `rootfiles rollback` removes units and drop-ins the helper wrote.
- **Config fingerprint stability** (#85): `config.Fingerprint` hashes the marshalled config, so a new `MonitoringConfig` field must use `omitempty` (as `exporter_firewall` does) or every host's recorded fingerprint changes on update and the fleet reports it unverified. `TestFingerprintStableAcrossReleases` in `internal/config/fingerprint_test.go` pins this.

## Behaviour

- Ordinary defaults/collector/unit writes use `ensureFile`; unit/collector changes retain their distinct restart predicates. A DCGM daemon-reload failure is a warning with manual repair guidance, rather than an immediate error; later service failures retain their prior policy. No reload is added on converged state. Exporter-guard regular-file/owner/mode verification remains a separate safety concern.

- Package-install changes use the distro-aware package command renderer, matching the backend Apply invokes.

- Check converts unknown firewall inspection from preflight/exporter checks into a single blocked change; it omits other pending changes until that blocker is repaired. Other configuration/port-conflict errors retain their existing behavior. Apply and real mutation preflight remain strict before any affected change. The shared model is owned by [ssh-access](ssh-access.md).
- Check: preflight, then `checkMonitoringExporters` (package, defaults file, unit, collectors, wants link, active/enabled state, UFW source rules), `checkMonitoringHub`, `checkExporterGuard` (blocked when firewalld is active, `iptables`/`iptables-save`/`python3` is missing, or the `DOCKER-USER` chain is absent; pending when the docker module of this run may still create it).
- Apply: as in the order above. Unit or collectors changes restart a running DCGM exporter; a changed defaults file restarts node exporter. Exporters that stayed down while rules were missing are `reset-failed` and started after a guard setup.
- `rootfiles doctor` (`doctorExporterExposure` in `internal/module/doctor.go`, owned by [doctor-schedule](doctor-schedule.md)) counts exporters as covered only while `helper check` passes, warns about a helper installed without `exporter_firewall`, and otherwise warns when a non-private listener is covered by neither a restricting UFW rule nor `perimeter_firewall: true`.
- `schedule` publishes `rootfiles_*` metrics into `NodeExporterTextfileDir` (`/var/lib/prometheus/node-exporter`), which Apply creates.

## Distro support

- Ubuntu and DGX OS only. `config.SupportedModules` (`internal/config/distro.go`) leaves `monitoring` out on Rocky, so capability validation refuses it before any change.
- Node exporter is the Ubuntu `prometheus-node-exporter` package installed through `rc.APT`.
- The guard is IPv4 iptables only and refuses hosts where firewalld is active.

## Safety

- No new listener except the exporters, bound to `listen_address` when set.
- The guard changes no containers, global policies or other ports; `rollback` in the helper removes only owner-marked chains, jumps and files.
- Secrets are not involved in exporters; the guard config holds addresses only and is root 0600.

## Tests that pin it

- `internal/module/monitoring_test.go`: listener parsing, unmanaged and unrelated `docker-proxy` listeners, default hub port collision, broad UFW rule and allow-default refusals, DCGM prerequisites (blocked vs pending, no unit written, node exporter not held back), dry-run failing on a blocked change, node exporter idempotency, pinned DCGM unit, collectors fields, doctor exposure.
- `internal/module/monitoring_guard_test.go`: install and convergence, start of refused exporters, dry-run, guard off leaves an existing guard alone, blocked states, hub bridge, doctor coverage, embedded helper, units start with Docker, unsafe helper replacement, setup files backed up.
- `internal/config/monitoring_firewall_test.go` (`exporter_firewall` validation), `internal/config/fingerprint_test.go` (fingerprint stability).
- `tests/scenarios/exporter-firewall.sh` (CI job `exporter-firewall-scenario`): validation and dry run, adoption of a hand-installed v1 guard without rule changes, source filtering, exporters surviving guard and Docker restarts, fail-closed exporter starts.

Verification evidence (folded from the 2026-10-03 rollout checklist):

| Contract | Evidence |
|---|---|
| Pre-mutation port ownership checks | `TestMonitoringPreflightRejectsUnmanagedListener`, `TestMonitoringPreflightDoesNotTrustUnrelatedDockerProxy` |
| Collector syntax | `TestDCGMCollectorsIncludeRequiredHealthFields`; the CI `monitoring-hub` job checks that the default DCGM image is published |
| Real GPU metrics | Not in CI (no GPU runner); verified on a GPU host after release (unverified by tests) |
| Exporter firewall guard | CI `exporter-firewall-scenario` |
