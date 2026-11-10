package module

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

// Account lifecycle beyond creation: delete, lock, key management, expiry,
// disk usage and metadata audit.

func usersDBPath(rc *RunContext) string {
	homeBase := rc.Config.Users.Base()
	return filepath.Join(homeBase, ".rootfiles", "users.json")
}

// updateUserMeta edits one user's metadata entry in place. fn returns
// false to delete the entry. Missing DB or entry is not an error.
func updateUserMeta(rc *RunContext, name string, fn func(*UserMeta) bool) error {
	path := usersDBPath(rc)
	var db UsersDB
	found, err := readJSONDB(path, &db)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	var kept []UserMeta
	for i := range db.Users {
		u := db.Users[i]
		if u.Name == name && !fn(&u) {
			continue
		}
		kept = append(kept, u)
	}
	db.Users = kept
	return writeJSONDB(rc, path, &db, 0600)
}

// lookupManaged returns the account, refusing system accounts.
func lookupManaged(name string) (*user.User, error) {
	if err := checkUsername(name); err != nil {
		return nil, err
	}
	u, err := lookupAccount(name)
	if err != nil {
		return nil, fmt.Errorf("user %s not found", name)
	}
	if uid, _ := strconv.Atoi(u.Uid); uid < 1000 || uid == 65534 {
		return nil, fmt.Errorf("refusing to modify system account %s (uid %s)", name, u.Uid)
	}
	return u, nil
}

// DeleteHomeMode selects what DeleteUser does with the home directory.
type DeleteHomeMode int

const (
	KeepHome    DeleteHomeMode = iota // leave the home in place (default)
	ArchiveHome                       // tar.gz it under <home_base>/.rootfiles/archive, then remove
	RemoveHome                        // remove without archive
)

// DeleteUser removes an account and everything rootfiles attached to it
// (sudoers drop-in, GPU allocation, metadata entry).
func DeleteUser(ctx context.Context, rc *RunContext, name string, mode DeleteHomeMode) error {
	if err := requireTrustedMetadata(rc); err != nil {
		return err
	}
	u, err := lookupManaged(name)
	if err != nil {
		return err
	}
	home := u.HomeDir

	if mode == ArchiveHome && rc.Runner.FileExists(home) {
		archDir := filepath.Join(filepath.Dir(usersDBPath(rc)), "archive")
		if err := rc.Runner.MkdirAll(archDir, 0700); err != nil {
			return err
		}
		archive := filepath.Join(archDir, fmt.Sprintf("%s-%s.tar.gz", name, time.Now().Format("20060102-150405")))
		if _, err := rc.Runner.Run(ctx, "tar", "-czf", archive, "-C", filepath.Dir(home), filepath.Base(home)); err != nil {
			return fmt.Errorf("archiving %s (account left intact): %w", home, err)
		}
		ui.WriteText(rc.Output(), "  home archived to %s\n", archive)
	}

	if err := RevokeGPUs(ctx, rc, name); err != nil && !strings.Contains(err.Error(), "no GPU allocation") {
		return fmt.Errorf("revoking GPU allocation: %w", err)
	}

	if _, err := rc.Runner.Run(ctx, "userdel", name); err != nil {
		return fmt.Errorf("userdel (is %s still logged in?): %w", name, err)
	}
	if err := rc.Runner.Remove(sudoersPath(name)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing sudoers drop-in: %w", err)
	}
	if err := updateUserMeta(rc, name, func(*UserMeta) bool { return false }); err != nil {
		return err
	}

	switch {
	case mode != KeepHome && rc.Runner.FileExists(home):
		if _, err := rc.Runner.Run(ctx, "rm", "-rf", "--one-file-system", home); err != nil {
			return fmt.Errorf("removing %s: %w", home, err)
		}
		ui.WriteText(rc.Output(), "User %s deleted (home removed)\n", name)
	default:
		ui.WriteText(rc.Output(), "User %s deleted (home kept at %s)\n", name, home)
	}
	return nil
}

