package module

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Remote-access helpers shared by the ssh and network modules. Both modules
// can cut off the operator's SSH session, so they consult the same view of
// "which port is sshd on" and "who can still log in with a key".

// passwdPath and shadowPath are overridable in tests.
var (
	passwdPath = "/etc/passwd"
	shadowPath = "/etc/shadow"
)

// sshPorts returns the ports sshd listens on (or will, once the ssh module
// has run): the configured port when set, otherwise the effective ports
// reported by `sshd -T`, otherwise every Port directive in the config
// files, falling back to 22. Erring towards more ports is deliberate: the
// result decides what the firewall must keep open.
func sshPorts(ctx context.Context, rc *RunContext) []int {
	if rc.Config.SSH.Port > 0 {
		return []int{rc.Config.SSH.Port}
	}
	if bin := sshdBinary(rc); bin != "" {
		// sshd -T refuses to run without its privilege separation dir,
		// which only exists while sshd runs. Creating it is harmless.
		_ = os.MkdirAll("/run/sshd", 0755)
		if res, err := rc.Runner.Query(ctx, bin, "-T"); err == nil {
			if ports := parsePortDirectives(res.Stdout); len(ports) > 0 {
				return ports
			}
		}
	}
	var all string
	for _, p := range append([]string{sshdConfigPath}, globFiles(sshdConfigDir, "*.conf")...) {
		if data, err := rc.Runner.ReadFile(p); err == nil {
			all += string(data) + "\n"
		}
	}
	if ports := parsePortDirectives(all); len(ports) > 0 {
		return ports
	}
	return []int{22}
}

// Overridable in tests.
var (
	sshdConfigPath = "/etc/ssh/sshd_config"
	sshdConfigDir  = "/etc/ssh/sshd_config.d"
)

// parsePortDirectives collects unique "Port N" values (case-insensitive).
func parsePortDirectives(text string) []int {
	seen := map[int]bool{}
	var ports []int
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.EqualFold(f[0], "port") {
			if p, err := strconv.Atoi(f[1]); err == nil && p > 0 && !seen[p] {
				seen[p] = true
				ports = append(ports, p)
			}
		}
	}
	return ports
}

func globFiles(dir, pattern string) []string {
	m, _ := filepath.Glob(filepath.Join(dir, pattern))
	return m
}

// sshdBinary locates sshd, which lives in /usr/sbin and may be off PATH.
func sshdBinary(rc *RunContext) string {
	if rc.Runner.CommandExists("sshd") {
		return "sshd"
	}
	if rc.Runner.FileExists("/usr/sbin/sshd") {
		return "/usr/sbin/sshd"
	}
	return ""
}

// loginAccount is a passwd entry that can open an interactive SSH session.
type loginAccount struct {
	name, home string
	uid        int
}

// loginAccounts lists passwd entries with a real login shell: root (only
// when includeRoot is set) and regular users (uid >= 1000, not nobody).
func loginAccounts(includeRoot bool) []loginAccount {
	f, err := os.Open(passwdPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	var out []loginAccount
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.Split(sc.Text(), ":")
		if len(parts) < 7 {
			continue
		}
		name, uidStr, home, shell := parts[0], parts[2], parts[5], parts[6]
		uid, err := strconv.Atoi(uidStr)
		if err != nil {
			continue
		}
		if uid == 0 {
			if !includeRoot {
				continue
			}
		} else if uid < 1000 || uid == 65534 {
			continue
		}
		if strings.HasSuffix(shell, "nologin") || strings.HasSuffix(shell, "/false") {
			continue
		}
		out = append(out, loginAccount{name: name, home: home, uid: uid})
	}
	return out
}

// keyLoginAccounts lists accounts that can log in over SSH with a public
// key: a real login shell and a non-empty authorized_keys. root is included
// only when includeRoot is set (i.e. root login stays permitted).
func keyLoginAccounts(includeRoot bool) []string {
	var names []string
	for _, a := range loginAccounts(includeRoot) {
		if hasAuthorizedKey(filepath.Join(a.home, ".ssh", "authorized_keys")) {
			names = append(names, a.name)
		}
	}
	return names
}

// passwordOnlyAccounts lists regular login accounts (root excluded) that have
// no authorized key but can still log in with a password, i.e. the accounts
// that disabling password authentication would lock out. known is false when
// the shadow file is unreadable (not root): every keyless account is then
// listed because its password state cannot be told.
func passwordOnlyAccounts() (names []string, known bool) {
	usable, known := shadowPasswords()
	for _, a := range loginAccounts(false) {
		if hasAuthorizedKey(filepath.Join(a.home, ".ssh", "authorized_keys")) {
			continue
		}
		if known && !usable[a.name] {
			continue
		}
		names = append(names, a.name)
	}
	return names, known
}

// shadowPasswords reports which accounts have a usable password hash:
// non-empty and not locked ("!" or "*" prefix). ok is false when the shadow
// file cannot be read.
func shadowPasswords() (usable map[string]bool, ok bool) {
	data, err := os.ReadFile(shadowPath)
	if err != nil {
		return nil, false
	}
	usable = map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 2 {
			continue
		}
		hash := parts[1]
		usable[parts[0]] = hash != "" && !strings.HasPrefix(hash, "!") && !strings.HasPrefix(hash, "*")
	}
	return usable, true
}

func hasAuthorizedKey(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			return true
		}
	}
	return false
}
