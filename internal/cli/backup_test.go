package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

func TestNewBackupCmd_Basics(t *testing.T) {
	cmd := newBackupCmd()
	if cmd.Use != "backup" {
		t.Errorf("Use = %q, want backup", cmd.Use)
	}
	if cmd.RunE == nil {
		t.Error("RunE is nil")
	}
}

func TestBackupCmd_Flags(t *testing.T) {
	cmd := newBackupCmd()
	for _, tc := range []struct {
		name       string
		defaultVal string
	}{
		{"output", "/raid/backup"},
		{"skip-docker", "false"},
		{"skip-etc", "false"},
	} {
		f := cmd.Flags().Lookup(tc.name)
		if f == nil {
			t.Errorf("missing flag %q", tc.name)
			continue
		}
		if f.DefValue != tc.defaultVal {
			t.Errorf("flag %q default = %q, want %q", tc.name, f.DefValue, tc.defaultVal)
		}
	}
}

// runBackup executes `backup` with stdout and stderr parked in temp files so
// the progress report and the runner log stay out of the test output.
func runBackup(t *testing.T, args ...string) error {
	t.Helper()
	dir := t.TempDir()
	stdout, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdout, stderr
	t.Cleanup(func() { os.Stdout, os.Stderr = oldOut, oldErr })
	root := NewRootCmd("test", "abc")
	root.SetArgs(append([]string{"backup"}, args...))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	err = root.Execute()
	os.Stdout, os.Stderr = oldOut, oldErr
	stdout.Close()
	stderr.Close()
	return err
}

