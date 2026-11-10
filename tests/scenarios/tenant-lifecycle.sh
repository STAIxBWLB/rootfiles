#!/usr/bin/env bash
# Host-run acceptance scenario for `rootfiles tenant` on a disposable Docker +
# systemd host (the CI runner): host prep, create, SSH with a temporary
# password, recreate on an image change, volume quota and online growth,
# mount after a unit restart, operator delegation, dry run and import.
# GPU steps (runtime=nvidia, reload safety) need a GPU host and are not run.
set -euo pipefail

if [[ "${GITHUB_ACTIONS:-}" != true || "${GITHUB_JOB:-}" != tenant-scenario || "${ROOTFILES_TENANT_SCENARIO_CI:-}" != 1 ]]; then
    echo "tenant-lifecycle changes host services and volumes; run it only through the disposable CI tenant-scenario job" >&2
    exit 1
fi
REMOUNT_CYCLES="${ROOTFILES_TENANT_REMOUNT_CYCLES:-1}"
if [[ ! "$REMOUNT_CYCLES" =~ ^[1-9][0-9]?$ ]] || (( REMOUNT_CYCLES < 1 || REMOUNT_CYCLES > 50 )); then
    echo "invalid remount cycle count; set ROOTFILES_TENANT_REMOUNT_CYCLES to 1..50 in the CI job" >&2
    exit 1
