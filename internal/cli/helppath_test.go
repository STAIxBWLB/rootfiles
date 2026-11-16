package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestHelpWidthWithALongStateDir renders the whole tree with a state directory
// long enough to wrap a concatenated path, so no page can exceed the budget on
// a value the environment supplies.
//
// Flag defaults are part of the page: a default that names the state directory
// is longer on one machine than another, which is why a flag whose default is
// such a path carries none (`backup --dir`, `fleet --inventory`) and resolves
// it at run time instead.
func TestHelpWidthWithALongStateDir(t *testing.T) {
	pinInventoryPage(t)
	long := filepath.Join(t.TempDir(), strings.Repeat("nested-state-", 12), "rootfiles")
	t.Setenv("ROOTFILES_STATE_DIR", long)
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), strings.Repeat("xdg-state-", 12)))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), strings.Repeat("xdg-config-", 12)))
	root := NewRootCmd("dev", "test")
	pages := 0
	walkCommands(root, func(c *cobra.Command) {
		pages++
		if n := widestLine(renderHelp(c)); n > InventoryWidth {
			t.Errorf("%s: help page is %d columns with a %d-character state directory", c.CommandPath(), n, len(long))
		}
	})
	if pages < 90 {
		t.Fatalf("rendered %d pages, fewer than the tree has", pages)
	}
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
