package module

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/exec"
)

func testRunContext(t *testing.T, homeBase string, method string) *RunContext {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	runner := exec.NewRunner(true, logger) // dry-run
	return &RunContext{
		Config: &config.Config{
			Users: config.UsersConfig{HomeBase: homeBase},
			Modules: config.ModulesConfig{
				Nvidia: config.NvidiaConfig{
					Enabled: true,
					GPUAllocation: config.GPUAllocationConfig{
						Enabled: true,
						Method:  method,
					},
				},
			},
		},
		Runner: runner,
		DryRun: true,
	}
}

func TestGPUModule_Name(t *testing.T) {
	m := NewGPUModule()
	if m.Name() != "gpu" {
		t.Errorf("Name() = %q, want gpu", m.Name())
	}
}

func TestGPUModule_CheckNoAllocations(t *testing.T) {
	tmpDir := t.TempDir()
	rc := testRunContext(t, tmpDir, "env")

	m := NewGPUModule()
	result, err := m.Check(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Satisfied {
		t.Error("Check should be satisfied when no allocations exist")
	}
}

func TestGPUModule_CheckWithAllocations(t *testing.T) {
	tmpDir := t.TempDir()
	rc := testRunContext(t, tmpDir, "env")

	// Write a GPU allocation DB
	metaDir := filepath.Join(tmpDir, ".rootfiles")
	os.MkdirAll(metaDir, 0755)
	db := GPUAllocationsDB{
		Version:   1,
		TotalGPUs: 4,
		Allocations: []GPUAllocation{
			{Username: "alice", GPUs: []int{0, 1}, Method: "env"},
		},
	}
	data, _ := json.MarshalIndent(db, "", "  ")
	os.WriteFile(filepath.Join(metaDir, "gpu-allocations.json"), data, 0600)

	m := NewGPUModule()
	result, err := m.Check(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Script doesn't exist yet, so there should be pending changes (env script + docker wrapper)
	if result.Satisfied {
		t.Error("Check should NOT be satisfied when script is missing")
	}
	if len(result.Changes) < 1 {
		t.Errorf("expected at least 1 change, got %d", len(result.Changes))
	}
}

func TestBuildProfileDScript(t *testing.T) {
	script := buildProfileDScript("alice", []int{0, 1})
	expected := `# Managed by rootfiles-v2 — do not edit
if [ "$(id -un)" = "alice" ]; then
    export CUDA_VISIBLE_DEVICES=0,1
    export NVIDIA_VISIBLE_DEVICES=0,1
fi
`
	if script != expected {
		t.Errorf("buildProfileDScript mismatch:\ngot:\n%s\nwant:\n%s", script, expected)
	}
}

func TestBuildCgroupConf(t *testing.T) {
	conf := buildCgroupConf([]int{2, 3})
	if conf == "" {
		t.Fatal("buildCgroupConf returned empty string")
	}
	// Should contain DeviceAllow lines for the specific GPUs
	for _, expect := range []string{
		"DevicePolicy=strict",
		"DeviceAllow=/dev/null rwm",
		"DeviceAllow=/dev/nvidia2 rwm",
		"DeviceAllow=/dev/nvidia3 rwm",
		"DeviceAllow=/dev/nvidiactl rwm",
		"DeviceAllow=/dev/nvidia-uvm rwm",
		"DeviceAllow=/dev/nvidia-uvm-tools rwm",
		"DeviceAllow=/dev/nvidia-caps/* rwm",
		"[Slice]",
	} {
		if !bytes.Contains([]byte(conf), []byte(expect)) {
			t.Errorf("cgroup conf missing %q", expect)
		}
	}
}

func TestGPUListStr(t *testing.T) {
	tests := []struct {
		gpus []int
		want string
	}{
		{[]int{0}, "0"},
		{[]int{1, 0}, "0,1"},
		{[]int{3, 1, 2}, "1,2,3"},
	}
	for _, tt := range tests {
		got := gpuListStr(tt.gpus)
		if got != tt.want {
			t.Errorf("gpuListStr(%v) = %q, want %q", tt.gpus, got, tt.want)
		}
	}
}

func TestProfileDScriptPath(t *testing.T) {
	path := profileDScriptPath("bob")
	if path != "/etc/profile.d/rootfiles-gpu-bob.sh" {
		t.Errorf("profileDScriptPath = %q, unexpected", path)
	}
}

func TestCgroupSlicePath(t *testing.T) {
	path := cgroupSlicePath("1001")
	if path != "/etc/systemd/system/user-1001.slice.d/gpu-access.conf" {
		t.Errorf("cgroupSlicePath = %q, unexpected", path)
	}
}

func TestEffectiveMethod(t *testing.T) {
	tmpDir := t.TempDir()

	// Default from config
	rc := testRunContext(t, tmpDir, "both")
	if got := effectiveMethod(rc, ""); got != "both" {
		t.Errorf("effectiveMethod empty = %q, want both", got)
	}

	// Explicit override
	if got := effectiveMethod(rc, "cgroup"); got != "cgroup" {
		t.Errorf("effectiveMethod cgroup = %q, want cgroup", got)
	}

	// Default when config has no method
	rc2 := testRunContext(t, tmpDir, "")
	if got := effectiveMethod(rc2, ""); got != "env" {
		t.Errorf("effectiveMethod fallback = %q, want env", got)
	}
}

func TestLoadSaveGPUDB(t *testing.T) {
	tmpDir := t.TempDir()
	metaDir := filepath.Join(tmpDir, ".rootfiles")
	os.MkdirAll(metaDir, 0755)

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	runner := exec.NewRunner(false, logger) // not dry-run to actually write
	rc := &RunContext{
		Config: &config.Config{
			Users: config.UsersConfig{HomeBase: tmpDir},
		},
		Runner: runner,
	}

	db := &GPUAllocationsDB{
		Version:   1,
		TotalGPUs: 8,
		GPUModel:  "NVIDIA H100 80GB",
		Allocations: []GPUAllocation{
			{Username: "user1", GPUs: []int{0, 1}, Method: "env"},
			{Username: "user2", GPUs: []int{2, 3}, Method: "both"},
		},
	}

	if err := saveGPUDB(rc, db); err != nil {
		t.Fatalf("saveGPUDB failed: %v", err)
	}

	loaded, err := loadGPUDB(rc)
	if err != nil {
		t.Fatalf("loadGPUDB failed: %v", err)
	}

	if loaded.Version != 1 {
		t.Errorf("version = %d, want 1", loaded.Version)
	}
	if loaded.TotalGPUs != 8 {
		t.Errorf("total_gpus = %d, want 8", loaded.TotalGPUs)
	}
	if loaded.GPUModel != "NVIDIA H100 80GB" {
		t.Errorf("gpu_model = %q, want NVIDIA H100 80GB", loaded.GPUModel)
	}
	if len(loaded.Allocations) != 2 {
		t.Fatalf("allocations = %d, want 2", len(loaded.Allocations))
	}
	if loaded.Allocations[0].Username != "user1" {
		t.Errorf("alloc[0].username = %q, want user1", loaded.Allocations[0].Username)
	}
	if len(loaded.Allocations[0].GPUs) != 2 || loaded.Allocations[0].GPUs[0] != 0 {
		t.Errorf("alloc[0].gpus = %v, want [0,1]", loaded.Allocations[0].GPUs)
	}
}

func TestLoadGPUDB_NoFile(t *testing.T) {
	tmpDir := t.TempDir()
	rc := testRunContext(t, tmpDir, "env")

	db, err := loadGPUDB(rc)
	if err == nil {
		t.Log("loadGPUDB with no file returns nil error (empty db)")
	}
	// Should return an empty DB
	if len(db.Allocations) != 0 {
		t.Errorf("expected 0 allocations, got %d", len(db.Allocations))
	}
}

func TestListGPUAllocations_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	rc := testRunContext(t, tmpDir, "env")

	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := ListGPUAllocations(rc)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "No GPU allocations configured.") {
		t.Errorf("empty allocations output missing hint, got %q", output)
	}
	if !strings.Contains(output, "GPU Allocations") {
		t.Errorf("empty allocations output missing section header, got %q", output)
	}
}

