package tenant

import (
	"fmt"
	"path/filepath"
	"strings"
)

// InitScript is the image-agnostic boot contract, bind-mounted read-only at
// /etc/tenant-system/init.sh and run as the container entrypoint. "users"
// re-runs only the account step in a running container.
const InitScript = `#!/bin/sh
# Managed by rootfiles: tenant boot contract. Do not edit.
set -u
SYS=` + SystemMount + `
BOOTED=/var/lib/rootfiles-tenant-booted
log() { echo "tenant-init: $*"; }

sync_users() {
  [ -f "$SYS/users" ] || return 0
  while IFS=: read -r name uid gid sudo; do
    [ -n "$name" ] || continue
    if ! getent group "$gid" >/dev/null 2>&1; then
      groupadd -g "$gid" "$name" 2>/dev/null || groupadd -g "$gid" "g$gid" || log "groupadd $gid failed"
    fi
    if ! id -u "$name" >/dev/null 2>&1; then
      shell=/bin/sh; [ -x /bin/bash ] && shell=/bin/bash
      # -o: images such as ubuntu:24.04 already hold uid 1000 for their own user.
      useradd -M -d "/home/$name" -s "$shell" -u "$uid" -g "$gid" "$name" 2>/dev/null ||
        useradd -o -M -d "/home/$name" -s "$shell" -u "$uid" -g "$gid" "$name" || log "useradd $name failed"
    fi
    if [ ! -d "/home/$name" ]; then
      mkdir -p "/home/$name" && cp -a /etc/skel/. "/home/$name/" 2>/dev/null
      chown -R "$uid:$gid" "/home/$name"; chmod 750 "/home/$name"
    fi
    for g in sudo wheel; do
      getent group "$g" >/dev/null 2>&1 || continue
      if [ "$sudo" = 1 ]; then usermod -aG "$g" "$name"; else gpasswd -d "$name" "$g" >/dev/null 2>&1; fi
    done
  done < "$SYS/users"
  # On a container's first boot the stored hashes replace whatever the
  # image ships; later they only seed accounts without a usable password,
  # so a password changed in this container survives a restart.
  [ -f "$SYS/shadow" ] || return 0
  while IFS=: read -r name hash lastchg; do
    [ -n "$name" ] && [ -n "$hash" ] || continue
    cur=$(getent shadow "$name" | cut -d: -f2) || continue
    [ -e "$BOOTED" ] && case "$cur" in ''|'!'*|'*'*) ;; *) continue ;; esac
    printf '%s:%s\n' "$name" "$hash" | chpasswd -e || { log "restoring password of $name failed"; continue; }
    [ -n "$lastchg" ] && chage -d "$lastchg" "$name"
  done < "$SYS/shadow"
}

if [ "${1:-boot}" = users ]; then
  sync_users
  exit 0
fi

sync_users
mkdir -p /var/lib && : > "$BOOTED"
if ls "$SYS"/ssh/ssh_host_*_key >/dev/null 2>&1; then
  rm -f /etc/ssh/ssh_host_*
  cp "$SYS"/ssh/ssh_host_* /etc/ssh/
  chmod 600 /etc/ssh/ssh_host_*_key
  chmod 644 /etc/ssh/ssh_host_*_key.pub
fi
if [ -x /usr/sbin/sshd ]; then
  mkdir -p /run/sshd && chmod 755 /run/sshd
  # With sshpiper routing, sshpiperd checks client keys itself and then logs
  # in with the host's upstream key, which sshd trusts for every account.
  if [ -f "$SYS/piper.pub" ]; then
    install -m 0644 "$SYS/piper.pub" /etc/ssh/rootfiles_piper_authorized_keys
    /usr/sbin/sshd -o "AuthorizedKeysFile=.ssh/authorized_keys .ssh/authorized_keys2 /etc/ssh/rootfiles_piper_authorized_keys" || log "sshd failed to start"
  else
    /usr/sbin/sshd || log "sshd failed to start"
  fi
else
  log "openssh-server is not installed in this image"
fi
first=$(head -n 1 "$SYS/users" 2>/dev/null | cut -d: -f1)
if [ -n "$first" ] && [ -f /data/.tenant/autostart.sh ]; then
  su -s /bin/sh "$first" -c 'sh /data/.tenant/autostart.sh' >>/tmp/tenant-autostart.log 2>&1 &
fi
trap 'exit 0' TERM INT
while :; do sleep 3600 & wait $!; done
`

// usersFile renders name:uid:gid:sudo lines for the boot contract.
func usersFile(s *Spec) []byte {
	var b strings.Builder
	for _, u := range s.Users {
		sudo := 0
		if u.Sudo {
			sudo = 1
		}
		fmt.Fprintf(&b, "%s:%d:%d:%d\n", u.Name, u.UID, u.GID, sudo)
	}
	return []byte(b.String())
}

// UnitName escapes a mount point the way systemd-escape --path does.
func UnitName(where string) string {
	p := strings.Trim(filepath.Clean(where), "/")
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '/':
			b.WriteByte('-')
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '.' && i > 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, `\x%02x`, c)
		}
	}
	return b.String() + ".mount"
}

// MountUnit mounts the tenant volume image before docker starts, so a
// container never starts on the bare mount point.
func MountUnit(name string, p Paths, fs string) string {
	return `# Managed by rootfiles: tenant ` + name + ` volume
[Unit]
Description=rootfiles tenant ` + name + ` volume
RequiresMountsFor=` + p.Dir + `
Before=docker.service

[Mount]
What=` + p.Image + `
Where=` + p.Mount + `
Type=` + fs + `
Options=loop,discard,nodev,nosuid

[Install]
WantedBy=multi-user.target docker.service
`
}
