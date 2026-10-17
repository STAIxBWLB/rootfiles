package module

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

type UsersModule struct{}

func NewUsersModule() *UsersModule  { return &UsersModule{} }
func (m *UsersModule) Name() string { return "users" }

// UserMeta stores metadata for a managed user.
type UserMeta struct {
	Name         string   `json:"name"`
	UID          int      `json:"uid"`
	GID          int      `json:"gid"`
	Shell        string   `json:"shell"`
	Groups       []string `json:"groups"`
	SudoNopasswd bool     `json:"sudo_nopasswd"`
	SSHPubkeys   []string `json:"ssh_pubkeys,omitempty"`
	CreatedAt    string   `json:"created_at"`
	Home         string   `json:"home"`
	Quota        string   `json:"quota,omitempty"` // e.g. "500G", re-applied on restore
}

// UsersDB is the metadata file format.
type UsersDB struct {
	Version   int        `json:"version"`
	HomeBase  string     `json:"home_base"`
	CreatedBy string     `json:"created_by"`
	Users     []UserMeta `json:"users"`
}

// LoadUsersDB reads the users metadata file under <home-base>/.rootfiles/users.json.
// Returns an empty DB (not an error) when the file is absent so callers can
// treat "no managed users" as a valid state. Parse errors are surfaced.
func LoadUsersDB(rc *RunContext) (*UsersDB, error) {
	homeBase := rc.Config.Users.HomeBase
	if homeBase == "" {
		homeBase = "/home"
	}
	var db UsersDB
	data, err := rc.Runner.ReadFile(filepath.Join(homeBase, ".rootfiles", "users.json"))
	if err != nil {
		return &db, nil
	}
	if err := json.Unmarshal(data, &db); err != nil {
		return &db, fmt.Errorf("parsing users DB: %w", err)
	}
	return &db, nil
}

// useraddDefaultsFile holds useradd's default HOME; tests redirect it.
var useraddDefaultsFile = "/etc/default/useradd"

// homeBaseRoot is where the ownership walk of a custom home base starts and
// homeBaseOwner the uid that must own it; tests stub both.
var (
	homeBaseRoot         = "/"
	homeBaseOwner uint32 = 0
)

// checkHomeBase refuses a custom home base that a user other than root
// could control, by the same rule as home-base detection: that user could
// replace the homes created under it. /home is the distribution's own.
func checkHomeBase(base string) error {
	if base == "" || filepath.Clean(base) == "/home" {
		return nil
	}
	if err := config.RootOnlyHomeBase(homeBaseRoot, base, homeBaseOwner); err != nil {
		return fmt.Errorf("refusing home base %s: %w; / and every existing directory down to the base must be owned by root and not writable by group or others", base, err)
	}
	return nil
}

// warnOut receives the warnings of read-only reports; tests capture it.
var warnOut io.Writer = os.Stderr

// metaHomeBase is the configured home base, /home when unset.
func metaHomeBase(rc *RunContext) string {
	if hb := rc.Config.Users.HomeBase; hb != "" {
		return hb
	}
	return "/home"
}

// requireTrustedMetadata refuses the user and GPU databases under the home
// base when a user other than root could control them: checkHomeBase
// rejects the base, or <base>/.rootfiles (a directory, or the legacy flat
// users database) or anything in it is a symlink, a special file, owned by
// another uid, or writable by group or others. Such a user could plant a
// symlink at a database path so root overwrites another file, or edit what
// the databases grant; fixing the directory modes alone would not remove
// what was planted while they were open.
func requireTrustedMetadata(rc *RunContext) error {
	base := metaHomeBase(rc)
	if err := checkHomeBase(base); err != nil {
		return err
	}
	meta := filepath.Join(base, ".rootfiles")
	if _, err := os.Lstat(meta); os.IsNotExist(err) {
		return nil
	}
	if err := rootOnlyTree(meta, homeBaseOwner); err != nil {
		return fmt.Errorf("refusing rootfiles metadata in %s: %w", meta, err)
	}
	return nil
}

// rootOnlyTree reports the first entry under path, path included, that is a
// symlink, not a regular file or directory, owned by a uid other than uid,
// or writable by group or others. Symlinks are never followed.
func rootOnlyTree(path string, uid uint32) error {
	return filepath.WalkDir(path, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("%s is a symlink", p)
		case !fi.IsDir() && !fi.Mode().IsRegular():
			return fmt.Errorf("%s is not a regular file or directory", p)
		case !ok || st.Uid != uid:
			return fmt.Errorf("%s is not owned by root", p)
		case fi.Mode().Perm()&0o022 != 0:
			return fmt.Errorf("%s is writable by group or others", p)
		}
		return nil
	})
}

// WarnUntrustedMetadata lets a read-only report go on, naming the reason.
func WarnUntrustedMetadata(rc *RunContext) {
	if err := requireTrustedMetadata(rc); err != nil {
		fmt.Fprintf(warnOut, "⚠ untrusted rootfiles metadata (%v); this report reads it anyway\n", err)
	}
}

