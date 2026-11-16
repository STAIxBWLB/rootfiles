package cli

import "fmt"

// helpWidth is the column count help is laid out for. It is fixed rather than
// read from the terminal, so a server, a CI log and the committed command
// reference all show the same page.
const helpWidth = 80

// usageTemplate is cobra v1.10.2's defaultUsageTemplate with three changes:
// the footer sentence is shortened, because
// `Use "rootfiles tenant volume [command] --help" for more information about a
// command` goes past helpWidth on the deepest paths; flag descriptions are
// wrapped at helpWidth, so a detailed default is not a line a reader has to
// scroll sideways; and the command-group branch is dropped, because no command
// here calls AddGroup.
//
// The footer names `help` from the root, which is where cobra registers it: a
// nested page that said `<command path> help <command>` named a subcommand no
// parent has, and resolving it failed with "unknown command".
var usageTemplate = fmt.Sprintf(`Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}

Available Commands:{{range .Commands}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

Flags:
{{.LocalFlags.FlagUsagesWrapped %d | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

Global Flags:
{{.InheritedFlags.FlagUsagesWrapped %d | trimTrailingWhitespaces}}{{end}}{{if .HasHelpSubCommands}}

Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

Run "{{.Root.CommandPath}} help <command>" for details.{{end}}
`, helpWidth, helpWidth)
