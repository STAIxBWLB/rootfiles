#!/bin/bash
# Test full system backup command
set -euo pipefail
source /tests/assert.sh

echo "=== Scenario: system-backup ==="
export ROOTFILES_HOME_BASE=/raid/home

# Setup: create home base and a test user
echo "--- Setup ---"
mkdir -p /raid/home/.rootfiles /raid/backup
rootfiles apply --profile dgx --module packages --yes 2>&1 || true
rootfiles user add testuser --profile dgx --yes --home-base /raid/home 2>&1 || true

# Step 1: Run backup
echo "--- Step 1: Run backup ---"
rootfiles backup --dir /raid/backup --profile dgx --yes 2>&1

# Find the backup dir
BACKUP_DIR=$(ls -d /raid/backup/rootfiles-backup-* | head -1)
echo "  Backup dir: $BACKUP_DIR"

# Step 2: Verify all expected files
echo "--- Step 2: Verify backup files ---"
assert_file_exists "$BACKUP_DIR/system-info.json"
assert_file_exists "$BACKUP_DIR/users.json"
assert_file_exists "$BACKUP_DIR/config-snapshot.yaml"
assert_file_exists "$BACKUP_DIR/usr-local-bin.tar.gz"

# system-info.json should have hostname
assert_file_contains "$BACKUP_DIR/system-info.json" "hostname"
assert_file_contains "$BACKUP_DIR/system-info.json" "arch"

# users.json should have the test user
assert_file_contains "$BACKUP_DIR/users.json" "testuser"

# config-snapshot.yaml should be valid rootfiles config
assert_file_contains "$BACKUP_DIR/config-snapshot.yaml" "locale"
assert_file_contains "$BACKUP_DIR/config-snapshot.yaml" "timezone"
assert_file_contains "$BACKUP_DIR/config-snapshot.yaml" "ssh"

# Step 3: Verify config snapshot can be used with --dry-run
echo "--- Step 3: Dry-run apply from snapshot ---"
assert_exit_code "apply from snapshot" rootfiles apply --config "$BACKUP_DIR/config-snapshot.yaml" --dry-run --yes

# Step 4: Test --skip-docker and --skip-etc flags
echo "--- Step 4: Test skip flags ---"
rm -rf /raid/backup/rootfiles-backup-*
rootfiles backup --dir /raid/backup --skip-docker --skip-etc --profile dgx --yes 2>&1

BACKUP_DIR2=$(ls -d /raid/backup/rootfiles-backup-* | head -1)
assert_file_exists "$BACKUP_DIR2/system-info.json"
assert_file_exists "$BACKUP_DIR2/config-snapshot.yaml"
# docker-images.txt should NOT exist when --skip-docker
if [ -f "$BACKUP_DIR2/docker-images.txt" ]; then
    fail "docker-images.txt should not exist with --skip-docker"
else
    pass "docker-images.txt skipped with --skip-docker"
fi
# etc-config.tar.gz should NOT exist when --skip-etc
if [ -f "$BACKUP_DIR2/etc-config.tar.gz" ]; then
    fail "etc-config.tar.gz should not exist with --skip-etc"
else
    pass "etc-config.tar.gz skipped with --skip-etc"
fi

report
