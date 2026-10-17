package module

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/exec"
)

// untrustedMetaBases builds home bases whose metadata a user other than
// root could control, each with a users.json listing "mallory".
var untrustedMetaBases = []struct {
	name  string
	setup func(t *testing.T, parent string) string
	want  string
}{
	{"world-writable parent", func(t *testing.T, parent string) string {
		base := writeMeta(t, filepath.Join(parent, "home"))
		mustChmod(t, parent, 0o777)
		return base
	}, "writable by group or others"},
	{"group-writable .rootfiles", func(t *testing.T, parent string) string {
		base := writeMeta(t, filepath.Join(parent, "home"))
		mustChmod(t, filepath.Join(base, ".rootfiles"), 0o775)
		return base
	}, "writable by group or others"},
	{"symlinked .rootfiles", func(t *testing.T, parent string) string {
		base := filepath.Join(parent, "home")
		if err := os.Mkdir(base, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(writeMeta(t, t.TempDir()), ".rootfiles"), filepath.Join(base, ".rootfiles")); err != nil {
			t.Fatal(err)
		}
		return base
	}, "is a symlink"},
	{"group-writable users.json", func(t *testing.T, parent string) string {
		base := writeMeta(t, filepath.Join(parent, "home"))
		mustChmod(t, filepath.Join(base, ".rootfiles", "users.json"), 0o664)
		return base
	}, "writable by group or others"},
	{"symlink planted inside .rootfiles", func(t *testing.T, parent string) string {
		base := writeMeta(t, filepath.Join(parent, "home"))
		if err := os.Symlink(filepath.Join(t.TempDir(), "x"), filepath.Join(base, ".rootfiles", "gpu-allocations.json.tmp")); err != nil {
			t.Fatal(err)
		}
		return base
	}, "is a symlink"},
	{"group-writable legacy flat file", func(t *testing.T, parent string) string {
		base := filepath.Join(parent, "home")
		if err := os.Mkdir(base, 0o755); err != nil {
			t.Fatal(err)
		}
		db, _ := json.Marshal(UsersDB{Users: []UserMeta{{Name: "mallory"}}})
		if err := os.WriteFile(filepath.Join(base, ".rootfiles"), db, 0o666); err != nil {
			t.Fatal(err)
		}
		mustChmod(t, filepath.Join(base, ".rootfiles"), 0o666)
		return base
	}, "writable by group or others"},
}

func writeMeta(t *testing.T, base string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(base, ".rootfiles"), 0o755); err != nil {
		t.Fatal(err)
	}
	db, _ := json.Marshal(UsersDB{Users: []UserMeta{{Name: "mallory", Shell: "/bin/sh", Home: "/home/mallory", SudoNopasswd: true}}})
	if err := os.WriteFile(filepath.Join(base, ".rootfiles", "users.json"), db, 0o600); err != nil {
		t.Fatal(err)
	}
	return base
}

func mustChmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// AC1: commands that write the metadata or act on it refuse before any
// change.
func TestMetadataCommandsRefuseUntrustedBase(t *testing.T) {
	for _, tc := range untrustedMetaBases {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			mustChmod(t, parent, 0o755)
			rc := newDryRunRC(t)
			rc.Config.Users.HomeBase = tc.setup(t, parent)
			ctx := context.Background()
			name := "rootfiles-test-nobody"
			errs := map[string]error{
				"user del":    DeleteUser(ctx, rc, name, ArchiveHome),
				"key add":     AddKey(ctx, rc, name, "ssh-ed25519 AAAA k"),
				"key rm":      RemoveKey(ctx, rc, name, "k"),
				"quota set":   SetQuota(ctx, rc, name, "1G"),
				"gpu assign":  AssignGPUs(ctx, rc, name, []int{0}, "env"),
				"gpu revoke":  RevokeGPUs(ctx, rc, name),
				"user add":    AddUser(ctx, rc, name, nil, nil, true),
				"restore all": RestoreUsers(ctx, rc, ""),
			}
			_, errs["users Check"] = NewUsersModule().Check(ctx, rc)
			_, errs["storage Check"] = NewStorageModule().Check(ctx, rc)
			_, errs["gpu Check"] = NewGPUModule().Check(ctx, rc)
			_, errs["gpu Apply"] = NewGPUModule().Apply(ctx, rc)
			for what, err := range errs {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Errorf("%s error = %v, want %q", what, err, tc.want)
				}
			}
		})
	}
}

// AC1b: a backup leaves out the users database of an untrusted base.
func TestBackupUsersLeavesOutUntrustedDatabase(t *testing.T) {
	for _, tc := range untrustedMetaBases {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			mustChmod(t, parent, 0o755)
			rc := newDryRunRC(t)
			// BackupUsers writes through the runner, which a dry run gates.
			rc.DryRun = false
			rc.Runner = exec.NewRunner(false, rc.Runner.Logger)
			rc.Config.Users.HomeBase = tc.setup(t, parent)
			var warn bytes.Buffer
			old := warnOut
			warnOut = &warn
			t.Cleanup(func() { warnOut = old })
			out := filepath.Join(t.TempDir(), "backup.json")
			if err := BackupUsers(rc, out); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "mallory") {
				t.Errorf("backup carries the untrusted users database:\n%s", data)
			}
			if !strings.Contains(warn.String(), tc.want) {
				t.Errorf("warning = %q, want %q", warn.String(), tc.want)
			}
		})
	}
}

// AC2: a database write never follows a symlink, even under a trusted base.
func TestMetadataWritesRefuseSymlinks(t *testing.T) {
	base := writeMeta(t, t.TempDir())
	target := filepath.Join(t.TempDir(), "target")
	keep := `{"users":[{"name":"mallory"}]}`
	if err := os.WriteFile(target, []byte(keep), 0o600); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(base, ".rootfiles", "users.json")
	if err := os.Remove(db); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, db); err != nil {
		t.Fatal(err)
	}
	rc := newDryRunRC(t)
	rc.DryRun = false
	rc.Runner = exec.NewRunner(false, rc.Runner.Logger)
	rc.Config.Users.HomeBase = base
	if err := updateUserMeta(rc, "mallory", func(*UserMeta) bool { return false }); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("updateUserMeta error = %v, want a symlink refusal", err)
	}
	if err := saveUserMeta(rc, "root", "/root", "/bin/sh", nil, false, nil); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("saveUserMeta error = %v, want a symlink refusal", err)
	}
	if data, _ := os.ReadFile(target); string(data) != keep {
		t.Errorf("symlink target was written: %q", data)
	}
}

// AC3: read-only reports still run and name the untrusted base.
func TestReportsWarnOnUntrustedBase(t *testing.T) {
	parent := t.TempDir()
	mustChmod(t, parent, 0o755)
	rc := newDryRunRC(t)
	rc.Config.Users.HomeBase = untrustedMetaBases[0].setup(t, parent)
	var warn bytes.Buffer
	old := warnOut
	warnOut = &warn
	t.Cleanup(func() { warnOut = old })
	if err := ListUsers(rc); err != nil {
		t.Errorf("ListUsers: %v", err)
	}
	if err := ListGPUAllocations(rc); err != nil {
		t.Errorf("ListGPUAllocations: %v", err)
	}
	if got := strings.Count(warn.String(), "⚠"); got != 2 || !strings.Contains(warn.String(), parent) {
		t.Errorf("warnings = %q, want two naming %s", warn.String(), parent)
	}
}
