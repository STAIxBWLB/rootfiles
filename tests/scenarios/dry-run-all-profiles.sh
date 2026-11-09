#!/bin/bash
# Verify dry-run mode makes zero actual changes for all profiles
set -euo pipefail
source "${ROOTFILES_TEST_ASSERT:-/tests/assert.sh}"

echo "=== Scenario: dry-run-all-profiles ==="

for profile in base minimal dgx gpu-server full; do
    echo "--- Dry-run: $profile ---"

    # Snapshot key files before
    md5_locale=$(md5sum /etc/default/locale 2>/dev/null || echo "none")
    md5_sshd=$(md5sum /etc/ssh/sshd_config.d/00-rootfiles.conf 2>/dev/null || echo "none")

    # Run dry-run
    exit_code=0
    output=$(rootfiles apply --profile "$profile" --dry-run --yes 2>&1) || exit_code=$?

    if [ $exit_code -eq 0 ]; then
        pass "dry-run $profile: exit 0"
    else
        printf '%s\n' "$output"
        fail "dry-run $profile: exit $exit_code"
    fi

    # Verify no changes
    md5_locale_after=$(md5sum /etc/default/locale 2>/dev/null || echo "none")
    md5_sshd_after=$(md5sum /etc/ssh/sshd_config.d/00-rootfiles.conf 2>/dev/null || echo "none")

    if [ "$md5_locale" = "$md5_locale_after" ]; then
        pass "dry-run $profile: locale unchanged"
    else
        fail "dry-run $profile: locale was modified"
    fi

    if [ "$md5_sshd" = "$md5_sshd_after" ]; then
        pass "dry-run $profile: sshd unchanged"
    else
        fail "dry-run $profile: sshd was modified"
    fi

    # Verify output contains module names
    if echo "$output" | grep -q "locale\|packages\|ssh"; then
        pass "dry-run $profile: output lists modules"
    else
        fail "dry-run $profile: output missing module info"
    fi
done

report