// refuseSymlink stops a database write that would follow a symlink.
func refuseSymlink(path string) error {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to write %s: it is a symlink", path)
	}
	return nil
}

func (m *UsersModule) Check(ctx context.Context, rc *RunContext) (*CheckResult, error) {
	var changes []Change
	cfg := rc.Config.Users
	if err := requireTrustedMetadata(rc); err != nil {
		return nil, err
	}

	if cfg.HomeBase != "" && cfg.HomeBase != "/home" {
		if !rc.Runner.FileExists(cfg.HomeBase) {
			changes = append(changes, Change{
				Description: fmt.Sprintf("Create custom home base directory %s", cfg.HomeBase),
				Command:     fmt.Sprintf("mkdir -p %s", cfg.HomeBase),
			})
		}
		metaDir := filepath.Join(cfg.HomeBase, ".rootfiles")
		if !rc.Runner.FileExists(metaDir) {
			changes = append(changes, Change{
				Description: "Create rootfiles metadata directory",
				Command:     fmt.Sprintf("mkdir -p %s", metaDir),
			})
		}
	}

	// Check /etc/default/useradd HOME setting. /home is written too: it is
	// how home-base detection keeps a /home choice on a host with a data drive.
	if cfg.HomeBase != "" {
		want := filepath.Clean(cfg.HomeBase)
		data, _ := rc.Runner.ReadFile(useraddDefaultsFile)
		if config.UseraddHome(data) != want {
			changes = append(changes, Change{
				Description: fmt.Sprintf("Set default useradd HOME to %s", want),
				Command:     fmt.Sprintf("update HOME=%s in %s", want, useraddDefaultsFile),
			})
		}
	}

	for _, a := range rc.Config.Users.Accounts {
		drift, err := accountDrift(ctx, rc, a)
		if err != nil {
			return nil, err
		}
		for _, d := range drift {
			changes = append(changes, Change{Description: d.desc, Command: d.cmd})
		}
	}

	fleetChanges, err := checkFleetSudo(ctx, rc)
	if err != nil {
		return nil, err
	}
	changes = append(changes, fleetChanges...)

	return &CheckResult{
		Satisfied: len(changes) == 0,
		Changes:   changes,
	}, nil
}

func (m *UsersModule) Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	cfg := rc.Config.Users
	if err := requireTrustedMetadata(rc); err != nil {
		return nil, err
	}
	var messages, warnings []string
	changed := false

	// Create custom home base
	if cfg.HomeBase != "" && cfg.HomeBase != "/home" &&
		(!rc.Runner.FileExists(cfg.HomeBase) || !isDir(filepath.Join(cfg.HomeBase, ".rootfiles"))) {
		if err := rc.Runner.MkdirAll(cfg.HomeBase, 0755); err != nil {
			return nil, fmt.Errorf("creating home base: %w", err)
		}
		if err := ensureMetaDir(rc.Runner, cfg.HomeBase); err != nil {
			return nil, fmt.Errorf("creating metadata dir: %w", err)
		}
		messages = append(messages, fmt.Sprintf("home base %s ready", cfg.HomeBase))
		changed = true
	}

	// Update /etc/default/useradd
	if cfg.HomeBase != "" {
		want := filepath.Clean(cfg.HomeBase)
		data, err := rc.Runner.ReadFile(useraddDefaultsFile)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			// Rewriting from an unread file would drop its other defaults.
			return nil, fmt.Errorf("reading %s: %w", useraddDefaultsFile, err)
		}
		if config.UseraddHome(data) != want {
			// Replace or append HOME= line
			var lines []string
			if trimmed := strings.TrimRight(string(data), "\n"); trimmed != "" {
				lines = strings.Split(trimmed, "\n")
			}
			var newLines []string
			found := false
			for _, line := range lines {
				if strings.HasPrefix(line, "HOME=") {
					newLines = append(newLines, "HOME="+want)
					found = true
				} else {
					newLines = append(newLines, line)
				}
			}
			if !found {
				newLines = append(newLines, "HOME="+want)
			}
			content := strings.Join(newLines, "\n") + "\n"
			if err := rc.Runner.WriteFile(useraddDefaultsFile, []byte(content), 0644); err != nil {
				return nil, fmt.Errorf("writing %s: %w", useraddDefaultsFile, err)
			}
			messages = append(messages, "updated "+useraddDefaultsFile)
			changed = true
		}
	}

	for _, a := range rc.Config.Users.Accounts {
		msgs, err := convergeAccount(ctx, rc, a)
		if err != nil {
			return nil, fmt.Errorf("account %s: %w", a.Name, err)
		}
		if len(msgs) > 0 {
			messages = append(messages, msgs...)
			changed = true
		}
	}

	fleetResult, err := applyFleetSudo(ctx, rc)
	if err != nil {
		return nil, err
	}
	if fleetResult.Changed {
		changed = true
	}
	messages = append(messages, fleetResult.Messages...)
	warnings = append(warnings, fleetResult.Warnings...)

	return &ApplyResult{Changed: changed, Messages: messages, Warnings: warnings}, nil
}

