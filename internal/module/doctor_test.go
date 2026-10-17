package module

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

func TestDiskLevel(t *testing.T) {
	for pct, want := range map[float64]string{10: LevelOK, 79.9: LevelOK, 80: LevelWarn, 89: LevelWarn, 90: LevelFail, 100: LevelFail} {
		if got := diskLevel(config.DoctorDiskConfig{}, pct, 1<<40); got != want {
			t.Errorf("diskLevel(%v) = %s, want %s", pct, got, want)
		}
	}
	// h200: 91% used with 164 GiB free is not a failure under min_free 100GiB.
	cfg := config.DoctorDiskConfig{MinFree: "100GiB"}
	if got := diskLevel(cfg, 91, 164<<30); got != LevelWarn {
		t.Errorf("91%% with 164 GiB free = %s, want warn", got)
	}
	if got := diskLevel(cfg, 91, 50<<30); got != LevelFail {
		t.Errorf("91%% with 50 GiB free = %s, want fail", got)
	}
	if got := diskLevel(config.DoctorDiskConfig{WarnPercent: 95, FailPercent: 98}, 96, 0); got != LevelWarn {
		t.Errorf("custom percentages: got %s, want warn", got)
	}
}

func TestDoctorReboot(t *testing.T) {
	dir := t.TempDir()
	saved := []string{rebootRequiredPath, rebootRequiredPkgsPath}
	rebootRequiredPath = filepath.Join(dir, "reboot-required")
	rebootRequiredPkgsPath = filepath.Join(dir, "reboot-required.pkgs")
	t.Cleanup(func() { rebootRequiredPath, rebootRequiredPkgsPath = saved[0], saved[1] })

	if f := doctorReboot(); f.Level != LevelOK {
		t.Errorf("no marker: %+v", f)
	}
	os.WriteFile(rebootRequiredPath, []byte("*** System restart required ***\n"), 0644)
	os.WriteFile(rebootRequiredPkgsPath, []byte("linux-image-6.8\nlinux-base\nlinux-base\n"), 0644)
	f := doctorReboot()
	if f.Level != LevelWarn || !strings.Contains(f.Detail, "linux-image-6.8, linux-base") {
		t.Errorf("unexpected finding: %+v", f)
	}
}

func TestDoctorDisksDedupesFilesystems(t *testing.T) {
	rc := newDryRunRC(t)
	tmp := t.TempDir()
	rc.Config.Users.HomeBase = tmp
	rc.Config.Modules.Storage.DataDir = tmp
	// Same path twice (and possibly the same fs as "/") is reported once.
	if n := len(doctorDisks(rc)); n < 1 || n > 2 {
		t.Errorf("expected / plus at most one distinct fs, got %d", n)
	}
}

// fakeSSHD puts an sshd on PATH whose `-T` prints the given effective config;
// `-T -C user=<u>` for a user in passwordUsers reports password auth on.
func fakeSSHD(t *testing.T, effective string, passwordUsers ...string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n"
	for _, u := range passwordUsers {
		script += "case \"$*\" in *user=" + u + ",*) echo 'passwordauthentication yes'; exit 0;; esac\n"
	}
	script += "cat <<'EOF'\n" + effective + "\nEOF\n"
	if err := os.WriteFile(filepath.Join(dir, "sshd"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

func TestDoctorSSHWarnsPasswordOnlyAccounts(t *testing.T) {
	fakePasswd(t, "alice")
	withPasswords(t, "alice", "bob")
	rc := newRealRC(t)

	fakeSSHD(t, "permitrootlogin no\npasswordauthentication yes")
	var warn *Finding
	for _, f := range doctorSSH(context.Background(), rc) {
		if f.Check == "ssh password-only" {
			f := f
			warn = &f
		}
	}
	if warn == nil || warn.Level != LevelWarn || !strings.Contains(warn.Detail, "bob") || strings.Contains(warn.Detail, "alice") {
		t.Fatalf("want a warn finding naming bob only, got %+v", warn)
	}

	// Password auth off globally: bob has no key and no exception, so he
	// cannot log in at all.
	fakeSSHD(t, "permitrootlogin no\npasswordauthentication no")
	got := map[string]Finding{}
	for _, f := range doctorSSH(context.Background(), rc) {
		got[f.Check] = f
	}
	if _, ok := got["ssh password-only"]; ok {
		t.Errorf("no password-only warning when nobody can use a password, got %+v", got["ssh password-only"])
	}
	if f, ok := got["ssh no-login"]; !ok || !strings.Contains(f.Detail, "bob") {
		t.Errorf("want a no-login warning naming bob, got %+v", got)
	}

	// Off globally but kept for bob through a Match block: still reported
	// as relying on a password.
	fakeSSHD(t, "permitrootlogin no\npasswordauthentication no", "bob")
	got = map[string]Finding{}
	for _, f := range doctorSSH(context.Background(), rc) {
		got[f.Check] = f
	}
	if f, ok := got["ssh password-only"]; !ok || !strings.Contains(f.Detail, "bob") {
		t.Errorf("exception user should be listed as password-only, got %+v", got)
	}
	if _, ok := got["ssh no-login"]; ok {
		t.Errorf("exception user is not locked out, got %+v", got["ssh no-login"])
	}
}
