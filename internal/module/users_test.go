package module

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

func TestUsersModule_Name(t *testing.T) {
	if n := NewUsersModule().Name(); n != "users" {
		t.Errorf("Name() = %q, want users", n)
	}
}

func TestUsersModule_CheckDefaultHomeBaseIsSatisfied(t *testing.T) {
	oldSudoersDir := sudoersDir
	sudoersDir = t.TempDir()
	t.Cleanup(func() { sudoersDir = oldSudoersDir })
	rc := newDryRunRC(t)
	useraddFixture(t, "HOME="+rc.Config.Users.HomeBase+"\n")
	result, err := NewUsersModule().Check(context.Background(), rc)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !result.Satisfied {
		t.Errorf("Check with default HomeBase should be satisfied, got %+v", result.Changes)
	}
}

// useraddFixture points useraddDefaultsFile at a temp file with content.
func useraddFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "useradd")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	old := useraddDefaultsFile
	useraddDefaultsFile = path
	t.Cleanup(func() { useraddDefaultsFile = old })
	return path
}

func TestUsersModule_CheckSyncsUseraddHome(t *testing.T) {
	cases := []struct {
		name, useradd, homeBase string
		want                    bool
	}{
		{"stock Ubuntu comment", "SHELL=/bin/sh\n# HOME=/home\n", "/home", true},
		{"stock Rocky", "GROUP=100\nHOME=/home\n", "/home", false},
		{"stale data drive", "HOME=/data/home\n", "/home", true},
		{"prefix is not a match", "HOME=/raid/home2\n", "/raid/home", true},
		{"in sync", "HOME=/raid/home\n", "/raid/home", false},
		{"trailing slash in config", "HOME=/raid/home\n", "/raid/home/", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldSudoersDir := sudoersDir
			sudoersDir = t.TempDir()
			t.Cleanup(func() { sudoersDir = oldSudoersDir })
			// /raid stands for a data drive under the test's temp root.
			raid := filepath.Join(t.TempDir(), "raid")
			rc := newDryRunRC(t)
			body := strings.ReplaceAll(tc.useradd, "/raid", raid)
			base := strings.ReplaceAll(tc.homeBase, "/raid", raid)
			if base == "/home" {
				base = rc.Config.Users.HomeBase
				body = strings.ReplaceAll(body, "HOME=/home", "HOME="+base)
			}
			path := useraddFixture(t, body)
			rc.Config.Users = config.UsersConfig{HomeBase: base}
			result, err := NewUsersModule().Check(context.Background(), rc)
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			got := false
			for _, c := range result.Changes {
				if strings.HasPrefix(c.Command, "update HOME=") && strings.HasSuffix(c.Command, " in "+path) {
					got = true
				}
			}
			if got != tc.want {
				t.Errorf("useradd change = %v, want %v (changes %+v)", got, tc.want, result.Changes)
			}
		})
	}
}

// Applying the chosen home base rewrites stale HOME= while preserving comments.
func TestUsersModule_ApplyWritesHomeToUseradd(t *testing.T) {
	oldSudoersDir := sudoersDir
	sudoersDir = t.TempDir()
	t.Cleanup(func() { sudoersDir = oldSudoersDir })
	path := useraddFixture(t, "SHELL=/bin/sh\n# HOME=/home\nHOME=/data/home\n")
	rc := newDryRunRC(t)
	rc.DryRun = false
	rc.Runner = guardedTestRunner(t, false, rc.Runner.Logger)
	home := rc.Config.Users.HomeBase
	rc.Config.Users = config.UsersConfig{HomeBase: home}
	if _, err := NewUsersModule().Apply(context.Background(), rc); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := config.UseraddHome(data); got != home {
		t.Errorf("useradd HOME = %q, want %q:\n%s", got, home, data)
	}
	if want := "SHELL=/bin/sh\n# HOME=/home\nHOME=" + home + "\n"; string(data) != want {
		t.Errorf("useradd = %q, want %q", data, want)
	}
	result, err := NewUsersModule().Check(context.Background(), rc)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !result.Satisfied {
		t.Errorf("Check after Apply not satisfied: %+v", result.Changes)
	}
}

