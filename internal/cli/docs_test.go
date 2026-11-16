package cli

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// TestDocsCommandsCoverCobraTree keeps docs/commands.md in step with the help:
// every visible command has a "### <command path>" section that opens with the
// summary its help shows and names every flag the command declares; no section
// documents a flag no command declares, or a command the help hides or lacks;
// and a child's section follows its parent's. The reference stays hand-written
// rather than generated from the help, because it also carries the exit codes,
// JSON schemas and environment tables a generated page would drop, so the
// summary line is what makes the two comparable.
func TestDocsCommandsCoverCobraTree(t *testing.T) {
	headings, sections := readCommandReference(t)
	position := map[string]int{}
	for i, heading := range headings {
		position[heading] = i
	}

	root := NewRootCmd("dev", "test")
	walkCommands(root, func(c *cobra.Command) {
		path := c.CommandPath()
		if c.Hidden {
			if _, ok := sections[path]; ok {
				t.Errorf("%s: hidden command has a reference section", path)
			}
			return
		}
		section, ok := sections[path]
		if !ok {
			t.Errorf("docs/commands.md has no heading %q", "### "+path)
			return
		}
		if at := strings.LastIndex(path, " "); at > 0 {
			if parentAt, ok := position[path[:at]]; ok && parentAt > position[path] {
				t.Errorf("%s: section precedes its parent %s", path, path[:at])
			}
		}
		if opening := firstNonEmptyLine(section); c.Short != "" && !strings.HasPrefix(opening, c.Short+".") {
			t.Errorf("%s: section opens with %q, help summary is %q", path, opening, c.Short)
		}
		for _, name := range declaredFlags(c) {
			if !namesFlag(section, name) {
				t.Errorf("docs/commands.md section %q does not mention --%s", path, name)
			}
		}
		known := subtreeFlags(root, c)
		for _, name := range documentedFlags(section) {
			if !known[name] {
				t.Errorf("%s: table documents --%s, which no command in the tree declares", path, name)
			}
		}
	})

	for _, heading := range headings {
		if _, _, err := root.Find(strings.Fields(strings.TrimPrefix(heading, "rootfiles "))); err != nil {
			t.Errorf("%s: section documents a command the tree lacks: %v", heading, err)
		}
	}
}

// readCommandReference splits docs/commands.md into its "### <command path>"
// sections: the headings in file order, and each section's body.
func readCommandReference(t *testing.T) ([]string, map[string]string) {
	t.Helper()
	data, err := os.ReadFile("../../docs/commands.md")
	if err != nil {
		t.Fatalf("read docs/commands.md: %v", err)
	}
	var headings []string
	sections := map[string]string{}
	var current string
	var text strings.Builder
	flush := func() {
		if current != "" {
			sections[current] = text.String()
		}
		text.Reset()
	}
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "### rootfiles"):
			flush()
			current = strings.TrimSpace(strings.TrimPrefix(line, "### "))
			headings = append(headings, current)
			continue
		case strings.HasPrefix(line, "#"):
			flush()
			current = ""
		}
		if current != "" {
			text.WriteString(line + "\n")
		}
	}
	flush()
	return headings, sections
}

// namesFlag reports whether a section names one flag, without accepting a
// longer flag that merely starts the same way.
func namesFlag(section, name string) bool {
	return regexp.MustCompile(`--` + regexp.QuoteMeta(name) + `([^a-z0-9-]|$)`).MatchString(section)
}

// declaredFlags names the flags one command declares itself, hidden ones
// included: a deprecated spelling is still a flag a person can pass. Cobra's own
// --help belongs to no command's documentation.
func declaredFlags(c *cobra.Command) []string {
	var out []string
	c.NonInheritedFlags().VisitAll(func(f *pflag.Flag) {
		if f.Name != "help" {
			out = append(out, f.Name)
		}
	})
	return out
}

// subtreeFlags names every flag a section may document: those the command
// declares, those of the commands below it (a group documents the selection
// flags its children share), and the root flags every command accepts.
func subtreeFlags(root, c *cobra.Command) map[string]bool {
	names := map[string]bool{}
	root.PersistentFlags().VisitAll(func(f *pflag.Flag) { names[f.Name] = true })
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		cmd.Flags().VisitAll(func(f *pflag.Flag) { names[f.Name] = true })
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(c)
	return names
}

// flagRow and flagToken pick the flags a section's tables document, so a row
// that survives its flag's removal fails the check.
var (
	flagRow   = regexp.MustCompile(`^\|`)
	flagToken = regexp.MustCompile(`--[a-z0-9][a-z0-9-]*`)
)

func documentedFlags(section string) []string {
	var out []string
	for _, line := range strings.Split(section, "\n") {
		if !flagRow.MatchString(line) {
			continue
		}
		for _, token := range flagToken.FindAllString(line, -1) {
			out = append(out, strings.TrimPrefix(token, "--"))
		}
	}
	return out
}

// firstNonEmptyLine is the line a reader sees first in a section body.
func firstNonEmptyLine(section string) string {
	for _, line := range strings.Split(section, "\n") {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}
