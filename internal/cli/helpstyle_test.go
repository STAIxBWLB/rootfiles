package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestHelpStyleAcrossTree holds every command to the help style the audit
// settled: one short ASCII summary, a page inside the 80-column budget, and
// boolean flags printed bare.
//
// It is the guard for those rules, not the whole of T2: an example on every
// runnable command and the usage notation are still being written, so a command
// with no example is not failed here yet.
func TestHelpStyleAcrossTree(t *testing.T) {
	pinInventoryPage(t)
	root := NewRootCmd("dev", "test")
	walkCommands(root, func(c *cobra.Command) {
		facts := factsFor(c)
		for _, issue := range facts.Issues() {
			if issue == "no example" {
				continue
			}
			t.Errorf("%s: %s", facts.Path, issue)
		}
		if page := renderHelp(c); strings.Contains(page, "[=true|false]") {
			t.Errorf("%s: help prints a boolean's implicit value", facts.Path)
		}
	})
}
