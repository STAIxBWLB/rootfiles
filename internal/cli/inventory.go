package cli

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// InventoryWidth is the column count a help page has to fit in.
const InventoryWidth = 80

// SummaryLimit is the ceiling the audit holds a one-line summary to. It is
// shorter than InventoryWidth so a summary still fits beside the longest
// command path in the root list.
const SummaryLimit = 60

// InventoryHome is the home directory a `$HOME`-relative flag default is
// rendered against. The fleet inventory default falls back to the home of
// whoever runs the walk, which would make the committed page differ between a
// developer's checkout and CI and record that person's path.
const InventoryHome = "/home/operator"

// InventoryPlatform is the host the page records: help that names a controller-
// only input exists only off Linux, and a committed page cannot be both.
const InventoryPlatform = "linux"

// CommandFacts is what the audit records about one command: the text a person
// reads, how wide it renders, and the flags it declares itself.
type CommandFacts struct {
	Path     string
	Runnable bool
	Hidden   bool
	Summary  string
	// SummaryLen, EndsWithPeriod and ASCII are measured, not judged, so the
	// inventory does not have to agree with a hand-written count.
	SummaryLen     int
	EndsWithPeriod bool
	ASCII          bool
	FirstWord      string
	HasExample     bool
	Usage          string
	WidestLine     int
	Flags          []FlagFacts
}

// FlagFacts is one flag a command declares itself. Inherited root flags are
// recorded on the root command, not repeated for every subcommand.
type FlagFacts struct {
	Name      string
	Shorthand string
	Type      string
	Default   string
	Hidden    bool
	NoOptDef  string
}

// Issues names every deviation the walk can measure. A judgement the walk
// cannot make (a noun-phrase summary, say) is left to the table's FirstWord
// column rather than guessed at.
func (c CommandFacts) Issues() []string {
	var out []string
	if c.Summary == "" {
		out = append(out, "no summary")
	}
	if !c.ASCII {
		out = append(out, "non-ASCII summary")
	}
	if c.EndsWithPeriod {
		out = append(out, "summary ends with a period")
	}
	if c.SummaryLen > SummaryLimit {
		out = append(out, fmt.Sprintf("summary is %d chars", c.SummaryLen))
	}
	if c.Runnable && !c.HasExample {
		out = append(out, "no example")
	}
	if c.WidestLine > InventoryWidth {
		out = append(out, fmt.Sprintf("help is %d columns", c.WidestLine))
	}
	if strings.Contains(c.Usage, "[") && strings.Contains(c.Usage, "]=") {
		out = append(out, "usage mixes conventions")
	}
	return out
}

// Inventory walks the command tree the way a person reads it: parents before
// children, and every child in the order cobra shows them.
func Inventory(root *cobra.Command) []CommandFacts {
	var out []CommandFacts
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		out = append(out, factsFor(c))
		for _, child := range c.Commands() {
			walk(child)
		}
	}
	walk(root)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func factsFor(c *cobra.Command) CommandFacts {
	help := renderHelp(c)
	summary := c.Short
	facts := CommandFacts{
		Path:           c.CommandPath(),
		Runnable:       c.RunE != nil || c.Run != nil,
		Hidden:         c.Hidden,
		Summary:        summary,
		SummaryLen:     ansi.StringWidth(summary),
		EndsWithPeriod: strings.HasSuffix(strings.TrimSpace(summary), "."),
		ASCII:          isASCII(summary),
		FirstWord:      firstWord(summary),
		HasExample:     strings.TrimSpace(c.Example) != "",
		Usage:          c.UseLine(),
		WidestLine:     widestLine(help),
	}
	c.NonInheritedFlags().VisitAll(func(f *pflag.Flag) {
		facts.Flags = append(facts.Flags, FlagFacts{
			Name:      f.Name,
			Shorthand: f.Shorthand,
			Type:      f.Value.Type(),
			Default:   f.DefValue,
			Hidden:    f.Hidden,
			NoOptDef:  f.NoOptDefVal,
		})
	})
	return facts
}

// renderHelp renders one command's own help page the way `--help` would, into a
// buffer: nothing here executes the command or touches the host.
func renderHelp(c *cobra.Command) string {
	var buf bytes.Buffer
	saved := c.OutOrStdout()
	c.SetOut(&buf)
	c.SetErr(&buf)
	c.HelpFunc()(c, nil)
	c.SetOut(saved)
	return buf.String()
}

func widestLine(text string) int {
	widest := 0
	for _, line := range strings.Split(text, "\n") {
		if n := ansi.StringWidth(line); n > widest {
			widest = n
		}
	}
	return widest
}

func isASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}

func firstWord(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return strings.TrimRight(fields[0], ",;:")
}

// RenderInventory renders the committed inventory: one row per command, then
// every flag a command declares, so a reviewer can read the whole surface
// without running the binary.
func RenderInventory(facts []CommandFacts) string {
	var b strings.Builder
	b.WriteString("# Command inventory\n\n")
	b.WriteString("Generated by `make cli-inventory` from `Inventory` in `internal/cli/inventory.go`; ")
	b.WriteString("`go test ./internal/cli -run TestDocsCommandInventory` fails when this page and the command tree disagree. ")
	b.WriteString("Do not edit it by hand.\n\n")
	b.WriteString(fmt.Sprintf("Widths are measured with the same counter the help pages use, against an %d-column budget; ", InventoryWidth))
	b.WriteString(fmt.Sprintf("summaries are held to %d characters so they fit beside the longest command path.\n\n", SummaryLimit))
	b.WriteString(fmt.Sprintf("`$HOME`-relative flag defaults are rendered against `%s` so this page is the same on every machine; ", InventoryHome))
	b.WriteString("a width that includes such a default is one char longer or shorter on a host whose home is a different length.\n\n")
	b.WriteString(fmt.Sprintf("The platform is pinned to `%s`, the host this binary configures: help that differs\n", InventoryPlatform))
	b.WriteString("between a Linux server and a controller (which flags exist, which report is read) is\n")
	b.WriteString("recorded here as the server sees it.\n\n")

	b.WriteString(fmt.Sprintf("## Commands (%d)\n\n", len(facts)))
	b.WriteString("| command | kind | summary | chars | example | widest | issues |\n")
	b.WriteString("|---|---|---|---|---|---|---|\n")
	for _, f := range facts {
		kind := "group"
		if f.Runnable {
			kind = "runnable"
		}
		if f.Hidden {
			kind += ", hidden"
		}
		example := "no"
		if f.HasExample {
			example = "yes"
		}
		issues := "-"
		if list := f.Issues(); len(list) > 0 {
			issues = strings.Join(list, "; ")
		}
		b.WriteString(fmt.Sprintf("| `%s` | %s | %s | %d | %s | %d | %s |\n",
			f.Path, kind, cell(f.Summary), f.SummaryLen, example, f.WidestLine, issues))
	}

	b.WriteString("\n## First word of each summary\n\n")
	b.WriteString("The audit has to settle one form for every summary, so the first word of each is listed here rather than classified: ")
	b.WriteString("a verb and a noun read the same to a script.\n\n")
	words := map[string][]string{}
	for _, f := range facts {
		if f.Summary == "" {
			continue
		}
		words[f.FirstWord] = append(words[f.FirstWord], f.Path)
	}
	keys := make([]string, 0, len(words))
	for k := range words {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(fmt.Sprintf("- `%s`: %s\n", k, strings.Join(words[k], ", ")))
	}

	b.WriteString("\n## Notes\n\n")
	b.WriteString("- Boolean flags print bare (`--ascii`, not `--ascii[=true|false]`). Measured cause: pflag v1.1.0's `FlagUsagesWrapped` appends the hint before it measures the column width, and v1.0.9 (what `dot` pins) does not. Help rendering drops the hint (`withoutBoolValueHint`) rather than pin the dependency back.\n")
	b.WriteString("- Widths here are the unwrapped help lines. A page over 80 columns is a page a person has to scroll sideways, and the count is the same whatever `COLUMNS` says.\n")
	b.WriteString("- The inventory cannot tell a verb from a noun, so it records each summary's first word above instead of guessing at the form.\n")

	b.WriteString("\n## Flags\n\n")
	b.WriteString("Inherited root flags are recorded once, on `rootfiles`; every other flag is recorded on the command that declares it.\n\n")
	b.WriteString("| command | flag | type | default | shorthand | hidden |\n")
	b.WriteString("|---|---|---|---|---|---|\n")
	for _, f := range facts {
		for _, flag := range f.Flags {
			hidden := "no"
			if flag.Hidden {
				hidden = "yes"
			}
			name := "--" + flag.Name
			if flag.Shorthand != "" {
				name = "-" + flag.Shorthand + ", " + name
			}
			b.WriteString(fmt.Sprintf("| `%s` | `%s` | %s | %s | %s | %s |\n",
				f.Path, name, flag.Type, cell(flag.Default), dash(flag.Shorthand), hidden))
		}
	}
	return b.String()
}

// cell keeps a table row on one line whatever the help text holds.
func cell(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ")
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