// An unreadable useradd file is not rewritten from nothing.
func TestUsersModule_ApplyRefusesUnreadableUseradd(t *testing.T) {
	oldSudoersDir := sudoersDir
	sudoersDir = t.TempDir()
	t.Cleanup(func() { sudoersDir = oldSudoersDir })
	old := useraddDefaultsFile
	useraddDefaultsFile = t.TempDir() // a directory: ReadFile fails with EISDIR
	t.Cleanup(func() { useraddDefaultsFile = old })
	rc := newDryRunRC(t)
	rc.DryRun = false
	rc.Runner = guardedTestRunner(t, false, rc.Runner.Logger)
	home := rc.Config.Users.HomeBase
	rc.Config.Users = config.UsersConfig{HomeBase: home}
	if _, err := NewUsersModule().Apply(context.Background(), rc); err == nil || !strings.Contains(err.Error(), "reading") {
		t.Fatalf("Apply error = %v, want a read error", err)
	}
}

// A home base written with a trailing slash still converges.
func TestUsersModule_ApplySettlesUncleanHomeBase(t *testing.T) {
	oldSudoersDir := sudoersDir
	sudoersDir = t.TempDir()
	t.Cleanup(func() { sudoersDir = oldSudoersDir })
	path := useraddFixture(t, "# HOME=/home\n")
	rc := newDryRunRC(t)
	rc.DryRun = false
	rc.Runner = guardedTestRunner(t, false, rc.Runner.Logger)
	base := t.TempDir()
	rc.Config.Users = config.UsersConfig{HomeBase: base + "/"}
	for i, wantChanged := range []bool{true, false} {
		result, err := NewUsersModule().Apply(context.Background(), rc)
		if err != nil {
			t.Fatalf("Apply %d: %v", i, err)
		}
		if result.Changed != wantChanged {
			t.Errorf("Apply %d changed = %v, want %v (%v)", i, result.Changed, wantChanged, result.Messages)
		}
	}
	check, err := NewUsersModule().Check(context.Background(), rc)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !check.Satisfied {
		t.Errorf("Check after Apply not satisfied: %+v", check.Changes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "# HOME=/home\nHOME=" + base + "\n"; string(data) != want {
		t.Errorf("useradd = %q, want %q", data, want)
	}
}

func TestUsersModule_ApplyCustomHomeBaseDryRun(t *testing.T) {
	oldSudoersDir := sudoersDir
	sudoersDir = t.TempDir()
	t.Cleanup(func() { sudoersDir = oldSudoersDir })
	tmp := t.TempDir()
	rc := newDryRunRC(t)
	rc.Config.Users = config.UsersConfig{HomeBase: filepath.Join(tmp, "home2")}
	result, err := NewUsersModule().Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Changed {
		t.Error("Apply with custom HomeBase should report Changed=true")
	}
}

// AC1, AC2: a custom home base that a user other than root could control is
// reported by Check and refused by Apply and the home-creating commands
// before anything changes.
func TestUsersModule_RefusesUntrustedHomeBase(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, parent string) string
		want  string
	}{
		{"group-writable base", func(t *testing.T, parent string) string {
			base := filepath.Join(parent, "home")
			if err := os.Mkdir(base, 0o775); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(base, 0o775); err != nil {
				t.Fatal(err)
			}
			return base
		}, "writable by group or others"},
		{"world-writable parent", func(t *testing.T, parent string) string {
			if err := os.Chmod(parent, 0o1777); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(parent, "home")
		}, "writable by group or others"},
		{"world-writable ancestor above a missing parent", func(t *testing.T, parent string) string {
			if err := os.Chmod(parent, 0o777); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(parent, "team", "home")
		}, "writable by group or others"},
		{"world-writable ancestor above a root-owned parent", func(t *testing.T, parent string) string {
			team := filepath.Join(parent, "team")
			if err := os.Mkdir(team, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(parent, 0o777); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(team, "home")
		}, "writable by group or others"},
		{"symlinked base", func(t *testing.T, parent string) string {
			base := filepath.Join(parent, "home")
			if err := os.Symlink(t.TempDir(), base); err != nil {
				t.Fatal(err)
			}
			return base
		}, "is a symlink"},
		{"not owned by root", func(t *testing.T, parent string) string {
			old := homeBaseOwner
			homeBaseOwner = old + 1
			t.Cleanup(func() { homeBaseOwner = old })
			return filepath.Join(parent, "home")
		}, "not owned by root"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldSudoersDir := sudoersDir
			sudoersDir = t.TempDir()
			t.Cleanup(func() { sudoersDir = oldSudoersDir })
			path := useraddFixture(t, "# HOME=/home\n")
			parent := t.TempDir()
			if err := os.Chmod(parent, 0o755); err != nil {
				t.Fatal(err)
			}
			base := tc.setup(t, parent)
			rc := newDryRunRC(t)
			rc.DryRun = false
			rc.Runner = guardedTestRunner(t, false, rc.Runner.Logger)
			rc.Config.Users = config.UsersConfig{HomeBase: base}
			ctx := context.Background()
			wantErr := func(what string, err error) {
				t.Helper()
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Errorf("%s error = %v, want %q", what, err, tc.want)
				}
			}
			_, err := NewUsersModule().Check(ctx, rc)
			wantErr("users Check", err)
			_, err = NewUsersModule().Apply(ctx, rc)
			wantErr("users Apply", err)
			_, err = NewStorageModule().Check(ctx, rc)
			wantErr("storage Check", err)
			_, err = NewStorageModule().Apply(ctx, rc)
			wantErr("storage Apply", err)
			dry := newDryRunRC(t)
			dry.Config.Users = rc.Config.Users
			wantErr("AddUser", AddUser(ctx, dry, "rootfiles-test-nobody", nil, nil, true))
			wantErr("RehomeUser", RehomeUser(ctx, dry, "rootfiles-test-nobody", false))
			backup := filepath.Join(t.TempDir(), "users.json")
			db, _ := json.Marshal(UsersDB{Users: []UserMeta{{Name: "rootfiles-test-nobody", Shell: "/bin/sh", Home: filepath.Join(base, "rootfiles-test-nobody")}}})
			if err := os.WriteFile(backup, db, 0o600); err != nil {
				t.Fatal(err)
			}
			wantErr("RestoreUsers", RestoreUsers(ctx, dry, backup))
			wantErr("RestoreUsers from the base", RestoreUsers(ctx, dry, ""))

			if data, _ := os.ReadFile(path); string(data) != "# HOME=/home\n" {
				t.Errorf("useradd defaults changed: %q", data)
			}
			if _, err := os.Lstat(filepath.Join(base, ".rootfiles")); !os.IsNotExist(err) {
				t.Errorf("metadata dir created under refused base: %v", err)
			}
		})
	}
}

