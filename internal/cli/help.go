package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

// helpWidth is the fixed column budget for help pages so a server, a CI log
// and the committed command inventory all measure the same width.
const helpWidth = 80

func configureRootGroups(root *cobra.Command) {
	groups := []struct{ id, title, commands string }{
		{"setup", "Setup", "apply check status doctor config schedule shutdown backup rollback update"},
		{"resources", "Users, GPUs and tenants", "user gpu tenant tunnel"},
		{"fleet", "Fleet", "fleet"},
		{"help", "Help", "help completion"},
	}
	for _, group := range groups {
		root.AddGroup(&cobra.Group{ID: group.id, Title: group.title})
		for _, name := range strings.Fields(group.commands) {
			for _, child := range root.Commands() {
				if child.Name() == name {
					child.GroupID = group.id
				}
			}
		}
	}
	root.SetHelpCommandGroupID("help")
	root.SetCompletionCommandGroupID("help")
}

type helpRow struct{ label, description string }

func writeHelpPage(cmd *cobra.Command) {
	out := cmd.OutOrStdout()
	ui.WriteHeader(out, cmd.CommandPath())
	if cmd.Short != "" {
		ui.WriteHangingAt(out, helpWidth, "  ", ui.StyleHint.Render(cmd.Short))
	}
	if cmd.Long != "" && cmd.Long != cmd.Short {
		ui.WriteLine(out)
		ui.WriteHangingAt(out, helpWidth, "  ", reflowHelpLong(cmd.Long))
	}
	ui.WriteSection(out, "Usage")
	if cmd.Runnable() {
		ui.WriteHangingAt(out, helpWidth, "  ", cmd.UseLine())
	}
	if cmd.HasAvailableSubCommands() {
		ui.WriteHangingAt(out, helpWidth, "  ", cmd.CommandPath()+" [command]")
	}
	if len(cmd.Aliases) > 0 {
		ui.WriteSection(out, "Aliases")
		ui.WriteHangingAt(out, helpWidth, "  ", strings.Join(cmd.Aliases, ", "))
	}
	if cmd.Example != "" {
		ui.WriteSection(out, "Examples")
		ui.WriteHangingAt(out, helpWidth, "  ", dedentHelpExample(cmd.Example))
	}

	children := availableHelpCommands(cmd)
	if len(children) > 0 {
		if !cmd.HasParent() && len(cmd.Groups()) > 0 {
			for _, group := range cmd.Groups() {
				var rows []helpRow
				for _, child := range children {
					if child.GroupID == group.ID {
						rows = append(rows, helpRow{child.Name(), child.Short})
					}
				}
				if len(rows) > 0 {
					ui.WriteSection(out, group.Title)
					writeHelpRows(out, rows)
				}
			}
			var additional []helpRow
			for _, child := range children {
				if child.GroupID == "" {
					additional = append(additional, helpRow{child.Name(), child.Short})
				}
			}
			if len(additional) > 0 {
				ui.WriteSection(out, "Additional commands")
				writeHelpRows(out, additional)
			}
		} else {
			ui.WriteSection(out, "Commands")
			var rows []helpRow
			for _, child := range children {
				rows = append(rows, helpRow{child.Name(), child.Short})
			}
			writeHelpRows(out, rows)
		}
	}

	local := cmd.LocalFlags()
	global := cmd.InheritedFlags()
	if !cmd.HasParent() {
		local, global = cmd.LocalNonPersistentFlags(), cmd.PersistentFlags()
	}
	for _, section := range []struct {
		name  string
		flags *pflag.FlagSet
	}{{"Flags", local}, {"Global flags", global}} {
		if rows := helpFlagRows(section.flags); len(rows) > 0 {
			ui.WriteSection(out, section.name)
			writeHelpRows(out, rows)
		}
	}

	if cmd.HasAvailableSubCommands() {
		ui.WriteLine(out)
		ui.WriteLine(out, fmt.Sprintf("Run %q for details.", cmd.Root().CommandPath()+" help <command>"))
	}
}

func dedentHelpExample(text string) string {
	lines := strings.Split(strings.Trim(text, "\r\n"), "\n")
	var common string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		common = line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		break
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		for !strings.HasPrefix(line, common) {
			common = common[:len(common)-1]
		}
	}
	for i, line := range lines {
		lines[i] = strings.TrimPrefix(line, common)
	}
	return strings.Join(lines, "\n")
}

// reflowHelpLong joins prose lines while preserving paragraph and example boundaries.
func reflowHelpLong(text string) string {
	lines := strings.Split(strings.Trim(text, "\r\n"), "\n")
	var out strings.Builder
	previousProse := false
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		prose := strings.TrimSpace(line) != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t")
		if i > 0 {
			if prose && previousProse {
				out.WriteByte(' ')
			} else {
				out.WriteByte('\n')
			}
		}
		out.WriteString(line)
		previousProse = prose
	}
	return out.String()
}

func availableHelpCommands(cmd *cobra.Command) []*cobra.Command {
	var commands []*cobra.Command
	for _, child := range cmd.Commands() {
		if !child.Hidden && (child.IsAvailableCommand() || child.Name() == "help") {
			commands = append(commands, child)
		}
	}
	return commands
}

func helpFlagRows(flags *pflag.FlagSet) []helpRow {
	var rows []helpRow
	flags.VisitAll(func(flag *pflag.Flag) {
		if flag.Hidden {
			return
		}
		one := pflag.NewFlagSet("help", pflag.ContinueOnError)
		one.AddFlag(flag)
		usage := strings.TrimSpace(withoutBoolValueHint(one.FlagUsages()))
		label, description, _ := strings.Cut(usage, "   ")
		rows = append(rows, helpRow{strings.TrimSpace(label), strings.TrimSpace(description)})
	})
	return rows
}

func writeHelpRows(w io.Writer, rows []helpRow) {
	labelWidth := 0
	for _, row := range rows {
		labelWidth = max(labelWidth, min(30, ansi.StringWidth(row.label)))
	}
	for _, row := range rows {
		width := ansi.StringWidth(row.label)
		label := ui.StyleKey.Render(row.label)
		if width > labelWidth {
			ui.WriteHangingAt(w, helpWidth, "  ", label)
			ui.WriteHangingAt(w, helpWidth, strings.Repeat(" ", labelWidth+4), row.description)
			continue
		}
		prefix := fmt.Sprintf("  %s%s  ", label, strings.Repeat(" ", labelWidth-width))
		ui.WriteHangingAt(w, helpWidth, prefix, row.description)
	}
}
