#!/usr/bin/env bash
# rootfiles exporter firewall guard (installed by the rootfiles monitoring
# module when modules.monitoring.exporter_firewall is true; do not edit).
#
# rootfiles writes this file to /usr/local/sbin/rootfiles-exporter-firewall
# (root:root, 0750) and /etc/rootfiles/exporter-firewall.conf (root:root, 0600):
#   BIND_IP=192.0.2.10
#   ALLOW_FROM=198.51.100.200/32
#   WEB_INTERFACE=br-0123456789ab   # monitoring hub Compose bridge, hub hosts only
# IPv4 iptables only. Setup writes rules and units but does not start or
# restart Docker, UFW, exporter, or application units.
#
# Actions: plan (read-only), setup, apply, apply-host, apply-docker, check,
# verify-host, verify-docker (read-only rule checks run by the exporters'
# ExecStartPre), rollback.
# The Docker guard matches only forwarded traffic with conntrack original
# destination BIND_IP:9400. It changes no Docker containers, global firewall
# policies, or other ports. Host-originated Docker checks do not traverse these
# DOCKER-USER original-destination rules.
set -euo pipefail

readonly OWNER="rootfiles-exporter-firewall:v1"
readonly CONFIG="/etc/rootfiles/exporter-firewall.conf"
readonly HELPER="/usr/local/sbin/rootfiles-exporter-firewall"
readonly SERVICE="rootfiles-exporter-firewall.service"
readonly SERVICE_FILE="/etc/systemd/system/rootfiles-exporter-firewall.service"
readonly DOCKER_DROPIN="/etc/systemd/system/docker.service.d/90-rootfiles-exporter-firewall.conf"
readonly UFW_DROPIN="/etc/systemd/system/ufw.service.d/90-rootfiles-exporter-firewall.conf"
readonly NODE_DROPIN="/etc/systemd/system/prometheus-node-exporter.service.d/90-rootfiles-exporter-firewall.conf"
readonly DCGM_DROPIN="/etc/systemd/system/rootfiles-dcgm-exporter.service.d/90-rootfiles-exporter-firewall.conf"
readonly HUB_DROPIN="/etc/systemd/system/rootfiles-monitoring-hub.service.d/90-rootfiles-exporter-firewall.conf"
readonly INPUT_CHAIN="RFILES_EXPORTER_IN"
readonly DOCKER_CHAIN="RFILES_DCGM_DU"
readonly NODE_PORT=9100
readonly DCGM_PORT=9400
KEEP_GUARDS=0

ACTION=plan
if [[ "$#" -gt 0 ]]; then ACTION="$1"; fi
BIND_IP=""
ALLOW_FROM=""
WEB_INTERFACE=""

die() { echo "ERROR: $*" >&2; exit 1; }
note() { echo "rootfiles exporter firewall: $*"; }
warn() { echo "WARNING: $*" >&2; }
require_root() { [[ "$EUID" -eq 0 ]] || die "run as root"; }
ipt() { iptables -w 5 "$@"; }

validate_config() {
    require_root
    mode_is_trusted /etc/rootfiles || die "/etc/rootfiles must be root-owned and not group/world writable"
    load_config
}

require_helper_installed() {
    local current owner mode
    mode_is_trusted /usr/local/sbin || die "/usr/local/sbin must be a trusted root-owned directory"
    current="$(readlink -f -- "$0")"
    [[ "$current" == "$HELPER" && -f "$HELPER" && ! -L "$HELPER" ]] || die "install this script as $HELPER before setup"
    owner="$(stat -c '%u:%g' "$HELPER")"
    [[ "$owner" == "0:0" ]] || die "$HELPER must be root:root owned"
    mode="$(stat -c '%a' "$HELPER")"
    (( (0$mode & 022) == 0 )) || die "$HELPER must not be group/world writable"
    grep -Fq "$OWNER" "$HELPER" || die "$HELPER has no matching owner marker"
}

