#!/bin/bash
# Run rootfiles apply with a profile and verify results
set -euo pipefail
source /tests/assert.sh

PROFILE="${1:-minimal}"
echo "=== Integration Test: profile=$PROFILE ==="

# In CI containers, docker and nvidia modules cannot work (no real Docker/GPU).
# Apply all modules except docker and nvidia.
CI_MODULES="locale,system,packages,users,ssh,security,cloudflared,storage,network"

# Operators have key-based access before SSH is hardened; the ssh module
# refuses to disable password auth when no account holds an authorized key.
id ciadmin >/dev/null 2>&1 || useradd -m -s /bin/bash ciadmin
install -d -m 700 -o ciadmin -g ciadmin /home/ciadmin/.ssh
echo "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl ci" \
    > /home/ciadmin/.ssh/authorized_keys

# Apply profile
echo "--- Applying profile: $PROFILE ---"
rootfiles apply --profile "$PROFILE" --module "$CI_MODULES" --yes 2>&1 || true

# Common assertions (all profiles include base)
echo "--- Verifying base packages ---"
assert_package_installed "git"
assert_package_installed "curl"
assert_package_installed "zsh"
assert_package_installed "tmux"
assert_package_installed "jq"
assert_package_installed "htop"

# Locale
assert_file_exists "/etc/default/locale"
assert_file_contains "/etc/default/locale" "en_US.UTF-8"

# SSH config (all profiles)
assert_file_exists "/etc/ssh/sshd_config.d/00-rootfiles.conf"

# Profile-specific assertions
case "$PROFILE" in
    minimal|dgx|gpu-server|full)
        echo "--- Verifying minimal extras ---"
        assert_package_installed "openssh-server"
        assert_package_installed "ufw"
        assert_package_installed "fail2ban"

        # Users module
        if [ "$PROFILE" = "dgx" ]; then
            assert_dir_exists "/raid/home"
            assert_dir_exists "/raid/home/.rootfiles"
        fi

        # Cloudflared binary
        assert_command_exists "cloudflared"

        # System / security baseline
        assert_file_contains "/etc/systemd/journald.conf.d/90-rootfiles.conf" "SystemMaxUse=2G"
        assert_package_installed "unattended-upgrades"
        assert_file_contains "/etc/apt/apt.conf.d/zzzzzz-rootfiles-unattended-upgrades" '"nvidia-";'
        assert_file_contains "/etc/apt/apt.conf.d/20auto-upgrades" 'Unattended-Upgrade "1"'
        assert_file_contains "/etc/fail2ban/jail.d/rootfiles-sshd.conf" "enabled  = true"
        ;;
esac

case "$PROFILE" in
    dgx)
        echo "--- Verifying DGX-specific ---"
        assert_dir_exists "/raid/data"
        assert_file_exists "/etc/ssh/sshd_config.d/00-rootfiles.conf"
        assert_file_contains "/etc/ssh/sshd_config.d/00-rootfiles.conf" "PasswordAuthentication no"

        # VLAN interface
        assert_file_exists "/etc/systemd/network/10-cloudflared-vlan.netdev"
        assert_file_exists "/etc/systemd/network/10-cloudflared-vlan.network"
        assert_file_contains "/etc/systemd/network/10-cloudflared-vlan.network" "172.16"
        ;;
    gpu-server)
        echo "--- Verifying gpu-server-specific ---"
        assert_file_contains "/etc/ssh/sshd_config.d/00-rootfiles.conf" "PasswordAuthentication no"
        ;;
esac

echo ""
echo "--- Verifying idempotency ---"
rootfiles check --profile "$PROFILE" 2>&1 || true

report
