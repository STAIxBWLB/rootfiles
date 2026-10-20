#!/usr/bin/env bash
# Host-run acceptance scenario for modules.monitoring.exporter_firewall on a
# disposable Docker + iptables + systemd host (the CI runner): config
# validation and dry run, adoption of a hand-installed v1 guard without rule
# changes, source filtering, exporters surviving guard and Docker restarts,
# and fail-closed exporter starts. Docker containers stand in for the allowed
# scraper and for another host in the segment.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$REPO_ROOT/tests/integration/assert.sh"

BIN="${ROOTFILES_TEST_BINARY:?ROOTFILES_TEST_BINARY must name a Linux rootfiles binary}"
sudo install -m 0755 -o root -g root "$BIN" /usr/local/bin/rootfiles
RF="sudo /usr/local/bin/rootfiles"
HELPER=/usr/local/sbin/rootfiles-exporter-firewall
CFG=/etc/rootfiles-test/monitoring.yaml
CURL=curlimages/curl:8.10.1
BIND="$(ip -4 -o route get 1.1.1.1 | sed -n 's/.* src \([0-9.]*\).*/\1/p')"
ALLOWED=172.30.9.10
OTHER=172.30.9.11

cleanup() {
    sudo "$HELPER" rollback >/dev/null 2>&1 || true
    docker network rm efw-test >/dev/null 2>&1 || true
}
trap cleanup EXIT

check() { # check DESCRIPTION COMMAND...
    local desc=$1; shift
    if "$@"; then pass "$desc"; else fail "$desc"; fi
}
refused() { ! "$@" >/dev/null 2>&1; }
scrape() { # scrape SOURCE_IP: fetch node exporter metrics from that address
    local out
    out="$(docker run --rm --network efw-test --ip "$1" "$CURL" -fsS --max-time 5 "http://$BIND:9100/metrics" 2>&1)" || { echo "    scrape from $1: ${out:0:200}"; return 1; }
    grep -q '^node_exporter_build_info' <<<"$out"
}
rules() { # the owned rules, without counters
    { sudo iptables -S RFILES_EXPORTER_IN; sudo iptables -S RFILES_DCGM_DU
      sudo iptables -S INPUT | grep rootfiles-exporter-firewall || true
      sudo iptables -S DOCKER-USER | grep rootfiles-exporter-firewall || true; } 2>&1
}
write_config() { # write_config ALLOW_FROM_YAML
    sudo tee "$CFG" >/dev/null <<YAML
extends: minimal
modules:
  monitoring:
    enabled: true
    node_exporter: true
    listen_address: $BIND
    allow_from: $1
    exporter_firewall: true
YAML
}

echo "=== Scenario: exporter-firewall (bind $BIND) ==="
[ -n "$BIND" ] || { echo "no IPv4 source address"; exit 1; }
sudo mkdir -p /etc/rootfiles-test
docker network create --subnet 172.30.9.0/24 efw-test >/dev/null
docker pull -q "$CURL" >/dev/null

echo "--- AC5: invalid config and dry run change nothing ---"
write_config "[$ALLOWED/32, $OTHER/32]"
check "two sources rejected" refused $RF apply --config "$CFG" --module monitoring --yes
write_config "[$ALLOWED/32]"
$RF apply --config "$CFG" --module monitoring --dry-run --yes >/dev/null
check "dry run wrote no helper" sudo test ! -e "$HELPER"
check "dry run wrote no config" sudo test ! -e /etc/rootfiles/exporter-firewall.conf

echo "--- AC1: adopt a hand-installed v1 guard ---"
sudo install -m 0750 -o root -g root "$REPO_ROOT/tests/scenarios/fixtures/exporter-firewall-v1.sh" "$HELPER"
sudo install -d -m 0755 /etc/rootfiles
printf 'BIND_IP=%s\nALLOW_FROM=%s/32\n' "$BIND" "$ALLOWED" | sudo tee /etc/rootfiles/exporter-firewall.conf >/dev/null
sudo chmod 0600 /etc/rootfiles/exporter-firewall.conf
sudo "$HELPER" setup
before="$(rules)"
check "v1 node exporter drop-in has Requires=" sudo grep -q '^Requires=rootfiles-exporter-firewall.service' /etc/systemd/system/prometheus-node-exporter.service.d/90-rootfiles-exporter-firewall.conf
$RF apply --config "$CFG" --module monitoring --yes
after="$(rules)"
check "owned rules unchanged by adoption" [ "$before" = "$after" ]
[ "$before" = "$after" ] || diff <(echo "$before") <(echo "$after") || true
check "monitoring satisfied after apply" $RF check --config "$CFG" --module monitoring
check "helper check passes" sudo "$HELPER" check
check "exporter drop-in no longer requires the guard unit" bash -c '! grep -q "^Requires=" /etc/systemd/system/prometheus-node-exporter.service.d/90-rootfiles-exporter-firewall.conf'
check "guard unit only wants docker" bash -c 'grep -q "^Wants=network-online.target docker.service" /etc/systemd/system/rootfiles-exporter-firewall.service && ! grep -q "^Requires=" /etc/systemd/system/rootfiles-exporter-firewall.service'
check "node exporter active" systemctl is-active --quiet prometheus-node-exporter

echo "--- AC2: only allow_from reaches the exporter ---"
check "allowed source scrapes" scrape "$ALLOWED"
check "other source is refused" refused scrape "$OTHER"

echo "--- AC4: guard unit stop does not stop the exporter; missing rules block its start ---"
sudo systemctl stop rootfiles-exporter-firewall
check "node exporter survives guard unit stop" systemctl is-active --quiet prometheus-node-exporter
check "rules outlive the guard unit" sudo "$HELPER" verify-host
sudo systemctl start rootfiles-exporter-firewall
sudo iptables -D INPUT -m comment --comment rootfiles-exporter-firewall:v1:jump:input -j RFILES_EXPORTER_IN
check "restart refused without INPUT rules" refused sudo systemctl restart prometheus-node-exporter
check "exporter not running without rules" refused systemctl is-active --quiet prometheus-node-exporter
check "other source still refused" refused scrape "$OTHER"
check "check reports the drift" refused $RF check --config "$CFG" --module monitoring
$RF apply --config "$CFG" --module monitoring --yes
check "apply restores the rules" sudo "$HELPER" check
check "apply starts the refused exporter" systemctl is-active --quiet prometheus-node-exporter
systemctl is-active --quiet prometheus-node-exporter || sudo systemctl status prometheus-node-exporter --no-pager -l | tail -20
check "allowed source scrapes after restore" scrape "$ALLOWED"

echo "--- AC3: Docker stop and start ---"
sudo systemctl stop docker.socket docker.service
check "node exporter survives docker stop" systemctl is-active --quiet prometheus-node-exporter
check "guard unit survives docker stop" systemctl is-active --quiet rootfiles-exporter-firewall
sudo systemctl start docker.service
for _ in $(seq 1 30); do sudo iptables -S DOCKER-USER 2>/dev/null | grep -q jump:docker && break; sleep 1; done
check "node exporter active after docker start" systemctl is-active --quiet prometheus-node-exporter
check "DOCKER-USER rules back after docker start" sudo "$HELPER" check
check "monitoring satisfied after docker restart" $RF check --config "$CFG" --module monitoring
check "allowed source scrapes after docker restart" scrape "$ALLOWED"
check "other source refused after docker restart" refused scrape "$OTHER"

report
