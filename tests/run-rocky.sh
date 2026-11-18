#!/usr/bin/env bash
set -euo pipefail

if [[ "${1:-}" != "--inside" ]]; then
    image="${1:?Usage: tests/run-rocky.sh <built-image>}"
    docker_bin="${DOCKER:-docker}"
    name="rootfiles-rocky-${RANDOM}-$$"
    cleanup() { "$docker_bin" rm -f "$name" >/dev/null 2>&1 || true; }
    trap cleanup EXIT
    "$docker_bin" run -d -t --name "$name" --privileged --cgroupns=host -e container=docker \
        --tmpfs /run --tmpfs /run/lock --volume /sys/fs/cgroup:/sys/fs/cgroup:rw \
        "$image" >/dev/null
    ready=0
    for _ in $(seq 1 60); do
        state=$("$docker_bin" exec "$name" systemctl is-system-running 2>/dev/null || true)
        if [[ "$state" == running || "$state" == degraded ]]; then ready=1; break; fi
        sleep 1
    done
    if [[ "$ready" != 1 ]]; then "$docker_bin" logs "$name" >&2 || true; echo "systemd failed in $image" >&2; exit 1; fi
    "$docker_bin" exec "$name" /tests/run-rocky.sh --inside
    exit $?
fi

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "PASS: $*"; }
assert_eq() { [[ "$1" == "$2" ]] || fail "$3: got '$1', expected '$2'"; pass "$3"; }

expected="${ROOTFILES_EXPECTED_ROCKY_RELEASE:?fixture expected-release missing}"
release_before=$(cat /etc/rocky-release)
case "$expected" in
    8.9) [[ "$release_before" == *"8.9"* ]] || fail "expected $expected, got $release_before" ;;
    8.10) [[ "$release_before" == *"8.10"* ]] || fail "expected $expected, got $release_before" ;;
    9) [[ "$release_before" == *"release 9."* ]] || fail "expected Rocky 9, got $release_before" ;;
    *) fail "unexpected fixture version $expected" ;;
esac
assert_eq "$(ps -p 1 -o comm= | xargs)" systemd "systemd is PID 1"
assert_eq "$(cat /etc/rootfiles-rocky-release.before)" "$release_before" "release recorded before prerequisite install"
rpm_release_before=$(rpm -q --qf '%{VERSION}' rocky-release)
case "$expected" in
    8.9|8.10) assert_eq "$(cut -d. -f1,2 <<<"$rpm_release_before")" "$expected" "Rocky RPM version pinned" ;;
    9) assert_eq "$(cut -d. -f1 <<<"$rpm_release_before")" 9 "Rocky RPM major 9" ;;
esac

groupadd developers 2>/dev/null || true
groupadd keepgroup 2>/dev/null || true
useradd -m -s /bin/sh -G developers,keepgroup rockyfixture 2>/dev/null || true
printf 'rockyfixture:fixture-password\n' | chpasswd
home_before=$(getent passwd rockyfixture | cut -d: -f6)
shell_before=$(getent passwd rockyfixture | cut -d: -f7)
hash_before=$(getent shadow rockyfixture | cut -d: -f2)
ssh-keygen -q -t ed25519 -N '' -f /tmp/preserved-key
ssh-keygen -q -t ed25519 -N '' -f /tmp/managed-key
ssh-keygen -q -t ed25519 -N '' -f /tmp/root-key
install -d -m 0700 -o rockyfixture -g rockyfixture /home/rockyfixture/.ssh
install -m 0600 -o rockyfixture -g rockyfixture /tmp/preserved-key.pub /home/rockyfixture/.ssh/authorized_keys
install -d -m 0700 /root/.ssh
install -m 0600 /tmp/root-key.pub /root/.ssh/authorized_keys