mode_is_trusted() {
    local path="$1" owner mode
    [[ -d "$path" && ! -L "$path" ]] || return 1
    owner="$(stat -c '%u:%g' "$path")"
    [[ "$owner" == "0:0" ]] || return 1
    mode="$(stat -c '%a' "$path")"
    (( (0$mode & 022) == 0 ))
}

ensure_trusted_dir() {
    local path="$1" parent
    if [[ "$path" == "/" ]]; then
        mode_is_trusted / || die "untrusted root directory"
        return
    fi
    parent="$(dirname "$path")"
    ensure_trusted_dir "$parent"
    [[ ! -L "$path" ]] || die "refusing symlink directory: $path"
    if [[ -e "$path" ]]; then
        mode_is_trusted "$path" || die "untrusted directory: $path"
    else
        mode_is_trusted "$parent" || die "untrusted parent directory: $parent"
        install -d -o root -g root -m 0755 "$path"
        mode_is_trusted "$path" || die "created directory is not trusted: $path"
    fi
}

file_is_owned_or_absent() {
    local path="$1"
    [[ ! -L "$path" ]] || die "refusing symlink: $path"
    if [[ -e "$path" ]]; then
        [[ -f "$path" ]] || die "not a regular file: $path"
        [[ "$(stat -c '%u:%g' "$path")" == "0:0" ]] || die "file is not root-owned: $path"
        grep -Fq "$OWNER" "$path" || die "unowned file collision: $path"
    fi
}

write_owned_file() {
    local path="$1" content="$2" parent tmp
    parent="$(dirname "$path")"
    ensure_trusted_dir "$parent"
    file_is_owned_or_absent "$path"
    tmp="$(mktemp "$parent/.rootfiles-exporter-fw.XXXXXX")"
    trap 'rm -f -- "$tmp"' EXIT
    printf '%s' "$content" > "$tmp"
    chown root:root "$tmp"
    chmod 0644 "$tmp"
    mv -f -- "$tmp" "$path"
    trap - EXIT
}

remove_owned_file() {
    local path="$1"
    [[ -e "$path" || -L "$path" ]] || return 0
    [[ ! -L "$path" && -f "$path" ]] || die "refusing to remove non-regular managed path: $path"
    [[ "$(stat -c '%u:%g' "$path")" == "0:0" ]] || die "refusing to remove non-root-owned file: $path"
    grep -Fq "$OWNER" "$path" || die "refusing to remove unowned file: $path"
    rm -f -- "$path"
}

load_config() {
    local key value seen_bind=0 seen_allow=0 seen_if=0 owner mode
    [[ -f "$CONFIG" && ! -L "$CONFIG" ]] || die "missing regular config file: $CONFIG"
    owner="$(stat -c '%u:%g' "$CONFIG")"
    [[ "$owner" == "0:0" ]] || die "config must be root:root owned"
    mode="$(stat -c '%a' "$CONFIG")"
    (( (0$mode & 022) == 0 )) || die "config must not be group/world writable"
    while IFS='=' read -r key value || [[ -n "$key$value" ]]; do
        case "$key" in
            ""|\#*) continue ;;
            BIND_IP)
                (( seen_bind == 0 )) || die "duplicate BIND_IP"
                seen_bind=1
                BIND_IP="$value"
                ;;
            ALLOW_FROM)
                (( seen_allow == 0 )) || die "duplicate ALLOW_FROM"
                seen_allow=1
                ALLOW_FROM="$value"
                ;;
            WEB_INTERFACE)
                (( seen_if == 0 )) || die "duplicate WEB_INTERFACE"
                seen_if=1
                WEB_INTERFACE="$value"
                ;;
            *) die "unsupported config key: $key" ;;
        esac
    done < "$CONFIG"
    [[ "$seen_bind" -eq 1 && "$seen_allow" -eq 1 ]] || die "config requires BIND_IP and ALLOW_FROM"
    [[ "$BIND_IP" =~ ^[0-9.]+$ ]] || die "BIND_IP must be an IPv4 address"
    [[ "$ALLOW_FROM" =~ ^[0-9./]+$ && "$ALLOW_FROM" != *,* ]] || die "ALLOW_FROM must be one IPv4 CIDR"
    if [[ -n "$WEB_INTERFACE" && ! "$WEB_INTERFACE" =~ ^br-[0-9a-fA-F]{12}$ ]]; then
        die "WEB_INTERFACE must be a Docker bridge name like br-0123456789ab"
    fi
    command -v python3 >/dev/null || die "python3 is required for strict IPv4/CIDR validation"
    python3 -c 'import ipaddress,sys; ipaddress.IPv4Address(sys.argv[1]); n=ipaddress.IPv4Network(sys.argv[2], strict=True); assert n.prefixlen == 32, "exporter source must be one host /32"' "$BIND_IP" "$ALLOW_FROM" ||
        die "BIND_IP or ALLOW_FROM is not a valid canonical IPv4 value"
    command -v iptables >/dev/null || die "iptables is required"
    if command -v firewall-cmd >/dev/null && firewall-cmd --state >/dev/null 2>&1; then
        die "active firewalld is unsupported by this iptables/UFW template"
    fi
}

