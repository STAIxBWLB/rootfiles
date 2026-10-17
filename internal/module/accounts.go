package module

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

// Account helpers shared by user add/restore/passwd.

// validUsername mirrors the Debian adduser default NAME_REGEX. Rejecting
// anything else keeps usernames safe to embed in sudoers lines and paths.
var validUsername = regexp.MustCompile(`^[a-z_][a-z0-9_.-]*[$]?$`)

// lookupAccountGroup is replaceable in tests so distro-native administrator
// group handling can be checked without consulting or changing the host.
var lookupAccountGroup = user.LookupGroup

func checkUsername(name string) error {
	if len(name) == 0 || len(name) > 32 || !validUsername.MatchString(name) {
		return fmt.Errorf("invalid username %q (lowercase letters, digits, '_', '-', '.')", name)
	}
	return nil
}

// accountGroupsForSystem maps the conventional sudo group to the platform's
// native administrator group while retaining every other requested group.
// Membership changes are additive; this never removes existing groups.
func accountGroupsForSystem(system *config.SystemInfo, groups []string) []string {
	adminGroup := config.AdminGroup(system)
	seen := make(map[string]bool, len(groups))
	result := make([]string, 0, len(groups))
	for _, group := range groups {
		if group == "sudo" && adminGroup != "sudo" {
			group = adminGroup
		}
		if group == "" || seen[group] {
			continue
		}
		seen[group] = true
		result = append(result, group)
	}
	return result
}

// requireNativeAdminGroup fails rather than silently creating an account that
// lacks the administrator group requested through the conventional "sudo" name.
func requireNativeAdminGroup(system *config.SystemInfo, groups []string) error {
	adminGroup := config.AdminGroup(system)
	requested := false
	for _, group := range groups {
		if group == "sudo" || group == adminGroup {
			requested = true
			break
		}
	}
	if !requested {
		return nil
	}
	if _, err := lookupAccountGroup(adminGroup); err != nil {
		return fmt.Errorf("native administrator group %q is unavailable: %w", adminGroup, err)
	}
	return nil
}

// sudoersDir is overridable in tests.
var sudoersDir = "/etc/sudoers.d"

// sudoersPath returns the drop-in path for a user. sudo's #includedir
// skips files containing '.', so dots in usernames are replaced.
func sudoersPath(username string) string {
	return filepath.Join(sudoersDir, strings.ReplaceAll(username, ".", "_"))
}

// writeSudoers installs a NOPASSWD drop-in for username. The file is
// written under a name sudo ignores, validated with `visudo -cf`, and only
// then renamed into place, so a bad file can never break sudo.
func writeSudoers(ctx context.Context, rc *RunContext, username string) error {
	if err := checkUsername(username); err != nil {
		return err
	}
	final := sudoersPath(username)
	tmp := filepath.Join(sudoersDir, ".rootfiles-"+filepath.Base(final)+".tmp")
	content := fmt.Sprintf("%s ALL=(ALL) NOPASSWD:ALL\n", username)

	if err := rc.Runner.WriteFile(tmp, []byte(content), 0440); err != nil {
		return fmt.Errorf("writing sudoers: %w", err)
	}
	if !rc.DryRun && rc.Runner.CommandExists("visudo") {
		if _, err := rc.Runner.Run(ctx, "visudo", "-cf", tmp); err != nil {
			_ = rc.Runner.Remove(tmp)
			return fmt.Errorf("sudoers validation failed: %w", err)
		}
	}
	if err := rc.Runner.Rename(tmp, final); err != nil {
		return fmt.Errorf("installing sudoers: %w", err)
	}
	return nil
}

// existingGroups splits groups into those present on the system and those
// missing (e.g. "docker" before Docker is installed). Duplicates are dropped.
func existingGroups(groups []string) (present, missing []string) {
	seen := map[string]bool{}
	for _, g := range groups {
		if g == "" || seen[g] {
			continue
		}
		seen[g] = true
		if _, err := lookupAccountGroup(g); err == nil {
			present = append(present, g)
		} else {
			missing = append(missing, g)
		}
	}
	return present, missing
}

// installAuthorizedKeys writes keys to ~/.ssh/authorized_keys with the
// ownership and modes sshd's StrictModes requires.
func installAuthorizedKeys(ctx context.Context, rc *RunContext, username, home string, keys []string) error {
	sshDir := filepath.Join(home, ".ssh")
	if err := rc.Runner.MkdirAll(sshDir, 0700); err != nil {
		return fmt.Errorf("creating %s: %w", sshDir, err)
	}
	path := filepath.Join(sshDir, "authorized_keys")
	if err := rc.Runner.WriteFile(path, []byte(strings.Join(keys, "\n")+"\n"), 0600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if rc.DryRun {
		return nil
	}
	u, err := user.Lookup(username)
	if err != nil {
		return fmt.Errorf("looking up %s: %w", username, err)
	}
	if _, err := rc.Runner.Run(ctx, "chown", "-R", u.Uid+":"+u.Gid, sshDir); err != nil {
		return fmt.Errorf("chown %s: %w", sshDir, err)
	}
	return nil
}

const passwordAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// generatePassword returns a random password without look-alike characters.
func generatePassword(n int) (string, error) {
	b := make([]byte, n)
	max := big.NewInt(int64(len(passwordAlphabet)))
	for i := range b {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b[i] = passwordAlphabet[idx.Int64()]
	}
	return string(b), nil
}

// setPassword feeds "user:password" to chpasswd over stdin: no shell, and
// the password never appears in argv or logs.
func setPassword(ctx context.Context, rc *RunContext, username, password string) error {
	if strings.ContainsAny(username+password, ":\n") {
		return fmt.Errorf("username/password must not contain ':' or newlines")
	}
	_, err := rc.Runner.RunInput(ctx, username+":"+password+"\n", "chpasswd")
	return err
}