type accountChange struct{ desc, cmd string }

// accountGroups is default_groups ∪ the account's own groups.
func accountGroups(rc *RunContext, a config.AccountConfig) []string {
	groups := append(append([]string{}, rc.Config.Users.DefaultGroups...), a.Groups...)
	return accountGroupsForSystem(rc.Config.System, groups)
}

// accountDrift lists what differs between a declared account and the
// system. Groups that do not exist yet (e.g. docker before Docker is
// installed) are not reported.
func accountDrift(ctx context.Context, rc *RunContext, a config.AccountConfig) ([]accountChange, error) {
	groups := accountGroups(rc, a)
	if err := requireNativeAdminGroup(rc.Config.System, groups); err != nil {
		return nil, err
	}
	u, err := user.Lookup(a.Name)
	if err != nil {
		return []accountChange{{fmt.Sprintf("Create user %s", a.Name), "useradd " + a.Name}}, nil
	}
	var out []accountChange
	if missing := missingKeys(filepath.Join(u.HomeDir, ".ssh", "authorized_keys"), a.SSHPubkeys); len(missing) > 0 {
		out = append(out, accountChange{fmt.Sprintf("Add %d SSH key(s) for %s", len(missing), a.Name), "append ~/.ssh/authorized_keys"})
	}
	if missing := missingGroups(ctx, rc, a.Name, groups); len(missing) > 0 {
		out = append(out, accountChange{fmt.Sprintf("Add %s to groups %v", a.Name, missing), "usermod -aG " + strings.Join(missing, ",") + " " + a.Name})
	}
	return out, nil
}

// convergeAccount creates the account or adds missing keys/groups.
func convergeAccount(ctx context.Context, rc *RunContext, a config.AccountConfig) ([]string, error) {
	groups := accountGroups(rc, a)
	if err := requireNativeAdminGroup(rc.Config.System, groups); err != nil {
		return nil, err
	}
	u, err := user.Lookup(a.Name)
	if err != nil {
		if err := AddUser(ctx, rc, a.Name, a.SSHPubkeys, a.Groups, false); err != nil {
			return nil, err
		}
		return []string{"created user " + a.Name}, nil
	}
	var msgs []string
	akPath := filepath.Join(u.HomeDir, ".ssh", "authorized_keys")
	if missing := missingKeys(akPath, a.SSHPubkeys); len(missing) > 0 {
		// Append, keeping existing lines (including comments) verbatim.
		var lines []string
		if existing, _ := rc.Runner.ReadFile(akPath); len(strings.TrimSpace(string(existing))) > 0 {
			lines = strings.Split(strings.TrimRight(string(existing), "\n"), "\n")
		}
		if err := installAuthorizedKeys(ctx, rc, a.Name, u.HomeDir, append(lines, missing...)); err != nil {
			return nil, err
		}
		msgs = append(msgs, fmt.Sprintf("added %d SSH key(s) for %s", len(missing), a.Name))
	}
	if missing := missingGroups(ctx, rc, a.Name, groups); len(missing) > 0 {
		if _, err := rc.Runner.Run(ctx, "usermod", "-aG", strings.Join(missing, ","), a.Name); err != nil {
			return nil, fmt.Errorf("adding groups: %w", err)
		}
		msgs = append(msgs, fmt.Sprintf("added %s to groups %v", a.Name, missing))
	}
	return msgs, nil
}

// missingKeys returns the wanted keys not present in an authorized_keys
// file, comparing key type + blob (comments/options are ignored).
func missingKeys(path string, want []string) []string {
	data, _ := os.ReadFile(path)
	have := map[string]bool{}
	for _, line := range nonEmptyLines(string(data)) {
		have[keyID(line)] = true
	}
	var missing []string
	for _, k := range want {
		if !have[keyID(k)] {
			missing = append(missing, k)
		}
	}
	return missing
}

// keyID extracts "<type> <base64>" from an authorized_keys line, skipping
// any leading options.
func keyID(line string) string {
	f := strings.Fields(line)
	for i := 0; i+1 < len(f); i++ {
		if strings.HasPrefix(f[i], "ssh-") || strings.HasPrefix(f[i], "ecdsa-") || strings.HasPrefix(f[i], "sk-") {
			return f[i] + " " + f[i+1]
		}
	}
	return strings.TrimSpace(line)
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "#") {
			out = append(out, t)
		}
	}
	return out
}

// missingGroups returns existing groups in want that username is not in.
func missingGroups(ctx context.Context, rc *RunContext, username string, want []string) []string {
	present, _ := existingGroups(want)
	if len(present) == 0 {
		return nil
	}
	res, err := rc.Runner.Query(ctx, "id", "-Gn", username)
	if err != nil {
		return nil
	}
	have := map[string]bool{}
	for _, g := range strings.Fields(res.Stdout) {
		have[g] = true
	}
	var missing []string
	for _, g := range present {
		if !have[g] {
			missing = append(missing, g)
		}
	}
	return missing
}