ufw_unit_loaded() {
    [[ "$(systemctl show -p LoadState --value ufw.service 2>/dev/null || true)" == "loaded" ]]
}

ufw_active() {
    command -v ufw >/dev/null && ufw status 2>/dev/null | grep -qi '^Status: active$'
}

warn_legacy_publisher() {
    command -v docker >/dev/null || return 0
    local name
    while IFS= read -r name; do
        [[ -n "$name" && "$name" != "rootfiles-dcgm-exporter" ]] &&
            warn "container '$name' already publishes $DCGM_PORT; it is untouched but gets this source restriction on BIND_IP:$DCGM_PORT"
    done < <(docker ps --filter "publish=$DCGM_PORT" --format '{{.Names}}' 2>/dev/null || true)
    return 0
}

chain_exists() { ipt -S "$1" >/dev/null 2>&1; }
chain_owned() {
    local chain="$1" family="$2" line marker_found=0
    while IFS= read -r line; do
        [[ "$line" == *"-A $chain "* ]] || continue
        [[ "$line" == *"$OWNER:$family:"* ]] || return 1
        [[ "$line" == *"$OWNER:$family:owner"* ]] && marker_found=1
    done < <(ipt -S "$chain" 2>/dev/null)
    (( marker_found == 1 ))
}

check_parent_references() {
    local parent="$1" chain="$2" jump_mark="$3" line
    while IFS= read -r line; do
        [[ "$line" == *"-j $chain"* ]] || continue
        [[ "$line" == *"$jump_mark"* ]] || die "unowned jump references $chain from $parent: $line"
    done < <(ipt -S "$parent" 2>/dev/null)
}

check_all_chain_references() {
    local parent="$1" chain="$2" jump_mark="$3" line
    while IFS= read -r line; do
        [[ "$line" == *"-j $chain"* ]] || continue
        [[ "$line" == "-A $parent "* && "$line" == *"$jump_mark"* ]] ||
            die "unowned filter-table reference to $chain: $line"
    done < <(iptables-save -t filter)
}

