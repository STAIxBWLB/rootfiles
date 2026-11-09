package exec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQueryGuardRunsBeforeExecution(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "fixture-query")
	marker := filepath.Join(dir, "executed")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf called > \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	refused := errors.New("outside fixture binaries")
	runner := quietRunner(true)
	var resolved string
	runner.QueryGuard = func(executable string) error {
		resolved = executable
		return refused
	}
	result, err := runner.Query(context.Background(), "fixture-query", marker)
	if !errors.Is(err, refused) || !strings.Contains(err.Error(), "refused before execution") {
		t.Fatalf("Query error = %v, want guard refusal", err)
	}
	if resolved != binary {
		t.Fatalf("guard received %q, want resolved executable %q", resolved, binary)
	}
	if result == nil || result.ExitCode != -1 {
		t.Fatalf("refused result = %+v, want no process exit status", result)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("refused command executed: marker stat = %v", err)
	}
}

func TestQueryGuardOptionalAndAllowsExecution(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "fixture-query")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf fixture-output\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, guarded := range []bool{false, true} {
		runner := quietRunner(true)
		called := false
		if guarded {
			runner.QueryGuard = func(executable string) error {
				called = true
				if executable != binary {
					t.Errorf("guard received %q, want %q", executable, binary)
				}
				return nil
			}
		}
		result, err := runner.Query(context.Background(), binary)
		if err != nil || result == nil || result.Stdout != "fixture-output" {
			t.Fatalf("guarded=%t: Query = %+v, %v", guarded, result, err)
		}
		if called != guarded {
			t.Errorf("guarded=%t: guard called=%t", guarded, called)
		}
	}
}
