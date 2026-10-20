#!/usr/bin/env bash
# Host-run acceptance scenario for `rootfiles tenant` on a disposable Docker +
# systemd host (the CI runner): host prep, create, SSH with a temporary
# password, recreate on an image change, volume quota and online growth,
# mount after a unit restart, operator delegation, dry run and import.
# GPU steps (runtime=nvidia, reload safety) need a GPU host and are not run.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$REPO_ROOT/tests/integration/assert.sh"

BIN="${ROOTFILES_TEST_BINARY:?ROOTFILES_TEST_BINARY must name a Linux rootfiles binary}"
# The operator sudoers rule needs a binary only root can replace; hosted
# runners ship a group-writable /usr/local/bin.
sudo chmod go-w /usr/local /usr/local/bin
sudo install -m 0755 -o root -g root "$BIN" /usr/local/bin/rootfiles
RF="sudo /usr/local/bin/rootfiles"
command -v sshpass >/dev/null || sudo apt-get install -y -qq sshpass >/dev/null

TMP="$(mktemp -d)"
KH="$TMP/known_hosts"
cleanup() {
    docker rm -f tenant-t1 tenant-t1-prev tenant-t2 tenant-t3 legacy >/dev/null 2>&1 || true
}
trap cleanup EXIT

check() { # check DESCRIPTION COMMAND...
    local desc=$1; shift
    if "$@"; then pass "$desc"; else fail "$desc"; fi
}
ssh_as() { # ssh_as USER PASSWORD PORT COMMAND
    sshpass -p "$2" ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile="$KH" -o PreferredAuthentications=password \
        -o PubkeyAuthentication=no -o ConnectTimeout=10 -p "$3" "$1@127.0.0.1" "$4"
}
temp_password() { sed -n 's/^Temporary password for [^:]*: \([^ ]*\) .*/\1/p' | head -n1; }

echo "=== Scenario: tenant-lifecycle ==="

