package module

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

func TestStorageModule_Name(t *testing.T) {
	if n := NewStorageModule().Name(); n != "storage" {
		t.Errorf("Name() = %q, want storage", n)
	}
}

func TestStorageModule_CheckMissingDataDir(t *testing.T) {
	tmp := t.TempDir()
	rc := newDryRunRC(t)
	rc.Config.Modules.Storage = config.StorageConfig{DataDir: filepath.Join(tmp, "data-does-not-exist")}

	result, err := NewStorageModule().Check(context.Background(), rc)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.Satisfied {
		t.Error("Check should not be satisfied when DataDir is missing")
	}
	if len(result.Changes) == 0 {
		t.Error("Check should report at least one change for missing DataDir")
	}
}

func TestStorageModule_CheckDefaultHomeBaseIsSatisfied(t *testing.T) {
	rc := newDryRunRC(t)
	// Default HomeBase="" and no DataDir/Symlinks → nothing to do.
	result, err := NewStorageModule().Check(context.Background(), rc)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !result.Satisfied {
		t.Errorf("Check with empty storage config should be satisfied, got %+v", result.Changes)
	}
}

func TestStorageModule_ApplyDryRun(t *testing.T) {
	tmp := t.TempDir()
	rc := newDryRunRC(t)
	rc.Config.Modules.Storage = config.StorageConfig{DataDir: filepath.Join(tmp, "data")}
	result, err := NewStorageModule().Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Changed {
		t.Error("Apply with configured DataDir should report Changed=true")
	}
}

func TestIsSymlinkTo(t *testing.T) {
	if isSymlinkTo("/path/that/does/not/exist", "/other") {
		t.Error("isSymlinkTo should return false for non-existent path")
	}
}

func newRealRC(t *testing.T) *RunContext {
	t.Helper()
	rc := newDryRunRC(t)
	rc.Runner.DryRun = false
	rc.DryRun = false
	return rc
}

func TestStorageModule_ApplyRefusesNonEmptyDirectory(t *testing.T) {
	tmp := t.TempDir()
	link := filepath.Join(tmp, "data")
	target := filepath.Join(tmp, "raid-data")
	if err := os.MkdirAll(link, 0755); err != nil {
		t.Fatal(err)
	}
	precious := filepath.Join(link, "precious.txt")
	if err := os.WriteFile(precious, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}

	rc := newRealRC(t)
	rc.Config.Modules.Storage = config.StorageConfig{Symlinks: map[string]string{link: target}}

	check, err := NewStorageModule().Check(context.Background(), rc)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if check.Satisfied || !strings.Contains(check.Changes[0].Description, "blocked") {
		t.Errorf("Check should flag the blocked symlink, got %+v", check.Changes)
	}

	if _, err := NewStorageModule().Apply(context.Background(), rc); err == nil {
		t.Fatal("Apply should refuse to replace a non-empty directory")
	}
	if _, err := os.Stat(precious); err != nil {
		t.Fatalf("existing data was removed: %v", err)
	}
}

func TestStorageModule_ApplyReplacesEmptyDirAndStaleSymlink(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "raid-data")
	emptyDir := filepath.Join(tmp, "empty")
	stale := filepath.Join(tmp, "stale")
	if err := os.MkdirAll(emptyDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(tmp, "elsewhere"), stale); err != nil {
		t.Fatal(err)
	}

	rc := newRealRC(t)
	rc.Config.Modules.Storage = config.StorageConfig{Symlinks: map[string]string{
		emptyDir: target,
		stale:    target,
	}}
	result, err := NewStorageModule().Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Changed {
		t.Error("Apply should report Changed=true")
	}
	for _, l := range []string{emptyDir, stale} {
		if !isSymlinkTo(l, target) {
			t.Errorf("%s should now point to %s", l, target)
		}
	}

	// Second run is a no-op.
	result, err = NewStorageModule().Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if result.Changed {
		t.Errorf("second Apply should be a no-op, got %v", result.Messages)
	}
}