func TestListGPUAllocations_WithData(t *testing.T) {
	tmpDir := t.TempDir()
	metaDir := filepath.Join(tmpDir, ".rootfiles")
	os.MkdirAll(metaDir, 0755)

	db := GPUAllocationsDB{
		Version:   1,
		TotalGPUs: 4,
		GPUModel:  "NVIDIA A100",
		Allocations: []GPUAllocation{
			{Username: "alice", GPUs: []int{0, 1}, Method: "env", UpdatedAt: "2026-03-20T00:00:00Z"},
		},
	}
	data, _ := json.MarshalIndent(db, "", "  ")
	os.WriteFile(filepath.Join(metaDir, "gpu-allocations.json"), data, 0600)

	rc := testRunContext(t, tmpDir, "env")

	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := ListGPUAllocations(rc)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !bytes.Contains([]byte(output), []byte("alice")) {
		t.Error("output should contain alice")
	}
	if !bytes.Contains([]byte(output), []byte("NVIDIA A100")) {
		t.Error("output should contain GPU model")
	}
	if !bytes.Contains([]byte(output), []byte("0,1")) {
		t.Error("output should contain GPU list")
	}
}

func TestGPUDBPath(t *testing.T) {
	tmpDir := t.TempDir()
	rc := testRunContext(t, tmpDir, "env")
	path := gpuDBPath(rc)
	expected := filepath.Join(tmpDir, ".rootfiles", "gpu-allocations.json")
	if path != expected {
		t.Errorf("gpuDBPath = %q, want %q", path, expected)
	}
}