preflight_chains() {
    ipt -S INPUT >/dev/null || die "INPUT chain is unavailable"
    ipt -S DOCKER-USER >/dev/null || die "DOCKER-USER is missing; start Docker before setup"
    if chain_exists "$INPUT_CHAIN" && ! chain_owned "$INPUT_CHAIN" input; then
        die "chain collision: $INPUT_CHAIN exists without this template's owner marker"
    fi
    if chain_exists "$DOCKER_CHAIN" && ! chain_owned "$DOCKER_CHAIN" docker; then
        die "chain collision: $DOCKER_CHAIN exists without this template's owner marker"
    fi
    check_parent_references INPUT "$INPUT_CHAIN" "$OWNER:jump:input"
    check_parent_references DOCKER-USER "$DOCKER_CHAIN" "$OWNER:jump:docker"
    check_all_chain_references INPUT "$INPUT_CHAIN" "$OWNER:jump:input"
    check_all_chain_references DOCKER-USER "$DOCKER_CHAIN" "$OWNER:jump:docker"
    check_guard_config INPUT "$OWNER:guard:input" "-p tcp -d $BIND_IP --dport $NODE_PORT"
    check_guard_config DOCKER-USER "$OWNER:guard:docker" "-p tcp -m conntrack --ctdir ORIGINAL --ctorigdst $BIND_IP --ctorigdstport $DCGM_PORT"
}

preflight_managed_files() {
	mode_is_trusted /etc/systemd/system || die "untrusted systemd unit directory"
	for dir in /etc/systemd/system/docker.service.d /etc/systemd/system/ufw.service.d /etc/systemd/system/prometheus-node-exporter.service.d /etc/systemd/system/rootfiles-dcgm-exporter.service.d /etc/systemd/system/rootfiles-monitoring-hub.service.d; do
		if [[ -e "$dir" || -L "$dir" ]]; then mode_is_trusted "$dir" || die "untrusted systemd drop-in directory: $dir"; fi
	done
	file_is_owned_or_absent "$SERVICE_FILE"
	file_is_owned_or_absent "$DOCKER_DROPIN"
	file_is_owned_or_absent "$UFW_DROPIN"
	file_is_owned_or_absent "$NODE_DROPIN"
	file_is_owned_or_absent "$DCGM_DROPIN"
	file_is_owned_or_absent "$HUB_DROPIN"
	if ! ufw_unit_loaded && ufw_active; then
		die "UFW is active but ufw.service is unavailable; cannot persist INPUT rules across UFW reloads"
	fi
}

ensure_owned_chain() {
    local chain="$1" family="$2"
    if chain_exists "$chain"; then
        chain_owned "$chain" "$family" || die "refusing to modify unowned chain: $chain"
    else
        ipt -N "$chain"
    fi
}

remove_owned_jumps() {
    local parent="$1" chain="$2" marker="$3"
    while ipt -C "$parent" -m comment --comment "$marker" -j "$chain" >/dev/null 2>&1; do
        ipt -D "$parent" -m comment --comment "$marker" -j "$chain"
    done
}

check_guard_config() {
    local parent="$1" marker="$2" exact="$3" line
    while IFS= read -r line; do
        [[ "$line" == *"$marker"* ]] || continue
        ipt -C "$parent" $exact -m comment --comment "$marker" -j DROP >/dev/null 2>&1 ||
            die "owned fail-closed guard has stale or unexpected parameters in $parent; inspect it before changing config"
    done < <(ipt -S "$parent" 2>/dev/null)
}

ensure_input_guard() {
    local marker="$OWNER:guard:input"
    check_guard_config INPUT "$marker" "-p tcp -d $BIND_IP --dport $NODE_PORT"
    ipt -C INPUT -p tcp -d "$BIND_IP" --dport "$NODE_PORT" -m comment --comment "$marker" -j DROP >/dev/null 2>&1 ||
        ipt -I INPUT 1 -p tcp -d "$BIND_IP" --dport "$NODE_PORT" -m comment --comment "$marker" -j DROP
}

ensure_docker_guard() {
    local marker="$OWNER:guard:docker"
    check_guard_config DOCKER-USER "$marker" "-p tcp -m conntrack --ctdir ORIGINAL --ctorigdst $BIND_IP --ctorigdstport $DCGM_PORT"
    ipt -C DOCKER-USER -p tcp -m conntrack --ctdir ORIGINAL --ctorigdst "$BIND_IP" --ctorigdstport "$DCGM_PORT" -m comment --comment "$marker" -j DROP >/dev/null 2>&1 ||
        ipt -I DOCKER-USER 1 -p tcp -m conntrack --ctdir ORIGINAL --ctorigdst "$BIND_IP" --ctorigdstport "$DCGM_PORT" -m comment --comment "$marker" -j DROP
}

