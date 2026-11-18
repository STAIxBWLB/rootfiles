package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

func TestRootHelpGroupsAccountForEveryCommand(t *testing.T) {
	root := NewRootCmd("dev", "test")
	prepareCompletionBoundary(root)
	if len(root.Groups()) != 4 {
		t.Fatalf("groups=%d, want 4", len(root.Groups()))
	}
	for _, cmd := range root.Commands() {
		if !cmd.Hidden && (cmd.GroupID == "" || !root.ContainsGroup(cmd.GroupID)) {
			t.Errorf("command %s has no valid group (group=%q)", cmd.Name(), cmd.GroupID)
		}
	}
}

func TestHelpColorASCIIAndPipePolicy(t *testing.T) {
	for _, tc := range []struct {
		name, color, noColor, force string
		ascii, escapes              bool
	}{
		{"pipe", "auto", "", "", false, false},
		{"force", "auto", "", "1", false, true},
		{"no color", "auto", "1", "1", false, false},
		{"explicit always", "always", "1", "", false, true},
		{"explicit ASCII", "never", "", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinInventoryPage(t)
			t.Setenv("LC_ALL", "C.UTF-8")
			t.Setenv("COLUMNS", "")
			t.Setenv("NO_COLOR", tc.noColor)
			t.Setenv("FORCE_COLOR", tc.force)
			t.Setenv("CLICOLOR_FORCE", "")
			t.Setenv("CLICOLOR", "")
			root := NewRootCmd("dev", "test")
			var out, stderr bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&stderr)
			args := []string{"--help", "--color=" + tc.color}
			if tc.ascii {
				args = append(args, "--ascii")
			}
			root.SetArgs(args)
			if err := executeCommand(context.Background(), root); err != nil || stderr.Len() != 0 {
				t.Fatalf("help policy: err=%v stderr=%q", err, stderr.String())
			}
			if strings.ContainsRune(out.String(), 27) != tc.escapes {
				t.Fatalf("help escapes=%v, want %v:\n%q", strings.ContainsRune(out.String(), 27), tc.escapes, out.String())
			}
			plain := ansi.Strip(out.String())
			for _, section := range []string{"Usage", "Setup", "Users, GPUs and tenants", "Fleet", "Global flags"} {
				if !strings.Contains(plain, section) {
					t.Errorf("missing section %q in:\n%s", section, plain)
				}
			}
			if tc.ascii {
				if !strings.Contains(plain, "> Usage") || strings.Contains(plain, ui.MarkSection) {
					t.Fatalf("help ignored ASCII: %q", plain)
				}
			} else if !strings.Contains(plain, ui.MarkSection+" Usage") {
				t.Fatalf("help missing Unicode section marker: %q", plain)
			}
		})
	}
}

func TestThemeWarningOnInvalidEnv(t *testing.T) {
	pinInventoryPage(t)
	t.Setenv("LC_ALL", "C.UTF-8")
	t.Setenv("ROOTFILES_COLOR_THEME", "invalid-theme")
	root := NewRootCmd("dev", "test")
	var out, stderr bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&stderr)
	root.SetArgs([]string{"--help", "--color=never"})
	if err := executeCommand(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "rootfiles: warning: unknown ROOTFILES_COLOR_THEME") {
		t.Fatalf("expected theme warning on stderr, got %q", stderr.String())
	}
}