// AddUser creates a user with the given config. Called from CLI `rootfiles user add`.
func AddUser(ctx context.Context, rc *RunContext, username string, pubkeys []string, extraGroups []string, noDocker bool) error {
	if err := checkUsername(username); err != nil {
		return err
	}
	cfg := rc.Config.Users
	homeBase := cfg.HomeBase
	if homeBase == "" {
		homeBase = "/home"
	}
	if err := requireTrustedMetadata(rc); err != nil {
		return err
	}
	homeDir := filepath.Join(homeBase, username)
	shell := cfg.DefaultShell
	if shell == "" {
		shell = "/usr/bin/zsh"
	}
	if !rc.DryRun && !rc.Runner.FileExists(shell) {
		fmt.Printf("  %s shell %s not found, using /bin/bash\n", "⚠", shell)
		shell = "/bin/bash"
	}

	// Check if user exists
	if _, err := user.Lookup(username); err == nil {
		return fmt.Errorf("user %s already exists", username)
	}

	// Build group list (copy: never append into the config's slice)
	groups := accountGroups(rc, config.AccountConfig{Groups: extraGroups})
	if noDocker {
		var filtered []string
		for _, g := range groups {
			if g != "docker" {
				filtered = append(filtered, g)
			}
		}
		groups = filtered
	}
	if err := requireNativeAdminGroup(rc.Config.System, groups); err != nil {
		return err
	}
	groups, missing := existingGroups(groups)
	for _, g := range missing {
		fmt.Printf("  ⚠ group %s does not exist yet, skipped (add later with 'rootfiles user group-add')\n", g)
	}

	// Create user
	args := []string{
		"--home-dir", homeDir,
		"--create-home",
		"--shell", shell,
	}
	if len(groups) > 0 {
		args = append(args, "--groups", strings.Join(groups, ","))
	}
	if _, err := rc.Runner.Run(ctx, "useradd", append(args, username)...); err != nil {
		return fmt.Errorf("creating user: %w", err)
	}

	if cfg.SudoNopasswd {
		if err := writeSudoers(ctx, rc, username); err != nil {
			return err
		}
	}

	if len(pubkeys) > 0 {
		if err := installAuthorizedKeys(ctx, rc, username, homeDir, pubkeys); err != nil {
			return err
		}
	}

	if err := saveUserMeta(rc, username, homeDir, shell, groups, cfg.SudoNopasswd, pubkeys); err != nil {
		return fmt.Errorf("recording user metadata: %w", err)
	}

	fmt.Printf("User %s created (home: %s, shell: %s, groups: %v)\n", username, homeDir, shell, groups)
	return nil
}

// BackupUsers exports user metadata to JSON, including unmanaged system users.
func BackupUsers(rc *RunContext, outputPath string) error {
	ctx := context.Background()
	cfg := rc.Config.Users
	homeBase := cfg.HomeBase
	if homeBase == "" {
		homeBase = "/home"
	}

	// Load existing managed users DB (if any). A backup is a restore input,
	// so the DB of an untrusted base is left out, not just warned about.
	var db UsersDB
	dbPath := filepath.Join(homeBase, ".rootfiles", "users.json")
	if err := requireTrustedMetadata(rc); err != nil {
		fmt.Fprintf(warnOut, "⚠ %v; leaving the users database out of the backup\n", err)
	} else if data, err := rc.Runner.ReadFile(dbPath); err == nil {
		json.Unmarshal(data, &db)
	}

	// Scan system users and merge
	sysUsers, err := scanSystemUsers(ctx, rc)
	if err != nil {
		fmt.Printf("  Warning: system user scan failed: %v\n", err)
	}

	managed := make(map[string]bool)
	for _, u := range db.Users {
		managed[u.Name] = true
	}
	for _, su := range sysUsers {
		if !managed[su.Name] {
			db.Users = append(db.Users, su)
		}
	}

	if db.Version == 0 {
		db.Version = 1
	}
	if db.HomeBase == "" {
		db.HomeBase = homeBase
	}
	if db.CreatedBy == "" {
		db.CreatedBy = "rootfiles-v2"
	}

	data, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling users: %w", err)
	}

	if outputPath == "" {
		hostname, _ := os.Hostname()
		outputPath = fmt.Sprintf("rootfiles-users-%s-%s.json", hostname, time.Now().Format("20060102"))
	}

	if err := rc.Runner.WriteFile(outputPath, data, 0600); err != nil {
		return fmt.Errorf("writing backup: %w", err)
	}

	fmt.Printf("User backup saved to %s (%d users)\n", outputPath, len(db.Users))
	return nil
}

// ScanSystemUsersExported is the exported wrapper for scanSystemUsers.
func ScanSystemUsersExported(ctx context.Context, rc *RunContext) ([]UserMeta, error) {
	return scanSystemUsers(ctx, rc)
}

