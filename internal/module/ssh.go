package module

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

type SSHModule struct{}

func NewSSHModule() *SSHModule    { return &SSHModule{} }
func (m *SSHModule) Name() string { return "ssh" }

// sshdDropInPath is overridable in tests.
var sshdDropInPath = "/etc/ssh/sshd_config.d/00-rootfiles.conf"
var sshdPrivilegeSeparationDir = "/run/sshd"

func (m *SSHModule) Check(ctx context.Context, rc *RunContext) (*CheckResult, error) {
	var changes []Change
	cfg := rc.Config.SSH
	existing, readErr := rc.Runner.ReadFile(sshdDropInPath)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return nil, fmt.Errorf("reading sshd drop-in %s: %w; repair its permissions before retrying", sshdDropInPath, readErr)
	}

	includeChanged := false
	if config.IsRocky(rc.Config.System) {
		_, _, _, changed, err := rockySSHDMainState(rc)
		if err != nil {
			return nil, err
		}
		includeChanged = changed
		if includeChanged {
			desc := "Enable Rocky sshd_config.d managed drop-ins"
			if err := m.lockoutGuard(rc); err != nil {
				desc += fmt.Sprintf(" (blocked: %v)", err)
			}
			changes = append(changes, Change{Description: desc, Command: "add Include " + sshdConfigDir + "/*.conf to " + sshdConfigPath})
		}
	}
	activeDropIn := existing
	if includeChanged {
		activeDropIn = nil
	}
	portChanges, err := managedSSHPortChanges(ctx, rc, activeDropIn, cfg.Port)
	if err != nil {
		return nil, err
	}
	if config.IsRocky(rc.Config.System) {
		if cfg.Port > 0 {
			if portChanges {
				allowed, err := rockyFirewallAllowsSSH(ctx, rc, cfg.Port)
				if err != nil {
					return blockedFirewallCheck(err), nil
				}
				if !allowed {
					return blockedFirewallCheck(fmt.Errorf("firewall policy does not allow SSH port %d; add the port through the host firewall change process before changing sshd", cfg.Port)), nil
				}
			}
			labeled, err := rockySSHSELinuxPortLabeled(ctx, rc, cfg.Port)
			if err != nil {
				return nil, err
			}
			if !labeled {
				changes = append(changes, Change{Description: fmt.Sprintf("Label TCP port %d for sshd in SELinux", cfg.Port), Command: fmt.Sprintf("semanage port -a -t ssh_port_t -p tcp %d", cfg.Port)})
			}
		}
	}

	if !config.IsRocky(rc.Config.System) && portChanges {
		needsAllow, err := sshFirewallPlan(ctx, rc, cfg.Port)
		if err != nil {
			return blockedFirewallCheck(err), nil
		}
		if needsAllow {
			changes = append(changes, Change{Description: fmt.Sprintf("Allow SSH port %d in UFW", cfg.Port), Command: fmt.Sprintf("ufw allow %d/tcp", cfg.Port)})
		}
	}

	desired := m.buildConfig(cfg)

	if strings.TrimSpace(string(existing)) != strings.TrimSpace(desired) {
		desc := "Deploy custom sshd configuration"
		if err := m.lockoutGuard(rc); err != nil {
			desc += fmt.Sprintf(" (blocked: %v)", err)
		}
		changes = append(changes, Change{
			Description: desc,
			Command:     "write " + sshdDropInPath,
		})
	}

	return &CheckResult{
		Satisfied: len(changes) == 0,
		Changes:   changes,
	}, nil
}