# Ubuntu Docker hosts can auto-attach their unix-chkpwd AppArmor profile to
# Rocky's helper, denying the DAC capability needed for Rocky's mode-000
# shadow file. Relocate the identical helper only in this disposable fixture,
# and only after observing that specific host-policy denial. Keep PAM and
# shadow permissions unchanged; production code never performs this step.
if ! /usr/sbin/unix_chkpwd rockyfixture chkexpiry >/dev/null 2>&1; then
    if dmesg 2>/dev/null | grep -E 'apparmor="DENIED".*profile="unix-chkpwd".*capname="dac_(override|read_search)"' >/dev/null; then
        install -d -m 0755 /usr/local/libexec
        helper=/usr/local/libexec/rootfiles-fixture-unix-chkpwd
        install -o root -g root -m 4755 /usr/sbin/unix_chkpwd "$helper"
        [[ "$(sha256sum /usr/sbin/unix_chkpwd | awk '{print $1}')" == "$(sha256sum "$helper" | awk '{print $1}')" ]] || fail "fixture PAM helper bytes changed"
        mv /usr/sbin/unix_chkpwd /usr/sbin/unix_chkpwd.fixture-original
        ln -s "$helper" /usr/sbin/unix_chkpwd
        /usr/sbin/unix_chkpwd rockyfixture chkexpiry >/dev/null || fail "fixture helper relocation did not restore account lookup"
        pass "identical PAM helper isolated from foreign host AppArmor profile"
    else
        fail "stock PAM account lookup failed without the known host AppArmor denial"
    fi
fi

# Start stock sshd first, so the native SSH module must preserve a real
# existing key-based login while applying its drop-in.
ssh-keygen -A >/dev/null
systemctl enable --now sshd
ssh_opts=(-p 22 -o BatchMode=yes -o ConnectTimeout=5 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null)
initial_login=$(ssh "${ssh_opts[@]}" -i /tmp/preserved-key rockyfixture@127.0.0.1 id -un)
assert_eq "$initial_login" rockyfixture "pre-apply login over stock loopback sshd"

managed_key=$(cat /tmp/managed-key.pub)
cat >/tmp/rocky-users.yaml <<YAML
extends: rocky
users:
  default_groups: [sudo]
  accounts:
    - name: rockyfixture
      groups: [developers]
      ssh_pubkeys:
        - "$managed_key"
YAML

echo "=== Native Rocky profile preflight ==="
repos=$(dnf repolist --enabled -q | awk 'NF {print tolower($1)}')
grep -qx baseos <<<"$repos" || fail "baseos DNF repository is not enabled: $repos"
grep -qx appstream <<<"$repos" || fail "appstream DNF repository is not enabled: $repos"
pass "enabled DNF repositories include baseos and appstream"

/usr/local/bin/rootfiles status --config /tmp/rocky-users.yaml -o json >/tmp/status.json
grep -q '"system"' /tmp/status.json || fail "status JSON omitted system details"
grep -q '"os": "rocky"' /tmp/status.json || fail "status did not detect Rocky Linux"
/usr/local/bin/rootfiles doctor --config /tmp/rocky-users.yaml -o json >/tmp/doctor.json
grep -q '"failed": 0' /tmp/doctor.json || fail "doctor reported failed checks"
pass "status and doctor read the Rocky host"

if /usr/local/bin/rootfiles check --config /tmp/rocky-users.yaml -o json >/tmp/check-before.json; then
    fail "fresh fixture unexpectedly passed profile check before apply"
else
    rc=$?
    [[ "$rc" == 2 ]] || { cat /tmp/check-before.json >&2; fail "pre-apply check returned error $rc, expected drift code 2"; }
fi
pass "pre-apply check reports profile drift"

trap_bin=/tmp/rootfiles-traps
mkdir -p "$trap_bin"
for cmd in apt apt-get dpkg locale-gen; do
    cat >"$trap_bin/$cmd" <<'TRAP'
#!/usr/bin/env bash
echo "$0 $*" >>/tmp/rootfiles-forbidden-command.log
exit 99
TRAP
    chmod 0755 "$trap_bin/$cmd"
done
rm -f /tmp/rootfiles-forbidden-command.log
PATH="$trap_bin:$PATH" /usr/local/bin/rootfiles apply --config /tmp/rocky-users.yaml --dry-run --yes >/tmp/rootfiles-dry-run.txt
grep -Eq 'Mode: +dry-run' /tmp/rootfiles-dry-run.txt || fail "dry-run mode was not reported"
if [[ -e /tmp/rootfiles-forbidden-command.log ]]; then
    cat /tmp/rootfiles-forbidden-command.log >&2
    fail "Rocky dry-run invoked an Ubuntu APT/dpkg/locale-gen command"
fi
pass "native Rocky profile dry-run completed without Ubuntu commands"