// backupStubs puts tar, crontab and docker stubs first on PATH. The tar stub
// creates an empty archive, so it honours the umask the real run sets.
func backupStubs(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	stubs := map[string]string{
		"tar":     "#!/bin/sh\n: > \"$2\"\n",
		"crontab": "#!/bin/sh\necho '0 0 * * * /usr/bin/true'\n",
		"docker":  "#!/bin/sh\necho 'img:latest 1GB abc123'\n",
	}
	for name, script := range stubs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

// stubSnapshot replaces the live system snapshot; token lands in the
// cloudflared config, which the backup must strip from the snapshot file.
func stubSnapshot(t *testing.T, token string) {
	t.Helper()
	old := snapshotCurrent
	snapshotCurrent = func() (*config.Config, error) {
		cfg := &config.Config{}
		cfg.Modules.Cloudflared.TunnelToken = token
		return cfg, nil
	}
	t.Cleanup(func() { snapshotCurrent = old })
}

// backupFixtures points the archived paths at temp fixtures so the run is
// deterministic and never stats host paths.
func backupFixtures(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	sshDir := filepath.Join(dir, "root-ssh")
	binDir := filepath.Join(dir, "usr-local-bin")
	etcDir := filepath.Join(dir, "sshd_config.d")
	for _, d := range []string{sshDir, binDir, etcDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(sshDir, "id_ed25519"):    "PRIVATE KEY",
		filepath.Join(binDir, "tool"):          "#!/bin/sh\n",
		filepath.Join(etcDir, "99-local.conf"): "PasswordAuthentication no\n",
	}
	for name, content := range files {
		if err := os.WriteFile(name, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	oldSSH, oldBin, oldEtc := rootSSHDir, usrLocalBinDir, etcBackupCandidates
	rootSSHDir, usrLocalBinDir = sshDir, binDir
	etcBackupCandidates = []string{etcDir, filepath.Join(dir, "missing")}
	t.Cleanup(func() {
		rootSSHDir, usrLocalBinDir, etcBackupCandidates = oldSSH, oldBin, oldEtc
	})
}

// AC1: a dry run creates no directory and no file.
func TestBackupCmd_DryRunWritesNothing(t *testing.T) {
	noHostCommands(t)
	stubSnapshot(t, "secret-token")
	out := filepath.Join(t.TempDir(), "backups")
	if err := runBackup(t, "--output", out, "--dry-run"); err != nil {
		t.Fatalf("backup --dry-run: %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("dry run created %s", out)
	}
}

// AC2 + AC3: a real backup lands in a 0700 directory, every file in it,
// archives included, is 0600, and the snapshot never carries the tunnel token.
func TestBackupCmd_RealRunIsRootOnly(t *testing.T) {
	backupStubs(t)
	backupFixtures(t)
	stubSnapshot(t, "secret-token-123")
	out := filepath.Join(t.TempDir(), "backups")
	if err := runBackup(t, "--output", out); err != nil {
		t.Fatalf("backup: %v", err)
	}
	entries, err := os.ReadDir(out)
	if err != nil || len(entries) != 1 {
		t.Fatalf("output entries = %v, err = %v; want the one backup directory", entries, err)
	}
	backupDir := filepath.Join(out, entries[0].Name())
	if fi, err := os.Stat(backupDir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("backup dir mode = %v, err = %v; want 0700", fi.Mode().Perm(), err)
	}
	want := []string{
		"system-info.json", "users.json", "etc-config.tar.gz", "crontab-root.txt",
		"root-ssh.tar.gz", "usr-local-bin.tar.gz", "docker-images.txt", "config-snapshot.yaml",
	}
	for _, name := range want {
		fi, err := os.Stat(filepath.Join(backupDir, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o, want 600", name, fi.Mode().Perm())
		}
	}
	snapshot, err := os.ReadFile(filepath.Join(backupDir, "config-snapshot.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(snapshot), "secret-token-123") {
		t.Error("config-snapshot.yaml contains the tunnel token")
	}
}

// AC4: an existing output directory writable by group or others, which a user
// other than root could control, is refused before anything is written.
func TestBackupCmd_RefusesUntrustedOutputDir(t *testing.T) {
	backupStubs(t)
	stubSnapshot(t, "")
	out := t.TempDir()
	if err := os.Chmod(out, 0o777); err != nil {
		t.Fatal(err)
	}
	err := runBackup(t, "--output", out)
	if err == nil || !strings.Contains(err.Error(), "writable by group or others") {
		t.Fatalf("backup err = %v, want a group/other-writable refusal", err)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Errorf("refused backup still wrote into %s: %v", out, entries)
	}
}

// The directory name is deterministic per day: a re-run into one an older
// run left at 0755 with 0644 files tightens both to 0700 and 0600.
func TestBackupCmd_RerunHardensExistingDir(t *testing.T) {
	backupStubs(t)
	backupFixtures(t)
	stubSnapshot(t, "")
	hostname, _ := os.Hostname()
	dirName := fmt.Sprintf("rootfiles-backup-%s-%s", hostname, time.Now().Format("20060102"))
	out := t.TempDir()
	backupDir := filepath.Join(out, dirName)
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"system-info.json", "root-ssh.tar.gz", "stale.txt"} {
		if err := os.WriteFile(filepath.Join(backupDir, name), []byte("old"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := runBackup(t, "--output", out); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if fi, err := os.Stat(backupDir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("reused backup dir mode = %v, err = %v; want 0700", fi.Mode().Perm(), err)
	}
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o, want 600", e.Name(), fi.Mode().Perm())
		}
	}
}

// A relative --output is resolved to an absolute path before the ownership
// walk, so it is accepted like any other root-only destination.
func TestBackupCmd_RelativeOutputDir(t *testing.T) {
	backupStubs(t)
	backupFixtures(t)
	stubSnapshot(t, "")
	// os.Getwd resolves symlinks (TMPDIR sits under /var -> /private/var on
	// macOS), so the walk root and the work directory must use the resolved
	// form; production walks from "/", where this cannot diverge.
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oldRoot := configTrustRoot
	if resolved, err := filepath.EvalSymlinks(configTrustRoot); err == nil {
		configTrustRoot = resolved
		t.Cleanup(func() { configTrustRoot = oldRoot })
	}
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(oldWd) })
	if err := runBackup(t, "--output", "backups"); err != nil {
		t.Fatalf("backup with relative --output: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(work, "backups"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("output entries = %v, err = %v; want the one backup directory", entries, err)
	}
	if fi, err := os.Stat(filepath.Join(work, "backups", entries[0].Name())); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("backup dir mode = %v, err = %v; want 0700", fi.Mode().Perm(), err)
	}
}