// scanSystemUsers reads /etc/passwd and collects metadata for regular users (UID 1000-65533).
func scanSystemUsers(ctx context.Context, rc *RunContext) ([]UserMeta, error) {
	data, err := rc.Runner.ReadFile("/etc/passwd")
	if err != nil {
		return nil, fmt.Errorf("reading /etc/passwd: %w", err)
	}

	var users []UserMeta
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 7 {
			continue
		}
		// fields: name:x:uid:gid:gecos:home:shell
		uid, err := strconv.Atoi(fields[2])
		if err != nil || uid < 1000 || uid > 65533 {
			continue
		}
		gid, _ := strconv.Atoi(fields[3])
		name := fields[0]
		home := fields[5]
		shell := fields[6]

		// Skip nologin/false shells
		if strings.HasSuffix(shell, "/nologin") || strings.HasSuffix(shell, "/false") {
			continue
		}

		meta := UserMeta{
			Name:  name,
			UID:   uid,
			GID:   gid,
			Shell: shell,
			Home:  home,
		}

		// Collect groups via `id -Gn`
		if result, err := rc.Runner.Query(ctx, "id", "-Gn", name); err == nil {
			groups := strings.Fields(strings.TrimSpace(result.Stdout))
			// Filter out the user's primary group (same as username)
			var supplementary []string
			for _, g := range groups {
				if g != name {
					supplementary = append(supplementary, g)
				}
			}
			meta.Groups = supplementary
		}

		// Check sudoers
		if rc.Runner.FileExists(sudoersPath(name)) {
			meta.SudoNopasswd = true
		}

		// Read SSH pubkeys
		authKeysPath := filepath.Join(home, ".ssh", "authorized_keys")
		if keyData, err := rc.Runner.ReadFile(authKeysPath); err == nil {
			var keys []string
			for _, line := range strings.Split(strings.TrimSpace(string(keyData)), "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "#") {
					keys = append(keys, line)
				}
			}
			if len(keys) > 0 {
				meta.SSHPubkeys = keys
			}
		}

		users = append(users, meta)
	}

	return users, nil
}

// RestoreUsers restores users from a backup JSON file.
func RestoreUsers(ctx context.Context, rc *RunContext, backupPath string) error {
	cfg := rc.Config.Users
	homeBase := cfg.HomeBase
	if homeBase == "" {
		homeBase = "/home"
	}

	// Auto-detect backup path. Its accounts, sudo rights and keys are
	// trusted only from a base that root alone controls.
	if backupPath == "" {
		if err := requireTrustedMetadata(rc); err != nil {
			return err
		}
		backupPath = filepath.Join(homeBase, ".rootfiles", "users.json")
	}

	data, err := os.ReadFile(backupPath)
	if err != nil {
		return fmt.Errorf("reading backup: %w", err)
	}

	var db UsersDB
	if err := json.Unmarshal(data, &db); err != nil {
		return fmt.Errorf("parsing backup: %w", err)
	}

	var failed []string
	for _, u := range db.Users {
		if err := checkUsername(u.Name); err != nil {
			failed = append(failed, err.Error())
			continue
		}
		// Check if user already exists
		if _, err := user.Lookup(u.Name); err == nil {
			fmt.Printf("  User %s already exists, skipping\n", u.Name)
			continue
		}

		if err := checkHomeBase(filepath.Dir(u.Home)); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", u.Name, err))
			continue
		}

		// Check if home dir exists (preserved from previous install)
		homeExists := rc.Runner.FileExists(u.Home)

		args := []string{
			"--home-dir", u.Home,
			"--shell", u.Shell,
			"--uid", strconv.Itoa(u.UID),
		}
		if homeExists {
			args = append(args, "--no-create-home")
		} else {
			args = append(args, "--create-home")
		}
		args = append(args, u.Name)

		if _, err := rc.Runner.Run(ctx, "useradd", args...); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", u.Name, firstLine(err.Error())))
			continue
		}

		if err := restoreUserExtras(ctx, rc, u, homeExists); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", u.Name, err))
			continue
		}
		reapplyQuota(ctx, rc, u)

		status := "created"
		if homeExists {
			status = "restored (home preserved)"
		}
		fmt.Printf("  User %s %s\n", u.Name, status)
	}

	if len(failed) > 0 {
		return fmt.Errorf("%d user(s) failed to restore: %s", len(failed), strings.Join(failed, "; "))
	}
	return nil
}

