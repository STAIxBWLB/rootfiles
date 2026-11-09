package module

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/exec"
)

func TestLegacyMetadataDryRunDoesNotMigrate(t *testing.T) {
	rc := newDryRunRC(t)
	base := t.TempDir()
	rc.Config.Users.HomeBase = base
	legacy := filepath.Join(base, ".rootfiles")
	const content = `{"version":1,"users":[]}`
	if err := os.WriteFile(legacy, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := NewUsersModule().Apply(context.Background(), rc)
	if err == nil || !strings.Contains(err.Error(), legacy) || !strings.Contains(err.Error(), "users.json") || !strings.Contains(err.Error(), "move") {
		t.Fatalf("missing migration guidance: %v", err)
	}
	data, err := os.ReadFile(legacy)
	if err != nil || string(data) != content {
		t.Fatalf("legacy data changed: %q, %v", data, err)
	}
	entries, err := os.ReadDir(base)
	if err != nil || len(entries) != 1 {
		t.Fatalf("dry-run added paths: %v, %v", entries, err)
	}
}

func TestJSONDBAtomicBackupAndModes(t *testing.T) {
	for _, mode := range []os.FileMode{0600, 0644} {
		t.Run(mode.String(), func(t *testing.T) {
			rc := newDryRunRC(t)
			rc.Runner.DryRun, rc.DryRun = false, false
			rc.Runner.Backup = exec.NewBackup(t.TempDir(), "JSON ledger test")
			file := filepath.Join(t.TempDir(), "db.json")
			const old = `{"version":1}`
			if err := os.WriteFile(file, []byte(old), mode); err != nil {
				t.Fatal(err)
			}
			if err := writeJSONDB(rc, file, map[string]int{"version": 2}, mode); err != nil {
				t.Fatal(err)
			}
			info, _ := os.Stat(file)
			if info.Mode().Perm() != mode {
				t.Fatalf("mode %o, want %o", info.Mode().Perm(), mode)
			}
			var db map[string]int
			found, err := readJSONDB(file, &db)
			if !found || err != nil || db["version"] != 2 {
				t.Fatalf("new database: %v, %v", db, err)
			}
			siblings, _ := os.ReadDir(filepath.Dir(file))
			if len(siblings) != 1 {
				t.Fatalf("temporary files leaked: %v", siblings)
			}
			if _, err := exec.RestoreBackup(rc.Runner.Backup.Root, rc.Runner.Backup.ID, false); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(file)
			if string(data) != old {
				t.Fatalf("backup did not preserve original: %q", data)
			}
		})
	}
}

func TestJSONDBDryRunAndReadErrors(t *testing.T) {
	rc := newDryRunRC(t)
	file := filepath.Join(t.TempDir(), "db.json")
	var db map[string]int
	if found, err := readJSONDB(file, &db); found || err != nil {
		t.Fatalf("absent DB: %v, %v", found, err)
	}
	if err := writeJSONDB(rc, file, map[string]int{"version": 1}, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("dry-run created DB: %v", err)
	}
	if err := os.WriteFile(file, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readJSONDB(file, &db); err == nil || !strings.Contains(err.Error(), "restore a valid backup") {
		t.Fatalf("corruption hidden: %v", err)
	}
	if _, err := readJSONDB(filepath.Dir(file), &db); err == nil {
		t.Fatal("directory read failure hidden")
	}
}

func TestModuleMutationsUseRunner(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]bool{"WriteFile": true, "Rename": true, "MkdirAll": true, "Mkdir": true, "Remove": true, "RemoveAll": true}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if ok && pkg.Name == "os" && mutations[sel.Sel.Name] {
				t.Errorf("%s: os.%s mutation bypasses Runner", file, sel.Sel.Name)
			}
			return true
		})
	}
}

func TestJSONDBRefusesSymlinksAndCorruptGPUMutations(t *testing.T) {
	rc := newDryRunRC(t)
	rc.Runner.DryRun, rc.DryRun = false, false
	target := filepath.Join(t.TempDir(), "original.json")
	if err := os.WriteFile(target, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "db.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONDB(rc, link, map[string]int{"version": 2}, 0600); err == nil {
		t.Fatal("symlink accepted")
	}
	if data, _ := os.ReadFile(target); string(data) != "preserve" {
		t.Fatal("symlink target changed")
	}
	gpuPath := gpuDBPath(rc)
	if err := os.WriteFile(gpuPath, []byte("broken"), 0644); err != nil {
		t.Fatal(err)
	}
	called := false
	err := withGPUDBLock(rc, func(db *GPUAllocationsDB) error { called = true; db.Version = 2; return nil })
	if err == nil || called {
		t.Fatalf("corrupt GPU ledger permitted mutation: called=%t, err=%v", called, err)
	}
	if data, _ := os.ReadFile(gpuPath); string(data) != "broken" {
		t.Fatal("corrupt GPU ledger overwritten")
	}
}