remove_input_guard() {
    while ipt -C INPUT -p tcp -d "$BIND_IP" --dport "$NODE_PORT" -m comment --comment "$OWNER:guard:input" -j DROP >/dev/null 2>&1; do
        ipt -D INPUT -p tcp -d "$BIND_IP" --dport "$NODE_PORT" -m comment --comment "$OWNER:guard:input" -j DROP
    done
}

remove_docker_guard() {
    while ipt -C DOCKER-USER -p tcp -m conntrack --ctdir ORIGINAL --ctorigdst "$BIND_IP" --ctorigdstport "$DCGM_PORT" -m comment --comment "$OWNER:guard:docker" -j DROP >/dev/null 2>&1; do
        ipt -D DOCKER-USER -p tcp -m conntrack --ctdir ORIGINAL --ctorigdst "$BIND_IP" --ctorigdstport "$DCGM_PORT" -m comment --comment "$OWNER:guard:docker" -j DROP
    done
}

apply_host_rules() {
    local jump="$OWNER:jump:input"
    check_parent_references INPUT "$INPUT_CHAIN" "$jump"
    check_all_chain_references INPUT "$INPUT_CHAIN" "$jump"
    check_guard_config INPUT "$OWNER:guard:input" "-p tcp -d $BIND_IP --dport $NODE_PORT"
    ensure_input_guard
    ensure_owned_chain "$INPUT_CHAIN" input
    remove_owned_jumps INPUT "$INPUT_CHAIN" "$jump"
    ipt -F "$INPUT_CHAIN"
    # Leave the owner marker in place even if the process is interrupted. The
    # parent DROP guard keeps this endpoint closed until the rebuilt chain is
    # attached and complete.
    ipt -A "$INPUT_CHAIN" -m comment --comment "$OWNER:input:owner" -j RETURN
    ipt -I "$INPUT_CHAIN" 1 -p tcp -d "$BIND_IP" --dport "$NODE_PORT" -m comment --comment "$OWNER:input:node-drop" -j DROP
    ipt -I "$INPUT_CHAIN" 1 -p tcp -s "$ALLOW_FROM" -d "$BIND_IP" --dport "$NODE_PORT" -m conntrack --ctstate NEW,ESTABLISHED -m comment --comment "$OWNER:input:web" -j ACCEPT
    if [[ -n "$WEB_INTERFACE" ]]; then
        ipt -I "$INPUT_CHAIN" 1 -i "$WEB_INTERFACE" -p tcp -d "$BIND_IP" --dport "$NODE_PORT" -m comment --comment "$OWNER:input:web-bridge" -j ACCEPT
    fi
    ipt -I "$INPUT_CHAIN" 1 -i lo -p tcp -d "$BIND_IP" --dport "$NODE_PORT" -m comment --comment "$OWNER:input:loopback" -j ACCEPT
    ipt -I INPUT 1 -m comment --comment "$jump" -j "$INPUT_CHAIN"
    if [[ "$KEEP_GUARDS" -eq 0 ]]; then remove_input_guard; fi
}