func TestGPUDBPath_DefaultHomeBase(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	runner := exec.NewRunner(true, logger)
	rc := &RunContext{
		Config: &config.Config{},
		Runner: runner,
	}
	path := gpuDBPath(rc)
	if path != "/home/.rootfiles/gpu-allocations.json" {
		t.Errorf("gpuDBPath default = %q, want /home/.rootfiles/gpu-allocations.json", path)
	}
}

// currentUsername returns the current OS user's name for tests that need user.Lookup to succeed.
func currentUsername(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatalf("cannot get current user: %v", err)
	}
	return u.Username
}

// testRunContextReal creates a non-dry-run RunContext so file writes actually happen.
func testRunContextReal(t *testing.T, homeBase string, method string) *RunContext {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	runner := exec.NewRunner(false, logger)
	return &RunContext{
		Config: &config.Config{
			Users: config.UsersConfig{HomeBase: homeBase},
			Modules: config.ModulesConfig{
				Nvidia: config.NvidiaConfig{
					Enabled: true,
					GPUAllocation: config.GPUAllocationConfig{
						Enabled: true,
						Method:  method,
					},
				},
			},
		},
		Runner: runner,
		DryRun: true, // DryRun on RunContext so Apply/Assign skip writing /etc files, but Runner is non-dry-run so DB writes work
	}
}

// setupDBWithAllocations writes a pre-populated GPU allocation DB for testing.
func setupDBWithAllocations(t *testing.T, tmpDir string, db *GPUAllocationsDB) {
	t.Helper()
	metaDir := filepath.Join(tmpDir, ".rootfiles")
	os.MkdirAll(metaDir, 0755)
	data, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		t.Fatalf("marshal db: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metaDir, "gpu-allocations.json"), data, 0600); err != nil {
		t.Fatalf("write db: %v", err)
	}
}

// --- AssignGPUs tests ---

