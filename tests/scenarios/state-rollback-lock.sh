#!/bin/bash
# apply records state + audit log, backs up overwritten files for rollback,
# refuses to run concurrently or without root.
set -euo pipefail
source /tests/assert.sh

echo "=== Scenario: state-rollback-lock ==="
CONF=/etc/ssh/sshd_config.d/00-rootfiles.conf
rootfiles apply --profile minimal --module packages --yes 2>&1 || true

# Operator with a key so the dgx ssh hardening is allowed.
useradd -m -s /bin/bash opsuser
install -d -m 700 -o opsuser -g opsuser /home/opsuser/.ssh
echo "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl ops" \
    > /home/opsuser/.ssh/authorized_keys

echo "--- Step 1: apply over a hand-edited file ---"
mkdir -p /etc/ssh/sshd_config.d
printf '# hand edited\nPermitRootLogin yes\n' > "$CONF"
rootfiles apply --profile dgx --module ssh --yes 2>&1 || true
assert_file_contains "$CONF" "PasswordAuthentication no"
assert_file_exists "/var/lib/rootfiles/state.json"
assert_file_contains "/var/lib/rootfiles/state.json" '"profile": "dgx"'
assert_file_exists "/var/log/rootfiles.log"
assert_file_contains "/var/log/rootfiles.log" "apply finished"

echo "--- Step 2: status reports the last apply ---"
out=$(rootfiles status 2>&1 || true)
if grep -q "Last apply" <<<"$out" && grep -Eq "Active: +dgx" <<<"$out"; then
    pass "status uses recorded profile"
else
    fail "status does not show recorded apply: $out"
fi

echo "--- Step 3: rollback restores the hand-edited file ---"
# The listing is a table: one row per backup, newest first, with the session id
# in the first column. Take the first cell that has the id shape rather than
# matching a heading, so the parse survives wording and column changes.
id=$(rootfiles rollback 2>/dev/null | awk '{print $1}' | grep -Ex '[0-9]{8}-[0-9]{6}' | head -1 || true)
if [ -n "$id" ]; then pass "backup listed ($id)"; else fail "no backup listed"; fi
rootfiles rollback "$id" --yes 2>&1 || true
assert_file_contains "$CONF" "hand edited"

echo "--- Step 4: concurrent runs are refused ---"
(flock /run/rootfiles.lock sleep 5) &
sleep 1
out=$(rootfiles apply --profile base --module locale --yes 2>&1 || true)
if grep -q "another rootfiles command is running" <<<"$out"; then
    pass "lock prevents concurrent apply"
else
    fail "concurrent apply was not refused"
fi
wait

echo "--- Step 5: non-root is refused (dry-run allowed) ---"
out=$(su -s /bin/sh nobody -c "rootfiles apply --profile base --yes" 2>&1 || true)
if grep -q "must run as root" <<<"$out"; then
    pass "non-root apply refused"
else
    fail "non-root apply not refused"
fi
if su -s /bin/sh nobody -c "rootfiles apply --profile base --yes --dry-run" >/dev/null 2>&1; then
    pass "non-root dry-run allowed"
else
    fail "non-root dry-run refused"
fi

report
