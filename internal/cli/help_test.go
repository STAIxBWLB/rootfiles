package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// walkCommands visits every command in the tree, root included.
func walkCommands(cmd *cobra.Command, fn func(*cobra.Command)) {
	fn(cmd)
	for _, sub := range cmd.Commands() {
		walkCommands(sub, fn)
	}
}

// minArgs returns the fewest positional arguments Args accepts, found by
// asking it rather than by reading the Use string. 4 means "more than any
// command takes".
func minArgs(cmd *cobra.Command) int {
	if cmd.Args == nil {
		return 0 // cobra's default, ArbitraryArgs
	}
	for n := 0; n <= 3; n++ {
		if err := cmd.Args(cmd, make([]string, n)); err == nil {
			return n
		}
	}
	return 4
}

// CLI surface audit AC3: the Use line is the contract the operator reads, so
// it must not carry flags and must not show an argument as optional while Args
// refuses the command without it.
func TestCommandTreeUseStrings(t *testing.T) {
	walkCommands(NewRootCmd("test", "abc"), func(cmd *cobra.Command) {
		words := strings.Fields(cmd.Use)
		if len(words) == 0 {
			t.Errorf("%s: empty Use", cmd.CommandPath())
			return
		}
		required := 0
		for _, w := range words[1:] {
			if strings.HasPrefix(w, "-") {
				t.Errorf("%s: Use %q names a flag; flags belong in the flag set", cmd.CommandPath(), cmd.Use)
				continue
			}
			if !strings.HasPrefix(w, "[") {
				required++
			}
		}
		if min := minArgs(cmd); min > required {
			t.Errorf("%s: Use %q shows %d required argument(s), but Args refuses fewer than %d",
				cmd.CommandPath(), cmd.Use, required, min)
		}
	})
}

// The commands whose flags an operator has to get right carry an Example, so
// "rootfiles help X" shows a working invocation and not only flag names.
func TestAuditedCommandsHaveExamples(t *testing.T) {
	root := NewRootCmd("test", "abc")
	for _, path := range []string{"apply", "check", "user add", "gpu assign", "tenant create", "fleet apply"} {
		cmd, _, err := root.Find(strings.Fields(path))
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if strings.TrimSpace(cmd.Example) == "" {
			t.Errorf("%s: no Example", cmd.CommandPath())
		}
	}
}