// AC3: a root-owned 0755 base is unaffected.
func TestUsersModule_AcceptsRootOwnedHomeBase(t *testing.T) {
	oldSudoersDir := sudoersDir
	sudoersDir = t.TempDir()
	t.Cleanup(func() { sudoersDir = oldSudoersDir })
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(parent, "home")
	if err := os.Mkdir(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	// Missing directories below a root-owned one are created by root.
	for _, base := range []string{existing, filepath.Join(parent, "team", "home")} {
		useraddFixture(t, "# HOME=/home\n")
		rc := newDryRunRC(t)
		rc.DryRun = false
		rc.Runner = guardedTestRunner(t, false, rc.Runner.Logger)
		rc.Config.Users = config.UsersConfig{HomeBase: base}
		result, err := NewUsersModule().Apply(context.Background(), rc)
		if err != nil || !result.Changed {
			t.Fatalf("%s: Apply = %+v, %v; want a change", base, result, err)
		}
		if check, err := NewUsersModule().Check(context.Background(), rc); err != nil || !check.Satisfied {
			t.Errorf("%s: Check after Apply = %+v, %v; want satisfied", base, check, err)
		}
	}
}

func TestListUserNames(t *testing.T) {
	// Create temp dir with a users.json
	tmpDir := t.TempDir()
	metaDir := filepath.Join(tmpDir, ".rootfiles")
	os.MkdirAll(metaDir, 0755)

	db := UsersDB{
		Version:  1,
		HomeBase: tmpDir,
		Users: []UserMeta{
			{Name: "alice", UID: 1001, Home: filepath.Join(tmpDir, "alice")},
			{Name: "bob", UID: 1002, Home: filepath.Join(tmpDir, "bob")},
			{Name: "charlie", UID: 1003, Home: filepath.Join(tmpDir, "charlie")},
		},
	}
	data, _ := json.MarshalIndent(db, "", "  ")
	os.WriteFile(filepath.Join(metaDir, "users.json"), data, 0600)

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	runner := guardedTestRunner(t, true, logger)
	rc := &RunContext{
		Config: &config.Config{
			Users: config.UsersConfig{HomeBase: tmpDir},
		},
		Runner: runner,
		DryRun: true,
	}

	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := ListUserNames(rc)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if output != "alice\nbob\ncharlie\n" {
		t.Errorf("unexpected output:\n%s\nwant: alice\\nbob\\ncharlie\\n", output)
	}
}

func TestListUserNames_NoDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	runner := guardedTestRunner(t, true, logger)
	rc := &RunContext{
		Config: &config.Config{
			Users: config.UsersConfig{HomeBase: tmpDir},
		},
		Runner: runner,
		DryRun: true,
	}

	err := ListUserNames(rc)
	if err != nil {
		t.Fatalf("should return nil when no database exists, got: %v", err)
	}
}