apply_docker_rules() {
    local jump="$OWNER:jump:docker"
    ipt -S DOCKER-USER >/dev/null || die "DOCKER-USER is missing; start Docker before applying DCGM protection"
    check_parent_references DOCKER-USER "$DOCKER_CHAIN" "$jump"
    check_all_chain_references DOCKER-USER "$DOCKER_CHAIN" "$jump"
    check_guard_config DOCKER-USER "$OWNER:guard:docker" "-p tcp -m conntrack --ctdir ORIGINAL --ctorigdst $BIND_IP --ctorigdstport $DCGM_PORT"
    ensure_docker_guard
    ensure_owned_chain "$DOCKER_CHAIN" docker
    remove_owned_jumps DOCKER-USER "$DOCKER_CHAIN" "$jump"
    ipt -F "$DOCKER_CHAIN"
    ipt -A "$DOCKER_CHAIN" -m comment --comment "$OWNER:docker:owner" -j RETURN
    ipt -I "$DOCKER_CHAIN" 1 -p tcp -m conntrack --ctdir ORIGINAL --ctorigdst "$BIND_IP" --ctorigdstport "$DCGM_PORT" -m comment --comment "$OWNER:docker:dcgm-drop" -j DROP
    ipt -I "$DOCKER_CHAIN" 1 -p tcp -s "$ALLOW_FROM" -m conntrack --ctstate NEW,ESTABLISHED --ctdir ORIGINAL --ctorigdst "$BIND_IP" --ctorigdstport "$DCGM_PORT" -m comment --comment "$OWNER:docker:web" -j ACCEPT
    ipt -I "$DOCKER_CHAIN" 1 -p tcp -m conntrack --ctstate ESTABLISHED,RELATED --ctdir REPLY --ctorigdst "$BIND_IP" --ctorigdstport "$DCGM_PORT" -m comment --comment "$OWNER:docker:reply" -j RETURN
    ipt -I DOCKER-USER 1 -m comment --comment "$jump" -j "$DOCKER_CHAIN"
    if [[ "$KEEP_GUARDS" -eq 0 ]]; then remove_docker_guard; fi
}

check_rules() {
    check_host_rules && check_docker_rules
}

check_host_rules() {
    ipt -S INPUT | grep -Fq -- "$OWNER:guard:input" && return 1
    ipt -C INPUT -m comment --comment "$OWNER:jump:input" -j "$INPUT_CHAIN" || return 1
    ipt -C "$INPUT_CHAIN" -i lo -p tcp -d "$BIND_IP" --dport "$NODE_PORT" -m comment --comment "$OWNER:input:loopback" -j ACCEPT || return 1
    if [[ -n "$WEB_INTERFACE" ]]; then
        ipt -C "$INPUT_CHAIN" -i "$WEB_INTERFACE" -p tcp -d "$BIND_IP" --dport "$NODE_PORT" -m comment --comment "$OWNER:input:web-bridge" -j ACCEPT || return 1
    fi
    ipt -C "$INPUT_CHAIN" -p tcp -s "$ALLOW_FROM" -d "$BIND_IP" --dport "$NODE_PORT" -m conntrack --ctstate NEW,ESTABLISHED -m comment --comment "$OWNER:input:web" -j ACCEPT || return 1
    ipt -C "$INPUT_CHAIN" -p tcp -d "$BIND_IP" --dport "$NODE_PORT" -m comment --comment "$OWNER:input:node-drop" -j DROP || return 1
    return 0
}

check_docker_rules() {
    ipt -S DOCKER-USER 2>/dev/null | grep -Fq -- "$OWNER:guard:docker" && return 1
    ipt -C DOCKER-USER -m comment --comment "$OWNER:jump:docker" -j "$DOCKER_CHAIN" || return 1
    ipt -C "$DOCKER_CHAIN" -p tcp -s "$ALLOW_FROM" -m conntrack --ctstate NEW,ESTABLISHED --ctdir ORIGINAL --ctorigdst "$BIND_IP" --ctorigdstport "$DCGM_PORT" -m comment --comment "$OWNER:docker:web" -j ACCEPT || return 1
    ipt -C "$DOCKER_CHAIN" -p tcp -m conntrack --ctdir ORIGINAL --ctorigdst "$BIND_IP" --ctorigdstport "$DCGM_PORT" -m comment --comment "$OWNER:docker:dcgm-drop" -j DROP || return 1
    return 0
}

