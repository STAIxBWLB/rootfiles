package module

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/tenant"
)

func TestReserveTenantGPUsSharesTheLedger(t *testing.T) {
	fakeBin(t, "nvidia-smi")
	home := t.TempDir()
	rc := newRealRC(t)
	rc.Config.Users.HomeBase = home
	if err := os.MkdirAll(filepath.Join(home, ".rootfiles"), 0o755); err != nil {
		t.Fatal(err)
	}
	db := GPUAllocationsDB{Version: 1, Allocations: []GPUAllocation{{Username: "alice", GPUs: []int{0, 1}, Method: "env"}}}
	data, _ := json.Marshal(db)
	if err := os.WriteFile(gpuDBPath(rc), data, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if err := ReserveTenantGPUs(ctx, rc, "est", []int{1, 2}); err == nil || !strings.Contains(err.Error(), "alice") {
		t.Errorf("GPU held by a user was given to a tenant: %v", err)
	}
	if err := ReserveTenantGPUs(ctx, rc, "est", []int{2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := ReserveTenantGPUs(ctx, rc, "pulse", []int{3}); err == nil || !strings.Contains(err.Error(), "tenant:est") {
		t.Errorf("GPU held by another tenant was given away: %v", err)
	}
	if err := ReserveTenantGPUs(ctx, rc, "est", []int{2}); err != nil {
		t.Errorf("tenant could not shrink its own GPUs: %v", err)
	}
	if err := validateGPURequest(mustLoadGPUDB(t, rc), "bob", []int{2}); err == nil {
		t.Error("tenant GPU offered to a user")
	}
	if err := RevokeGPUs(ctx, rc, "tenant:est"); err == nil {
		t.Error("gpu revoke removed a tenant entry")
	}
	if err := ReserveTenantGPUs(ctx, rc, "est", nil); err != nil {
		t.Fatal(err)
	}
	for _, a := range mustLoadGPUDB(t, rc).Allocations {
		if isTenantOwner(a.Username) {
			t.Errorf("released tenant still in ledger: %+v", a)
		}
	}
}

func mustLoadGPUDB(t *testing.T, rc *RunContext) *GPUAllocationsDB {
	t.Helper()
	db, err := loadGPUDB(rc)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestGPUModuleIgnoresTenantEntries(t *testing.T) {
	home := t.TempDir()
	rc := testRunContext(t, home, "env")
	os.MkdirAll(filepath.Join(home, ".rootfiles"), 0o755)
	data, _ := json.Marshal(GPUAllocationsDB{Version: 1, Allocations: []GPUAllocation{{Username: "tenant:est", GPUs: []int{0}, Method: "tenant"}}})
	os.WriteFile(gpuDBPath(rc), data, 0o644)
	res, err := NewGPUModule().Check(context.Background(), rc)
	if err != nil || !res.Satisfied {
		t.Errorf("tenant-only ledger asked for user GPU setup: %+v %v", res, err)
	}
}

func TestTenantModuleCheck(t *testing.T) {
	setFleetSudoTestState(t, "")
	fakeBin(t, "systemctl", "getent")
	dir := t.TempDir()
	oldUnit, oldGuard := tenantUnitDir, tenantGuardPath
	tenantUnitDir, tenantGuardPath = filepath.Join(dir, "units"), filepath.Join(dir, "guard")
	t.Cleanup(func() { tenantUnitDir, tenantGuardPath = oldUnit, oldGuard })

	home := t.TempDir()
	rc := newDryRunRC(t)
	rc.Config.Users.HomeBase = home
	rc.Config.Modules.Tenant = config.TenantConfig{Enabled: true, Root: "/raid/tenants"}
	specDir := tenant.SpecDir(home)
	os.MkdirAll(specDir, 0o755)
	s := &tenant.Spec{Name: "est", Image: "img", SSHPort: 3889, Volume: tenant.Volume{Size: "10G"},
		Users: []tenant.User{{Name: "est", UID: 1000, GID: 1000}}}
	data, _ := s.Marshal()
	os.WriteFile(filepath.Join(specDir, "est.yaml"), data, 0o600)

	res, err := NewTenantModule().Check(context.Background(), rc)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range res.Changes {
		got = append(got, c.Description)
	}
	all := strings.Join(got, "\n")
	for _, want := range []string{"Write rootfiles-gpu-guard", "Write rootfiles-gpu-guard.timer", "Write rootfiles-tenant-sync.timer",
		"Enable fstrim.timer", "Install tenant operator sudoers rule", "Install volume mount unit of tenant est"} {
		if !strings.Contains(all, want) {
			t.Errorf("check lacks %q:\n%s", want, all)
		}
	}
	if !strings.Contains(string(tenantSudoersContent("ops")), "%ops ALL=(root) NOPASSWD: /usr/local/bin/rootfiles tenant ops *") {
		t.Errorf("sudoers rule: %s", tenantSudoersContent("ops"))
	}
}

func TestInstallPiperRejectsWrongChecksum(t *testing.T) {
	dir := t.TempDir()
	oldLib, oldURL, oldArch := piperLibDir, piperURL, piperGOARCH
	piperLibDir, piperGOARCH = filepath.Join(dir, "lib"), "amd64"
	archive := filepath.Join(dir, "fake.tar.gz")
	os.WriteFile(archive, []byte("not the release"), 0o644)
	piperURL = "file://" + archive + "?%s%s"
	t.Cleanup(func() { piperLibDir, piperURL, piperGOARCH = oldLib, oldURL, oldArch })
	rc := newRealRC(t)
	err := installPiper(context.Background(), rc, "v1.6.1")
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("tampered archive installed: %v", err)
	}
	if piperInstalled("v1.6.1") || fileExists(piperDir("v1.6.1")) {
		t.Error("files left after a rejected download")
	}
	if _, _, err := piperSum("v9.9.9"); err == nil {
		t.Error("unpinned version accepted")
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestPiperUnit(t *testing.T) {
	rc := newDryRunRC(t)
	rc.Config.Modules.Tenant.SSHPiper = config.SSHPiperConfig{Enabled: true}
	u := piperUnitContent(rc)
	for _, want := range []string{"--port 2222", "--address 0.0.0.0", "/v1.6.1/plugins/yaml --config " + piperConfDir + "/tenants/*.yaml -- ", "/plugins/failtoban", "--server-key-generate-mode notexist"} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q:\n%s", want, u)
		}
	}
}

func TestPiperBlock(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	rc := newDryRunRC(t)

	write("ss", `echo 'LISTEN 0 4096 0.0.0.0:2222 0.0.0.0:* users:(("sshpiperd",pid=1,fd=3))'`)
	write("ufw", `echo "Status: inactive"`)
	if b := piperBlock(context.Background(), rc, 2222); b != "" {
		t.Errorf("own listener blocked: %s", b)
	}
	write("ss", `echo 'LISTEN 0 4096 0.0.0.0:2222 0.0.0.0:* users:(("sshd",pid=1,fd=3))'`)
	if b := piperBlock(context.Background(), rc, 2222); !strings.Contains(b, "held by another process") {
		t.Errorf("foreign listener not blocked: %q", b)
	}
	write("ss", `exit 0`)
	write("ufw", `printf 'Status: active\n\nTo Action From\n22/tcp ALLOW Anywhere\n'`)
	if b := piperBlock(context.Background(), rc, 2222); !strings.Contains(b, "UFW is active") {
		t.Errorf("closed firewall not blocked: %q", b)
	}
	rc.Config.Modules.Network = config.NetworkConfig{Enabled: true, UFW: true}
	if b := piperBlock(context.Background(), rc, 2222); b != "" {
		t.Errorf("network module will open the port, still blocked: %s", b)
	}
	write("ufw", `exit 1`)
	if b := piperBlock(context.Background(), rc, 2222); !strings.Contains(b, "cannot read the UFW state") {
		t.Errorf("unknown firewall state not blocked: %q", b)
	}
}

func TestPiperUnitFailtobanIgnore(t *testing.T) {
	rc := newDryRunRC(t)
	rc.Config.Modules.Tenant.SSHPiper = config.SSHPiperConfig{Enabled: true, FailtobanIgnore: []string{"172.16.229.33/32", "10.0.0.1"}}
	if u := piperUnitContent(rc); !strings.Contains(u, "/plugins/failtoban --ignore-ip 172.16.229.33/32 --ignore-ip 10.0.0.1\n") {
		t.Errorf("unit lacks ignore list:\n%s", u)
	}
	for _, bad := range []string{"not-an-ip", "fe80::1%eth0"} {
		rc.Config.Modules.Tenant.SSHPiper.FailtobanIgnore = []string{bad}
		if err := rc.Config.Validate(); err == nil || !strings.Contains(err.Error(), "failtoban_ignore") {
			t.Errorf("bad entry %q accepted: %v", bad, err)
		}
	}
}

func TestPiperRemovalReloadFailureWarnsWithoutConvergedReload(t *testing.T) {
	dir := t.TempDir()
	oldUnitDir, oldConf := tenantUnitDir, piperConfDir
	tenantUnitDir, piperConfDir = dir, filepath.Join(dir, "piper")
	t.Cleanup(func() { tenantUnitDir, piperConfDir = oldUnitDir, oldConf })
	if err := os.WriteFile(piperUnitPath(), []byte("old unit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(dir, "calls")
	fakeMonitoringCommand(t, "systemctl", "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+calls+"'\ncase $1 in daemon-reload) echo 'systemd unavailable' >&2; exit 1;; esac\n")
	rc := newRealRC(t)
	var warnings []string
	note := func(string) {}
	warn := func(warning string) { warnings = append(warnings, warning) }
	if err := applyPiper(context.Background(), rc, note, warn); err != nil {
		t.Fatalf("removed unit should survive reload failure: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "run sudo systemctl daemon-reload") {
		t.Fatalf("removal lost manual repair warning: %v", warnings)
	}
	if changes, err := piperChanges(context.Background(), rc); err != nil || len(changes) != 0 {
		t.Fatalf("disabled piper should converge: changes=%+v error=%v", changes, err)
	}
	if err := applyPiper(context.Background(), rc, note, warn); err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || strings.Count(readCalls(t, calls), "daemon-reload\n") != 1 {
		t.Fatalf("converged disabled piper retried reload: warnings=%v calls=%s", warnings, readCalls(t, calls))
	}
}

func TestLegacyTenantHostFilesNoChurnAndUnsafeSudoersRepair(t *testing.T) {
	sudoCalls := setFleetSudoTestState(t, "")
	dir := t.TempDir()
	oldUnits, oldGuard := tenantUnitDir, tenantGuardPath
	tenantUnitDir, tenantGuardPath = filepath.Join(dir, "units"), filepath.Join(dir, "guard")
	t.Cleanup(func() { tenantUnitDir, tenantGuardPath = oldUnits, oldGuard })
	calls := filepath.Join(dir, "systemctl-calls")
	fakeMonitoringCommand(t, "systemctl", "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+calls+"'\ncase $1 in is-enabled) echo enabled;; *) exit 1;; esac\n")
	fakeMonitoringCommand(t, "getent", "#!/bin/sh\nexit 0\n")
	rc := newRealRC(t)
	rc.Config.Modules.Tenant.Enabled = true
	files := map[string]string{}
	for _, file := range tenantFiles() {
		files[file.path] = file.content
	}
	snapshots := seedLegacyManagedFiles(t, files)
	for _, file := range tenantFiles() {
		if err := os.Chmod(file.path, file.mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(tenantSudoersPath(), tenantSudoersContent(rc.Config.Modules.Tenant.Group()), 0o440); err != nil {
		t.Fatal(err)
	}
	checkApplyLegacyHeaders(t, NewTenantModule(), rc, snapshots)
	for _, file := range tenantFiles() {
		info, err := os.Stat(file.path)
		if err != nil || info.Mode().Perm() != file.mode {
			t.Fatalf("legacy file mode changed for %s: %v", file.path, err)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(readCalls(t, calls)), "\n") {
		if !strings.HasPrefix(line, "is-enabled ") {
			t.Fatalf("legacy host files caused a service mutation: %s", line)
		}
	}
	if data, _ := os.ReadFile(sudoCalls); strings.Contains(string(data), "visudo") {
		t.Fatalf("legacy host files rewrote safe sudoers: %s", data)
	}

	// Metadata safety is independent of content equivalence: an unsafe
	// sudoers mode must still be reported and repaired transactionally.
	if err := os.Chmod(tenantSudoersPath(), 0o666); err != nil {
		t.Fatal(err)
	}
	check, err := NewTenantModule().Check(context.Background(), rc)
	if err != nil || check.Satisfied {
		t.Fatalf("unsafe sudoers metadata was accepted: Check=%+v error=%v", check, err)
	}
	result, err := NewTenantModule().Apply(context.Background(), rc)
	if err != nil || result == nil || !result.Changed {
		t.Fatalf("unsafe sudoers was not repaired: Apply=%+v error=%v", result, err)
	}
	info, err := os.Stat(tenantSudoersPath())
	if err != nil || info.Mode().Perm() != 0o440 {
		t.Fatalf("sudoers repair did not restore safe mode: %v", err)
	}
	if data, _ := os.ReadFile(sudoCalls); !strings.Contains(string(data), "visudo -cf") {
		t.Fatalf("sudoers repair bypassed validation: %s", data)
	}
}
