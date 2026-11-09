package module

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/exec"
)

type managedHeaderSnapshot struct {
	data []byte
	info os.FileInfo
}

func seedLegacyManagedFiles(t *testing.T, files map[string]string) map[string]managedHeaderSnapshot {
	t.Helper()
	out := map[string]managedHeaderSnapshot{}
	for path, content := range files {
		content = strings.Replace(content, "Managed by rootfiles", "Managed by rootfiles-v2", 1)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		stamp := time.Unix(1234567890, 0)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		out[path] = managedHeaderSnapshot{data: []byte(content), info: info}
	}
	return out
}

func assertManagedFilesUnchanged(t *testing.T, files map[string]managedHeaderSnapshot, backup *exec.Backup) {
	t.Helper()
	for path, snapshot := range files {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != string(snapshot.data) {
			t.Fatalf("file bytes changed for %s: %v", path, err)
		}
		info, err := os.Stat(path)
		if err != nil || !os.SameFile(snapshot.info, info) || !snapshot.info.ModTime().Equal(info.ModTime()) {
			t.Fatalf("file identity or timestamp changed for %s: %v", path, err)
		}
	}
	if backup.Used() {
		t.Fatal("header-only compatibility created a backup")
	}
}

func checkApplyLegacyHeaders(t *testing.T, module Module, rc *RunContext, files map[string]managedHeaderSnapshot) {
	t.Helper()
	rc.Runner.Backup = exec.NewBackup(t.TempDir(), "legacy header no-churn")
	check, err := module.Check(context.Background(), rc)
	if err != nil || !check.Satisfied {
		t.Fatalf("legacy header Check=%+v err=%v", check, err)
	}
	applied, err := module.Apply(context.Background(), rc)
	if err != nil || applied.Changed {
		t.Fatalf("legacy header Apply=%+v err=%v", applied, err)
	}
	assertManagedFilesUnchanged(t, files, rc.Runner.Backup)
}

func TestLegacyManagedHeadersSSHNoChurn(t *testing.T) {
	rc := newRealRC(t)
	rc.Config.SSH.DisableRootLogin = true
	module := NewSSHModule()
	files := seedLegacyManagedFiles(t, map[string]string{sshdDropInPath: module.buildConfig(rc.Config.SSH)})
	checkApplyLegacyHeaders(t, module, rc, files)
}

func TestLegacyManagedHeadersSystemNoChurn(t *testing.T) {
	fakeSystemPaths(t)
	calls := fakeBin(t, "systemctl", "sysctl")
	rc := newRealRC(t)
	rc.Config.Modules.System = config.SystemConfig{Sysctl: map[string]string{"vm.swappiness": "10"}, JournaldMaxUse: "2G"}
	files := seedLegacyManagedFiles(t, map[string]string{sysctlPath: sysctlContent(rc.Config.Modules.System.Sysctl), journaldConfPath: journaldContent("2G")})
	checkApplyLegacyHeaders(t, NewSystemModule(), rc, files)
	if data, _ := os.ReadFile(calls); len(data) != 0 {
		t.Fatalf("header-only system files reloaded services: %s", data)
	}
}

func TestLegacyManagedHeadersUbuntuSecurityNoChurn(t *testing.T) {
	fakeUnattendedHost(t)
	calls := fakeBin(t, "systemctl")
	rc := newRealRC(t)
	rc.Config.SSH.Port = 2222
	rc.Config.Modules.Security = config.SecurityConfig{UnattendedUpgrades: true, Fail2ban: true}
	rc.APT = &agreementPackageManager{installed: map[string]bool{"unattended-upgrades": true, "fail2ban": true, "python3-systemd": true}}
	files := seedLegacyManagedFiles(t, map[string]string{autoUpgradesPath: autoUpgradesContent, unattendedPolicyPath: unattendedPolicyContent, fail2banJailPath: fail2banJail([]int{2222})})
	checkApplyLegacyHeaders(t, NewSecurityModule(), rc, files)
	if data, _ := os.ReadFile(calls); len(data) != 0 {
		t.Fatalf("header-only security files restarted a service: %s", data)
	}
}