remove_owned_chain() {
    local parent="$1" chain="$2" family="$3" jump="$4" line
    chain_exists "$chain" || return 0
    chain_owned "$chain" "$family" || die "refusing to remove unowned chain: $chain"
    check_parent_references "$parent" "$chain" "$jump"
    remove_owned_jumps "$parent" "$chain" "$jump"
    while IFS= read -r line; do
        [[ "$line" == *"-j $chain"* ]] && die "chain $chain still has an external reference: $line"
    done < <(iptables-save -t filter)
    ipt -F "$chain"
    ipt -X "$chain"
}

preflight_rollback() {
    if chain_exists "$INPUT_CHAIN" && ! chain_owned "$INPUT_CHAIN" input; then die "refusing unowned chain $INPUT_CHAIN"; fi
    if chain_exists "$DOCKER_CHAIN" && ! chain_owned "$DOCKER_CHAIN" docker; then die "refusing unowned chain $DOCKER_CHAIN"; fi
    check_parent_references INPUT "$INPUT_CHAIN" "$OWNER:jump:input"
    if ipt -S DOCKER-USER >/dev/null 2>&1; then check_parent_references DOCKER-USER "$DOCKER_CHAIN" "$OWNER:jump:docker"; fi
    check_all_chain_references INPUT "$INPUT_CHAIN" "$OWNER:jump:input"
    if ipt -S DOCKER-USER >/dev/null 2>&1; then check_all_chain_references DOCKER-USER "$DOCKER_CHAIN" "$OWNER:jump:docker"; fi
    check_guard_config INPUT "$OWNER:guard:input" "-p tcp -d $BIND_IP --dport $NODE_PORT"
    check_guard_config DOCKER-USER "$OWNER:guard:docker" "-p tcp -m conntrack --ctdir ORIGINAL --ctorigdst $BIND_IP --ctorigdstport $DCGM_PORT"
}

service_content() {
    printf '%s' "# $OWNER
[Unit]
Description=Rootfiles exporter firewall guard
After=network-online.target ufw.service docker.service
Wants=network-online.target docker.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=$HELPER apply

[Install]
WantedBy=multi-user.target
"
}

docker_content() {
    printf '%s' "# $OWNER
[Unit]
After=ufw.service

[Service]
ExecStartPost=$HELPER apply-docker
"
}

ufw_content() {
    printf '%s' "# $OWNER
[Service]
ExecStartPost=$HELPER apply-host
"
}

# Exporters start only while their rules are in place, but do not stop or
# fail when the guard unit or Docker stops: the rules outlive both.
exporter_content() {
    local verify="${1:-}"
    printf '%s' "# $OWNER
[Unit]
After=$SERVICE
"
    [[ -z "$verify" ]] || printf '%s' "
[Service]
ExecStartPre=+$HELPER $verify
"
}

managed_files() {
    printf '%s\n' "$SERVICE_FILE service_content" "$DOCKER_DROPIN docker_content" \
        "$NODE_DROPIN exporter_content verify-host" "$DCGM_DROPIN exporter_content verify-docker" \
        "$HUB_DROPIN exporter_content"
    if ufw_unit_loaded; then printf '%s\n' "$UFW_DROPIN ufw_content"; fi
}

install_systemd() {
    preflight_managed_files
    local path fn arg
    while read -r path fn arg; do
        ensure_trusted_dir "$(dirname "$path")"
        write_owned_file "$path" "$($fn $arg)"$'\n'
    done < <(managed_files)
    systemctl daemon-reload
}

check_managed_files() {
    local path fn arg
    while read -r path fn arg; do
        [[ -f "$path" && "$(cat -- "$path")" == "$($fn $arg)" ]] || return 1
    done < <(managed_files)
}

legacy_warning() {
    local name
    command -v docker >/dev/null || return 0
    while IFS= read -r name; do
        [[ -n "$name" && "$name" != "rootfiles-dcgm-exporter" ]] &&
            warn "container '$name' already publishes $DCGM_PORT; it is untouched but gets this source restriction on BIND_IP:$DCGM_PORT"
    done < <(docker ps --filter "publish=$DCGM_PORT" --format '{{.Names}}' 2>/dev/null || true)
    return 0
}