func (m *SSHModule) Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	cfg := rc.Config.SSH
	existing, readErr := rc.Runner.ReadFile(sshdDropInPath)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return nil, fmt.Errorf("reading sshd drop-in %s: %w; repair its permissions before retrying", sshdDropInPath, readErr)
	}

	needsUFWAllow := false

	var originalMain, updatedMain []byte
	var originalMainMode os.FileMode
	mainIncludeChanged := false
	if config.IsRocky(rc.Config.System) {
		var err error
		originalMain, updatedMain, originalMainMode, mainIncludeChanged, err = rockySSHDMainState(rc)
		if err != nil {
			return nil, err
		}
	}
	activeDropIn := existing
	if mainIncludeChanged {
		activeDropIn = nil
	}
	portChanges, err := managedSSHPortChanges(ctx, rc, activeDropIn, cfg.Port)
	if err != nil {
		return nil, err
	}
	if config.IsRocky(rc.Config.System) {
		if portChanges {
			allowed, err := rockyFirewallAllowsSSH(ctx, rc, cfg.Port)
			if err != nil {
				return nil, err
			}
			if !allowed {
				return nil, fmt.Errorf("firewalld is active and does not allow SSH port %d; add the port through the host firewall change process before changing sshd", cfg.Port)
			}
		}
	}
	content := m.buildConfig(cfg)

	if !config.IsRocky(rc.Config.System) && portChanges {
		var err error
		needsUFWAllow, err = sshFirewallPlan(ctx, rc, cfg.Port)
		if err != nil {
			return nil, err
		}
	}
	configChanged := readErr != nil || strings.TrimSpace(string(existing)) != strings.TrimSpace(content) || mainIncludeChanged
	if configChanged {
		if err := m.lockoutGuard(rc); err != nil {
			if !rc.Force {
				return nil, fmt.Errorf("%w (re-run with --force to override)", err)
			}
			fmt.Printf("  ⚠ ssh: %v — continuing because of --force\n", err)
		}
	}

	var messages []string
	rockyPortAdded := false
	if config.IsRocky(rc.Config.System) && cfg.Port > 0 {
		added, err := ensureRockySSHSELinuxPort(ctx, rc, cfg.Port)
		if err != nil {
			return nil, err
		}
		rockyPortAdded = added
		if added {
			messages = append(messages, fmt.Sprintf("SELinux TCP port %d labeled for sshd", cfg.Port))
		}
	}
	rollbackRockyPort := func() error {
		if rockyPortAdded {
			if _, err := rc.Runner.Run(ctx, "semanage", "port", "-d", "-p", "tcp", strconv.Itoa(cfg.Port)); err != nil {
				return fmt.Errorf("removing temporary SELinux SSH port label %d: %w", cfg.Port, err)
			}
			rockyPortAdded = false
		}
		return nil
	}
	if !configChanged && !needsUFWAllow {
		return &ApplyResult{Changed: rockyPortAdded, Messages: messages}, nil
	}

	var oldDropMode os.FileMode
	if readErr == nil {
		info, err := os.Lstat(sshdDropInPath)
		if err != nil {
			cause := fmt.Errorf("inspecting existing sshd drop-in: %w", err)
			if rollbackErr := rollbackRockyPort(); rollbackErr != nil {
				cause = fmt.Errorf("%w; SSH port rollback failed: %v", cause, rollbackErr)
			}
			return nil, cause
		}
		if !info.Mode().IsRegular() {
			cause := fmt.Errorf("existing sshd drop-in %s is not a regular file; refusing to overwrite it", sshdDropInPath)
			if rollbackErr := rollbackRockyPort(); rollbackErr != nil {
				cause = fmt.Errorf("%w; SSH port rollback failed: %v", cause, rollbackErr)
			}
			return nil, cause
		}
		oldDropMode = info.Mode().Perm()
	}
	mainWriteAttempted := false
	dropWriteAttempted := false
	rollbackConfigs := func() error {
		var rollbackErrs []error
		if mainWriteAttempted {
			if err := rc.Runner.WriteFile(sshdConfigPath, originalMain, originalMainMode); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("restoring %s: %w", sshdConfigPath, err))
			}
			mainWriteAttempted = false
		}
		if dropWriteAttempted {
			if readErr == nil {
				if err := rc.Runner.WriteFile(sshdDropInPath, existing, oldDropMode); err != nil {
					rollbackErrs = append(rollbackErrs, fmt.Errorf("restoring %s: %w", sshdDropInPath, err))
				}
			} else if err := rc.Runner.Remove(sshdDropInPath); err != nil && !os.IsNotExist(err) {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("removing %s: %w", sshdDropInPath, err))
			}
			dropWriteAttempted = false
		}
		return errors.Join(rollbackErrs...)
	}
	rollbackAfterFailure := func(cause error) error {
		rollbackErr := errors.Join(rollbackConfigs(), rollbackRockyPort())
		if rollbackErr != nil {
			return fmt.Errorf("%w; SSH configuration rollback failed: %v", cause, rollbackErr)
		}
		return cause
	}

	if mainIncludeChanged {
		mainWriteAttempted = true
		if err := rc.Runner.WriteFile(sshdConfigPath, updatedMain, originalMainMode); err != nil {
			return nil, fmt.Errorf("adding managed sshd drop-in include: %w", rollbackAfterFailure(err))
		}
	}

	// Admit the planned UFW port before sshd writes. Unknown state never reaches here.
	if needsUFWAllow {
		if _, err := rc.Runner.Run(ctx, "ufw", "allow", strconv.Itoa(cfg.Port)+"/tcp"); err != nil {
			return nil, fmt.Errorf("allowing SSH port %d in UFW: %w; inspect sudo ufw status before retrying", cfg.Port, err)
		}
		if !rc.DryRun {
			pending, err := sshFirewallPlan(ctx, rc, cfg.Port)
			if err != nil {
				return nil, err
			}
			if pending {
				return nil, fmt.Errorf("UFW did not confirm the unrestricted TCP allowance for SSH port %d; verify sudo ufw status or restore ssh.port before retrying", cfg.Port)
			}
		}
		messages = append(messages, fmt.Sprintf("ufw: allowed ssh port %d", cfg.Port))
	}

	if configChanged && (readErr != nil || strings.TrimSpace(string(existing)) != strings.TrimSpace(content)) {
		if err := rc.Runner.MkdirAll(sshdConfigDir, 0755); err != nil {
			return nil, fmt.Errorf("creating %s: %w", sshdConfigDir, rollbackAfterFailure(err))
		}
		dropWriteAttempted = true
		if err := rc.Runner.WriteFile(sshdDropInPath, []byte(content), 0644); err != nil {
			return nil, fmt.Errorf("writing sshd config: %w", rollbackAfterFailure(err))
		}
	}

	// Validate the full merged configuration; restore the previous drop-in
	// on failure so a reload/reboot never picks up a broken sshd config.
	if err := m.validate(ctx, rc); err != nil {
		return nil, fmt.Errorf("sshd config validation failed, change reverted: %w", rollbackAfterFailure(err))
	}
	messages = append(messages, "sshd configuration deployed")

	portChanged := mainIncludeChanged || portLine(string(existing)) != portLine(content)
	var warnings []string
	if msg, err := m.reload(ctx, rc, portChanged); err != nil {
		warnings = append(warnings, fmt.Sprintf("%v (restart sshd manually)", firstLine(err.Error())))
	} else {
		messages = append(messages, msg)
	}

	return &ApplyResult{Changed: true, Messages: messages, Warnings: warnings}, nil
}