func TestLegacyManagedHeadersRockySecurityNoChurn(t *testing.T) {
	rc := newRealRC(t)
	rc.Config.System = &config.SystemInfo{OS: "rocky", Version: "9.5"}
	rc.Config.Modules.Security.UnattendedUpgrades = true
	rc.APT = &agreementPackageManager{installed: map[string]bool{"dnf-automatic": true}}
	dir := t.TempDir()
	calls := filepath.Join(dir, "systemctl-calls")
	agreementCommand(t, dir, "python3", "echo 'rootfiles-updateinfo-ok:appstream,baseos'")
	agreementCommand(t, dir, "systemctl", "if [ \"$1\" = is-active ]; then echo active; exit 0; fi\nprintf '%s\\n' \"$*\" >> "+agreementQuote(calls))
	t.Setenv("PATH", dir)
	files := seedLegacyManagedFiles(t, map[string]string{rockyAutomaticConfigPath: rockyAutomaticConfig})
	checkApplyLegacyHeaders(t, NewSecurityModule(), rc, files)
	if data, _ := os.ReadFile(calls); len(data) != 0 {
		t.Fatalf("header-only Rocky policy changed timer state: %s", data)
	}
}

func TestLegacyManagedHeadersCloudflaredAndVLANNoChurn(t *testing.T) {
	fakeCloudflaredPaths(t)
	calls := fakeBin(t, "systemctl", "ip")
	rc := newRealRC(t)
	rc.Config.Modules.Cloudflared = config.CloudflaredConfig{Enabled: true, TunnelToken: "header-test-token", PrivateNetwork: config.PrivateNetworkConfig{Enabled: true, Interface: "vlan0", Address: "192.0.2.1/32"}}
	files := seedLegacyManagedFiles(t, map[string]string{cloudflaredUnitPath: cloudflaredUnit(), cloudflaredEnvPath: cloudflaredEnv("header-test-token"), vlanNetdevPath: vlanNetdev("vlan0"), vlanNetworkPath: vlanNetwork("vlan0", "192.0.2.1/32")})
	if err := os.Chmod(cloudflaredEnvPath, 0600); err != nil {
		t.Fatal(err)
	}
	checkApplyLegacyHeaders(t, NewCloudflaredModule(), rc, files)
	if data, _ := os.ReadFile(calls); len(data) != 0 {
		t.Fatalf("header-only cloudflared/VLAN restarted services: %s", data)
	}
}

func TestLegacyManagedHeadersFleetSudoNoChurn(t *testing.T) {
	calls := setFleetSudoTestState(t, fleetSudoTestListing)
	rc := newRealRC(t)
	rc.Config.Users.FleetSudoUsers = []string{"alice"}
	content, err := fleetSudoersContent([]string{"alice"})
	if err != nil {
		t.Fatal(err)
	}
	files := seedLegacyManagedFiles(t, map[string]string{fleetSudoersPath(): string(content)})
	if err := os.Chmod(fleetSudoersPath(), 0440); err != nil {
		t.Fatal(err)
	}
	rc.Runner.Backup = exec.NewBackup(t.TempDir(), "legacy sudoers header")
	changes, err := checkFleetSudo(context.Background(), rc)
	if err != nil || len(changes) != 0 {
		t.Fatalf("legacy sudoers Check=%+v err=%v", changes, err)
	}
	applied, err := applyFleetSudo(context.Background(), rc)
	if err != nil || applied.Changed {
		t.Fatalf("legacy sudoers Apply=%+v err=%v", applied, err)
	}
	assertManagedFilesUnchanged(t, files, rc.Runner.Backup)
	if data, _ := os.ReadFile(calls); strings.Contains(string(data), "visudo") {
		t.Fatalf("legacy header rewrote sudoers: %s", data)
	}
}