setup_firewall() {
    validate_config
    preflight_chains
    require_helper_installed
    preflight_managed_files
    legacy_warning
    install_systemd
    systemctl enable "$SERVICE" >/dev/null
    apply_all
    note "installed and applied INPUT $BIND_IP:$NODE_PORT and DOCKER-USER original destination $BIND_IP:$DCGM_PORT"
}

apply_all() {
    validate_config
    require_helper_installed
    preflight_chains
    ensure_input_guard
    ensure_docker_guard
    KEEP_GUARDS=1
    apply_host_rules
    apply_docker_rules
    KEEP_GUARDS=0
    remove_input_guard
    remove_docker_guard
    legacy_warning
    note "rules reapplied"
}

check_mode() {
    validate_config
    if check_rules; then
        systemctl is-enabled --quiet "$SERVICE" || die "$SERVICE is not enabled"
        check_managed_files || die "guard unit or drop-ins are missing or outdated; run setup"
        note "PASS: INPUT limits node exporter and DOCKER-USER limits only the $BIND_IP:$DCGM_PORT published destination"
        legacy_warning
    else
        die "required owner-marked firewall rules are missing or drifted"
    fi
}

rollback_mode() {
    require_root
    validate_config
    preflight_managed_files
    preflight_rollback
    systemctl disable --now "$SERVICE" >/dev/null 2>&1 || true
    remove_owned_file "$DOCKER_DROPIN"
    remove_owned_file "$UFW_DROPIN"
    remove_owned_file "$NODE_DROPIN"
    remove_owned_file "$DCGM_DROPIN"
    remove_owned_file "$HUB_DROPIN"
    remove_owned_file "$SERVICE_FILE"
    systemctl daemon-reload
    remove_owned_chain INPUT "$INPUT_CHAIN" input "$OWNER:jump:input"
    if ipt -S DOCKER-USER >/dev/null 2>&1; then
        remove_owned_chain DOCKER-USER "$DOCKER_CHAIN" docker "$OWNER:jump:docker"
    fi
    remove_input_guard
    if ipt -S DOCKER-USER >/dev/null 2>&1; then remove_docker_guard; fi
    note "removed only this template's marked hooks, jumps, and chains; config/helper retained"
}

main() {
    require_root
    case "$ACTION" in
        plan)
            validate_config
            note "would allow WEB CIDR $ALLOW_FROM to host INPUT $BIND_IP:$NODE_PORT"
            [[ -n "$WEB_INTERFACE" ]] && note "would allow local scrape traffic on interface $WEB_INTERFACE to INPUT $BIND_IP:$NODE_PORT"
            [[ -n "$WEB_INTERFACE" ]] || warn "WEB_INTERFACE is unset; same-host Prometheus scraping is not admitted by this template"
            note "would restrict only forwarded original destination $BIND_IP:$DCGM_PORT in DOCKER-USER"
            note "host-originated and container-local Docker checks are outside the DOCKER-USER rule"
            legacy_warning
            ;;
        setup)
            setup_firewall
            ;;
        apply)
            apply_all
            ;;
        apply-host)
            validate_config
            apply_host_rules
            ;;
        apply-docker)
            validate_config
            apply_docker_rules
            ;;
        check)
            check_mode
            ;;
        verify-host)
            validate_config
            check_host_rules || die "INPUT rules for $BIND_IP:$NODE_PORT are missing; refusing to start the exporter"
            ;;
        verify-docker)
            validate_config
            check_docker_rules || die "DOCKER-USER rules for $BIND_IP:$DCGM_PORT are missing; refusing to start the exporter"
            ;;
        rollback)
            rollback_mode
            ;;
        *)
            echo "Usage: $0 {plan|setup|apply|apply-host|apply-docker|check|verify-host|verify-docker|rollback}" >&2
            exit 2
            ;;
    esac
}

main "$@"