// restoreUserExtras re-applies groups, sudoers, ownership and SSH keys for
// a user just recreated from a backup.
func restoreUserExtras(ctx context.Context, rc *RunContext, u UserMeta, homeExists bool) error {
	groups := accountGroupsForSystem(rc.Config.System, u.Groups)
	if err := requireNativeAdminGroup(rc.Config.System, groups); err != nil {
		return err
	}
	groups, missing := existingGroups(groups)
	for _, g := range missing {
		fmt.Printf("  ⚠ %s: group %s does not exist, skipped\n", u.Name, g)
	}
	if len(groups) > 0 {
		if _, err := rc.Runner.Run(ctx, "usermod", "-aG", strings.Join(groups, ","), u.Name); err != nil {
			return fmt.Errorf("adding groups: %w", err)
		}
	}

	if u.SudoNopasswd {
		if err := writeSudoers(ctx, rc, u.Name); err != nil {
			return err
		}
	}

	// A preserved home keeps its files but the uid/gid may have changed.
	if homeExists {
		if _, err := rc.Runner.Run(ctx, "chown", "-R",
			strconv.Itoa(u.UID)+":"+strconv.Itoa(u.GID), u.Home); err != nil {
			return fmt.Errorf("fixing ownership of %s: %w", u.Home, err)
		}
	}

	// Restore SSH keys only when the home has none of its own.
	if len(u.SSHPubkeys) > 0 && !rc.Runner.FileExists(filepath.Join(u.Home, ".ssh", "authorized_keys")) {
		if err := installAuthorizedKeys(ctx, rc, u.Name, u.Home, u.SSHPubkeys); err != nil {
			return err
		}
	}
	return nil
}

// RehomeUser moves a user's home from /home/<name> to the custom home base.
func RehomeUser(ctx context.Context, rc *RunContext, username string, removeOld bool) error {
	cfg := rc.Config.Users
	homeBase := cfg.HomeBase
	if homeBase == "" {
		return fmt.Errorf("home_base not configured")
	}
	if err := checkHomeBase(homeBase); err != nil {
		return err
	}

	u, err := user.Lookup(username)
	if err != nil {
		return fmt.Errorf("user %s not found: %w", username, err)
	}

	oldHome := u.HomeDir
	newHome := filepath.Join(homeBase, username)

	if oldHome == newHome {
		return fmt.Errorf("user %s already at %s", username, newHome)
	}

	// Refuse to merge into a populated destination: rsync would silently
	// interleave two homes and the later cleanup could not tell them apart.
	if entries, err := os.ReadDir(newHome); err == nil && len(entries) > 0 {
		return fmt.Errorf("destination %s already exists and is not empty", newHome)
	}

	// Copy files
	if _, err := rc.Runner.Run(ctx, "rsync", "-aH", oldHome+"/", newHome+"/"); err != nil {
		return fmt.Errorf("copying home: %w", err)
	}

	// Verify the copy before touching the original: a checksum dry-run must
	// report no differences.
	if !rc.DryRun {
		res, err := rc.Runner.Query(ctx, "rsync", "-aH", "--checksum", "--dry-run", "--itemize-changes", oldHome+"/", newHome+"/")
		if err != nil {
			return fmt.Errorf("verifying copied home: %w", err)
		}
		if diff := strings.TrimSpace(res.Stdout); diff != "" {
			return fmt.Errorf("copied home differs from original, leaving %s untouched:\n%s", oldHome, diff)
		}
	}

	// Fix ownership
	if _, err := rc.Runner.Run(ctx, "chown", "-R", u.Uid+":"+u.Gid, newHome); err != nil {
		return fmt.Errorf("fixing ownership of %s: %w", newHome, err)
	}

	// Update user home
	if _, err := rc.Runner.Run(ctx, "usermod", "--home", newHome, username); err != nil {
		return fmt.Errorf("updating user home: %w", err)
	}

	// Keep the original as a backup (removed only on explicit request) and
	// leave a compatibility symlink in its place.
	backup := fmt.Sprintf("%s.rootfiles-bak-%s", oldHome, time.Now().Format("20060102-150405"))
	if err := rc.Runner.Rename(oldHome, backup); err != nil {
		return fmt.Errorf("moving old home aside: %w", err)
	}
	if err := rc.Runner.Symlink(newHome, oldHome); err != nil {
		return fmt.Errorf("creating compatibility symlink %s: %w", oldHome, err)
	}
	if removeOld {
		if _, err := rc.Runner.Run(ctx, "rm", "-rf", "--one-file-system", backup); err != nil {
			return fmt.Errorf("removing old home backup %s: %w", backup, err)
		}
	} else {
		fmt.Printf("Original home kept at %s (delete it once verified)\n", backup)
	}

	fmt.Printf("User %s moved: %s → %s (symlink created)\n", username, oldHome, newHome)
	return nil
}

// ListUsers shows managed users from metadata.
func ListUsers(rc *RunContext) error {
	WarnUntrustedMetadata(rc)
	cfg := rc.Config.Users
	homeBase := cfg.HomeBase
	if homeBase == "" {
		homeBase = "/home"
	}

	dbPath := filepath.Join(homeBase, ".rootfiles", "users.json")
	data, err := rc.Runner.ReadFile(dbPath)
	if err != nil {
		fmt.Println("No managed users found.")
		return nil
	}

	var db UsersDB
	if err := json.Unmarshal(data, &db); err != nil {
		return fmt.Errorf("parsing user database: %w", err)
	}

	fmt.Printf("Home base: %s\n", db.HomeBase)
	fmt.Printf("%-15s %-6s %-20s %-15s %s\n", "USER", "UID", "HOME", "SHELL", "GROUPS")
	fmt.Printf("%-15s %-6s %-20s %-15s %s\n", "----", "---", "----", "-----", "------")
	for _, u := range db.Users {
		fmt.Printf("%-15s %-6d %-20s %-15s %s\n",
			u.Name, u.UID, u.Home, u.Shell, strings.Join(u.Groups, ","))
	}
	return nil
}

