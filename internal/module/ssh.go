package module

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	if config.IsRocky(rc.Config.System) {
		_, _, _, includeChanged, err := rockySSHDMainState(rc)
		if err != nil {
			return nil, err
		}
		if includeChanged {
			desc := "Enable Rocky sshd_config.d managed drop-ins"
			if err := m.lockoutGuard(rc); err != nil {
				desc += fmt.Sprintf(" (blocked: %v)", err)
			}
			changes = append(changes, Change{Description: desc, Command: "add Include " + sshdConfigDir + "/*.conf to " + sshdConfigPath})
		}
		if cfg.Port > 0 {
			allowed, err := rockyFirewallAllowsSSH(ctx, rc, cfg.Port)
			if err != nil {
				return nil, err
			}
			if !allowed {
				return nil, fmt.Errorf("firewall policy does not allow SSH port %d; add the port through the host firewall change process before changing sshd", cfg.Port)
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

	desired := m.buildConfig(cfg)
	existing, _ := rc.Runner.ReadFile(sshdDropInPath)

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
	var originalMain, updatedMain []byte
	var originalMainMode os.FileMode
	mainIncludeChanged := false
	if config.IsRocky(rc.Config.System) {
		var err error
		originalMain, updatedMain, originalMainMode, mainIncludeChanged, err = rockySSHDMainState(rc)
		if err != nil {
			return nil, err
		}
		if cfg.Port > 0 {
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

	existing, readErr := rc.Runner.ReadFile(sshdDropInPath)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return nil, fmt.Errorf("reading sshd drop-in %s: %w", sshdDropInPath, readErr)
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
	if !configChanged {
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

	// Admit a new SSH port through an active firewall before sshd moves to it.
	if cfg.Port > 0 && !config.IsRocky(rc.Config.System) {
		if st, ok := queryUFW(ctx, rc); ok && st.Active && !st.Allowed[cfg.Port] {
			if _, err := rc.Runner.Run(ctx, "ufw", "allow", strconv.Itoa(cfg.Port)+"/tcp"); err != nil {
				return nil, fmt.Errorf("allowing ssh port %d in ufw: %w", cfg.Port, err)
			}
			messages = append(messages, fmt.Sprintf("ufw: allowed ssh port %d", cfg.Port))
		}
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