func TestLegacyManagedHeadersGPUFilesNoChurn(t *testing.T) {
	fakePasswd(t, "alice")
	calls := fakeBin(t, "systemctl")
	rc := newRealRC(t)
	oldWrapper, oldProfile, oldSystemd := dockerWrapperPath, gpuProfileDir, gpuSystemdDir
	dir := t.TempDir()
	dockerWrapperPath, gpuProfileDir, gpuSystemdDir = filepath.Join(dir, "docker"), filepath.Join(dir, "profile.d"), filepath.Join(dir, "systemd")
	t.Cleanup(func() { dockerWrapperPath, gpuProfileDir, gpuSystemdDir = oldWrapper, oldProfile, oldSystemd })
	setupDBWithAllocations(t, rc.Config.Users.HomeBase, &GPUAllocationsDB{Version: 1, Allocations: []GPUAllocation{{Username: "alice", GPUs: []int{0}, Method: "both"}}})
	files := seedLegacyManagedFiles(t, map[string]string{profileDScriptPath("alice"): buildProfileDScript("alice", []int{0}), cgroupSlicePath("1000"): buildCgroupConf([]int{0}), dockerWrapperPath: buildDockerWrapper(gpuDBPath(rc))})
	if err := os.Chmod(dockerWrapperPath, 0755); err != nil {
		t.Fatal(err)
	}
	checkApplyLegacyHeaders(t, NewGPUModule(), rc, files)
	if err := installDockerWrapper(rc); err != nil {
		t.Fatal(err)
	}
	assertManagedFilesUnchanged(t, files, rc.Runner.Backup)
	if data, _ := os.ReadFile(calls); len(data) != 0 {
		t.Fatalf("header-only GPU files reloaded systemd: %s", data)
	}
}

func TestLegacyManagedHeaderDoesNotHidePayloadDrift(t *testing.T) {
	fakeSystemPaths(t)
	fakeBin(t, "systemctl", "sysctl")
	rc := newRealRC(t)
	rc.Config.Modules.System.Sysctl = map[string]string{"vm.swappiness": "20"}
	seedLegacyManagedFiles(t, map[string]string{sysctlPath: sysctlContent(map[string]string{"vm.swappiness": "10"})})
	rc.Runner.Backup = exec.NewBackup(t.TempDir(), "actual payload drift")
	module := NewSystemModule()
	check, err := module.Check(context.Background(), rc)
	if err != nil || check.Satisfied {
		t.Fatalf("payload drift Check=%+v err=%v", check, err)
	}
	applied, err := module.Apply(context.Background(), rc)
	if err != nil || !applied.Changed {
		t.Fatalf("payload drift Apply=%+v err=%v", applied, err)
	}
	data, err := os.ReadFile(sysctlPath)
	if err != nil || string(data) != sysctlContent(rc.Config.Modules.System.Sysctl) || strings.Contains(string(data), "rootfiles-v2") {
		t.Fatalf("updated file must use canonical header: %q err=%v", data, err)
	}
	if !rc.Runner.Backup.Used() {
		t.Fatal("payload update did not preserve the original file")
	}
}

func TestUsersCreatedByUsesCanonicalNameForNewDatabaseOnly(t *testing.T) {
	fakePasswd(t, "alice")
	rc := newRealRC(t)
	home := filepath.Join(rc.Config.Users.HomeBase, "alice")
	if err := saveUserMeta(rc, "alice", home, "/bin/bash", nil, false, nil); err != nil {
		t.Fatal(err)
	}
	db, err := LoadUsersDB(rc)
	if err != nil || db.CreatedBy != "rootfiles" {
		t.Fatalf("new database created_by=%+v err=%v", db, err)
	}
	db.CreatedBy = "rootfiles-v2"
	path := filepath.Join(rc.Config.Users.HomeBase, ".rootfiles", "users.json")
	if err := writeJSONDB(rc, path, db, 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveUserMeta(rc, "alice", home, "/bin/bash", nil, false, nil); err != nil {
		t.Fatal(err)
	}
	db, err = LoadUsersDB(rc)
	if err != nil || db.CreatedBy != "rootfiles-v2" {
		t.Fatalf("legacy database created_by was migrated: %+v err=%v", db, err)
	}
}