docker build -q -t rootfiles-tenant-test:1 - >/dev/null <<'EOF'
FROM ubuntu:24.04
RUN apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq openssh-server >/dev/null && rm -rf /var/lib/apt/lists/*
EOF
docker build -q -t rootfiles-tenant-test:2 - >/dev/null <<'EOF'
FROM rootfiles-tenant-test:1
RUN touch /image-v2
EOF

sudo mkdir -p /etc/rootfiles-test
sudo tee /etc/rootfiles-test/tenant.yaml >/dev/null <<'EOF'
extends: minimal
users:
  home_base: /srv/home
modules:
  tenant:
    enabled: true
    root: /srv/tenants
    ops_group: tenant-ops
EOF

echo "--- host prep ---"
$RF apply --config /etc/rootfiles-test/tenant.yaml --module tenant --yes
check "guard timer enabled" systemctl is-enabled --quiet rootfiles-gpu-guard.timer
check "sync timer enabled" systemctl is-enabled --quiet rootfiles-tenant-sync.timer
check "operator group created" getent group tenant-ops
check "operator sudoers rule" sudo grep -q '^%tenant-ops ALL=(root) NOPASSWD: /usr/local/bin/rootfiles tenant ops \*$' /etc/sudoers.d/rootfiles-tenant-ops
check "tenant module satisfied after apply" $RF check --module tenant

echo "--- AC10: dry run changes nothing ---"
$RF tenant create t9 --image rootfiles-tenant-test:1 --ssh-port 2209 --size 64M --account bob:2009 --dry-run >/dev/null
check "dry run wrote no spec" sudo test ! -e /srv/home/.rootfiles/tenants/t9.yaml
check "dry run made no volume" sudo test ! -e /srv/tenants/t9
check "dry run started no container" bash -c '! docker inspect tenant-t9 >/dev/null 2>&1'

echo "--- AC1: create and first login ---"
out=$($RF tenant create t1 --image rootfiles-tenant-test:1 --ssh-port 2201 --size 100M --account alice:2000 --sudo --operator opsuser)
echo "$out"
PW=$(temp_password <<<"$out")
check "temporary password issued" test -n "$PW"
check "container running" bash -c '[ "$(docker inspect -f {{.State.Running}} tenant-t1)" = true ]'
check "volume mounted" sudo mountpoint -q /srv/tenants/t1/vol
check "mount unit enabled" systemctl is-enabled --quiet srv-tenants-t1-vol.mount
login=$(ssh_as alice "$PW" 2201 true 2>&1 || true)
echo "$login" | tail -n 3
check "temporary password accepted and change forced" grep -qiE 'expired|change.*password' <<<"$login"

echo "--- AC7: no password in logs ---"
check "password not in rootfiles.log" bash -c "! sudo grep -qF '$PW' /var/log/rootfiles.log"
check "password not in tenant audit log" bash -c "! sudo grep -qF '$PW' /var/log/rootfiles-tenant.jsonl"
check "audit records create" sudo grep -q '"action":"create","result":"ok"' /var/log/rootfiles-tenant.jsonl

echo "--- AC2: image change keeps data, password, host key ---"
docker exec -i tenant-t1 chpasswd <<<"alice:Changed-Pass-42"
check "changed password logs in" ssh_as alice Changed-Pass-42 2201 'echo kept > /data/hello && id -u'
key_before=$(ssh-keyscan -t ed25519 -p 2201 127.0.0.1 2>/dev/null | awk '{print $3}')
$RF tenant image t1 rootfiles-tenant-test:2
check "new image running" docker exec tenant-t1 test -f /image-v2
check "previous container kept" docker inspect tenant-t1-prev
key_after=$(ssh-keyscan -t ed25519 -p 2201 127.0.0.1 2>/dev/null | awk '{print $3}')
check "host key survived" test -n "$key_before" -a "$key_before" = "$key_after"
check "changed password survived" ssh_as alice Changed-Pass-42 2201 'test "$(cat /data/hello)" = kept'
check "recreate refused while -prev exists" bash -c "! $RF tenant port t1 2211 >/dev/null 2>&1"
docker rm tenant-t1-prev >/dev/null
# Back to the running container's settings: nothing to recreate.
check "apply is idempotent" bash -c "$RF tenant port t1 2201 | grep -q 'up to date'"

echo "--- AC3: quota and online growth ---"
started=$(docker inspect -f '{{.State.StartedAt}}' tenant-t1)
check "writes past the quota fail" bash -c '! docker exec tenant-t1 dd if=/dev/zero of=/data/fill bs=1M count=150 2>/dev/null'
docker exec tenant-t1 rm -f /data/fill
$RF tenant volume resize t1 300M
check "grown volume takes the write" docker exec tenant-t1 dd if=/dev/zero of=/data/fill bs=1M count=150 status=none
check "container kept running through growth" test "$started" = "$(docker inspect -f '{{.State.StartedAt}}' tenant-t1)"
check "online shrink refused" bash -c "! $RF tenant volume resize t1 200M >/dev/null 2>&1"
docker exec tenant-t1 rm -f /data/fill

echo "--- AC8: volume comes back after a unit restart ---"
check "mount unit ordered before docker" bash -c 'systemctl show -p Before srv-tenants-t1-vol.mount | grep -q docker.service'
docker stop tenant-t1 >/dev/null
sudo systemctl restart srv-tenants-t1-vol.mount
docker start tenant-t1 >/dev/null
check "data intact after remount" docker exec tenant-t1 test -f /data/hello

echo "--- AC6: operator delegation ---"
$RF tenant create t2 --image rootfiles-tenant-test:1 --ssh-port 2202 --size 64M --account carol:2100 >/dev/null
sudo useradd -m opsuser
sudo usermod -aG tenant-ops opsuser
ops() { sudo -u opsuser sudo -n /usr/local/bin/rootfiles tenant ops "$@"; }
check "operator status" ops t1 status
check "operator restart" ops t1 restart
check "operator reset" bash -c "sudo -u opsuser sudo -n /usr/local/bin/rootfiles tenant ops t1 reset alice | grep -q 'Temporary password'"
check "operator refused on unlisted tenant" bash -c '! sudo -u opsuser sudo -n /usr/local/bin/rootfiles tenant ops t2 status 2>/dev/null'
check "operator refused other actions" bash -c '! sudo -u opsuser sudo -n /usr/local/bin/rootfiles tenant ops t1 image 2>/dev/null'
check "operator refused flags" bash -c '! sudo -u opsuser sudo -n /usr/local/bin/rootfiles tenant ops t1 status --config /tmp/x 2>/dev/null'
check "operator cannot run other commands" bash -c '! sudo -u opsuser sudo -n /usr/local/bin/rootfiles tenant create x 2>/dev/null'
check "refusals audited" bash -c 'sudo grep -c "\"actor\":\"opsuser\".*refused" /var/log/rootfiles-tenant.jsonl | grep -q "^3$"'

echo "--- AC9: import ---"
sudo mkdir -p /srv/legacy-data
sudo dd if=/dev/zero of=/srv/legacy-data/blob bs=1M count=200 status=none
docker run -d --name legacy -p 2203:22 -v /srv/legacy-data:/data --entrypoint sleep rootfiles-tenant-test:1 infinity >/dev/null
docker exec legacy sh -c 'mkdir -p /home/ubuntu && echo keep > /home/ubuntu/keep.txt'
check "import refused when data exceeds the volume" bash -c "! $RF tenant import t3 --from legacy --size 128M --data /srv/legacy-data"
check "refused import changed nothing" sudo test ! -e /srv/home/.rootfiles/tenants/t3.yaml
check "dry-run import prints the plan" bash -c "$RF tenant import t3 --from legacy --size 512M --data /srv/legacy-data --keep /home/ubuntu --dry-run | grep -q 'Import plan'"
check "dry-run import changed nothing" bash -c '[ "$(docker inspect -f {{.State.Running}} legacy)" = true ] && ! sudo test -e /srv/tenants/t3'
$RF tenant import t3 --from legacy --size 512M --data /srv/legacy-data --keep /home/ubuntu
check "imported data" docker exec tenant-t3 test -s /data/blob
check "imported writable-layer path" docker exec tenant-t3 test -f /home/ubuntu/keep.txt
check "legacy container kept stopped" bash -c '[ "$(docker inspect -f {{.State.Running}}/{{.HostConfig.RestartPolicy.Name}} legacy)" = false/no ]'

$RF tenant status
report
