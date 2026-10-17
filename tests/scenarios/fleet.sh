#!/usr/bin/env bash
# Host-run acceptance scenario: two disposable SSH containers exercise the
# controller's reporting and serial stop-on-first-failure behavior.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$REPO_ROOT/tests/integration/assert.sh"

CONTROLLER_BIN="${ROOTFILES_TEST_BINARY:-$REPO_ROOT/rootfiles}"
REMOTE_BIN="${ROOTFILES_REMOTE_BINARY:-$CONTROLLER_BIN}"
if [[ ! -x "$CONTROLLER_BIN" ]]; then
    echo "ROOTFILES_TEST_BINARY must name an executable rootfiles binary" >&2
    exit 2
fi
if [[ ! -x "$REMOTE_BIN" ]]; then
    echo "ROOTFILES_REMOTE_BINARY must name an executable Linux rootfiles binary for the SSH containers" >&2
    exit 2
fi
for command in docker ssh-keygen ssh-keyscan python3; do
    if ! command -v "$command" >/dev/null 2>&1; then
        echo "required command not found: $command" >&2
        exit 2
    fi
done

TMP="$(mktemp -d)"
IMAGE="rootfiles-fleet-ssh:scenario-$$"
GOOD="rootfiles-fleet-good-$$"
BAD="rootfiles-fleet-bad-$$"
cleanup() {
    docker rm -f "$GOOD" "$BAD" >/dev/null 2>&1 || true
    docker image rm "$IMAGE" >/dev/null 2>&1 || true
    rm -rf "$TMP"
}
trap cleanup EXIT

