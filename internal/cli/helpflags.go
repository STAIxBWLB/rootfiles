package cli

import (
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// rootConfigInputs are the inherited inputs that describe the native host this
// binary configures: which profile and config file to read, and the values
// `apply` writes. A command declares the ones it reads (usesRootFlags), and
// help lists a command only the ones it declares, so a fleet or tenant page
// never offers an input that command refuses.
//
// This is the same set rejectUnusedNativeFlags refuses, from one list: an input
// refused for every command but listed on every page is the drift this audit
// exists to remove.
var rootConfigInputs = []string{
	"profile", "config", "home-base", "user", "ssh-pubkey", "tunnel-token", "vlan-address",
}

func isRootConfigInput(name string) bool {
	for _, input := range rootConfigInputs {
		if input == name {
			return true
		}
	}
	return false
}

// declaresRootFlag reports whether cmd or one of its parents declares a reader
// for an inherited flag, walking the same way rejectUnusedNativeFlags does.
func declaresRootFlag(cmd *cobra.Command, name string) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Annotations[annotReadsRootFlag+name] == "true" {
			return true
		}
	}
	return false
}

// scopeInheritedHelpFlags hides the inherited config inputs cmd does not read
// for the duration of one help render, and returns the restore.
//
// The flag stays registered and parsing is untouched, so an input a command
// refuses is still refused with the same message and exit code; only the page
// stops advertising it.
func scopeInheritedHelpFlags(cmd *cobra.Command) (restore func()) {
	if cmd.Parent() == nil {
		return func() {}
	}
	var scoped []*pflag.Flag
	cmd.InheritedFlags().VisitAll(func(flag *pflag.Flag) {
		if flag.Hidden || !isRootConfigInput(flag.Name) || declaresRootFlag(cmd, flag.Name) {
			return
		}
		flag.Hidden = true
		scoped = append(scoped, flag)
	})
	return func() {
		for _, flag := range scoped {
			flag.Hidden = false
		}
	}
}

// boolValueHint is how pflag prints a boolean's implicit value: v1.1.0's
// FlagUsagesWrapped appends it for every no-option bool before it measures the
// column width, v1.0.9 (what `dot` pins) does not. The standard shows the flag
// alone, so help text drops the hint while keeping the columns pflag aligned
// for it; a bare --flag still parses as true and nothing about parsing changes.
var boolValueHint = regexp.MustCompile(`(--[A-Za-z0-9-]+)\[=true\|false\]`)

func withoutBoolValueHint(help string) string {
	return boolValueHint.ReplaceAllStringFunc(help, func(match string) string {
		name := boolValueHint.FindStringSubmatch(match)[1]
		return name + strings.Repeat(" ", len(match)-len(name))
	})
}