// LockUser blocks all logins. `usermod -L` alone only disables the
// password; expiring the account also stops SSH key logins.
func LockUser(ctx context.Context, rc *RunContext, name string) error {
	if _, err := lookupManaged(name); err != nil {
		return err
	}
	if _, err := rc.Runner.Run(ctx, "usermod", "-L", "-e", "1", name); err != nil {
		return fmt.Errorf("locking %s: %w", name, err)
	}
	ui.WriteText(rc.Output(), "User %s locked (password and SSH key logins blocked)\n", name)
	return nil
}

// UnlockUser reverses LockUser.
func UnlockUser(ctx context.Context, rc *RunContext, name string) error {
	if _, err := lookupManaged(name); err != nil {
		return err
	}
	if _, err := rc.Runner.Run(ctx, "usermod", "-U", "-e", "", name); err != nil {
		return fmt.Errorf("unlocking %s: %w", name, err)
	}
	ui.WriteText(rc.Output(), "User %s unlocked\n", name)
	return nil
}

// SetExpiry sets the account expiry date (YYYY-MM-DD) or clears it ("never").
func SetExpiry(ctx context.Context, rc *RunContext, name, date string) error {
	if _, err := lookupManaged(name); err != nil {
		return err
	}
	arg := "-1"
	if date != "never" {
		if _, err := time.Parse("2006-01-02", date); err != nil {
			return fmt.Errorf("invalid date %q (want YYYY-MM-DD or never)", date)
		}
		arg = date
	}
	if _, err := rc.Runner.Run(ctx, "chage", "-E", arg, name); err != nil {
		return fmt.Errorf("setting expiry: %w", err)
	}
	ui.WriteText(rc.Output(), "User %s expiry: %s\n", name, date)
	return nil
}

func authorizedKeysPath(u *user.User) string {
	return filepath.Join(u.HomeDir, ".ssh", "authorized_keys")
}

// ListKeys prints a user's authorized keys, numbered.
func ListKeys(rc *RunContext, name string) error {
	u, err := lookupManaged(name)
	if err != nil {
		return err
	}
	data, _ := rc.Runner.ReadFile(authorizedKeysPath(u))
	keys := nonEmptyLines(string(data))
	if len(keys) == 0 {
		ui.WriteText(rc.Output(), "%s has no authorized keys\n", name)
		return nil
	}
	for i, k := range keys {
		blob := keyID(k)
		if len(blob) > 40 {
			blob = blob[:25] + "…" + blob[len(blob)-12:]
		}
		ui.WriteText(rc.Output(), "%2d  %-40s %s\n", i+1, blob, keyComment(k))
	}
	return nil
}

// AddKey appends a public key (no-op if already present).
func AddKey(ctx context.Context, rc *RunContext, name, key string) error {
	if err := requireTrustedMetadata(rc); err != nil {
		return err
	}
	u, err := lookupManaged(name)
	if err != nil {
		return err
	}
	key = strings.TrimSpace(key)
	if len(strings.Fields(key)) < 2 || strings.ContainsAny(key, "\n\r") {
		return fmt.Errorf("not an OpenSSH public key line")
	}
	path := authorizedKeysPath(u)
	if len(missingKeys(path, []string{key})) == 0 {
		ui.WriteText(rc.Output(), "key already present for %s\n", name)
		return nil
	}
	var lines []string
	if existing, _ := rc.Runner.ReadFile(path); len(strings.TrimSpace(string(existing))) > 0 {
		lines = strings.Split(strings.TrimRight(string(existing), "\n"), "\n")
	}
	if err := installAuthorizedKeys(ctx, rc, name, u.HomeDir, append(lines, key)); err != nil {
		return err
	}
	if err := updateUserMeta(rc, name, func(m *UserMeta) bool {
		m.SSHPubkeys = append(m.SSHPubkeys, key)
		return true
	}); err != nil {
		return err
	}
	ui.WriteText(rc.Output(), "key added for %s\n", name)
	return nil
}