mkdir -p "$TMP/home/.ssh" "$TMP/config/rootfiles" "$TMP/state"
chmod 0700 "$TMP/home/.ssh"
ssh-keygen -q -t ed25519 -N '' -f "$TMP/id_ed25519"
cat > "$TMP/Dockerfile" <<'EOF'
FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update -qq && apt-get install -y -qq openssh-server sudo && rm -rf /var/lib/apt/lists/*
RUN mkdir -p /run/sshd && printf '%s\n' 'PermitRootLogin prohibit-password' 'PasswordAuthentication no' 'PubkeyAuthentication yes' >> /etc/ssh/sshd_config
EOF
docker build -q -t "$IMAGE" -f "$TMP/Dockerfile" "$TMP" >/dev/null

start_host() {
    local name="$1"
    docker run -d --name "$name" -p 127.0.0.1::22 \
        "$IMAGE" /bin/bash -c \
        'set -e; install -d -m 0700 /root/.ssh; exec /usr/sbin/sshd -D -e' >/dev/null
    docker cp "$TMP/id_ed25519.pub" "$name:/root/.ssh/authorized_keys" >/dev/null
    docker exec "$name" chown root:root /root/.ssh/authorized_keys
    docker exec "$name" chmod 0600 /root/.ssh/authorized_keys
	# The controller's read-only path is fixed at /usr/local/bin/rootfiles.
	docker cp "$REMOTE_BIN" "$name:/usr/local/bin/rootfiles" >/dev/null
	docker exec "$name" chown root:root /usr/local/bin/rootfiles
	docker exec "$name" chmod 0755 /usr/local/bin/rootfiles
	docker exec "$name" cp /usr/local/bin/rootfiles /usr/local/bin/rootfiles.real
	docker exec "$name" chown root:root /usr/local/bin/rootfiles.real
	docker exec "$name" chmod 0755 /usr/local/bin/rootfiles.real
    local port
    port="$(docker port "$name" 22/tcp | awk -F: '{print $NF}')"
    printf '%s\n' "$port"
}

GOOD_PORT="$(start_host "$GOOD")"
BAD_PORT="$(start_host "$BAD")"

known_host() {
    local alias="$1" port="$2" container="$3"
    # Obtain the public host key through the trusted Docker control channel.
    docker exec "$container" cat /etc/ssh/ssh_host_ed25519_key.pub |
        awk -v names="$alias,[$alias]:$port,[127.0.0.1]:$port" 'NF >= 2 { print names, $1, $2 }' >> "$TMP/home/.ssh/known_hosts"
}

known_host fleet-good "$GOOD_PORT" "$GOOD"
known_host fleet-good-reader "$GOOD_PORT" "$GOOD"
known_host fleet-bad "$BAD_PORT" "$BAD"
cat > "$TMP/home/.ssh/config" <<EOF
Host fleet-good
  HostName 127.0.0.1
  Port $GOOD_PORT
  User root
  IdentityFile $TMP/id_ed25519
  IdentitiesOnly yes
  HostKeyAlias fleet-good
  StrictHostKeyChecking yes
  UserKnownHostsFile $TMP/home/.ssh/known_hosts
Host fleet-good-reader
  HostName 127.0.0.1
  Port $GOOD_PORT
  User fleetreader
  IdentityFile $TMP/id_ed25519
  IdentitiesOnly yes
  HostKeyAlias fleet-good-reader
  StrictHostKeyChecking yes
  UserKnownHostsFile $TMP/home/.ssh/known_hosts
Host fleet-bad
  HostName 127.0.0.1
  Port $BAD_PORT
  User root
  IdentityFile $TMP/id_ed25519
  IdentitiesOnly yes
  HostKeyAlias fleet-bad
  StrictHostKeyChecking yes
  UserKnownHostsFile $TMP/home/.ssh/known_hosts
EOF
chmod 0600 "$TMP/home/.ssh/config" "$TMP/home/.ssh/known_hosts" "$TMP/id_ed25519"

# OpenSSH can derive its config home from the invoking account's passwd entry,
# even when HOME is overridden. This test-only adapter pins -F to the isolated
# fixture and then execs the real SSH client; production `rootfiles fleet` still
# resolves `ssh` normally and retains the user's existing OpenSSH behavior.
REAL_SSH="$(command -v ssh)"
mkdir -p "$TMP/bin"
cat > "$TMP/bin/ssh" <<EOF
#!/bin/sh
exec "$REAL_SSH" -F "$TMP/home/.ssh/config" "\$@"
EOF
chmod 0755 "$TMP/bin/ssh"

cat > "$TMP/status.yaml" <<'EOF'
defaults:
  sudo: root
  parallel: 2
hosts:
  ssh-good:
    ssh: fleet-good
  ssh-bad:
    ssh: fleet-bad
EOF

run_rootfiles() {
	PATH="$TMP/bin:$PATH" HOME="$TMP/home" XDG_CONFIG_HOME="$TMP/config" XDG_STATE_HOME="$TMP/state" \
        "$CONTROLLER_BIN" "$@"
}

# Seed the good host with one declared, non-admin account and the exact
# passwordless read-only sudo boundary before the first fleet status report.
FLEET_READER_PUBKEY="$(cat "$TMP/id_ed25519.pub")"
cat > "$TMP/users-site.yaml" <<EOF
modules:
  users:
    enabled: true
users:
  home_base: /home
  default_shell: /bin/bash
  default_groups: []
  sudo_nopasswd: false
  accounts:
    - name: fleetreader
      ssh_pubkeys:
        - $FLEET_READER_PUBKEY
  fleet_sudo_users:
    - fleetreader
EOF
cat > "$TMP/users-inventory.yaml" <<'EOF'
defaults:
  sudo: root
  parallel: 1
hosts:
  fleet-good-users:
    ssh: fleet-good
    config: users-site.yaml
EOF
set +e
run_rootfiles fleet apply --inventory "$TMP/users-inventory.yaml" --host fleet-good-users --yes -o json >"$TMP/users-apply.json" 2>&1
USERS_APPLY_CODE=$?
set -e
if [[ "$USERS_APPLY_CODE" -eq 0 ]]; then
    pass "users-only site applied before the initial fleet report"
else
    fail "users-only site apply exited $USERS_APPLY_CODE: $(cat "$TMP/users-apply.json")"
fi
GOOD_SITE_FILES_BEFORE="$(docker exec "$GOOD" sh -c 'find /etc/rootfiles/fleet -maxdepth 1 -type f -print | sort')"
GOOD_BINARY_OWNER="$(docker exec "$GOOD" stat -c %U /usr/local/bin/rootfiles)"
if [[ "$GOOD_BINARY_OWNER" == root ]]; then
    pass "read-only sudo target binary is root-owned before the grant"
else
    fail "read-only sudo target binary is not root-owned"
fi

echo "=== Scenario: fleet ==="
set +e
run_rootfiles fleet status --inventory "$TMP/status.yaml" --all -o json >"$TMP/status.json" 2>&1
STATUS_CODE=$?
set -e
if [[ "$STATUS_CODE" -eq 0 || "$STATUS_CODE" -eq 2 ]]; then
    pass "read-only fleet status completed through real SSH"
else
    fail "fleet status exited $STATUS_CODE: $(cat "$TMP/status.json")"
fi
if python3 - "$TMP/status.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as f:
    report = json.load(f)
states = {row.get("host"): row.get("state") for row in report.get("hosts", [])}
expected = {"ssh-good": "ok", "ssh-bad": "drift"}
if states != expected:
    raise SystemExit(f"expected explicit states {expected}, got {states}")
rows = {row.get("host"): row for row in report["hosts"]}
if any(not row.get("remote") for row in rows.values()):
    raise SystemExit("status did not include remote documents for both hosts")
good_remote = rows["ssh-good"]["remote"]
modules = good_remote.get("modules", [])
if not good_remote.get("applied_config_sha256") or not modules or not all(m.get("satisfied") for m in modules):
    raise SystemExit("good host did not record the users-only config with all enabled modules satisfied")
PY
then
    pass "JSON report proves good host config/modules are satisfied and bad host is drift"
else
    fail "JSON report did not prove explicit ok/drift host states: $(cat "$TMP/status.json")"
fi

reader_ssh() {
    "$REAL_SSH" -F "$TMP/home/.ssh/config" fleet-good-reader "$@"
}

set +e
reader_ssh sudo -n /usr/local/bin/rootfiles status -o json >"$TMP/sudo-status.json" 2>&1
SUDO_STATUS_CODE=$?
set -e
if [[ "$SUDO_STATUS_CODE" -eq 0 ]] && python3 -m json.tool "$TMP/sudo-status.json" >/dev/null 2>&1; then
    pass "fleetreader can run the exact passwordless rootfiles status command"
else
    fail "exact rootfiles status command was not allowed (exit $SUDO_STATUS_CODE): $(cat "$TMP/sudo-status.json")"
fi

for operation in check doctor; do
    set +e
    reader_ssh sudo -n /usr/local/bin/rootfiles "$operation" -o json >"$TMP/sudo-$operation.json" 2>&1
    SUDO_CODE=$?
    set -e
    if { [[ "$SUDO_CODE" -eq 0 ]] || [[ "$SUDO_CODE" -eq 2 ]]; } && python3 -m json.tool "$TMP/sudo-$operation.json" >/dev/null 2>&1; then
        pass "fleetreader can run the exact passwordless rootfiles $operation command"
    else
        fail "rootfiles $operation command was not allowed with an accepted status (exit $SUDO_CODE): $(cat "$TMP/sudo-$operation.json")"
    fi
done

set +e
reader_ssh sudo -n /usr/local/bin/rootfiles status -o json --config /etc/shadow >"$TMP/sudo-extra-config.json" 2>&1
SUDO_EXTRA_CODE=$?
set -e
if [[ "$SUDO_EXTRA_CODE" -ne 0 ]] && grep -Eqi 'not allowed to execute|a password is required' "$TMP/sudo-extra-config.json"; then
    pass "sudo rejects the exact status command with an extra --config argument"
else
    fail "sudo did not prove rejection of extra --config (exit $SUDO_EXTRA_CODE): $(cat "$TMP/sudo-extra-config.json")"
fi

set +e
reader_ssh sudo -n /usr/local/bin/rootfiles apply --yes >"$TMP/sudo-apply.txt" 2>&1
SUDO_APPLY_CODE=$?
set -e
if [[ "$SUDO_APPLY_CODE" -ne 0 ]] && grep -Eqi 'not allowed to execute|a password is required' "$TMP/sudo-apply.txt"; then
    pass "sudo rejects native apply for the read-only account"
else
    fail "sudo did not reject native apply (exit $SUDO_APPLY_CODE): $(cat "$TMP/sudo-apply.txt")"
fi

if docker exec "$GOOD" visudo -c -f /etc/sudoers.d/rootfiles-fleet >/dev/null 2>&1; then
    pass "generated fleet sudoers file passes visudo validation"
else
    fail "generated fleet sudoers file failed visudo validation"
fi
GOOD_GROUPS="$(docker exec "$GOOD" id -nG fleetreader)"
if ! tr ' ' '\n' <<<"$GOOD_GROUPS" | grep -qx sudo; then
    pass "fleetreader is not a member of the broad sudo group"
else
    fail "fleetreader unexpectedly belongs to the broad sudo group"
fi

cat > "$TMP/rootfiles-wrapper" <<'EOF'
#!/bin/sh
printf '%s\n' "$1" >> /run/rootfiles-fleet-calls
if [ "$1" = apply ] && [ -e /run/rootfiles-fleet-fail-apply ]; then
    echo 'intentional scenario apply failure' >&2
    exit 23
fi
exec /usr/local/bin/rootfiles.real "$@"
EOF
chmod 0755 "$TMP/rootfiles-wrapper"
for container in "$GOOD" "$BAD"; do
    docker cp "$TMP/rootfiles-wrapper" "$container:/usr/local/bin/rootfiles" >/dev/null
    docker exec "$container" chown root:root /usr/local/bin/rootfiles
    docker exec "$container" chmod 0755 /usr/local/bin/rootfiles
    docker exec "$container" sh -c ': > /run/rootfiles-fleet-calls'
done
docker exec "$BAD" touch /run/rootfiles-fleet-fail-apply

cat > "$TMP/site.yaml" <<'EOF'
extends: base
users:
  home_base: /home
EOF
cat > "$TMP/rollout.yaml" <<EOF
defaults:
  sudo: root
  parallel: 2
hosts:
  first:
    ssh: fleet-good
    config: site.yaml
  failure:
    ssh: fleet-bad
    config: site.yaml
  must-be-skipped:
    ssh: fleet-good
    config: site.yaml
EOF

set +e
run_rootfiles fleet apply --inventory "$TMP/rollout.yaml" --all --yes --dry-run -o json >"$TMP/apply.json" 2>&1
APPLY_CODE=$?
set -e
if [[ "$APPLY_CODE" -eq 2 ]] && grep -q '"state": "skipped"' "$TMP/apply.json"; then
    pass "serial dry-run stops at the first failed host and reports later host skipped"
else
    fail "fleet apply did not report the stop rule (exit $APPLY_CODE): $(cat "$TMP/apply.json")"
fi
GOOD_CALLS="$(docker exec "$GOOD" cat /run/rootfiles-fleet-calls)"
if [[ "$GOOD_CALLS" == $'status\napply' ]]; then
    pass "controller did not contact the third host after the failure"
else
    fail "unexpected calls on shared good container: $GOOD_CALLS"
fi
BAD_CALLS="$(docker exec "$BAD" cat /run/rootfiles-fleet-calls)"
if [[ "$BAD_CALLS" == $'status\napply' ]]; then
    pass "controller contacted the failing second host"
else
    fail "unexpected calls on failing container: $BAD_CALLS"
fi
GOOD_SITE_FILES_AFTER="$(docker exec "$GOOD" sh -c 'find /etc/rootfiles/fleet -maxdepth 1 -type f -print | sort')"
if [[ "$GOOD_SITE_FILES_AFTER" == "$GOOD_SITE_FILES_BEFORE" ]] && docker exec "$BAD" test ! -e /etc/rootfiles/fleet; then
    pass "dry-run did not persist another remote site config"
else
    fail "dry-run changed persisted fleet config files (before: $GOOD_SITE_FILES_BEFORE, after: $GOOD_SITE_FILES_AFTER)"
fi

# Leave the good host running and make the bad endpoint unreachable only after
# every prior assertion and call-count check has completed.
docker stop "$BAD" >/dev/null
set +e
run_rootfiles fleet status --inventory "$TMP/status.yaml" --all -o json >"$TMP/unreachable.json" 2>&1
UNREACHABLE_CODE=$?
set -e
if [[ "$UNREACHABLE_CODE" -eq 2 ]] && python3 - "$TMP/unreachable.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as f:
    report = json.load(f)
states = {row.get("host"): row.get("state") for row in report.get("hosts", [])}
expected = {"ssh-good": "ok", "ssh-bad": "unreachable"}
if states != expected:
    raise SystemExit(f"expected explicit states {expected}, got {states}")
PY
then
    pass "fleet status reports the stopped final host unreachable while the first remains ok"
else
    fail "fleet status did not prove final-host unreachable state (exit $UNREACHABLE_CODE): $(cat "$TMP/unreachable.json")"
fi

# With --skip-unreachable the now-stopped middle host is skipped and the
# third host still runs; the run exits 2 and prints a rerun command.
docker exec "$GOOD" sh -c ': > /run/rootfiles-fleet-calls'
set +e
run_rootfiles fleet apply --inventory "$TMP/rollout.yaml" --all --yes --dry-run --skip-unreachable >"$TMP/skip.txt" 2>&1
SKIP_CODE=$?
set -e
GOOD_CALLS="$(docker exec "$GOOD" cat /run/rootfiles-fleet-calls)"
if [[ "$SKIP_CODE" -eq 2 ]] && grep -q 'skipped-unreachable' "$TMP/skip.txt" && grep -q -- '--host failure' "$TMP/skip.txt" && [[ "$GOOD_CALLS" == $'status\napply\nstatus\napply' ]]; then
    pass "--skip-unreachable continues past an unreachable host and prints its rerun command"
else
    fail "--skip-unreachable run (exit $SKIP_CODE, good calls: $GOOD_CALLS): $(cat "$TMP/skip.txt")"
fi

if [[ "$FAIL" -gt 0 ]]; then
    cat "$TMP/status.json" "$TMP/apply.json" >&2
    docker logs "$GOOD" >&2 || true
    docker logs "$BAD" >&2 || true
fi
report
