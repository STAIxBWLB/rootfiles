package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/module"
)

func TestParseGPUList(t *testing.T) {
	tests := []struct {
		input   string
		want    []int
		wantErr bool
	}{
		{"0", []int{0}, false},
		{"0,1,2", []int{0, 1, 2}, false},
		{"0, 1, 2", []int{0, 1, 2}, false},
		{"7", []int{7}, false},
		{"", nil, true},
		{"abc", nil, true},
		{"-1", nil, true},
		{"0,abc", nil, true},
	}
	for _, tt := range tests {
		got, err := parseGPUList(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseGPUList(%q) expected error, got nil", tt.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseGPUList(%q) unexpected error: %v", tt.input, err)
			continue
		}
		if len(got) != len(tt.want) {
			t.Errorf("parseGPUList(%q) = %v, want %v", tt.input, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("parseGPUList(%q)[%d] = %d, want %d", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

func TestNewGPUCmd_Structure(t *testing.T) {
	cmd := newGPUCmd()
	if cmd.Use != "gpu" {
		t.Errorf("Use = %q, want gpu", cmd.Use)
	}

	// Verify subcommands exist. The argument notation of a Use line is not part
	// of this check: TestHelpStyleAcrossTree holds that.
	subCmds := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		subCmds[sub.Name()] = true
	}

	expected := []string{"assign", "revoke", "list", "status"}
	for _, name := range expected {
		if !subCmds[name] {
			t.Errorf("missing subcommand %q", name)
		}
	}
}

func TestNewGPUCmd_AssignFlags(t *testing.T) {
	cmd := newGPUCmd()
	var assignCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "assign" {
			assignCmd = sub
			break
		}
	}
	if assignCmd == nil {
		t.Fatal("assign subcommand not found")
	}

	gpusFlag := assignCmd.Flags().Lookup("gpus")
	if gpusFlag == nil {
		t.Error("--gpus flag not found on assign command")
	}

	methodFlag := assignCmd.Flags().Lookup("method")
	if methodFlag == nil {
		t.Error("--method flag not found on assign command")
	}
}

func TestParseGPUList_DuplicateIndices(t *testing.T) {
	// Duplicates are currently allowed (no dedup logic)
	got, err := parseGPUList("0,0,1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("len = %d, want 3 (duplicates preserved)", len(got))
	}
}

func TestParseGPUList_Whitespace(t *testing.T) {
	got, err := parseGPUList("  0 , 1 , 2  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 2 {
		t.Errorf("got %v, want [0,1,2]", got)
	}
}

func TestParseGPUList_TrailingComma(t *testing.T) {
	got, err := parseGPUList("0,1,")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Trailing comma produces empty string → skipped
	if len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Errorf("got %v, want [0,1]", got)
	}
}

func TestParseGPUList_LargeIndex(t *testing.T) {
	got, err := parseGPUList("15")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[0] != 15 {
		t.Errorf("got %d, want 15", got[0])
	}
}

func TestNewGPUCmd_SubcommandHelp(t *testing.T) {
	cmd := newGPUCmd()

	// Verify each subcommand has a Short description
	for _, sub := range cmd.Commands() {
		if sub.Short == "" {
			t.Errorf("subcommand %q has no Short description", sub.Name())
		}
	}
}

func TestNewGPUCmd_AssignRequiresExactlyOneArg(t *testing.T) {
	cmd := newGPUCmd()
	cmd.SetArgs([]string{"assign"}) // no USERNAME arg
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Error("assign with no args should fail")
	}
}

func TestNewGPUCmd_RevokeRequiresExactlyOneArg(t *testing.T) {
	cmd := newGPUCmd()
	cmd.SetArgs([]string{"revoke"}) // no USERNAME arg
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Error("revoke with no args should fail")
	}
}

// A failed MIG read exits with its cause and a command that shows the
// driver state on the same host (#180).
func TestMIGStatusFailureNamesNextStep(t *testing.T) {
	pinInventoryPage(t)
	rootDir, site, _ := statusContractFixture(t, "populated")
	t.Chdir(rootDir)
	saved := migStatusCollector
	t.Cleanup(func() { migStatusCollector = saved })
	migStatusCollector = func(context.Context, *module.RunContext) ([]module.MIGGPU, error) {
		return nil, errors.New("nvidia-smi -L: fixture driver error")
	}
	root := NewRootCmd("test", "test")
	var stderr bytes.Buffer
	root.SetOut(io.Discard)
	root.SetErr(&stderr)
	root.SetArgs([]string{"gpu", "mig", "status", "--config", site})
	if err := executeCommand(context.Background(), root); err == nil {
		t.Fatal("failed MIG read exited 0")
	}
	if !strings.Contains(stderr.String(), "nvidia-smi -L: fixture driver error") || !strings.Contains(stderr.String(), " nvidia-smi\n") {
		t.Fatalf("stderr=%q, want the cause and the nvidia-smi next step", stderr.String())
	}
}
