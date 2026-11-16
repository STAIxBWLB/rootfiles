package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
)

// TestHelpProseWidthWithALongStateDir renders the whole tree with a state
// directory long enough to wrap a concatenated path, so no page can hard-code a
// dynamic path's length into its prose. Flag usage is measured separately: a
// default that names a path the environment supplies is data, not prose.
func TestHelpProseWidthWithALongStateDir(t *testing.T) {
	pinInventoryPage(t)
	long := filepath.Join(t.TempDir(), strings.Repeat("nested-state-", 12), "rootfiles")
	t.Setenv("ROOTFILES_STATE_DIR", long)
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), strings.Repeat("xdg-state-", 12)))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), strings.Repeat("xdg-config-", 12)))
	root := NewRootCmd("dev", "test")
	pages := 0
	walkCommands(root, func(c *cobra.Command) {
		pages++
		if n := proseWidth(c); n > InventoryWidth {
			t.Errorf("%s: help prose is %d columns with a %d-character state directory", c.CommandPath(), n, len(long))
		}
	})
	if pages < 90 {
		t.Fatalf("rendered %d pages, fewer than the tree has", pages)
	}
}

// proseWidth is the widest line the command itself wrote: its summary, long
// text, examples and usage line.
func proseWidth(c *cobra.Command) int {
	widest := 0
	for _, text := range []string{c.Short, c.Long, c.Example, c.UseLine()} {
		for _, line := range strings.Split(text, "\n") {
			if n := ansi.StringWidth(line); n > widest {
				widest = n
			}
		}
	}
	return widest
}

// TestLongStateDirAppearsInTheReportNotTheHelp names the trade: the resolved
// directory is data a report prints, so help stays inside its budget while the
// operator still learns the path in use.
func TestLongStateDirAppearsInTheReportNotTheHelp(t *testing.T) {
	pinInventoryPage(t)
	long := filepath.Join(t.TempDir(), strings.Repeat("nested-state-", 12), "rootfiles")
	t.Setenv("ROOTFILES_STATE_DIR", long)
	for _, path := range [][]string{{"schedule"}, {"rollback"}} {
		root := NewRootCmd("dev", "test")
		c, _, err := root.Find(path)
		if err != nil {
			t.Fatal(err)
		}
		if page := renderHelp(c); strings.Contains(page, long) {
			t.Errorf("%s: help prints the resolved state directory", c.CommandPath())
		}
	}
}
