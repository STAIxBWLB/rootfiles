package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
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

// Raw-payload and JSON commands keep machine stdout, but their help is human.
func TestHelpHonorsDisplayFlagsOnMachineCommands(t *testing.T) {
	for _, args := range [][]string{{"config", "show"}, {"tenant", "logs"}, {"fleet", "targets"}, {"status", "--json"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			pinInventoryPage(t)
			t.Setenv("LC_ALL", "C.UTF-8")
			root := NewRootCmd("dev", "test")
			var out, stderr bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&stderr)
			root.SetArgs(append(args, "--help", "--ascii", "--color=always"))
			if err := executeCommand(context.Background(), root); err != nil {
				t.Fatalf("help: %v (stderr=%q)", err, stderr.String())
			}
			plain := ansi.Strip(out.String())
			if !strings.ContainsRune(out.String(), 27) || !strings.Contains(plain, "> Usage") || strings.Contains(plain, ui.MarkSection) {
				t.Fatalf("help ignored --ascii/--color=always:\n%q", out.String())
			}
		})
	}
}

// The unknown-theme warning is printed once, after the display flags are
// parsed: the theme name carries a Relation marker that --ascii rewrites (#178).
func TestThemeWarningOnInvalidEnv(t *testing.T) {
	pinInventoryPage(t)
	t.Setenv("LC_ALL", "C.UTF-8")
	t.Setenv("ROOTFILES_COLOR_THEME", "bad"+ui.Relation+"theme")
	for _, tc := range []struct {
		args    []string
		want    string
		failing bool
	}{
		{args: []string{"--help", "--color=never"}, want: `"bad` + ui.Relation + `theme"`},
		{args: []string{"--help", "--ascii", "--color=never"}, want: `"bad->theme"`},
		{args: []string{"help", "config", "--ascii"}, want: `"bad->theme"`},
		{args: []string{"config", "show", "--profile", "minimal", "--ascii", "--color=never"}, want: `"bad->theme"`},
		{args: []string{"--version", "--ascii"}, want: `"bad->theme"`},
		{args: []string{"no-such-command", "--ascii"}, want: `"bad->theme"`, failing: true},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			root := NewRootCmd("dev", "test")
			var out, stderr bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&stderr)
			root.SetArgs(tc.args)
			if err := executeCommand(context.Background(), root); (err != nil) != tc.failing {
				t.Fatalf("err = %v, want failing=%v (stderr=%q)", err, tc.failing, stderr.String())
			}
			warning := "rootfiles: warning: unknown ROOTFILES_COLOR_THEME " + tc.want
			if n := strings.Count(stderr.String(), "unknown ROOTFILES_COLOR_THEME"); n != 1 || !strings.Contains(stderr.String(), warning) {
				t.Fatalf("want %q exactly once on stderr, got %d in %q", warning, n, stderr.String())
			}
		})
	}
}

// When parsing stops at a bad flag, the display flags after it still style
// stderr; a value flag keeps its argument, as pflag reads it (#185).
func TestDisplayFlagsAfterAFlagError(t *testing.T) {
	pinInventoryPage(t)
	t.Setenv("LC_ALL", "C.UTF-8")
	t.Setenv("ROOTFILES_COLOR_THEME", "bad"+ui.Relation+"theme")
	saved := os.Args
	t.Cleanup(func() { os.Args = saved })
	for _, tc := range []struct {
		args         []string
		theme, color string
	}{
		{[]string{"status", "--zzz-unknown", "--ascii", "--color", "never"}, `"bad->theme"`, "never"},
		{[]string{"--color=always", "status", "--zzz-unknown", "--ascii=false", "--color=never"}, `"bad` + ui.Relation + `theme"`, "never"},
		{[]string{"status", "--profile", "--ascii", "--zzz-unknown"}, `"bad` + ui.Relation + `theme"`, "auto"},
		{[]string{"status", "--zzz-unknown", "--", "--ascii"}, `"bad` + ui.Relation + `theme"`, "auto"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			os.Args = append([]string{"rootfiles"}, tc.args...)
			root := NewRootCmd("dev", "test")
			var out, stderr bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&stderr)
			root.SetArgs(tc.args)
			var exit *ExitError
			if err := executeCommand(context.Background(), root); !errors.As(err, &exit) || exit.Code != 2 {
				t.Fatalf("err = %v, want a usage error", err)
			}
			if !strings.Contains(stderr.String(), "unknown flag: --zzz-unknown") || !strings.Contains(stderr.String(), "unknown ROOTFILES_COLOR_THEME "+tc.theme) {
				t.Fatalf("want the usage error and theme %s on stderr, got %q", tc.theme, stderr.String())
			}
			if color, _ := root.PersistentFlags().GetString("color"); color != tc.color {
				t.Fatalf("--color = %q, want %q", color, tc.color)
			}
		})
	}
}
