# Monitoring

The `monitoring` module runs exporters on each host and, on one host, an optional Prometheus, Alertmanager and Grafana hub. Contracts: [docs/specs/monitoring-exporters.md](specs/monitoring-exporters.md), [docs/specs/monitoring-hub.md](specs/monitoring-hub.md). Not supported on Rocky Linux ([rocky.md](rocky.md)).

## Host exporters

```yaml
modules:
  monitoring:
    enabled: true
    node_exporter: true
    dcgm_exporter: true
    node_exporter_port: 9100        # default
    dcgm_exporter_port: 9400        # default
    listen_address: 203.0.113.11    # address the hub scrapes
    allow_from:
      - 203.0.113.5/32              # the hub
    perimeter_firewall: false
    exporter_firewall: false
```

- The node exporter also publishes the scheduled `check` and `doctor` reports through its textfile collector ([operations.md](operations.md#scheduled-reports)).
- The DCGM exporter needs a working NVIDIA GPU, driver, Docker daemon and NVIDIA container runtime. A missing prerequisite shows as `blocked` in `rootfiles check` and fails `apply --dry-run`. A real `apply` still sets up the node exporter and the rest of the module, then fails with the DCGM error and its fix (for a missing runtime: `sudo nvidia-ctk runtime configure --runtime=docker && sudo systemctl reload docker`).
- The DCGM exporter and the hub are wanted by `docker.service`, so they start again whenever Docker starts.

## Exporter access

Choose one way to keep the exporter ports closed to everyone but the hub:

- **UFW**: with UFW active, `allow_from` is applied only when the incoming default is deny or reject and no broader allow rule exists for either exporter port. Conflicts are reported and existing rules are left alone. rootfiles does not enable UFW for this.
- **Perimeter firewall**: when the network segment is already closed, set `perimeter_firewall: true` instead of host rules.
- **Exporter firewall guard**: for hosts without UFW and without a closed segment, set `exporter_firewall: true` (next section).

When firewall state cannot be read, exporter setup is blocked and `doctor` reports it.

## Exporter firewall guard

`exporter_firewall: true` installs an iptables guard that admits the exporter ports only from `allow_from`.

Requirements:

- Config validation: an IPv4 `listen_address`, exactly one IPv4 `/32` in `allow_from`, the default ports 9100 and 9400, and at least one exporter enabled.
- On the host: Docker running (DCGM is filtered in the `DOCKER-USER` chain) and `python3`. An active firewalld is reported as blocked.

What `apply` installs:

- `/usr/local/sbin/rootfiles-exporter-firewall` and `/etc/rootfiles/exporter-firewall.conf`, then runs the helper's `setup`.
- Owner-marked chains `RFILES_EXPORTER_IN` (on `INPUT`) and `RFILES_DCGM_DU` (on `DOCKER-USER`).
- `rootfiles-exporter-firewall.service` and drop-ins that reapply the rules when Docker or UFW starts.

Behaviour:

- An exporter starts only while its rules are present (`ExecStartPre` runs the helper's `verify-host` or `verify-docker`).
- Stopping the guard unit or Docker leaves the rules and the node exporter in place.
- On a hub host, the hub's Compose bridge is admitted so its Prometheus can scrape the host.
- A guard installed by hand with the same helper is adopted without rule changes.
- Setting the option back to `false` leaves the guard in place; `rootfiles doctor` reports it as unmanaged, and `sudo rootfiles-exporter-firewall rollback` removes it.

## DCGM image

- The default image is `nvcr.io/nvidia/k8s/dcgm-exporter:3.3.8-3.6.0-ubuntu22.04`, chosen for the R535 driver baseline. It is not qualified for Blackwell or newer drivers; NVIDIA documents that driver 580.126.16 needs DCGM 4.3 or newer.
- Set `dcgm_exporter_image` to a pinned newer image only after checking NVIDIA's supported GPU, driver and DCGM versions and validating the endpoint on that host. For example `nvcr.io/nvidia/k8s/dcgm-exporter:4.6.1-4.8.4-distroless`, which still needs real GPU validation before rollout.
- The managed collector file selects temperature, utilization, framebuffer used and free, XID and volatile double-bit ECC fields, which the old and new images share. ECC series may be absent on GPUs that do not expose ECC counters. The XID gauge of DCGM 3.3.8 reports the last observed XID and can stay set after recovery.

## Monitoring hub

The hub runs Prometheus, Alertmanager and Grafana with Docker Compose on one host. Enable it in that host's site config:

```yaml
users:
  home_base: /home                  # required in fleet site configs
modules:
  docker:
    enabled: true
  monitoring:
    enabled: true
    node_exporter: true
    hub:
      enabled: true
      data_dir: /data/monitoring                                   # default
      retention: 30d                                               # default
      targets_file: /etc/rootfiles/monitoring/discovery/targets.json   # default
      alert_receiver_file: /etc/rootfiles/monitoring/receiver.yaml
      telegram_bot_token_file: /etc/rootfiles/monitoring/telegram-bot-token
      listen_address: 127.0.0.1                                    # default
      grafana_port: 3000                                           # default
      prometheus_port: 9090                                        # default
      alertmanager_port: 9093                                      # default
      # alertmanager_retention: <positive duration>
```

- **Access**: the web ports bind to `127.0.0.1` by default and the hub opens no public port. For remote Grafana, route a Cloudflare Tunnel to `http://127.0.0.1:<grafana_port>` and set the access policy in Cloudflare.
- **Images**: Prometheus, Alertmanager and Grafana use pinned images (`prometheus_image`, `alertmanager_image`, `grafana_image` override them).
- **Grafana password**: generated into a root-only file (`grafana_admin_password_file`, default `/etc/rootfiles/monitoring/grafana-admin-password`); there is no default password.
- **Data**: persistent data lives under `data_dir`. Existing data directories must be real root-owned directories with root read, write and execute permission; otherwise rootfiles refuses them and leaves ownership and contents alone.
- **Scrape targets**: Prometheus reads them from file discovery. The discovery directory must contain only the targets file; `check` and `apply` refuse other entries (credentials, symlinks, subdirectories, leftover temporary files) before mounting it. A check that overlaps a target push can be retried after the push finishes.
- **Reloads**: when `apply` rewrites the Prometheus config or alert rules on a running hub, it calls Prometheus's `/-/reload` (and Alertmanager's when its config changed), so new rules take effect in the same run. A failed reload fails the apply.
- **Alertmanager retention**: `alertmanager_retention` adds Alertmanager's `--data.retention`. Use positive duration units the pinned Alertmanager accepts (no years). Unset, the image default applies. Choose at least the receiver's repeat interval, so unchanged alerts do not repeat because Alertmanager forgot it sent them.

## Alert receiver

`alert_receiver_file` is a complete Alertmanager configuration that you own: grouping, repeat intervals and silences are set there. Without it the default receiver sends nothing. The receiver file, the Telegram token file and any Grafana password file must be regular files owned by root with mode 0600.

For Telegram, point `bot_token_file` at `/run/secrets/telegram-bot-token`; rootfiles mounts `telegram_bot_token_file` there. Example:

```yaml
route:
  receiver: operations
receivers:
  - name: operations
    telegram_configs:
      - bot_token_file: /run/secrets/telegram-bot-token
        # Add the operator-owned chat_id here.
        send_resolved: true
        parse_mode: ""
        message: |-
          {{ $firing := .Alerts.Firing }}{{ range $index, $alert := $firing }}{{ if lt $index 4 }}[{{ $alert.Labels.severity }}] {{ printf "%.120s" $alert.Annotations.summary }}
          {{ printf "%.160s" $alert.Annotations.description }}
          {{ if and $alert.Annotations.runbook_url (le (len $alert.Annotations.runbook_url) 128) }}Runbook: {{ $alert.Annotations.runbook_url }}{{ end }}
          {{ end }}{{ end }}{{ if gt (len $firing) 4 }}{{ len (slice $firing 4) }} additional firing alert(s) omitted.{{ if and .ExternalURL (le (len .ExternalURL) 160) }} Details: {{ .ExternalURL }}{{ else }} Details link unavailable; verify Alertmanager externalURL.{{ end }}{{ end }}
          {{ $resolved := .Alerts.Resolved }}{{ range $index, $alert := $resolved }}{{ if lt $index 4 }}[resolved] {{ printf "%.120s" $alert.Annotations.summary }}
          {{ printf "%.160s" $alert.Annotations.description }}
          {{ if and $alert.Annotations.runbook_url (le (len $alert.Annotations.runbook_url) 128) }}Runbook: {{ $alert.Annotations.runbook_url }}{{ end }}
          {{ end }}{{ end }}{{ if gt (len $resolved) 4 }}{{ len (slice $resolved 4) }} additional resolved alert(s) omitted.{{ if and .ExternalURL (le (len .ExternalURL) 160) }} Details: {{ .ExternalURL }}{{ else }} Details link unavailable; verify Alertmanager externalURL.{{ end }}{{ end }}
```

- The template keeps firing and resolved alerts apart, shows at most four of each with bounded summary and description lengths, and counts the rest.
- It shows Alertmanager's external URL only in the overflow notice and only when it is set and short enough; make sure recipients can reach it.
- It prints `runbook_url` only when a rule provides one of usable length. Do not use Prometheus's `GeneratorURL`, which can point at the hub's container-internal service name.

## Alerts

The hub's managed rules:

| Alert | Fires when |
|---|---|
| `FleetTargetDown` | A scrape target is down for 5 minutes |
| `FleetFilesystemNearlyFull` | A filesystem is above 99% use for 10 minutes |
| `RootfilesConfigurationDrift` | A module reports unsatisfied (`rootfiles_module_satisfied == 0`) |
| `RootfilesDoctorFailure` | `rootfiles_doctor_findings{level="fail"}` is above 0; warnings do not fire it |
| `RootfilesReportStale` | A node exporter is up but its host has no check timestamp, or the last report is older than 48 hours |
| `GPUXIDError` | DCGM reports an XID code |
| `GPUUncorrectableECCError` | The uncorrectable ECC counter grew in the last 5 minutes |
| `GPUHighTemperature` | A GPU is above 85 C for 10 minutes (an alert threshold, not a GPU operating limit) |

The 48-hour stale limit allows for the daily schedule and its random delay; schedule reports daily or more often ([operations.md](operations.md#scheduled-reports)).

## Targets and labels

The hub scrapes the hosts listed in the fleet inventory. `rootfiles fleet targets` renders them ([fleet.md](fleet.md#scrape-targets)): one target per host with an `address` and an enabled exporter, labelled `host`, `exporter` (`node` or `dcgm`) and `group_<name>=true` for each inventory group. Prometheus adds `instance=<address>:<port>`. Use the `host` label to identify a machine; the DCGM container's own `Hostname` label may be the container name.

A matching exporter host, with Docker and the NVIDIA runtime already working, binds the exporters to its private address and admits the hub:

```yaml
users:
  home_base: /home
modules:
  docker:
    enabled: true
  nvidia:
    enabled: true
  monitoring:
    enabled: true
    node_exporter: true
    dcgm_exporter: true
    listen_address: 203.0.113.11
    allow_from:
      - 203.0.113.5/32
```

## Setup and verification

Apply the hub and the exporters, enable reports, then publish the targets from the operator machine:

```bash
sudo rootfiles check --config sites/monitor01.yaml
sudo rootfiles apply --yes --config sites/monitor01.yaml
sudo rootfiles schedule enable --on-calendar hourly
rootfiles fleet targets --inventory fleet.yaml --push monitor01 --yes
```

Check the hub:

```bash
sudo systemctl is-active rootfiles-monitoring-hub.service
sudo docker compose -f /etc/rootfiles/monitoring/compose.yaml ps --format json
curl -fsS http://127.0.0.1:9090/-/ready
curl -fsS http://127.0.0.1:9090/api/v1/targets
curl -fsS http://127.0.0.1:9093/-/ready
curl -fsS http://127.0.0.1:3000/api/health
```

Confirm that host reports reach Prometheus:

```bash
sudo rootfiles check -o prometheus | grep -E '^rootfiles_(module_satisfied|check_timestamp_seconds)'
sudo rootfiles doctor -o prometheus | grep -E '^rootfiles_doctor_findings'
```

The acceptance scenario `tests/scenarios/monitoring-hub.sh` exercises the generated hub; its requirements are in [development.md](development.md#ci-jobs).