PATH="$trap_bin:$PATH" /usr/local/bin/rootfiles apply --config /tmp/rocky-users.yaml --yes >/tmp/apply-first.txt
if [[ -e /tmp/rootfiles-forbidden-command.log ]]; then
    cat /tmp/rootfiles-forbidden-command.log >&2
    fail "Rocky core apply invoked an Ubuntu APT/dpkg/locale-gen command"
fi
pass "core profile applied without APT, dpkg, or locale-gen"

assert_eq "$(getent passwd rockyfixture | cut -d: -f6)" "$home_before" "existing home preserved"
assert_eq "$(getent passwd rockyfixture | cut -d: -f7)" "$shell_before" "existing shell preserved"
assert_eq "$(getent shadow rockyfixture | cut -d: -f2)" "$hash_before" "existing password preserved"
groups_after=$(id -Gn rockyfixture)
for group in wheel developers keepgroup; do
    [[ " $groups_after " == *" $group "* ]] || fail "group $group missing after apply: $groups_after"
done
for key in /tmp/preserved-key.pub /tmp/managed-key.pub; do
    key_id=$(awk '{print $1" "$2}' "$key")
    grep -Fq "$key_id" /home/rockyfixture/.ssh/authorized_keys || fail "SSH key lost/not added: $key_id"
done
pass "wheel granted; existing groups and keys preserved"
if /usr/local/bin/rootfiles check --config /tmp/rocky-users.yaml -o json >/tmp/check.json; then :; else
    rc=$?; cat /tmp/check.json >&2 || true; fail "full profile check failed after apply ($rc)"
fi
grep -q '"satisfied": true' /tmp/check.json || fail "users check is not satisfied"
pass "full profile converges under check"

sshd_drop=/etc/ssh/sshd_config.d/00-rootfiles.conf
[[ -f "$sshd_drop" ]] || fail "native SSH module did not write $sshd_drop"
sshd -T >/tmp/sshd-effective.txt
grep -qi '^permitrootlogin no$' /tmp/sshd-effective.txt || fail "effective sshd still permits root login"
grep -qi '^port 22$' /tmp/sshd-effective.txt || fail "native SSH module changed SSH away from port 22"
pass "native SSH module is effective and keeps port 22"
user_login=$(ssh "${ssh_opts[@]}" -i /tmp/managed-key rockyfixture@127.0.0.1 id -un)
assert_eq "$user_login" rockyfixture "managed key login survives native SSH apply"
preserved_login=$(ssh "${ssh_opts[@]}" -i /tmp/preserved-key rockyfixture@127.0.0.1 id -un)
assert_eq "$preserved_login" rockyfixture "pre-existing key login survives native SSH apply"
if ssh "${ssh_opts[@]}" -i /tmp/root-key root@127.0.0.1 true >/dev/null 2>&1; then
    fail "root SSH key login succeeded despite PermitRootLogin no"
fi
pass "root key login is refused after native SSH apply"

auto_cfg=/etc/dnf/automatic.conf
[[ -f "$auto_cfg" ]] || fail "Rocky security module did not write $auto_cfg"
grep -Eq '^[[:space:]]*upgrade_type[[:space:]]*=[[:space:]]*security[[:space:]]*$' "$auto_cfg" || fail "DNF automatic is not security-only"
grep -Eq '^[[:space:]]*reboot[[:space:]]*=[[:space:]]*never[[:space:]]*$' "$auto_cfg" || fail "DNF automatic reboot policy is not never"
grep -Eq '^[[:space:]]*exclude[[:space:]]*=.*nvidia.*cuda' "$auto_cfg" || fail "DNF automatic excludes do not protect NVIDIA/CUDA"
systemctl is-enabled --quiet dnf-automatic-install.timer || fail "DNF automatic install timer is not enabled"
systemctl is-active --quiet dnf-automatic-install.timer || fail "DNF automatic install timer is not active"
pass "security readback is security-only, no reboot, GPU packages excluded, timer active"

/usr/local/bin/rootfiles status --config /tmp/rocky-users.yaml -o json >/tmp/status-after.json
grep -q '"system"' /tmp/status-after.json || fail "post-apply status JSON omitted system details"
grep -q '"os": "rocky"' /tmp/status-after.json || fail "post-apply status did not detect Rocky Linux"
/usr/local/bin/rootfiles doctor --config /tmp/rocky-users.yaml -o json >/tmp/doctor-after.json
grep -q '"failed": 0' /tmp/doctor-after.json || fail "post-apply doctor reported failures"