// lockoutGuard refuses a configuration that would leave no way to log in
// over SSH: password auth disabled while no account (root only if root
// login stays permitted) has an authorized key.
func (m *SSHModule) lockoutGuard(rc *RunContext) error {
	cfg := rc.Config.SSH
	if !cfg.DisablePasswordAuth {
		return nil
	}
	if len(keyLoginAccounts(!cfg.DisableRootLogin)) == 0 && len(declaredKeyAccounts(rc)) == 0 {
		msg := "disabling password auth, but no account has an SSH authorized key"
		if cfg.DisableRootLogin {
			msg = "disabling password auth and root login, but no non-root account has an SSH authorized key"
		}
		if names := keyedAccounts(rc); len(names) > 0 {
			msg += fmt.Sprintf("; declared accounts %s do not count because the users module is not in this run, failed, or refuses the home base", strings.Join(names, ", "))
		}
		return errors.New(msg)
	}
	if stranded, known := strandedPasswordAccounts(rc); len(stranded) > 0 {
		if !known {
			return fmt.Errorf("cannot read the shadow file (run as root); accounts without an authorized key may lose access: %s",
				strings.Join(stranded, ", "))
		}
		return fmt.Errorf("disabling password auth would lock out password-only accounts: %s (add their keys or list them in ssh.password_auth_users)",
			strings.Join(stranded, ", "))
	}
	return nil
}

