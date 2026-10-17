package config

import (
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	validAccountName = regexp.MustCompile(`^[a-z_][a-z0-9_.-]*$`)
	validHostname    = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)
	validSize        = regexp.MustCompile(`^[1-9][0-9]*[KMGT]$`)
	validSysctlKey   = regexp.MustCompile(`^[a-z0-9_]+(\.[a-zA-Z0-9_*-]+)+$`)
)

// Validate reports semantic errors that YAML decoding cannot catch. All
// problems are returned together so a config can be fixed in one pass.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	c.validateMonitoring(add)

	checkPort := func(field string, p int) {
		if p < 0 || p > 65535 {
			add("%s: port %d out of range", field, p)
		}
	}
	checkAbs := func(field, p string) {
		if p != "" && !filepath.IsAbs(p) {
			add("%s: %q must be an absolute path", field, p)
		}
	}

	disk := c.Doctor.Disk
	if _, err := ParseSize(disk.MinFree); err != nil {
		add("doctor.disk.min_free: %v", err)
	}
	// Written as a positive range test so NaN and infinities fail it too.
	if warn, fail, _ := disk.Thresholds(); !(warn > 0 && warn <= fail && fail <= 100) {
		add("doctor.disk: want 0 < warn_percent (%g) <= fail_percent (%g) <= 100", warn, fail)
	}

	checkPort("ssh.port", c.SSH.Port)
	if c.SSH.MaxAuthTries < 0 || c.SSH.MaxAuthTries > 100 {
		add("ssh.max_auth_tries: %d out of range", c.SSH.MaxAuthTries)
	}
	seenPwUser := map[string]bool{}
	for _, u := range c.SSH.PasswordAuthUsers {
		switch {
		case u == "root":
			add("ssh.password_auth_users: root is governed by disable_root_login, not listed here")
		case !validAccountName.MatchString(u):
			add("ssh.password_auth_users: %q is not a valid account name", u)
		case seenPwUser[u]:
			add("ssh.password_auth_users: %q listed twice", u)
		}
		seenPwUser[u] = true
	}

	sys := c.Modules.System
	if sys.Hostname != "" && !validHostname.MatchString(sys.Hostname) {
		add("modules.system.hostname: %q is not a valid hostname", sys.Hostname)
	}
	if sys.SwapSize != "" && !validSize.MatchString(sys.SwapSize) {
		add("modules.system.swap_size: %q (want e.g. 8G, 512M)", sys.SwapSize)
	}
	if sys.JournaldMaxUse != "" && !validSize.MatchString(sys.JournaldMaxUse) {
		add("modules.system.journald_max_use: %q (want e.g. 2G)", sys.JournaldMaxUse)
	}
	for k, v := range sys.Sysctl {
		if !validSysctlKey.MatchString(k) || strings.ContainsAny(v, "\n") {
			add("modules.system.sysctl: invalid entry %q = %q", k, v)
		}
	}
	if m := sys.AptMirror; m != "" && !(strings.HasPrefix(m, "http://") || strings.HasPrefix(m, "https://")) || strings.ContainsAny(sys.AptMirror, " \n") {
		add("modules.system.apt_mirror: %q must be an http(s) URL", sys.AptMirror)
	}
	for _, p := range c.Modules.Network.AllowedPorts {
		if p <= 0 {
			add("modules.network.allowed_ports: port %d out of range", p)
		}
		checkPort("modules.network.allowed_ports", p)
	}

	checkAbs("users.home_base", c.Users.HomeBase)
	checkAbs("users.default_shell", c.Users.DefaultShell)
	checkAbs("modules.docker.storage_dir", c.Modules.Docker.StorageDir)
	checkAbs("modules.storage.data_dir", c.Modules.Storage.DataDir)
	for link, target := range c.Modules.Storage.Symlinks {
		checkAbs("modules.storage.symlinks key", link)
		checkAbs("modules.storage.symlinks["+link+"]", target)
		if filepath.Clean(link) == "/" {
			add("modules.storage.symlinks: refusing to replace /")
		}
	}

	seen := map[string]bool{}
	for _, a := range c.Users.Accounts {
		if !validAccountName.MatchString(a.Name) || len(a.Name) > 32 {
			add("users.accounts: invalid name %q", a.Name)
		}
		if seen[a.Name] {
			add("users.accounts: duplicate name %q", a.Name)
		}
		seen[a.Name] = true
		for _, k := range a.SSHPubkeys {
			if f := strings.Fields(k); len(f) < 2 || strings.ContainsAny(k, "\n") {
				add("users.accounts[%s].ssh_pubkeys: %q is not an OpenSSH public key line", a.Name, k)
			}
		}
	}

	switch c.Modules.Nvidia.GPUAllocation.Method {
	case "", "env", "cgroup", "both":
	default:
		add("modules.nvidia.gpu_allocation.method: %q (want env, cgroup or both)", c.Modules.Nvidia.GPUAllocation.Method)
	}

	if pn := c.Modules.Cloudflared.PrivateNetwork; pn.Address != "" {
		if _, err := netip.ParsePrefix(pn.Address); err != nil {
			add("modules.cloudflared.private_network.address: %q is not a CIDR (e.g. 172.16.229.32/32)", pn.Address)
		}
	}
	checkAbs("modules.cloudflared.tunnel_token_file", c.Modules.Cloudflared.TunnelTokenFile)
	if iface := c.Modules.Cloudflared.PrivateNetwork.Interface; len(iface) > 15 || strings.ContainsAny(iface, " /") {
		add("modules.cloudflared.private_network.interface: %q is not a valid interface name", iface)
	}

	if strings.ContainsAny(c.Timezone, " ") || strings.HasPrefix(c.Timezone, "/") || strings.Contains(c.Timezone, "..") {
		add("timezone: %q is not a zone name (e.g. Asia/Seoul)", c.Timezone)
	}

	if len(errs) > 0 {
		return fmt.Errorf("invalid configuration: %w", errors.Join(errs...))
	}
	return nil
}
