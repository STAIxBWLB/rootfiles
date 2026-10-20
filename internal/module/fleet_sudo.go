package module

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

const fleetSudoBinaryDefault = "/usr/local/bin/rootfiles"

var (
	fleetSudoBinaryPath = fleetSudoBinaryDefault
	fleetSudoLstat      = os.Lstat
	lookupFleetSudoUser = user.Lookup
)

var fleetSudoCommands = []string{
	fleetSudoBinaryDefault + " status -o json",
	fleetSudoBinaryDefault + " check -o json",
	fleetSudoBinaryDefault + " doctor -o json",
}

func fleetSudoersPath() string {
	return filepath.Join(sudoersDir, "rootfiles-fleet")
}

func fleetSudoersContent(users []string) ([]byte, error) {
	if len(users) == 0 {
		return nil, nil
	}
	names := append([]string(nil), users...)
	sort.Strings(names)
	for i, name := range names {
		if err := checkUsername(name); err != nil {
			return nil, fmt.Errorf("fleet_sudo_users: %w", err)
		}
		if i > 0 && names[i-1] == name {
			return nil, fmt.Errorf("fleet_sudo_users: duplicate user %q", name)
		}
	}

	var b strings.Builder
	b.WriteString("# Managed by rootfiles-v2\n")
	b.WriteString("Cmnd_Alias ROOTFILES_FLEET_READONLY = ")
	b.WriteString(strings.Join(fleetSudoCommands, ", "))
	b.WriteString("\n")
	b.WriteString(strings.Join(names, ","))
	b.WriteString(" ALL=(root) NOPASSWD: ROOTFILES_FLEET_READONLY\n")
	return []byte(b.String()), nil
}

func declaredFleetSudoUsers(accounts []config.AccountConfig) map[string]bool {
	declared := make(map[string]bool, len(accounts))
	for _, account := range accounts {
		declared[account.Name] = true
	}
	return declared
}

// validateFleetSudoUsers requires every target to be resolvable now, unless
// it is declared in users.accounts and will be created earlier in this Apply.
func validateFleetSudoUsers(users []string, accounts []config.AccountConfig, allowDeclared bool) error {
	if _, err := fleetSudoersContent(users); err != nil {
		return err
	}
	declared := declaredFleetSudoUsers(accounts)
	for _, name := range users {
		if _, err := lookupFleetSudoUser(name); err == nil {
			continue
		}
		if allowDeclared && declared[name] {
			continue
		}
		return fmt.Errorf("fleet_sudo_users: user %q does not exist; declare it in users.accounts first", name)
	}
	return nil
}