// strandedPasswordAccounts lists password-only accounts that neither get a
// declared key nor stay on the password_auth_users exception list. known is
// false when the shadow file was unreadable (see passwordOnlyAccounts).
func strandedPasswordAccounts(rc *RunContext) ([]string, bool) {
	keep := map[string]bool{}
	for _, n := range declaredKeyAccounts(rc) {
		keep[n] = true
	}
	for _, n := range rc.Config.SSH.PasswordAuthUsers {
		keep[n] = true
	}
	pwOnly, known := passwordOnlyAccounts()
	var out []string
	for _, n := range pwOnly {
		if !keep[n] {
			out = append(out, n)
		}
	}
	return out, known
}

// declaredKeyAccounts lists users.accounts entries with SSH keys. The
// users module (which runs before ssh) creates them, so they count as
// key-based logins even when not yet present (e.g. in check/dry-run), but
// only while that module is part of the run, has not failed, and does not
// refuse the home base.
func declaredKeyAccounts(rc *RunContext) []string {
	if !rc.moduleSucceeding("users") || checkHomeBase(rc.Config.Users.HomeBase) != nil {
		return nil
	}
	return keyedAccounts(rc)
}

// keyedAccounts lists the users.accounts entries that have SSH keys.
func keyedAccounts(rc *RunContext) []string {
	var names []string
	for _, a := range rc.Config.Users.Accounts {
		if len(a.SSHPubkeys) > 0 {
			names = append(names, a.Name)
		}
	}
	return names
}

// validate runs `sshd -t` when sshd is installed. sshd refuses to test
// without its privilege separation directory, so ensure it exists.
func (m *SSHModule) validate(ctx context.Context, rc *RunContext) error {
	bin := sshdBinary(rc)
	if bin == "" || rc.DryRun {
		return nil
	}
	_ = rc.Runner.MkdirAll(sshdPrivilegeSeparationDir, 0755)
	if _, err := rc.Runner.Run(ctx, bin, "-t"); err != nil {
		return err
	}
	if config.IsRocky(rc.Config.System) {
		if err := verifyRockySSHEffective(ctx, rc, bin); err != nil {
			return err
		}
	}
	return nil
}

