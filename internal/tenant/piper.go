package tenant

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Piper routes `ssh <tenant>.<user>@host -p <Port>` through sshpiperd.
type Piper struct {
	ConfigDir string // holds upstream_ed25519 and tenants/<t>.yaml
	Port      int
}

// UpstreamKey is the key sshpiperd logs in to tenants with after it has
// checked a client key itself.
func (p *Piper) UpstreamKey() string { return filepath.Join(p.ConfigDir, "upstream_ed25519") }

// RoutesDir holds one sshpiper yaml file per tenant.
func (p *Piper) RoutesDir() string { return filepath.Join(p.ConfigDir, "tenants") }

// RouteName is the SSH user name that reaches user u of tenant t. Tenant
// names have no '.', so names of different tenants cannot collide.
func RouteName(t, u string) string { return t + "." + u }

func (m *Manager) routeFile(t string) string {
	return filepath.Join(m.Piper.RoutesDir(), t+".yaml")
}

// writePiperKey gives the container the upstream public key (installed by
// init.sh on boot), or removes it when routing is off.
func (m *Manager) writePiperKey(s *Spec) error {
	dst := filepath.Join(m.Paths(s.Name).System, "piper.pub")
	if m.Piper == nil {
		if m.Runner.FileExists(dst) {
			return m.Runner.Remove(dst)
		}
		return nil
	}
	pub, err := os.ReadFile(m.Piper.UpstreamKey() + ".pub")
	if err != nil {
		if m.Runner.DryRun {
			return nil
		}
		return fmt.Errorf("sshpiper upstream key missing (%v); run `rootfiles apply --module tenant` with modules.tenant.sshpiper.enabled", err)
	}
	return m.writeIfChanged(dst, pub, 0o644)
}

// WriteRoutes refreshes tenant s's sshpiper routes: known hosts from the
// stored host keys, each user's authorized keys from the container, and
// the route file. With routing off it removes the route file.
func (m *Manager) WriteRoutes(ctx context.Context, s *Spec) error {
	if m.Piper == nil {
		return nil
	}
	p := m.Paths(s.Name)
	dir := filepath.Join(p.System, "piper")
	if err := m.run(ctx, "install", "-d", "-m", "0700", dir, filepath.Join(dir, "keys")); err != nil {
		return err
	}
	var known strings.Builder
	for _, kt := range []string{"ed25519", "ecdsa", "rsa"} {
		pub, err := os.ReadFile(filepath.Join(p.System, "ssh", "ssh_host_"+kt+"_key.pub"))
		if err != nil {
			continue
		}
		if f := strings.Fields(string(pub)); len(f) >= 2 {
			fmt.Fprintf(&known, "[127.0.0.1]:%d %s %s\n", s.SSHPort, f[0], f[1])
		}
	}
	knownPath := filepath.Join(dir, "known_hosts")
	if err := m.writeIfChanged(knownPath, []byte(known.String()), 0o600); err != nil {
		return err
	}
	running := m.inspect(ctx, ContainerName(s.Name)).Running
	var routes strings.Builder
	fmt.Fprintf(&routes, "# Managed by rootfiles: sshpiper routes of tenant %s\nversion: \"1.0\"\npipes:\n", s.Name)
	for _, u := range s.Users {
		keys := filepath.Join(dir, "keys", u.Name)
		if running {
			if err := m.writeIfChanged(keys, m.containerKeys(ctx, s, u.Name), 0o600); err != nil {
				return err
			}
		} else if !m.Runner.FileExists(keys) {
			if err := m.writeIfChanged(keys, nil, 0o600); err != nil {
				return err
			}
		}
		host := "127.0.0.1:" + strconv.Itoa(s.SSHPort)
		// Two pipes: a key login checked by sshpiper continues with the
		// upstream key; a password goes to the container's sshd, which
		// checks it. A password pipe must never carry a private key.
		fmt.Fprintf(&routes, `- from:
  - username: %q
    authorized_keys: %s
  to:
    host: %s
    username: %q
    private_key: %s
    known_hosts: %s
- from:
  - username: %q
  to:
    host: %s
    username: %q
    known_hosts: %s
`, RouteName(s.Name, u.Name), keys, host, u.Name, m.Piper.UpstreamKey(), knownPath,
			RouteName(s.Name, u.Name), host, u.Name, knownPath)
	}
	if err := m.run(ctx, "install", "-d", "-m", "0700", m.Piper.ConfigDir, m.Piper.RoutesDir()); err != nil {
		return err
	}
	return m.writeIfChanged(m.routeFile(s.Name), []byte(routes.String()), 0o600)
}

// Sync saves tenant s's password hashes and refreshes its sshpiper key and
// routes. It is safe to run while other rootfiles commands run: it only
// rewrites this tenant's derived files.
func (m *Manager) Sync(ctx context.Context, s *Spec) error {
	if err := m.SyncShadow(ctx, s); err != nil {
		return err
	}
	if err := m.writePiperKey(s); err != nil {
		return err
	}
	return m.WriteRoutes(ctx, s)
}

// Routes lists the ssh commands that reach tenant s through sshpiper.
func (m *Manager) Routes(s *Spec) []string {
	if m.Piper == nil {
		return nil
	}
	var out []string
	for _, u := range s.Users {
		out = append(out, fmt.Sprintf("ssh -p %d %s@HOST", m.Piper.Port, RouteName(s.Name, u.Name)))
	}
	return out
}

// RemoveRoutes deletes tenant t's route file.
func (m *Manager) RemoveRoutes(t string) error {
	if m.Piper == nil || !m.Runner.FileExists(m.routeFile(t)) {
		return nil
	}
	return m.Runner.Remove(m.routeFile(t))
}

// containerKeys reads user's authorized_keys inside the container, where
// its symlinks resolve against the container's root, bounded in size and
// time, and keeps only plain key lines that sshpiper can parse.
func (m *Manager) containerKeys(ctx context.Context, s *Spec, user string) []byte {
	res, err := m.readInContainer(ctx, ContainerName(s.Name), "/home/"+user+"/.ssh/authorized_keys")
	if err != nil {
		return nil
	}
	return cleanKeys(res.Stdout)
}

var keyType = regexp.MustCompile(`^(ssh-(rsa|ed25519|dss)|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com)$`)

// cleanKeys keeps "type base64" of lines whose blob names its own type.
// Lines with options are dropped: sshpiper would not honour them.
func cleanKeys(in string) []byte {
	var b strings.Builder
	for _, line := range strings.Split(in, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || !keyType.MatchString(f[0]) {
			continue
		}
		blob, err := base64.StdEncoding.DecodeString(f[1])
		if err != nil || len(blob) < 4 {
			continue
		}
		n := binary.BigEndian.Uint32(blob)
		if uint64(n)+4 > uint64(len(blob)) || string(blob[4:4+n]) != f[0] {
			continue
		}
		b.WriteString(f[0] + " " + f[1] + "\n")
	}
	return []byte(b.String())
}
