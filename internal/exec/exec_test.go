package exec

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func quietRunner(dryRun bool) *Runner {
	return NewRunner(dryRun, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestParseDpkgQuery(t *testing.T) {
	out := "git install ok installed\n" +
		"libc6:amd64 install ok installed\n" +
		"held hold ok installed\n" +
		"gone deinstall ok config-files\n" +
		"ghost unknown ok not-installed\n"
	got := parseDpkgQuery(out)
	for _, p := range []string{"git", "libc6", "held"} {
		if !got[p] {
			t.Errorf("%s should be installed", p)
		}
	}
	for _, p := range []string{"gone", "ghost"} {
		if got[p] {
			t.Errorf("%s should not be installed", p)
		}
	}
}

func TestRunner_DryRunDoesNotExecute(t *testing.T) {
	r := quietRunner(true)
	marker := filepath.Join(t.TempDir(), "marker")
	if _, err := r.Run(context.Background(), "touch", marker); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteFile(marker, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("dry-run must not create files")
	}
}

func TestRunner_RunEnvAndInput(t *testing.T) {
	r := quietRunner(false)
	res, err := r.RunEnv(context.Background(), []string{"ROOTFILES_TEST_VAR=hello"}, "sh", "-c", "echo $ROOTFILES_TEST_VAR")
	if err != nil || strings.TrimSpace(res.Stdout) != "hello" {
		t.Errorf("RunEnv = %q, %v", res.Stdout, err)
	}
	res, err = r.RunInput(context.Background(), "secret\n", "cat")
	if err != nil || res.Stdout != "secret\n" {
		t.Errorf("RunInput = %q, %v", res.Stdout, err)
	}
}

func TestRunner_RemoveIsNotRecursive(t *testing.T) {
	r := quietRunner(false)
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	os.MkdirAll(sub, 0755)
	os.WriteFile(filepath.Join(sub, "f"), []byte("x"), 0644)
	if err := r.Remove(sub); err == nil {
		t.Error("Remove must refuse a non-empty directory")
	}
	if _, err := os.Stat(filepath.Join(sub, "f")); err != nil {
		t.Error("file inside directory was removed")
	}
}

func TestRunnerWriteFileExclusive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "inventory")
	r := NewRunner(false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := r.WriteFileExclusive(path, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteFileExclusive(path, []byte("second"), 0600); err == nil {
		t.Fatal("existing file replaced")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "first" {
		t.Fatalf("content: %q %v", data, err)
	}
	r.DryRun = true
	preview := filepath.Join(dir, "preview")
	if err := r.WriteFileExclusive(preview, []byte("preview"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(preview); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote file: %v", err)
	}
}