fi

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
SYSTEMD_LOG_LEVEL_BEFORE=""
cleanup() {
    if [ -n "$SYSTEMD_LOG_LEVEL_BEFORE" ]; then
        sudo systemd-analyze log-level "$SYSTEMD_LOG_LEVEL_BEFORE" || echo "could not restore systemd log level; inspect systemd-analyze log-level on this disposable runner" >&2
    fi
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
piper_as() { # piper_as ROUTE PASSWORD COMMAND: through sshpiper on 2222
    sshpass -p "$2" ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile="$KH.piper" -o PreferredAuthentications=password \
        -o PubkeyAuthentication=no -o ConnectTimeout=10 -p 2222 "$1@127.0.0.1" "$3"
}
refused() { ! "$@" >/dev/null 2>&1; } # refused COMMAND...: the command fails
outputs() { local want=$1; shift; [ "$("$@" 2>/dev/null)" = "$want" ]; }
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
    sshpiper:
      enabled: true
      port: 2222
EOF

echo "--- host prep ---"
$RF apply --config /etc/rootfiles-test/tenant.yaml --module tenant --yes
check "guard timer enabled" systemctl is-enabled --quiet rootfiles-gpu-guard.timer
check "sync timer enabled" systemctl is-enabled --quiet rootfiles-tenant-sync.timer
check "operator group created" getent group tenant-ops
check "operator sudoers rule" sudo grep -q '^%tenant-ops ALL=(root) NOPASSWD: /usr/local/bin/rootfiles tenant ops \*$' /etc/sudoers.d/rootfiles-tenant-ops
check "tenant module satisfied after apply" $RF check --module tenant
check "sshpiper running" systemctl is-active --quiet rootfiles-sshpiper.service
check "sshpiper listening on 2222" bash -c 'ss -H -ltn "sport = :2222" | grep -q .' 

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

echo "--- sshpiper routing (#80) ---"
check "route file written" sudo test -f /etc/rootfiles/sshpiper/tenants/t1.yaml
check "routed password login" outputs 2000 piper_as t1.alice Changed-Pass-42 "id -u"
check "routed wrong password refused" refused piper_as t1.alice wrong-password true
check "unknown route refused" refused piper_as nobody.alice Changed-Pass-42 true
ssh-keygen -q -t ed25519 -N '' -f "$TMP/alice_key"
key_login() {
    ssh -i "$TMP/alice_key" -o IdentitiesOnly=yes -o PreferredAuthentications=publickey -o StrictHostKeyChecking=no \
        -o UserKnownHostsFile="$KH.piper" -o ConnectTimeout=10 -p 2222 t1.alice@127.0.0.1 "id -u"
}
check "unlisted key refused" refused key_login
docker exec -i tenant-t1 sh -c 'mkdir -p /home/alice/.ssh && cat > /home/alice/.ssh/authorized_keys && chown -R 2000:2000 /home/alice/.ssh && chmod 700 /home/alice/.ssh && chmod 600 /home/alice/.ssh/authorized_keys' < "$TMP/alice_key.pub"
$RF tenant sync
check "key added in the container logs in after sync" outputs 2000 key_login

echo "--- AC3: quota and online growth ---"
started=$(docker inspect -f '{{.State.StartedAt}}' tenant-t1)
check "writes past the quota fail" bash -c '! docker exec tenant-t1 dd if=/dev/zero of=/data/fill bs=1M count=150 2>/dev/null'
docker exec tenant-t1 rm -f /data/fill
$RF tenant volume resize t1 300M
check "grown volume takes the write" docker exec tenant-t1 dd if=/dev/zero of=/data/fill bs=1M count=150 status=none
check "container kept running through growth" test "$started" = "$(docker inspect -f '{{.State.StartedAt}}' tenant-t1)"
check "online shrink refused" bash -c "! $RF tenant volume resize t1 200M >/dev/null 2>&1"
docker exec tenant-t1 rm -f /data/fill

# Read-only evidence, also on the host-bare failure branch. Do not create
# data/home/tmp or repair a mount here: those would hide the failure.
mount_diagnostics() {
    local phase=$1 service daemon_pid
    sudo readlink /proc/1/ns/mnt "/proc/$$/ns/mnt" || true
    sudo nsenter -t 1 -m -- findmnt --mountpoint /srv/tenants/t1/vol || true
    echo "--- mount diagnostics: $phase ---"
    sudo systemctl show srv-tenants-t1-vol.mount -p ActiveState -p SubState -p Result -p What -p Where -p Job -p BindsTo -p Requires -p After -p StopPropagatedFrom -p StateChangeTimestampMonotonic || true
    sudo journalctl -u srv-tenants-t1-vol.mount --no-pager -n 160 -o short-monotonic || true
    sudo findmnt --mountpoint /srv/tenants/t1/vol -o TARGET,SOURCE,MAJ:MIN,FSTYPE,PROPAGATION || true
    sudo findmnt -T /srv/tenants/t1/vol/data || true
    sudo losetup -j /srv/tenants/t1/volume.img || true
    sudo blkid /srv/tenants/t1/volume.img || true
    for service in docker containerd; do
        daemon_pid=$(systemctl show "$service.service" -p MainPID --value 2>/dev/null || true)
        echo "$service MainPID=${daemon_pid:-unknown}"
        if [[ "$daemon_pid" =~ ^[1-9][0-9]*$ ]]; then
            sudo readlink "/proc/$daemon_pid/ns/mnt" || true
            sudo nsenter -t "$daemon_pid" -m -- findmnt --mountpoint /srv/tenants/t1/vol || true
            sudo nsenter -t "$daemon_pid" -m -- stat /srv/tenants/t1/vol/data || true
        fi
    done
}

echo "--- AC8: volume comes back after a unit restart ---"
check "mount unit ordered before docker" bash -c 'systemctl show -p Before srv-tenants-t1-vol.mount | grep -q docker.service'
image_identity=$(sudo stat -c '%i:%s' /srv/tenants/t1/volume.img)
image_uuid=$(sudo blkid -s UUID -o value /srv/tenants/t1/volume.img)
check "existing filesystem UUID recorded" test -n "$image_uuid"
SYSTEMD_LOG_LEVEL_BEFORE=$(sudo systemd-analyze log-level)
if [[ ! "$SYSTEMD_LOG_LEVEL_BEFORE" =~ ^(emerg|alert|crit|err|warning|notice|info|debug)$ ]]; then
    echo "cannot capture the systemd log level; inspect systemd-analyze log-level before rerunning the CI scenario" >&2
    SYSTEMD_LOG_LEVEL_BEFORE=""
    exit 1
fi
# Transient manager logging in this disposable runner only, restored by cleanup.
sudo systemd-analyze log-level debug
mount_diagnostics before-restarts
for ((cycle=1; cycle<=REMOUNT_CYCLES; cycle++)); do
    echo "--- remount trial $cycle/$REMOUNT_CYCLES ---"
    docker stop tenant-t1 >/dev/null
    if ! sudo systemctl restart srv-tenants-t1-vol.mount; then
        mount_diagnostics "restart-job-failed-trial-$cycle"
        fail "mount restart job failed; inspect the unit and dependency journal above"
        exit 1
    fi
    # A completed systemctl job is insufficient: a later device/dependency
    # event may have stopped the mount again. Preserve the first failure.
    if ! sudo mountpoint -q /srv/tenants/t1/vol || ! sudo test -d /srv/tenants/t1/vol/data; then
        mount_diagnostics "host-mount-missing-trial-$cycle"
        fail "tenant volume disappeared after restart; inspect the mount dependency journal above"
        exit 1
    fi
    if ! docker start tenant-t1 >/dev/null; then
        mount_diagnostics "container-start-failed-trial-$cycle"
        fail "Docker cannot see the mounted tenant data; inspect the daemon namespace evidence above"
        exit 1
    fi
    if ! outputs kept docker exec tenant-t1 cat /data/hello ||
       [ "$image_identity" != "$(sudo stat -c '%i:%s' /srv/tenants/t1/volume.img)" ] ||
       [ "$image_uuid" != "$(sudo blkid -s UUID -o value /srv/tenants/t1/volume.img)" ]; then
        mount_diagnostics "data-identity-changed-trial-$cycle"
        fail "existing image identity, UUID or data changed; preserve the image and inspect the evidence above"
        exit 1
    fi
    pass "existing data, image identity and filesystem UUID intact after remount trial $cycle"
done
mount_diagnostics after-restarts
sudo systemd-analyze log-level "$SYSTEMD_LOG_LEVEL_BEFORE"
SYSTEMD_LOG_LEVEL_BEFORE=""

echo "--- AC6: operator delegation ---"
out2=$($RF tenant create t2 --image rootfiles-tenant-test:1 --ssh-port 2202 --size 64M --account carol:2100)
PW2=$(temp_password <<<"$out2")
check "create prints the route" grep -q "ssh -p 2222 t2.carol@HOST" <<<"$out2"
login2=$(piper_as t2.carol "$PW2" true 2>&1 || true)
check "routed temporary password forces the change" grep -qiE 'expired|change.*password' <<<"$login2"
check "other tenant's user has no route" refused piper_as t2.alice Changed-Pass-42 true
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

echo "--- AC5: disabling sshpiper ---"
sudo python3 - <<'PY'
p = "/etc/rootfiles-test/tenant.yaml"
s = open(p).read().replace("    sshpiper:\n      enabled: true", "    sshpiper:\n      enabled: false")
open(p, "w").write(s)
PY
$RF apply --config /etc/rootfiles-test/tenant.yaml --module tenant --yes
check "sshpiper service removed" bash -c '! systemctl cat rootfiles-sshpiper.service >/dev/null 2>&1'
check "route files removed" bash -c '! sudo ls /etc/rootfiles/sshpiper/tenants/ | grep -q yaml'

$RF tenant status
report