// reload makes sshd pick up the new configuration. On socket-activated
// systems (Ubuntu 22.10+) the listening port belongs to ssh.socket, whose
// generator reads sshd_config: a port change needs daemon-reload plus a
// socket restart, a plain `reload ssh` is not enough.
func (m *SSHModule) reload(ctx context.Context, rc *RunContext, portChanged bool) (string, error) {
	if res, err := rc.Runner.Query(ctx, "systemctl", "is-active", "ssh.socket"); err == nil && strings.TrimSpace(res.Stdout) == "active" {
		msg := "ssh reloaded"
		if portChanged {
			if _, err := rc.Runner.Run(ctx, "systemctl", "daemon-reload"); err != nil {
				return "", fmt.Errorf("systemctl daemon-reload: %w", err)
			}
			if _, err := rc.Runner.Run(ctx, "systemctl", "restart", "ssh.socket"); err != nil {
				return "", fmt.Errorf("restarting ssh.socket: %w", err)
			}
			msg = "ssh.socket restarted on new port"
		}
		// The daemon spawned by the socket keeps running; reload it if up.
		if _, err := rc.Runner.Run(ctx, "systemctl", "try-reload-or-restart", "ssh.service"); err != nil {
			return "", fmt.Errorf("reloading ssh.service: %w", err)
		}
		return msg, nil
	}

	var lastErr error
	services := []string{"ssh", "sshd"}
	if config.IsRocky(rc.Config.System) {
		services = []string{"sshd", "ssh"}
	}
	for _, svc := range services {
		if _, err := rc.Runner.Run(ctx, "systemctl", "reload", svc); err == nil {
			return svc + " reloaded", nil
		} else {
			lastErr = err
		}
	}
	return "", fmt.Errorf("reloading sshd: %w", lastErr)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// managedSSHPortChanges shares current-port discovery across Ubuntu and Rocky.
// Callers pass only active managed bytes (nil when an Include is being added).
// Otherwise inspect sshd without writes, then resolve the current config.
func managedSSHPortChanges(ctx context.Context, rc *RunContext, activeDropIn []byte, requested int) (bool, error) {
	if requested <= 0 {
		return false, nil
	}
	if ports := parsePortDirectives(string(activeDropIn)); len(ports) > 0 {
		return ports[0] != requested, nil
	}
	if bin := sshdBinary(rc); bin != "" {
		result, err := rc.Runner.Query(ctx, bin, "-T", "-f", sshdConfigPath)
		if err == nil && result != nil {
			if ports := parsePortDirectives(result.Stdout); len(ports) > 0 {
				return ports[0] != requested, nil
			}
		}
	}
	// A non-root probe cannot read host keys, and socket activation may leave
	// /run/sshd absent. Resolve the config without creating that directory.
	if _, err := os.Stat(sshdConfigPath); errors.Is(err, os.ErrNotExist) {
		return requested != 22, nil
	}
	port, err := firstSSHConfigPort(rc, sshdConfigPath, filepath.Dir(sshdConfigPath), map[string]bool{})
	if err != nil {
		return false, err
	}
	if port == 0 {
		port = 22
	}
	return port != requested, nil
}

// firstSSHConfigPort follows Include directives in-place. Relative patterns
// are rooted at the SSH configuration directory, including nested Includes.
func firstSSHConfigPort(rc *RunContext, name, base string, stack map[string]bool) (int, error) {
	data, err := rc.Runner.ReadFile(name)
	if err != nil {
		return 0, fmt.Errorf("cannot read SSH configuration %s: %w; repair that Include path or its permissions before retrying", name, err)
	}
	key, err := filepath.EvalSymlinks(name)
	if err != nil {
		return 0, fmt.Errorf("cannot resolve SSH configuration %s: %w; repair that path before retrying", name, err)
	}
	if stack[key] || len(stack) >= 64 {
		return 0, fmt.Errorf("recursive or excessive SSH Include nesting at %s; remove the Include cycle or reduce nesting before retrying", name)
	}
	stack[key] = true
	defer delete(stack, key)
	for i, line := range strings.Split(string(data), "\n") {
		fields, err := sshConfigFields(line)
		if err != nil {
			return 0, fmt.Errorf("cannot parse SSH configuration %s:%d: %w; repair that directive before retrying", name, i+1, err)
		}
		if len(fields) == 0 {
			continue
		}
		switch strings.ToLower(fields[0]) {
		case "port":
			if len(fields) != 2 {
				return 0, fmt.Errorf("invalid SSH Port directive at %s:%d; set one numeric port from 1 to 65535 before retrying", name, i+1)
			}
			port, err := strconv.Atoi(fields[1])
			if err != nil || port < 1 || port > 65535 {
				return 0, fmt.Errorf("invalid SSH Port directive at %s:%d; set one numeric port from 1 to 65535 before retrying", name, i+1)
			}
			return port, nil
		case "include":
			if len(fields) < 2 {
				return 0, fmt.Errorf("SSH Include at %s:%d has no path; supply an Include path before retrying", name, i+1)
			}
			for _, pattern := range fields[1:] {
				if !filepath.IsAbs(pattern) {
					pattern = filepath.Join(base, pattern)
				}
				matches, err := expandSSHInclude(pattern)
				if err != nil {
					return 0, err
				}
				for _, match := range matches {
					port, err := firstSSHConfigPort(rc, match, base, stack)
					if err != nil || port != 0 {
						return port, err
					}
				}
			}
		}
	}
	return 0, nil
}

// Unlike filepath.Glob, expansion must surface unreadable directories rather
// than silently treating their contents as absent and guessing the default.
func expandSSHInclude(pattern string) ([]string, error) {
	absolute, err := filepath.Abs(pattern)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve SSH Include %s: %w; repair that path before retrying", pattern, err)
	}
	pattern = absolute
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(pattern), string(os.PathSeparator)), string(os.PathSeparator))
	for _, part := range parts {
		if _, err := filepath.Match(part, ""); err != nil {
			return nil, fmt.Errorf("cannot expand SSH Include %s: %w; repair that glob before retrying", pattern, err)
		}
	}
	matches := []string{string(os.PathSeparator)}
	for i, part := range parts {
		var next []string
		for _, dir := range matches {
			if !strings.ContainsAny(part, "*?[") {
				next = append(next, filepath.Join(dir, part))
				continue
			}
			entries, err := os.ReadDir(dir)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("cannot expand SSH Include %s in %s: %w; repair that directory's permissions before retrying", pattern, dir, err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".") && !strings.HasPrefix(part, ".") {
					continue
				}
				matched, _ := filepath.Match(part, entry.Name())
				if !matched {
					continue
				}
				candidate := filepath.Join(dir, entry.Name())
				if i < len(parts)-1 {
					info, err := os.Stat(candidate)
					if err != nil {
						return nil, fmt.Errorf("cannot expand SSH Include through %s: %w; repair that path before retrying", candidate, err)
					}
					if !info.IsDir() {
						continue
					}
				}
				next = append(next, candidate)
			}
		}
		matches = next
	}
	sort.Strings(matches)
	return matches, nil
}