func TestAssignGPUs_Basic(t *testing.T) {
	tmpDir := t.TempDir()
	username := currentUsername(t)
	rc := testRunContextReal(t, tmpDir, "env")

	err := AssignGPUs(context.Background(), rc, username, []int{0, 1}, "env")
	if err != nil {
		t.Fatalf("AssignGPUs failed: %v", err)
	}

	// Verify DB was written
	db, err := loadGPUDB(rc)
	if err != nil {
		t.Fatalf("loadGPUDB failed: %v", err)
	}
	if len(db.Allocations) != 1 {
		t.Fatalf("expected 1 allocation, got %d", len(db.Allocations))
	}
	if db.Allocations[0].Username != username {
		t.Errorf("username = %q, want %q", db.Allocations[0].Username, username)
	}
	if len(db.Allocations[0].GPUs) != 2 {
		t.Errorf("gpus = %v, want [0,1]", db.Allocations[0].GPUs)
	}
	if db.Version != 1 {
		t.Errorf("version = %d, want 1", db.Version)
	}
}

func TestAssignGPUs_UserNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	rc := testRunContext(t, tmpDir, "env")

	err := AssignGPUs(context.Background(), rc, "nonexistent_user_xyz_12345", []int{0}, "env")
	if err == nil {
		t.Fatal("expected error for nonexistent user")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found'", err.Error())
	}
}

func TestAssignGPUs_ConflictDetection(t *testing.T) {
	tmpDir := t.TempDir()
	username := currentUsername(t)
	rc := testRunContextReal(t, tmpDir, "env")

	// Pre-populate DB with another user's allocation
	setupDBWithAllocations(t, tmpDir, &GPUAllocationsDB{
		Version:   1,
		TotalGPUs: 0, // no validation when TotalGPUs=0
		Allocations: []GPUAllocation{
			{Username: "other_user", GPUs: []int{2, 3}, Method: "env"},
		},
	})

	// Try to assign GPU 2 which is already taken by other_user
	err := AssignGPUs(context.Background(), rc, username, []int{2}, "env")
	if err == nil {
		t.Fatal("expected conflict error")
	}
	if !strings.Contains(err.Error(), "already assigned") {
		t.Errorf("error = %q, want 'already assigned'", err.Error())
	}
}

func TestAssignGPUs_UpdateExisting(t *testing.T) {
	tmpDir := t.TempDir()
	username := currentUsername(t)
	rc := testRunContextReal(t, tmpDir, "env")

	// First assignment
	err := AssignGPUs(context.Background(), rc, username, []int{0}, "env")
	if err != nil {
		t.Fatalf("first assign: %v", err)
	}

	// Update to different GPUs
	err = AssignGPUs(context.Background(), rc, username, []int{1, 2}, "env")
	if err != nil {
		t.Fatalf("second assign: %v", err)
	}

	db, err := loadGPUDB(rc)
	if err != nil {
		t.Fatalf("loadGPUDB: %v", err)
	}
	// Should still be 1 allocation (updated, not appended)
	if len(db.Allocations) != 1 {
		t.Fatalf("expected 1 allocation after update, got %d", len(db.Allocations))
	}
	if len(db.Allocations[0].GPUs) != 2 || db.Allocations[0].GPUs[0] != 1 {
		t.Errorf("gpus = %v, want [1,2]", db.Allocations[0].GPUs)
	}
}

func TestAssignGPUs_GPUIndexOutOfRange(t *testing.T) {
	tmpDir := t.TempDir()
	username := currentUsername(t)
	rc := testRunContextReal(t, tmpDir, "env")

	// Pre-populate DB with TotalGPUs set so validation kicks in.
	// Note: detectGPUInfo may override TotalGPUs with real GPU count,
	// so use index 999 which exceeds any realistic GPU count.
	setupDBWithAllocations(t, tmpDir, &GPUAllocationsDB{
		Version:   1,
		TotalGPUs: 4,
	})

	err := AssignGPUs(context.Background(), rc, username, []int{999}, "env")
	if err == nil {
		t.Fatal("expected out-of-range error")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("error = %q, want 'out of range'", err.Error())
	}
}

func TestAssignGPUs_DefaultMethod(t *testing.T) {
	tmpDir := t.TempDir()
	username := currentUsername(t)
	rc := testRunContextReal(t, tmpDir, "both") // config default = "both"

	err := AssignGPUs(context.Background(), rc, username, []int{0}, "") // empty method → use config default
	if err != nil {
		t.Fatalf("AssignGPUs: %v", err)
	}

	db, _ := loadGPUDB(rc)
	if db.Allocations[0].Method != "both" {
		t.Errorf("method = %q, want 'both' (from config)", db.Allocations[0].Method)
	}
}