func TestBackupUsers_MergesSystemUsers(t *testing.T) {
	systemHome := fixtureSystemUsers(t)
	tmpDir := t.TempDir()
	metaDir := filepath.Join(tmpDir, ".rootfiles")
	os.MkdirAll(metaDir, 0755)

	// Existing managed user
	db := UsersDB{
		Version:   1,
		HomeBase:  tmpDir,
		CreatedBy: "rootfiles-v2",
		Users: []UserMeta{
			{Name: "managed", UID: 1001, GID: 1001, Shell: "/bin/bash", Home: filepath.Join(tmpDir, "managed")},
		},
	}
	data, _ := json.MarshalIndent(db, "", "  ")
	os.WriteFile(filepath.Join(metaDir, "users.json"), data, 0600)

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	runner := guardedTestRunner(t, false, logger)
	rc := &RunContext{
		Config: &config.Config{
			Users: config.UsersConfig{HomeBase: tmpDir},
		},
		Runner: runner,
		DryRun: false,
	}

	outputPath := filepath.Join(tmpDir, "backup.json")

	// Capture stdout
	old := os.Stdout
	_, w, _ := os.Pipe()
	os.Stdout = w

	err := BackupUsers(rc, outputPath)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("BackupUsers failed: %v", err)
	}

	// Read output and verify managed user is present
	outData, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("reading backup: %v", err)
	}

	var result UsersDB
	if err := json.Unmarshal(outData, &result); err != nil {
		t.Fatalf("parsing backup: %v", err)
	}

	// At minimum the managed user must be in the output
	found := false
	for _, u := range result.Users {
		if u.Name == "managed" {
			found = true
			break
		}
	}
	if !found {
		t.Error("managed user not found in backup output")
	}

	// The fixture's eligible system user is merged with the managed user.
	if len(result.Users) != 2 {
		t.Errorf("expected managed and fixture system users, got %+v", result.Users)
	}
	foundSystem := false
	for _, u := range result.Users {
		if u.Name == "fixture-user" && u.Home == systemHome && len(u.Groups) == 1 && u.Groups[0] == "sudo" {
			foundSystem = true
		}
	}
	if !foundSystem {
		t.Errorf("fixture system user or supplementary groups missing: %+v", result.Users)
	}
}

func TestRestoreUsers_RestoresSSHKeys(t *testing.T) {
	fakeMonitoringCommand(t, "id", "#!/bin/sh\nexit 1\n")
	tmpDir := t.TempDir()

	// Create a backup file with SSH pubkeys
	db := UsersDB{
		Version:  1,
		HomeBase: tmpDir,
		Users: []UserMeta{
			{
				Name:       "testuser",
				UID:        9901,
				GID:        9901,
				Shell:      "/bin/bash",
				Home:       filepath.Join(tmpDir, "testuser"),
				SSHPubkeys: []string{"ssh-ed25519 AAAAC3Nz_test_key user@test"},
			},
		},
	}
	backupData, _ := json.MarshalIndent(db, "", "  ")
	backupPath := filepath.Join(tmpDir, "backup.json")
	os.WriteFile(backupPath, backupData, 0600)

	// Create the home directory (simulating preserved home WITHOUT .ssh)
	homeDir := filepath.Join(tmpDir, "testuser")
	os.MkdirAll(homeDir, 0755)

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	runner := guardedTestRunner(t, true, logger) // dry-run: won't actually call useradd
	rc := &RunContext{
		Config: &config.Config{
			Users: config.UsersConfig{HomeBase: tmpDir},
		},
		Runner: runner,
		DryRun: true,
	}

	// Capture stdout
	old := os.Stdout
	_, w, _ := os.Pipe()
	os.Stdout = w

	err := RestoreUsers(context.Background(), rc, backupPath)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("dry-run restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(homeDir, ".ssh")); !os.IsNotExist(err) {
		t.Fatalf("dry-run restore wrote SSH keys: %v", err)
	}
}

