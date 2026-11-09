package cli

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// TestDocsCommandsCoverCobraTree keeps docs/commands.md in step with the
// command tree: every visible command has a "### <command path>" heading, and
// its section names every visible local flag as --name.
func TestDocsCommandsCoverCobraTree(t *testing.T) {
	data, err := os.ReadFile("../../docs/commands.md")
	if err != nil {
		t.Fatalf("read docs/commands.md: %v", err)
	}
	lines := strings.Split(string(data), "\n")

	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Hidden || c.Name() == "help" || c.Name() == "completion" {
			return
		}
		path := c.CommandPath()
		section, ok := docSection(lines, "### "+path)
		if !ok {
			t.Errorf("docs/commands.md has no heading %q", "### "+path)
		} else {
			c.LocalFlags().VisitAll(func(f *pflag.Flag) {
				if f.Hidden || f.Name == "help" {
					return
				}
				if !regexp.MustCompile(`--` + regexp.QuoteMeta(f.Name) + `([^a-z0-9-]|$)`).MatchString(section) {
					t.Errorf("docs/commands.md section %q does not mention --%s", path, f.Name)
				}
			})
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(NewRootCmd("test", "test"))
}

// docSection returns the text from the heading line to the next ## or ###
// heading.
func docSection(lines []string, heading string) (string, bool) {
	for i, line := range lines {
		if strings.TrimSpace(line) != heading {
			continue
		}
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(lines[j], "## ") || strings.HasPrefix(lines[j], "### ") {
				end = j
				break
			}
		}
		return strings.Join(lines[i:end], "\n"), true
	}
	return "", false
}
