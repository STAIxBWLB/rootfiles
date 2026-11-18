#!/bin/bash
# Test user creation, backup, deletion, and restore
set -euo pipefail
source /tests/assert.sh

echo "=== Scenario: user-backup-restore ==="
export ROOTFILES_HOME_BASE=/raid/home

# Setup: apply minimal to get base packages (zsh needed for user creation)
echo "--- Setup: install base packages ---"
rootfiles apply --profile minimal --module packages --yes 2>&1 || true

# Step 1: Create 3 users
echo "--- Step 1: Create users ---"
rootfiles user add alice --profile dgx --yes --home-base /raid/home 2>&1 || true
rootfiles user add bob --profile dgx --yes --home-base /raid/home 2>&1 || true
rootfiles user add charlie --profile dgx --yes --home-base /raid/home 2>&1 || true

assert_user_exists "alice"
assert_user_exists "bob"
assert_user_exists "charlie"
assert_user_home "alice" "/raid/home/alice"
assert_user_home "bob" "/raid/home/bob"
assert_user_home "charlie" "/raid/home/charlie"

# Step 2: Verify metadata
echo "--- Step 2: Verify metadata ---"
assert_file_exists "/raid/home/.rootfiles/users.json"
assert_file_contains "/raid/home/.rootfiles/users.json" "alice"
assert_file_contains "/raid/home/.rootfiles/users.json" "bob"
assert_file_contains "/raid/home/.rootfiles/users.json" "charlie"

# Step 3: Create test files in home dirs
echo "test-alice" > /raid/home/alice/testfile
echo "test-bob" > /raid/home/bob/testfile

# Step 4: Backup
echo "--- Step 3: Backup users ---"
rootfiles user backup --profile dgx --yes --file /tmp/users-backup.json 2>&1 || true
assert_file_exists "/tmp/users-backup.json"
assert_file_contains "/tmp/users-backup.json" "alice"

# Step 5: Record UIDs
ALICE_UID=$(id -u alice)
BOB_UID=$(id -u bob)

# Step 6: Delete users (simulate OS reinstall — keep home dirs)
echo "--- Step 4: Delete users (keep homes) ---"
userdel alice 2>/dev/null || true
userdel bob 2>/dev/null || true
userdel charlie 2>/dev/null || true
assert_user_not_exists "alice"
assert_user_not_exists "bob"
assert_user_not_exists "charlie"

# Verify homes still exist
assert_dir_exists "/raid/home/alice"
assert_dir_exists "/raid/home/bob"
assert_file_exists "/raid/home/alice/testfile"

# Step 7: Restore from backup
echo "--- Step 5: Restore users ---"
rootfiles user restore /tmp/users-backup.json --profile dgx --yes 2>&1 || true

assert_user_exists "alice"
assert_user_exists "bob"
assert_user_exists "charlie"
assert_user_home "alice" "/raid/home/alice"
assert_user_home "bob" "/raid/home/bob"

# Verify UIDs preserved
assert_user_uid "alice" "$ALICE_UID"
assert_user_uid "bob" "$BOB_UID"

# Verify test files still accessible
assert_file_exists "/raid/home/alice/testfile"

report
