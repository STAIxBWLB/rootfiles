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