// --- RevokeGPUs tests ---

func TestRevokeGPUs_Basic(t *testing.T) {
	tmpDir := t.TempDir()
	username := currentUsername(t)
	rc := testRunContextReal(t, tmpDir, "env")

	// Assign first
	err := AssignGPUs(context.Background(), rc, username, []int{0, 1}, "env")
	if err != nil {
		t.Fatalf("AssignGPUs: %v", err)
	}

	// Revoke
	err = RevokeGPUs(context.Background(), rc, username)
	if err != nil {
		t.Fatalf("RevokeGPUs: %v", err)
	}

	db, _ := loadGPUDB(rc)
	if len(db.Allocations) != 0 {
		t.Errorf("expected 0 allocations after revoke, got %d", len(db.Allocations))
	}
}

func TestRevokeGPUs_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	rc := testRunContext(t, tmpDir, "env")

	// Empty DB, revoke should fail
	setupDBWithAllocations(t, tmpDir, &GPUAllocationsDB{Version: 1})

	err := RevokeGPUs(context.Background(), rc, "nobody_here")
	if err == nil {
		t.Fatal("expected error for non-existent allocation")
	}
	if !strings.Contains(err.Error(), "no GPU allocation found") {
		t.Errorf("error = %q, want 'no GPU allocation found'", err.Error())
	}
}

func TestRevokeGPUs_PreservesOtherAllocations(t *testing.T) {
	tmpDir := t.TempDir()
	username := currentUsername(t)
	rc := testRunContextReal(t, tmpDir, "env")

	// Pre-populate with current user + another
	setupDBWithAllocations(t, tmpDir, &GPUAllocationsDB{
		Version: 1,
		Allocations: []GPUAllocation{
			{Username: username, GPUs: []int{0}, Method: "env"},
			{Username: "other_user", GPUs: []int{2}, Method: "env"},
		},
	})

	err := RevokeGPUs(context.Background(), rc, username)
	if err != nil {
		t.Fatalf("RevokeGPUs: %v", err)
	}

	db, _ := loadGPUDB(rc)
	if len(db.Allocations) != 1 {
		t.Fatalf("expected 1 allocation, got %d", len(db.Allocations))
	}
	if db.Allocations[0].Username != "other_user" {
		t.Errorf("remaining user = %q, want other_user", db.Allocations[0].Username)
	}
}

// --- Apply tests ---

func TestGPUModule_ApplyNoAllocations(t *testing.T) {
	tmpDir := t.TempDir()
	rc := testRunContext(t, tmpDir, "env")

	m := NewGPUModule()
	result, err := m.Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Changed {
		t.Error("Apply should not report changed when no allocations exist")
	}
}

func TestGPUModule_ApplyEnvMethod_DryRun(t *testing.T) {
	tmpDir := t.TempDir()
	username := currentUsername(t)

	// Use dry-run runner — WriteFile will succeed without writing to /etc
	rc := testRunContext(t, tmpDir, "env")

	// Pre-populate DB with an env allocation
	setupDBWithAllocations(t, tmpDir, &GPUAllocationsDB{
		Version: 1,
		Allocations: []GPUAllocation{
			{Username: username, GPUs: []int{0, 1}, Method: "env"},
		},
	})

	m := NewGPUModule()
	result, err := m.Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Script doesn't exist at /etc/profile.d, so Apply should detect a change
	if !result.Changed {
		t.Error("Apply should report changed when script is missing")
	}
	if len(result.Messages) < 1 {
		t.Errorf("expected at least 1 message, got %d", len(result.Messages))
	}
}

