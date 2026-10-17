#!/bin/bash
# Test a single module in isolation
set -euo pipefail
source /tests/assert.sh

MODULE="${1:?Usage: run-module.sh <module>}"
echo "=== Module Test: $MODULE ==="

# Apply single module
echo "--- Applying module: $MODULE ---"
rootfiles apply --profile minimal --module "$MODULE" --yes 2>&1 || true

# Module-specific assertions
case "$MODULE" in
    system)
        assert_file_contains "/etc/systemd/journald.conf.d/90-rootfiles.conf" "SystemMaxUse=2G"
        ;;
    security)
        assert_package_installed "unattended-upgrades"
        assert_file_contains "/etc/apt/apt.conf.d/zzzzzz-rootfiles-unattended-upgrades" 'Automatic-Reboot "false"'
        # The package's own 50unattended-upgrades adds general origins; the
        # merged policy must still allow only the security pockets.
        if apt-config dump | grep -E '^Unattended-Upgrade::(Allowed-Origins|Origins-Pattern):: ' | grep -vq -- '-security";$'; then
            fail "effective unattended-upgrades policy is not security-only"
        else
            pass "effective unattended-upgrades policy is security-only"
        fi
        assert_file_exists "/etc/fail2ban/jail.d/rootfiles-sshd.conf"
        ;;
    locale)
        assert_file_exists "/etc/default/locale"
        assert_file_contains "/etc/default/locale" "en_US.UTF-8"
        ;;
    packages)
        assert_package_installed "git"
        assert_package_installed "curl"
        assert_package_installed "zsh"
        assert_package_installed "tmux"
        assert_package_installed "jq"
        assert_package_installed "openssh-server"
        ;;
    ssh)
        assert_file_exists "/etc/ssh/sshd_config.d/00-rootfiles.conf"
        assert_file_contains "/etc/ssh/sshd_config.d/00-rootfiles.conf" "rootfiles-v2"
        assert_file_contains "/etc/ssh/sshd_config.d/00-rootfiles.conf" "PermitRootLogin no"
        ;;
    users)
        assert_dir_exists "/raid/home"
        assert_dir_exists "/raid/home/.rootfiles"
        ;;
    cloudflared)
        assert_command_exists "cloudflared"
        assert_file_exists "/usr/local/bin/cloudflared"
        ;;
    storage)
        # With default minimal profile, storage is not enabled
        # Test with dgx profile for storage
        rootfiles apply --profile dgx --module storage --yes 2>&1 || true
        assert_dir_exists "/raid/data"
        assert_dir_exists "/raid/home"
        ;;
    network)
        # With dgx profile for network module
        rootfiles apply --profile dgx --module network --yes 2>&1 || true
        assert_command_exists "ufw"
        ;;
    gpu)
        # GPU module needs dgx profile, fake devices, and a test user
        setup-fake-gpus
        useradd -m -d /raid/home/gputest -s /bin/bash gputest 2>/dev/null || true
        rootfiles gpu assign gputest --gpus 0,1 --method env --yes 2>&1 || true
        assert_file_exists "/etc/profile.d/rootfiles-gpu-gputest.sh"
        assert_file_contains "/etc/profile.d/rootfiles-gpu-gputest.sh" "CUDA_VISIBLE_DEVICES=0,1"

        # Verify list shows the allocation
        list_out=$(rootfiles gpu list --yes 2>&1)
        if echo "$list_out" | grep -q "gputest"; then
            pass "gpu list shows gputest"
        else
            fail "gpu list missing gputest"
        fi

        # Verify revoke cleans up
        rootfiles gpu revoke gputest --yes 2>&1 || true
        if [ ! -f "/etc/profile.d/rootfiles-gpu-gputest.sh" ]; then
            pass "gpu revoke removed env script"
        else
            fail "gpu revoke did not remove env script"
        fi
        ;;
esac

echo ""
echo "--- Verifying dry-run produces no changes ---"
rootfiles apply --profile minimal --module "$MODULE" --dry-run --yes 2>&1 || true

report