// RemoveKey removes keys matching sel: a 1-based index from ListKeys, the
// key itself, or its comment.
func RemoveKey(ctx context.Context, rc *RunContext, name, sel string) error {
	if err := requireTrustedMetadata(rc); err != nil {
		return err
	}
	u, err := lookupManaged(name)
	if err != nil {
		return err
	}
	path := authorizedKeysPath(u)
	data, err := rc.Runner.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%s has no authorized_keys", name)
	}
	idx, _ := strconv.Atoi(sel)
	n := 0
	var kept, removed []string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			kept = append(kept, line)
			continue
		}
		n++
		if n == idx || keyID(t) == keyID(sel) || (keyComment(t) != "" && keyComment(t) == sel) {
			removed = append(removed, t)
			continue
		}
		kept = append(kept, line)
	}
	if len(removed) == 0 {
		return fmt.Errorf("no key matching %q for %s", sel, name)
	}
	if err := installAuthorizedKeys(ctx, rc, name, u.HomeDir, kept); err != nil {
		return err
	}
	gone := map[string]bool{}
	for _, r := range removed {
		gone[keyID(r)] = true
	}
	if err := updateUserMeta(rc, name, func(m *UserMeta) bool {
		var ks []string
		for _, k := range m.SSHPubkeys {
			if !gone[keyID(k)] {
				ks = append(ks, k)
			}
		}
		m.SSHPubkeys = ks
		return true
	}); err != nil {
		return err
	}
	ui.WriteText(rc.Output(), "removed %d key(s) for %s\n", len(removed), name)
	return nil
}

// HomeUsage is one row of `user du`.
type HomeUsage struct {
	User  string
	Home  string
	Bytes int64
}

// HomesUsage measures every home directory under home_base.
func HomesUsage(ctx context.Context, rc *RunContext) ([]HomeUsage, error) {
	users, err := scanSystemUsers(ctx, rc)
	if err != nil {
		return nil, err
	}
	var out []HomeUsage
	for _, u := range users {
		if !rc.Runner.FileExists(u.Home) {
			continue
		}
		res, err := rc.Runner.Query(ctx, "du", "-sb", "--one-file-system", u.Home)
		if err != nil && (res == nil || res.Stdout == "") {
			continue
		}
		f := strings.Fields(res.Stdout)
		if len(f) == 0 {
			continue
		}
		b, _ := strconv.ParseInt(f[0], 10, 64)
		out = append(out, HomeUsage{User: u.Name, Home: u.Home, Bytes: b})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	return out, nil
}

// AuditFinding describes a mismatch between users.json and the system.
type AuditFinding struct {
	User   string
	Detail string
}

// AuditUsers compares the managed-user metadata with /etc/passwd: entries
// missing on the system (restore candidates after an OS reinstall), UID
// drift, and accounts under home_base that rootfiles does not track.
func AuditUsers(ctx context.Context, rc *RunContext) ([]AuditFinding, error) {
	WarnUntrustedMetadata(rc)
	db, err := LoadUsersDB(rc)
	if err != nil {
		return nil, err
	}
	sys, err := scanSystemUsers(ctx, rc)
	if err != nil {
		return nil, err
	}
	sysByName := map[string]UserMeta{}
	for _, u := range sys {
		sysByName[u.Name] = u
	}
	var out []AuditFinding
	tracked := map[string]bool{}
	for _, m := range db.Users {
		tracked[m.Name] = true
		s, ok := sysByName[m.Name]
		switch {
		case !ok:
			out = append(out, AuditFinding{m.Name, "in users.json but not on this system (rootfiles user restore)"})
		case m.UID != 0 && s.UID != m.UID:
			out = append(out, AuditFinding{m.Name, fmt.Sprintf("uid %d on system, %d in users.json (file ownership may be wrong)", s.UID, m.UID)})
		case s.Home != m.Home:
			out = append(out, AuditFinding{m.Name, fmt.Sprintf("home %s on system, %s in users.json", s.Home, m.Home)})
		}
	}
	homeBase := rc.Config.Users.HomeBase
	for _, s := range sys {
		if !tracked[s.Name] && homeBase != "" && strings.HasPrefix(s.Home, strings.TrimSuffix(homeBase, "/")+"/") {
			out = append(out, AuditFinding{s.Name, "account under home_base not tracked in users.json"})
		}
	}
	return out, nil
}

// keyComment returns the comment after the key blob, if any.
func keyComment(line string) string {
	f := strings.Fields(line)
	for i := 0; i+1 < len(f); i++ {
		if strings.HasPrefix(f[i], "ssh-") || strings.HasPrefix(f[i], "ecdsa-") || strings.HasPrefix(f[i], "sk-") {
			return strings.Join(f[i+2:], " ")
		}
	}
	return ""
}