// ListUserNames prints only usernames, one per line (for scripting).
func ListUserNames(rc *RunContext) error {
	cfg := rc.Config.Users
	homeBase := cfg.HomeBase
	if homeBase == "" {
		homeBase = "/home"
	}

	dbPath := filepath.Join(homeBase, ".rootfiles", "users.json")
	data, err := rc.Runner.ReadFile(dbPath)
	if err != nil {
		return nil // no users, no output
	}

	var db UsersDB
	if err := json.Unmarshal(data, &db); err != nil {
		return fmt.Errorf("parsing user database: %w", err)
	}

	for _, u := range db.Users {
		fmt.Println(u.Name)
	}
	return nil
}

// ListSystemUsers outputs all system users (UID 1000-65533) in a table.
func ListSystemUsers(ctx context.Context, rc *RunContext) error {
	users, err := scanSystemUsers(ctx, rc)
	if err != nil {
		return fmt.Errorf("scanning system users: %w", err)
	}
	if len(users) == 0 {
		fmt.Println("No system users found.")
		return nil
	}
	fmt.Printf("%-15s %-6s %-6s %-20s %-15s %s\n", "USER", "UID", "GID", "HOME", "SHELL", "GROUPS")
	fmt.Printf("%-15s %-6s %-6s %-20s %-15s %s\n", "----", "---", "---", "----", "-----", "------")
	for _, u := range users {
		fmt.Printf("%-15s %-6d %-6d %-20s %-15s %s\n",
			u.Name, u.UID, u.GID, u.Home, u.Shell, strings.Join(u.Groups, ","))
	}
	return nil
}

// ListSystemUserNames outputs system usernames only, one per line.
func ListSystemUserNames(ctx context.Context, rc *RunContext) error {
	users, err := scanSystemUsers(ctx, rc)
	if err != nil {
		return fmt.Errorf("scanning system users: %w", err)
	}
	for _, u := range users {
		fmt.Println(u.Name)
	}
	return nil
}

// ShowUserID displays UID/GID/groups for a user via the `id` command.
func ShowUserID(ctx context.Context, rc *RunContext, username string) error {
	if _, err := user.Lookup(username); err != nil {
		return fmt.Errorf("user %s not found", username)
	}
	result, err := rc.Runner.Query(ctx, "id", username)
	if err != nil {
		return fmt.Errorf("querying id for %s: %w", username, err)
	}
	fmt.Print(result.Stdout)
	return nil
}

// ListGroups displays all groups from /etc/group in a table.
func ListGroups(ctx context.Context, rc *RunContext) error {
	data, err := rc.Runner.ReadFile("/etc/group")
	if err != nil {
		return fmt.Errorf("reading /etc/group: %w", err)
	}
	fmt.Printf("%-20s %-6s %s\n", "GROUP", "GID", "MEMBERS")
	fmt.Printf("%-20s %-6s %s\n", "-----", "---", "-------")
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 4 {
			continue
		}
		name := fields[0]
		gid := fields[2]
		members := fields[3]
		fmt.Printf("%-20s %-6s %s\n", name, gid, members)
	}
	return nil
}

// ListUserGroups displays groups for a specific user.
func ListUserGroups(ctx context.Context, rc *RunContext, username string) error {
	if _, err := user.Lookup(username); err != nil {
		return fmt.Errorf("user %s not found", username)
	}
	result, err := rc.Runner.Query(ctx, "id", username)
	if err != nil {
		return fmt.Errorf("querying id for %s: %w", username, err)
	}
	fmt.Print(result.Stdout)
	return nil
}

// AddUserToGroups adds a user to the specified groups.
func AddUserToGroups(ctx context.Context, rc *RunContext, username string, groups []string) error {
	if _, err := user.Lookup(username); err != nil {
		return fmt.Errorf("user %s not found", username)
	}
	groups = accountGroupsForSystem(rc.Config.System, groups)
	if len(groups) == 0 {
		return fmt.Errorf("no groups specified")
	}
	if err := requireNativeAdminGroup(rc.Config.System, groups); err != nil {
		return err
	}
	if _, err := rc.Runner.Run(ctx, "usermod", "-aG", strings.Join(groups, ","), username); err != nil {
		return fmt.Errorf("adding %s to groups: %w", username, err)
	}
	fmt.Printf("User %s added to groups: %s\n", username, strings.Join(groups, ", "))
	return nil
}

