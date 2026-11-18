#!/bin/bash
# user key add/list/rm, lock/unlock, expire, du, audit, del --archive
set -euo pipefail
source /tests/assert.sh

echo "=== Scenario: user-lifecycle ==="
export ROOTFILES_HOME_BASE=/raid/home
KEY1="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl laptop"
KEY2="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHkRJcXi3BRu5lYx2L2Umn2k1vwRHNyxq1Y8r5yIS6xN desktop"

rootfiles apply --profile minimal --module packages --yes 2>&1 || true
rootfiles apply --profile dgx --module users --yes 2>&1 || true
rootfiles user add alice --profile dgx --yes --ssh-pubkey "$KEY1" 2>&1 || true
assert_user_exists "alice"
AK=/raid/home/alice/.ssh/authorized_keys

echo "--- keys ---"
rootfiles user key add alice "$KEY2" --profile dgx 2>&1 || true
assert_file_contains "$AK" "desktop"
out=$(rootfiles user key list alice --profile dgx 2>&1 || true)
if grep -q "laptop" <<<"$out" && grep -q "desktop" <<<"$out"; then pass "key list shows both"; else fail "key list: $out"; fi
rootfiles user key rm alice laptop --profile dgx 2>&1 || true
if grep -q "laptop" "$AK"; then fail "key not removed"; else pass "key removed by comment"; fi
assert_file_contains "$AK" "desktop"

echo "--- lock/unlock ---"
rootfiles user lock alice --profile dgx 2>&1 || true
if passwd -S alice | awk '{print $2}' | grep -q L && [ "$(chage -l alice | awk -F': ' '/Account expires/ {print $2}')" != "never" ]; then
    pass "alice locked and expired"
else
    fail "alice not fully locked"; passwd -S alice; chage -l alice
fi
rootfiles user unlock alice --profile dgx 2>&1 || true
if [ "$(chage -l alice | awk -F': ' '/Account expires/ {print $2}')" = "never" ]; then pass "alice unlocked"; else fail "alice still expired"; fi

echo "--- expire ---"
rootfiles user expire alice 2099-12-31 --profile dgx 2>&1 || true
if chage -l alice | grep -q "Dec 31, 2099"; then pass "expiry set"; else fail "expiry not set"; fi

echo "--- du / audit ---"
dd if=/dev/zero of=/raid/home/alice/blob bs=1M count=3 status=none
out=$(rootfiles user du --profile dgx 2>&1 || true)
if grep -Eq "MiB +alice" <<<"$out"; then pass "du reports alice"; else fail "du: $out"; fi
useradd -m -d /raid/home/stray stray
out=$(rootfiles user audit --profile dgx 2>&1 || true)
if grep -q "stray" <<<"$out" && grep -q "not tracked" <<<"$out"; then pass "audit finds untracked account"; else fail "audit: $out"; fi

echo "--- del --archive ---"
rootfiles user del alice --archive --profile dgx --yes 2>&1 || true
assert_user_not_exists "alice"
if ls /raid/home/.rootfiles/archive/alice-*.tar.gz >/dev/null 2>&1; then pass "home archived"; else fail "no archive"; fi
if [ -d /raid/home/alice ]; then fail "home still present"; else pass "home removed after archive"; fi
if grep -q '"alice"' /raid/home/.rootfiles/users.json; then fail "metadata not cleaned"; else pass "metadata cleaned"; fi

report