func trustedFleetPathComponent(path string, wantDir bool) (os.FileInfo, error) {
	info, err := fleetSudoLstat(path)
	if err != nil {
		return nil, fmt.Errorf("checking trusted rootfiles path %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("unsafe rootfiles path %s: symlinks are not allowed", path)
	}
	if wantDir && !info.IsDir() {
		return nil, fmt.Errorf("unsafe rootfiles path %s: parent is not a directory", path)
	}
	if wantDir && info.Mode().Perm()&0o100 == 0 {
		return nil, fmt.Errorf("unsafe rootfiles path %s: root cannot traverse parent directory", path)
	}
	if !wantDir && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("unsafe rootfiles binary %s: must be a regular file", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return nil, fmt.Errorf("unsafe rootfiles path %s: must be owned by root", path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("unsafe rootfiles path %s: group/world writable", path)
	}
	if !wantDir && info.Mode().Perm()&0o100 == 0 {
		return nil, fmt.Errorf("unsafe rootfiles binary %s: not executable by root", path)
	}
	return info, nil
}

// validateFleetSudoBinary protects the exact NOPASSWD command from replacement
// through either a writable ancestor or a symlink in the executable path.
func validateFleetSudoBinary() error {
	path := filepath.Clean(fleetSudoBinaryPath)
	if !filepath.IsAbs(path) || path == string(os.PathSeparator) {
		return fmt.Errorf("unsafe rootfiles binary path %q: must be an absolute file path", fleetSudoBinaryPath)
	}

	parts := strings.Split(strings.TrimPrefix(path, string(os.PathSeparator)), string(os.PathSeparator))
	current := string(os.PathSeparator)
	for i, part := range parts {
		if part == "" {
			return fmt.Errorf("unsafe rootfiles binary path %q", fleetSudoBinaryPath)
		}
		current = filepath.Join(current, part)
		wantDir := i < len(parts)-1
		if _, err := trustedFleetPathComponent(current, wantDir); err != nil {
			return err
		}
	}
	return nil
}

func fleetSudoDropInInfo() (os.FileInfo, bool, error) {
	path := fleetSudoersPath()
	info, err := fleetSudoLstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("checking fleet sudoers drop-in: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("unsafe fleet sudoers drop-in %s: must be a regular file, not a symlink", path)
	}
	return info, true, nil
}

func fleetSudoDropInSafe(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0 && info.Mode().Perm()&0o022 == 0 && info.Mode().Perm()&0o400 != 0
}

func fleetSudoListingSatisfied(output string) bool {
	normalized := strings.Join(strings.Fields(output), " ")
	allowed := "NOPASSWD: " + strings.Join(fleetSudoCommands, ", ")
	return strings.Contains(normalized, allowed)
}

func verifyFleetSudoUser(ctx context.Context, rc *RunContext, username string) error {
	result, err := rc.Runner.Query(ctx, "sudo", "-l", "-U", username)
	if err != nil {
		return fmt.Errorf("verifying fleet sudoers for %s with sudo -l -U: %w", username, err)
	}
	if !fleetSudoListingSatisfied(result.Stdout) {
		return fmt.Errorf("sudo -l -U %s did not show the exact passwordless rootfiles read-only commands", username)
	}
	return nil
}

func checkFleetSudo(ctx context.Context, rc *RunContext) ([]Change, error) {
	users := rc.Config.Users.FleetSudoUsers
	info, exists, err := fleetSudoDropInInfo()
	if err != nil {
		return nil, err
	}
	if len(users) == 0 {
		if exists {
			return []Change{{Description: "Remove rootfiles fleet sudoers rule", Command: "remove " + fleetSudoersPath()}}, nil
		}
		return nil, nil
	}
	if err := validateFleetSudoUsers(users, rc.Config.Users.Accounts, true); err != nil {
		return nil, err
	}
	if err := validateFleetSudoBinary(); err != nil {
		return nil, err
	}
	expected, err := fleetSudoersContent(users)
	if err != nil {
		return nil, err
	}
	if !exists {
		return []Change{{Description: "Install read-only fleet sudoers rule", Command: "write " + fleetSudoersPath()}}, nil
	}
	current, err := rc.Runner.ReadFile(fleetSudoersPath())
	if err != nil {
		return nil, fmt.Errorf("reading fleet sudoers drop-in: %w", err)
	}
	if string(current) != string(expected) || !fleetSudoDropInSafe(info) {
		return []Change{{Description: "Update read-only fleet sudoers rule", Command: "write " + fleetSudoersPath()}}, nil
	}
	for _, username := range users {
		if err := verifyFleetSudoUser(ctx, rc, username); err != nil {
			return []Change{{Description: "Verify read-only fleet sudoers rule for " + username, Command: "sudo -l -U " + username}}, nil
		}
	}
	return nil, nil
}

func applyFleetSudo(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	users := rc.Config.Users.FleetSudoUsers
	info, exists, err := fleetSudoDropInInfo()
	if err != nil {
		return nil, err
	}
	if len(users) == 0 {
		if !exists {
			return &ApplyResult{Changed: false}, nil
		}
		if err := rc.Runner.Remove(fleetSudoersPath()); err != nil {
			return nil, fmt.Errorf("removing fleet sudoers drop-in: %w", err)
		}
		return &ApplyResult{Changed: !rc.DryRun, Messages: []string{"read-only fleet sudoers rule removed"}}, nil
	}
	if err := validateFleetSudoUsers(users, nil, false); err != nil {
		return nil, err
	}
	if err := validateFleetSudoBinary(); err != nil {
		return nil, err
	}
	expected, err := fleetSudoersContent(users)
	if err != nil {
		return nil, err
	}
	changed := true
	if exists {
		current, err := rc.Runner.ReadFile(fleetSudoersPath())
		if err != nil {
			return nil, fmt.Errorf("reading fleet sudoers drop-in: %w", err)
		}
		changed = string(current) != string(expected) || !fleetSudoDropInSafe(info)
	}
	if changed {
		if err := writeFleetSudoers(ctx, rc, expected); err != nil {
			return nil, err
		}
	}
	if !rc.DryRun {
		for _, username := range users {
			if err := verifyFleetSudoUser(ctx, rc, username); err != nil {
				return nil, err
			}
		}
	}
	if !changed {
		return &ApplyResult{Changed: false}, nil
	}
	return &ApplyResult{Changed: !rc.DryRun, Messages: []string{"read-only fleet sudoers rule installed"}}, nil
}

func writeFleetSudoers(ctx context.Context, rc *RunContext, content []byte) error {
	return installSudoersFile(ctx, rc, fleetSudoersPath(), content, "fleet")
}

// installSudoersFile installs a sudoers drop-in through a temporary file
// that visudo must accept, so a broken rule never reaches sudoers.d.
func installSudoersFile(ctx context.Context, rc *RunContext, final string, content []byte, what string) error {
	tmp := filepath.Join(filepath.Dir(final), "."+filepath.Base(final)+".tmp")
	if info, err := fleetSudoLstat(tmp); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("unsafe temporary %s sudoers path %s", what, tmp)
		}
		if err := rc.Runner.Remove(tmp); err != nil {
			return fmt.Errorf("removing stale %s sudoers temp file: %w", what, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking temporary %s sudoers path: %w", what, err)
	}
	if err := rc.Runner.WriteFile(tmp, content, 0440); err != nil {
		return fmt.Errorf("writing %s sudoers temporary file: %w", what, err)
	}
	if rc.DryRun {
		return rc.Runner.Rename(tmp, final)
	}
	if !rc.Runner.CommandExists("visudo") {
		_ = rc.Runner.Remove(tmp)
		return fmt.Errorf("visudo is required to validate the %s sudoers rule", what)
	}
	if _, err := rc.Runner.Run(ctx, "visudo", "-cf", tmp); err != nil {
		_ = rc.Runner.Remove(tmp)
		return fmt.Errorf("%s sudoers validation failed: %w", what, err)
	}
	if err := rc.Runner.Rename(tmp, final); err != nil {
		return fmt.Errorf("installing %s sudoers drop-in: %w", what, err)
	}
	return nil
}