// RemoveUserFromGroups removes a user from the specified groups.
func RemoveUserFromGroups(ctx context.Context, rc *RunContext, username string, groups []string) error {
	if _, err := user.Lookup(username); err != nil {
		return fmt.Errorf("user %s not found", username)
	}
	groups = accountGroupsForSystem(rc.Config.System, groups)
	if len(groups) == 0 {
		return fmt.Errorf("no groups specified")
	}
	if err := requireNativeAdminGroup(rc.Config.System, groups); err != nil {
		return err
	}
	for _, group := range groups {
		if _, err := rc.Runner.Run(ctx, "gpasswd", "-d", username, group); err != nil {
			fmt.Printf("  Failed to remove %s from %s: %v\n", username, group, err)
		} else {
			fmt.Printf("  Removed %s from %s\n", username, group)
		}
	}
	return nil
}

// PasswordEntry represents a username-password pair for batch password setting.
type PasswordEntry struct {
	Username string
	Password string // empty means auto-generate (username + suffix)
}

// SetPasswords sets passwords for the given entries. Entries without a
// password get username+suffix when suffix is set (legacy scheme), otherwise
// a random password that is printed once. With expire, users must change
// the password at next login.
func SetPasswords(ctx context.Context, rc *RunContext, entries []PasswordEntry, suffix string, expire bool) error {
	var failed []string
	for _, e := range entries {
		pass := e.Password
		generated := ""
		if pass == "" {
			if suffix != "" {
				pass = e.Username + suffix
			} else {
				p, err := generatePassword(16)
				if err != nil {
					return fmt.Errorf("generating password: %w", err)
				}
				pass, generated = p, p
			}
		}
		if rc.DryRun {
			fmt.Printf("  [dry-run] set password for %s (password: ****)\n", e.Username)
			continue
		}
		if err := setPassword(ctx, rc, e.Username, pass); err != nil {
			fmt.Printf("  Failed to set password for %s: %s\n", e.Username, firstLine(err.Error()))
			failed = append(failed, e.Username)
			continue
		}
		if expire {
			if _, err := rc.Runner.Run(ctx, "chage", "-d", "0", e.Username); err != nil {
				fmt.Printf("  Password set for %s, but expiring it failed: %s\n", e.Username, firstLine(err.Error()))
				failed = append(failed, e.Username)
				continue
			}
		}
		if generated != "" {
			fmt.Printf("  Password set for %s: %s\n", e.Username, generated)
		} else {
			fmt.Printf("  Password set for %s\n", e.Username)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("failed to set password for: %s", strings.Join(failed, ", "))
	}
	return nil
}

// LoadPasswordFile reads a password file and returns PasswordEntry list.
// Lines with commas are treated as "username,password"; otherwise username-only (auto-generate).
func LoadPasswordFile(path, suffix string) ([]PasswordEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading password file: %w", err)
	}
	var entries []PasswordEntry
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if idx := strings.Index(line, ","); idx > 0 {
			entries = append(entries, PasswordEntry{
				Username: strings.TrimSpace(line[:idx]),
				Password: strings.TrimSpace(line[idx+1:]),
			})
		} else {
			entries = append(entries, PasswordEntry{
				Username: line,
			})
		}
	}
	return entries, nil
}

func saveUserMeta(rc *RunContext, username, home, shell string, groups []string, sudoNopasswd bool, pubkeys []string) error {
	cfg := rc.Config.Users
	homeBase := cfg.HomeBase
	if homeBase == "" {
		homeBase = "/home"
	}

	if err := ensureMetaDir(rc.Runner, homeBase); err != nil {
		return err
	}
	dbPath := filepath.Join(homeBase, ".rootfiles", "users.json")
	var db UsersDB

	if data, err := rc.Runner.ReadFile(dbPath); err == nil {
		if err := json.Unmarshal(data, &db); err != nil {
			return fmt.Errorf("parsing %s: %w", dbPath, err)
		}
	}
	if db.Version == 0 {
		db.Version = 1
		db.HomeBase = homeBase
		db.CreatedBy = "rootfiles-v2"
	}

	meta := UserMeta{
		Name:         username,
		Shell:        shell,
		Groups:       groups,
		SudoNopasswd: sudoNopasswd,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
		Home:         home,
	}
	// The account does not exist in dry-run; record what we can.
	if u, err := user.Lookup(username); err == nil {
		meta.UID, _ = strconv.Atoi(u.Uid)
		meta.GID, _ = strconv.Atoi(u.Gid)
	} else if !rc.DryRun {
		return fmt.Errorf("looking up %s: %w", username, err)
	}
	meta.SSHPubkeys = pubkeys

	// Update or append
	found := false
	for i, existing := range db.Users {
		if existing.Name == username {
			db.Users[i] = meta
			found = true
			break
		}
	}
	if !found {
		db.Users = append(db.Users, meta)
	}

	data, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return err
	}
	if err := refuseSymlink(dbPath); err != nil {
		return err
	}
	return rc.Runner.WriteFile(dbPath, data, 0600)
}