func sshConfigFields(line string) ([]string, error) {
	var fields []string
	var word strings.Builder
	var quote rune
	escaped, started := false, false
	flush := func() {
		if started {
			fields = append(fields, word.String())
			word.Reset()
			started = false
		}
	}
	for _, char := range line {
		if escaped {
			if strings.ContainsRune("*?[", char) {
				word.WriteRune('\\')
			}
			word.WriteRune(char)
			escaped = false
			continue
		}
		if char == '\\' {
			escaped, started = true, true
			continue
		}
		if quote != 0 {
			if char == quote {
				quote = 0
			} else {
				word.WriteRune(char)
			}
			continue
		}
		switch char {
		case '=':
			if len(fields) == 0 || len(fields) == 1 && !started {
				flush()
			} else {
				word.WriteRune(char)
			}
		case '#':
			flush()
			return fields, nil
		case '\'', '"':
			quote, started = char, true
		case ' ', '\t':
			flush()
		default:
			word.WriteRune(char)
			started = true
		}
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("unterminated quote or escape")
	}
	flush()
	return fields, nil
}

// portLine returns the "Port N" directive of a drop-in, or "" if none.
func portLine(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "Port ") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func (m *SSHModule) buildConfig(cfg config.SSHConfig) string {
	var b strings.Builder
	b.WriteString("# Managed by rootfiles-v2\n")

	if cfg.DisableRootLogin {
		b.WriteString("PermitRootLogin no\n")
	}
	if cfg.DisablePasswordAuth {
		b.WriteString("PasswordAuthentication no\n")
		// PAM keyboard-interactive would otherwise still accept passwords.
		b.WriteString("KbdInteractiveAuthentication no\n")
	}
	if cfg.MaxAuthTries > 0 {
		b.WriteString(fmt.Sprintf("MaxAuthTries %d\n", cfg.MaxAuthTries))
	}
	if cfg.Port > 0 {
		b.WriteString(fmt.Sprintf("Port %d\n", cfg.Port))
	}
	// A Match block runs to the end of the file, so it must stay last.
	if cfg.DisablePasswordAuth && len(cfg.PasswordAuthUsers) > 0 {
		b.WriteString("# Password login kept for these users while keys are rolled out\n")
		b.WriteString("Match User " + strings.Join(cfg.PasswordAuthUsers, ",") + "\n")
		b.WriteString("\tPasswordAuthentication yes\n")
		b.WriteString("\tKbdInteractiveAuthentication yes\n")
	}

	return b.String()
}