PATH="$trap_bin:$PATH" /usr/local/bin/rootfiles apply --config /tmp/rocky-users.yaml --yes >/tmp/apply-second.txt
if [[ -e /tmp/rootfiles-forbidden-command.log ]]; then
    cat /tmp/rootfiles-forbidden-command.log >&2
    fail "Rocky repeat apply invoked an Ubuntu APT/dpkg/locale-gen command"
fi
for module in locale system packages users ssh security; do
    grep -q "${module}: already satisfied" /tmp/apply-second.txt || fail "second apply did not report $module already satisfied"
done
pass "second full profile apply is idempotent"
for module in locale system packages users ssh security; do
    grep -q "\"name\": \"$module\"" /tmp/status-after.json || fail "status omitted module $module"
done
grep -q '"failed": 0' /tmp/doctor-after.json || fail "doctor reported failure"
pass "status lists enabled modules and doctor has no failures"

echo "NOTE: Rocky container tests do not validate SELinux behavior of the Docker host."

mkdir -p /tmp/installer-payload /tmp/installer-bin
install -m 0755 /usr/local/bin/rootfiles /tmp/installer-payload/rootfiles
version=0.0.0-ci
arch=$(uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
archive="rootfiles_${version}_linux_${arch}.tar.gz"
tar -czf "/tmp/installer-payload/$archive" -C /tmp/installer-payload rootfiles
(cd /tmp/installer-payload && sha256sum "$archive" >checksums.txt)
cat >/tmp/installer-bin/curl <<'CURL'
#!/usr/bin/env bash
set -euo pipefail
out=''; url=''
while (($#)); do
    case "$1" in
        -o) out="$2"; shift 2 ;;
        http*) url="$1"; shift ;;
        *) shift ;;
    esac
done
[[ -n "$out" ]] || exit 1
case "$url" in
    */checksums.txt) cp /tmp/installer-payload/checksums.txt "$out" ;;
    */rootfiles_*) cp "/tmp/installer-payload/${url##*/}" "$out" ;;
    *) echo "unexpected fixture URL: $url" >&2; exit 1 ;;
esac
CURL
chmod 0755 /tmp/installer-bin/curl
cat >/etc/sudoers.d/99-rootfiles-rocky-ci <<'SUDOERS'
rockyfixture ALL=(ALL) NOPASSWD: ALL
Defaults secure_path="/usr/sbin:/usr/bin:/sbin:/bin"
SUDOERS
chmod 0440 /etc/sudoers.d/99-rootfiles-rocky-ci
visudo -cf /etc/sudoers >/dev/null
rm -f /usr/local/bin/root /usr/bin/rootfiles
PATH="/tmp/installer-bin:$PATH" SUDO_USER=rockyfixture /tests/install.sh --version "v$version"
assert_eq "$(readlink -f /usr/bin/rootfiles)" "$(readlink -f /usr/local/bin/rootfiles)" "installer secure_path link verified"
sudo -n -u rockyfixture rootfiles --version >/dev/null || fail "sudo cannot resolve rootfiles"
pass "sudo command resolves with Rocky secure_path"

rm -f /usr/local/bin/root
ln -s /bin/true /usr/local/bin/root
if PATH="/tmp/installer-bin:$PATH" SUDO_USER=rockyfixture /tests/install.sh --version "v$version" >/tmp/root-collision.log 2>&1; then
    fail "installer accepted unrelated /usr/local/bin/root"
fi
assert_eq "$(readlink /usr/local/bin/root)" /bin/true "installer preserves unrelated root path"
rm -f /usr/local/bin/root
ln -s /usr/local/bin/rootfiles /usr/local/bin/root
rm -f /usr/bin/rootfiles
ln -s /bin/true /usr/bin/rootfiles
if PATH="/tmp/installer-bin:$PATH" SUDO_USER=rockyfixture /tests/install.sh --version "v$version" >/tmp/rootfiles-collision.log 2>&1; then
    fail "installer accepted unrelated /usr/bin/rootfiles"
fi
assert_eq "$(readlink /usr/bin/rootfiles)" /bin/true "installer preserves unrelated rootfiles path"
assert_eq "$(cat /etc/rocky-release)" "$release_before" "OS release unchanged by tests/installer"
assert_eq "$(rpm -q --qf '%{VERSION}' rocky-release)" "$rpm_release_before" "Rocky release RPM unchanged by profile apply"
echo "Rocky $expected fixture passed"