func TestScanSystemUsers_ParsesPasswd(t *testing.T) {
	home := fixtureSystemUsers(t)

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	runner := guardedTestRunner(t, false, logger)
	rc := &RunContext{
		Config: &config.Config{},
		Runner: runner,
	}

	users, err := scanSystemUsers(context.Background(), rc)
	if err != nil {
		t.Fatalf("scanSystemUsers failed: %v", err)
	}

	if len(users) != 1 || users[0].Name != "fixture-user" || users[0].Home != home || users[0].UID != 1001 || users[0].GID != 1001 {
		t.Fatalf("parsed users = %+v, want only fixture-user", users)
	}
	if len(users[0].Groups) != 1 || users[0].Groups[0] != "sudo" {
		t.Errorf("supplementary groups = %v, want [sudo]", users[0].Groups)
	}
	// Every returned user should have UID >= 1000
	for _, u := range users {
		if u.UID < 1000 || u.UID > 65533 {
			t.Errorf("user %s has UID %d outside expected range", u.Name, u.UID)
		}
		if u.Shell == "" {
			t.Errorf("user %s has empty shell", u.Name)
		}
		if u.Home == "" {
			t.Errorf("user %s has empty home", u.Name)
		}
		if strings.HasSuffix(u.Shell, "/nologin") || strings.HasSuffix(u.Shell, "/false") {
			t.Errorf("user %s has nologin shell %s, should have been filtered", u.Name, u.Shell)
		}
	}
}

func TestKeyIDAndMissingKeys(t *testing.T) {
	if keyID(`from="10.0.0.0/8" ssh-ed25519 AAAAblob user@host`) != "ssh-ed25519 AAAAblob" {
		t.Error("options/comment should be ignored")
	}
	p := filepath.Join(t.TempDir(), "authorized_keys")
	os.WriteFile(p, []byte("# keys\nssh-ed25519 AAAAone old-comment\n"), 0600)
	missing := missingKeys(p, []string{"ssh-ed25519 AAAAone new-comment", "ssh-rsa AAAAtwo"})
	if len(missing) != 1 || !strings.HasPrefix(missing[0], "ssh-rsa") {
		t.Errorf("missingKeys = %v", missing)
	}
}

func TestKeyComment(t *testing.T) {
	if c := keyComment(`no-pty ssh-ed25519 AAAAx alice@laptop work`); c != "alice@laptop work" {
		t.Errorf("keyComment = %q", c)
	}
	if c := keyComment("ssh-rsa AAAAy"); c != "" {
		t.Errorf("keyComment without comment = %q", c)
	}
}

// fixtureSystemUsers isolates passwd records, homes and group lookup together.
func fixtureSystemUsers(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "fixture-user")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "passwd")
	body := "# fixture accounts\nroot:x:0:0::" + dir + ":/bin/bash\n" +
		"fixture-user:x:1001:1001::" + home + ":/bin/bash\n" +
		"service:x:1002:1002::" + dir + ":/usr/sbin/nologin\n" +
		"disabled:x:1003:1003::" + dir + ":/bin/false\n" +
		"overflow:x:65534:65534::" + dir + ":/bin/bash\n" +
		"malformed:x:not-a-uid:1004::" + dir + ":/bin/bash\n"
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	oldPasswd, oldSudoers := passwdPath, sudoersDir
	passwdPath, sudoersDir = p, filepath.Join(dir, "sudoers.d")
	t.Cleanup(func() { passwdPath, sudoersDir = oldPasswd, oldSudoers })
	fakeMonitoringCommand(t, "id", "#!/bin/sh\nprintf 'fixture-user sudo\\n'\n")
	return home
}
