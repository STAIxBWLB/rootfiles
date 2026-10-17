package module

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/exec"
)

func TestUnattendedPolicyExcludesNvidiaAndNeverReboots(t *testing.T) {
	for _, want := range []string{`"nvidia-";`, `"libnvidia-";`, `"cuda-";`, `Automatic-Reboot "false"`, `-security";`} {
		if !strings.Contains(unattendedPolicyContent, want) {
			t.Errorf("policy missing %q", want)
		}
	}
	if strings.Contains(unattendedPolicyContent, `${distro_codename}-updates`) {
		t.Error("policy must be security-only")
	}
}

func TestFail2banJailUsesSSHPorts(t *testing.T) {
	j := fail2banJail([]int{22, 2222})
	if !strings.Contains(j, "port     = 22,2222") || !strings.Contains(j, "backend  = systemd") {
		t.Errorf("unexpected jail:\n%s", j)
	}
}

func TestSSHBuildConfigHardening(t *testing.T) {
	m := NewSSHModule()
	got := m.buildConfig(sshCfg(true, 3))
	for _, want := range []string{"PasswordAuthentication no", "KbdInteractiveAuthentication no", "MaxAuthTries 3"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// fakeUnattendedHost points the policy files at a temp tree and stubs
// apt-config: until the managed policy exists the merged dump still carries
// the installer's general origin and pattern, as on the fleet hosts.
func fakeUnattendedHost(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	oldAuto, oldPolicy, oldLegacy := autoUpgradesPath, unattendedPolicyPath, legacyUnattendedPaths
	autoUpgradesPath = filepath.Join(dir, "20auto-upgrades")
	unattendedPolicyPath = filepath.Join(dir, "zzzzzz-rootfiles-unattended-upgrades")
	legacyUnattendedPaths = []string{filepath.Join(dir, "52rootfiles-unattended-upgrades"), filepath.Join(dir, "zzzzz-rootfiles-security-origins.conf")}
	t.Cleanup(func() { autoUpgradesPath, unattendedPolicyPath, legacyUnattendedPaths = oldAuto, oldPolicy, oldLegacy })
	security := `Unattended-Upgrade::Allowed-Origins:: "${distro_id}:${distro_codename}-security";
Unattended-Upgrade::Allowed-Origins:: "${distro_id}ESMApps:${distro_codename}-apps-security";
Unattended-Upgrade::Allowed-Origins:: "${distro_id}ESM:${distro_codename}-infra-security";
`
	general := `Unattended-Upgrade::Allowed-Origins:: "${distro_id}:${distro_codename}-updates";
Unattended-Upgrade::Origins-Pattern:: "origin=*";
`
	fakeMonitoringCommand(t, "apt-config", "#!/bin/sh\nprintf 'APT \"\";\\nUnattended-Upgrade::Allowed-Origins \"\";\\n'\ncat <<'EOF2'\n"+security+"EOF2\nif ! grep -q '^#clear Unattended-Upgrade::Allowed-Origins;' '"+unattendedPolicyPath+"' 2>/dev/null || test -f '"+dir+"/later-installer'; then cat <<'EOF2'\n"+general+"EOF2\nfi\n")
	fakeMonitoringCommand(t, "dpkg-query", "#!/bin/sh\nprintf 'unattended-upgrades install ok installed\\n'\n")
	return dir
}

func securityRC(t *testing.T) *RunContext {
	rc := newRealRC(t)
	rc.Config.Modules.Security = config.SecurityConfig{UnattendedUpgrades: true}
	return rc
}

func TestUnattendedPolicyClearsInheritedOriginsAndMigratesHandFix(t *testing.T) {
	dir := fakeUnattendedHost(t)
	handFix := legacyUnattendedPaths[1]
	if err := os.WriteFile(handFix, []byte("// hand-placed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	rc := securityRC(t)
	backupRoot := filepath.Join(dir, "backups")
	rc.Runner.Backup = exec.NewBackup(backupRoot, "test")
	m := NewSecurityModule()

	if check, err := m.Check(context.Background(), rc); err != nil || check.Satisfied {
		t.Fatalf("Check = %+v, %v; want pending", check, err)
	}
	if _, err := m.Apply(context.Background(), rc); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := nonSecurityOrigins(context.Background(), rc); len(got) != 0 {
		t.Fatalf("effective policy after apply should be security-only, extra=%v", got)
	}
	if _, err := os.Stat(handFix); !os.IsNotExist(err) {
		t.Fatalf("hand-placed fix should be removed: %v", err)
	}
	if check, err := m.Check(context.Background(), rc); err != nil || !check.Satisfied {
		t.Fatalf("Check after apply = %+v, %v; want satisfied", check, err)
	}
	if _, err := exec.RestoreBackup(backupRoot, rc.Runner.Backup.ID, false); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if data, err := os.ReadFile(handFix); err != nil || string(data) != "// hand-placed\n" {
		t.Fatalf("rollback should restore the hand-placed fix: %q, %v", data, err)
	}
}

func TestUnattendedCheckJudgesEffectivePolicy(t *testing.T) {
	dir := fakeUnattendedHost(t)
	rc := securityRC(t)
	m := NewSecurityModule()
	if _, err := m.Apply(context.Background(), rc); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// A file sorting after the managed one re-adds general origins.
	if err := os.WriteFile(filepath.Join(dir, "later-installer"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	check, err := m.Check(context.Background(), rc)
	if err != nil || check.Satisfied || !strings.Contains(check.Changes[0].Description, "-updates") || !strings.Contains(check.Changes[0].Description, "origin=*") {
		t.Fatalf("Check with an intact file but widened policy = %+v, %v; want drift naming the extras", check, err)
	}
	if _, err := m.Apply(context.Background(), rc); err == nil || !strings.Contains(err.Error(), "not security-only") {
		t.Fatalf("Apply cannot fix a later file and must fail, got %v", err)
	}
}

func TestUnattendedPolicyMustBeWorldReadable(t *testing.T) {
	fakeUnattendedHost(t)
	rc := securityRC(t)
	m := NewSecurityModule()
	if _, err := m.Apply(context.Background(), rc); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := os.Chmod(unattendedPolicyPath, 0600); err != nil {
		t.Fatal(err)
	}
	if check, err := m.Check(context.Background(), rc); err != nil || check.Satisfied {
		t.Fatalf("a 0600 policy file should be drift, got %+v, %v", check, err)
	}
	if _, err := m.Apply(context.Background(), rc); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if info, _ := os.Stat(unattendedPolicyPath); info.Mode().Perm() != 0644 {
		t.Fatalf("mode = %v, want 0644", info.Mode().Perm())
	}
	if check, err := m.Check(context.Background(), rc); err != nil || !check.Satisfied {
		t.Fatalf("Check after fix = %+v, %v", check, err)
	}
}
