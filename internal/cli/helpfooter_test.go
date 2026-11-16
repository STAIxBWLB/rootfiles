package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestHelpFooterResolves runs what a parent page's footer tells a person to run
// instead of only measuring the line. `help` is registered on the root, so a
// footer that names the current command path first suggests an invocation no
// nested parent accepts.
func TestHelpFooterResolves(t *testing.T) {
	pinInventoryPage(t)
	root := NewRootCmd("dev", "test")
	parents := 0
	walkCommands(root, func(c *cobra.Command) {
		if !c.HasAvailableSubCommands() {
			return
		}
		parents++
		named, ok := footerHelpPath(renderHelp(c))
		if !ok {
			t.Fatalf("%s: no help footer", c.CommandPath())
		}
		if named != root.CommandPath() {
			t.Fatalf("%s: footer names %q, but `help` is a subcommand of %q", c.CommandPath(), named, root.CommandPath())
		}
		for _, child := range c.Commands() {
			args := append([]string{"help"}, strings.Fields(strings.TrimPrefix(child.CommandPath(), named))...)
			var out bytes.Buffer
			cmd := NewRootCmd("dev", "test")
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(args)
			if err := cmd.Execute(); err != nil {
				t.Errorf("%s: footer %q does not resolve: %v", c.CommandPath(), strings.Join(args, " "), err)
				continue
			}
			page := out.String()
			if strings.Contains(page, "Unknown help topic") || !strings.Contains(page, child.UseLine()) {
				t.Errorf("%s: %q printed %q, not the help of %s", c.CommandPath(), strings.Join(args, " "), clipLines(page, 3), child.CommandPath())
			}
		}
	})
	if parents < 10 {
		t.Fatalf("walked %d parents, fewer than the tree has", parents)
	}
}

// footerHelpPath reads the command path a page's footer names in
// `Run "<path> help <command>" for details.`
func footerHelpPath(page string) (string, bool) {
	const open, close = `Run "`, ` help <command>" for details.`
	for _, line := range strings.Split(page, "\n") {
		start := strings.Index(line, open)
		if start < 0 {
			continue
		}
		rest := line[start+len(open):]
		if end := strings.Index(rest, close); end >= 0 {
			return rest[:end], true
		}
	}
	return "", false
}

func clipLines(s string, limit int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > limit {
		lines = lines[:limit]
	}
	return strings.Join(lines, " | ")
}