func TestGPUModule_CheckCgroupMethod(t *testing.T) {
	tmpDir := t.TempDir()
	username := currentUsername(t)

	rc := testRunContext(t, tmpDir, "cgroup")

	// Pre-populate DB with cgroup method
	setupDBWithAllocations(t, tmpDir, &GPUAllocationsDB{
		Version: 1,
		Allocations: []GPUAllocation{
			{Username: username, GPUs: []int{0}, Method: "cgroup"},
		},
	})

	m := NewGPUModule()
	result, err := m.Check(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// cgroup method requires user.Lookup to find UID — if it works, there should be a pending change
	if result.Satisfied {
		t.Error("Check should NOT be satisfied when cgroup conf is missing")
	}
	if len(result.Changes) < 1 {
		t.Errorf("expected at least 1 change, got %d", len(result.Changes))
	}
	foundCgroup := false
	for _, c := range result.Changes {
		if strings.Contains(c.Description, "cgroup") {
			foundCgroup = true
			break
		}
	}
	if !foundCgroup {
		t.Error("expected a change containing 'cgroup'")
	}
}

func TestGPUModule_CheckBothMethod(t *testing.T) {
	tmpDir := t.TempDir()
	username := currentUsername(t)

	rc := testRunContext(t, tmpDir, "both")

	setupDBWithAllocations(t, tmpDir, &GPUAllocationsDB{
		Version: 1,
		Allocations: []GPUAllocation{
			{Username: username, GPUs: []int{0, 1}, Method: "both"},
		},
	})

	m := NewGPUModule()
	result, err := m.Check(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should have 3 changes: env script, cgroup conf, and docker wrapper
	if len(result.Changes) != 3 {
		t.Errorf("expected 3 changes (env+cgroup+docker), got %d", len(result.Changes))
	}
}

// --- Edge case tests ---

func TestAssignGPUs_NegativeGPUIndex(t *testing.T) {
	tmpDir := t.TempDir()
	username := currentUsername(t)
	rc := testRunContextReal(t, tmpDir, "env")

	// Pre-populate with TotalGPUs so validation triggers
	setupDBWithAllocations(t, tmpDir, &GPUAllocationsDB{
		Version:   1,
		TotalGPUs: 8,
	})

	err := AssignGPUs(context.Background(), rc, username, []int{-1}, "env")
	if err == nil {
		t.Fatal("expected error for negative GPU index")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("error = %q, want 'out of range'", err.Error())
	}
}

func TestAssignGPUs_SelfConflictIgnored(t *testing.T) {
	tmpDir := t.TempDir()
	username := currentUsername(t)
	rc := testRunContextReal(t, tmpDir, "env")

	// Assign GPU 0 first
	err := AssignGPUs(context.Background(), rc, username, []int{0}, "env")
	if err != nil {
		t.Fatalf("first assign: %v", err)
	}

	// Re-assigning same user to GPU 0 should succeed (update, not conflict)
	err = AssignGPUs(context.Background(), rc, username, []int{0, 1}, "env")
	if err != nil {
		t.Fatalf("self-reassign should succeed: %v", err)
	}
}

// TestWithGPUDBLock_NoLostUpdatesUnderConcurrency spawns many goroutines that
// each append an allocation under withGPUDBLock. Without the flock+atomic-write
// protection, read-modify-write races would cause allocations to be overwritten
// and the final count would be less than N. This test is the regression guard.
func TestWithGPUDBLock_NoLostUpdatesUnderConcurrency(t *testing.T) {
	tmpDir := t.TempDir()
	rc := testRunContextReal(t, tmpDir, "env")

	if err := saveGPUDB(rc, &GPUAllocationsDB{Version: 1, TotalGPUs: 64}); err != nil {
		t.Fatalf("seed saveGPUDB: %v", err)
	}

	const N = 20
	var wg sync.WaitGroup
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = withGPUDBLock(rc, func(db *GPUAllocationsDB) error {
				db.Allocations = append(db.Allocations, GPUAllocation{
					Username: fmt.Sprintf("testuser%d", i),
					GPUs:     []int{i},
					Method:   "env",
				})
				return nil
			})
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}

	loaded, err := loadGPUDB(rc)
	if err != nil {
		t.Fatalf("loadGPUDB after concurrent writes: %v", err)
	}
	if len(loaded.Allocations) != N {
		t.Errorf("expected %d allocations after %d concurrent appends, got %d — updates were lost", N, N, len(loaded.Allocations))
	}
}

func TestLoadGPUDB_MalformedJSON(t *testing.T) {
	tmpDir := t.TempDir()
	metaDir := filepath.Join(tmpDir, ".rootfiles")
	os.MkdirAll(metaDir, 0755)
	os.WriteFile(filepath.Join(metaDir, "gpu-allocations.json"), []byte("{invalid json"), 0600)

	rc := testRunContext(t, tmpDir, "env")
	_, err := loadGPUDB(rc)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !strings.Contains(err.Error(), "parsing GPU database") {
		t.Errorf("error = %q, want 'parsing GPU database'", err.Error())
	}
}

func TestBuildProfileDScript_SingleGPU(t *testing.T) {
	script := buildProfileDScript("bob", []int{3})
	if !strings.Contains(script, "CUDA_VISIBLE_DEVICES=3") {
		t.Error("script should contain CUDA_VISIBLE_DEVICES=3")
	}
	if !strings.Contains(script, "NVIDIA_VISIBLE_DEVICES=3") {
		t.Error("script should contain NVIDIA_VISIBLE_DEVICES=3")
	}
	if !strings.Contains(script, `"bob"`) {
		t.Error("script should contain username bob")
	}
}

func TestBuildCgroupConf_SingleGPU(t *testing.T) {
	conf := buildCgroupConf([]int{0})
	if !strings.Contains(conf, "DeviceAllow=/dev/nvidia0 rwm") {
		t.Error("missing DeviceAllow for nvidia0")
	}
	// Should NOT contain nvidia1, nvidia2, etc.
	if strings.Contains(conf, "nvidia1") {
		t.Error("should not contain nvidia1 for single GPU 0")
	}
}

func TestBuildCgroupConf_ManyGPUs(t *testing.T) {
	conf := buildCgroupConf([]int{0, 1, 2, 3, 4, 5, 6, 7})
	for i := 0; i < 8; i++ {
		expect := "DeviceAllow=/dev/nvidia" + strings.TrimSpace(strings.Replace("0,1,2,3,4,5,6,7", ",", "", -1)[i:i+1]) + " rwm"
		_ = expect
	}
	// Simpler: just check all 8 are present
	for i := 0; i < 8; i++ {
		s := filepath.Base("nvidia" + string(rune('0'+i)))
		if !strings.Contains(conf, "DeviceAllow=/dev/"+s+" rwm") {
			t.Errorf("missing DeviceAllow for %s", s)
		}
	}
}

func TestGPUListStr_Empty(t *testing.T) {
	got := gpuListStr([]int{})
	if got != "" {
		t.Errorf("gpuListStr([]) = %q, want empty", got)
	}
}

func TestListGPUAllocations_MultipleUsers(t *testing.T) {
	tmpDir := t.TempDir()
	setupDBWithAllocations(t, tmpDir, &GPUAllocationsDB{
		Version:   1,
		TotalGPUs: 8,
		GPUModel:  "NVIDIA H100",
		Allocations: []GPUAllocation{
			{Username: "alice", GPUs: []int{0, 1}, Method: "env", UpdatedAt: "2026-01-01T00:00:00Z"},
			{Username: "bob", GPUs: []int{2, 3}, Method: "cgroup", UpdatedAt: "2026-01-02T00:00:00Z"},
			{Username: "carol", GPUs: []int{4, 5, 6, 7}, Method: "both", UpdatedAt: "2026-01-03T00:00:00Z"},
		},
	})

	rc := testRunContext(t, tmpDir, "env")

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := ListGPUAllocations(rc)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	for _, expect := range []string{"alice", "bob", "carol", "NVIDIA H100", "0,1", "2,3", "4,5,6,7", "cgroup", "both"} {
		if !strings.Contains(output, expect) {
			t.Errorf("output missing %q", expect)
		}
	}
}

func TestGPUModule_CheckDetectsMultipleEnvChanges(t *testing.T) {
	tmpDir := t.TempDir()
	username := currentUsername(t)

	rc := testRunContext(t, tmpDir, "env")

	// Two users with env method — both scripts missing
	setupDBWithAllocations(t, tmpDir, &GPUAllocationsDB{
		Version: 1,
		Allocations: []GPUAllocation{
			{Username: username, GPUs: []int{0, 1}, Method: "env"},
			{Username: "other_user", GPUs: []int{2, 3}, Method: "env"},
		},
	})

	m := NewGPUModule()
	result, err := m.Check(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// At least the current user's script should be detected as missing.
	// "other_user" may or may not exist on the system, but env method doesn't
	// require user.Lookup so both changes should be detected.
	if result.Satisfied {
		t.Error("Check should NOT be satisfied when scripts are missing")
	}
	if len(result.Changes) < 1 {
		t.Errorf("expected at least 1 change, got %d", len(result.Changes))
	}
}

// --- Docker wrapper tests ---

func TestBuildDockerWrapper(t *testing.T) {
	script := buildDockerWrapper("/home/.rootfiles/gpu-allocations.json")

	for _, expect := range []string{
		"#!/bin/bash",
		"# Managed by rootfiles-v2",
		dockerRealPath,
		"python3",
		"--gpus",
		"NVIDIA_VISIBLE_DEVICES",
		"CUDA_VISIBLE_DEVICES",
		"compose",
		`$(id -u)`,
		"gpu-allocations.json",
	} {
		if !strings.Contains(script, expect) {
			t.Errorf("wrapper script missing %q", expect)
		}
	}
}

func TestBuildDockerWrapper_SyntaxCheck(t *testing.T) {
	script := buildDockerWrapper("/tmp/test-gpu-db.json")

	// Write to temp file and run bash -n for syntax check
	tmpFile := filepath.Join(t.TempDir(), "docker-wrapper.sh")
	if err := os.WriteFile(tmpFile, []byte(script), 0755); err != nil {
		t.Fatalf("writing temp script: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	runner := exec.NewRunner(false, logger)
	result, err := runner.Query(context.Background(), "bash", "-n", tmpFile)
	if err != nil {
		t.Fatalf("bash -n failed: %v\nstderr: %s", err, result.Stderr)
	}
}

func TestDockerWrapperIntegration_CheckIncludesWrapper(t *testing.T) {
	tmpDir := t.TempDir()
	username := currentUsername(t)
	rc := testRunContext(t, tmpDir, "env")

	setupDBWithAllocations(t, tmpDir, &GPUAllocationsDB{
		Version: 1,
		Allocations: []GPUAllocation{
			{Username: username, GPUs: []int{0}, Method: "env"},
		},
	})

	m := NewGPUModule()
	result, err := m.Check(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should include a change for the Docker wrapper
	foundWrapper := false
	for _, c := range result.Changes {
		if strings.Contains(c.Description, "Docker") {
			foundWrapper = true
			break
		}
	}
	if !foundWrapper {
		t.Error("Check should include Docker wrapper change when allocations exist")
	}
}

func TestDockerWrapperIntegration_CheckNoAllocations(t *testing.T) {
	tmpDir := t.TempDir()
	rc := testRunContext(t, tmpDir, "env")

	// No DB file — no allocations
	m := NewGPUModule()
	result, err := m.Check(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Satisfied {
		t.Error("Check should be satisfied when no allocations exist")
	}
}

func TestDockerWrapperIntegration_ApplyIncludesWrapper(t *testing.T) {
	tmpDir := t.TempDir()
	username := currentUsername(t)
	rc := testRunContext(t, tmpDir, "env")

	setupDBWithAllocations(t, tmpDir, &GPUAllocationsDB{
		Version: 1,
		Allocations: []GPUAllocation{
			{Username: username, GPUs: []int{0}, Method: "env"},
		},
	})

	m := NewGPUModule()
	result, err := m.Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Changed {
		t.Error("Apply should report changed when wrapper is missing")
	}
	foundWrapper := false
	for _, msg := range result.Messages {
		if strings.Contains(msg, "Docker") {
			foundWrapper = true
			break
		}
	}
	if !foundWrapper {
		t.Error("Apply messages should mention Docker wrapper")
	}
}
