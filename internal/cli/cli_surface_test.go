package cli

import (
	"fmt"
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
func commandUseError(cmd *cobra.Command) error {
	words := strings.Fields(cmd.Use)
	if len(words) == 0 {
		return fmt.Errorf("empty Use")
	}
	required := 0
	for _, word := range words[1:] {
		if strings.HasPrefix(strings.TrimLeft(word, "["), "-") {
			return fmt.Errorf("Use %q names a flag; put flags in the flag set", cmd.Use)
		}
		if !strings.HasPrefix(word, "[") {
			required++
		}
	}
	if minimum := minArgs(cmd); minimum > required {
		return fmt.Errorf("Use %q shows %d required args but Args needs %d", cmd.Use, required, minimum)
	}
	return nil
}

func TestCommandTreeUseStrings(t *testing.T) {
	walkCommands(NewRootCmd("test", "abc"), func(cmd *cobra.Command) {
		if err := commandUseError(cmd); err != nil {
			t.Errorf("%s: %v", cmd.CommandPath(), err)
		}
	})
}

func TestCommandUseLintRejectsMisleadingSurface(t *testing.T) {
	for _, command := range []*cobra.Command{
		{Use: "sample --flag", Args: cobra.NoArgs},
		{Use: "sample [--flag VALUE]", Args: cobra.NoArgs},
		{Use: "sample [USER]", Args: cobra.ExactArgs(1)},
	} {
		if err := commandUseError(command); err == nil {
			t.Fatalf("accepted misleading Use %q", command.Use)
		}
	}
	if err := commandUseError(&cobra.Command{Use: "sample USER [OPTIONAL]", Args: cobra.RangeArgs(1, 2)}); err != nil {
		t.Fatalf("valid arity rejected: %v", err)
	}
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

func TestReportHelpExplainsDifferentExitContracts(t *testing.T) {
	root := NewRootCmd("test", "test")
	for _, tc := range []struct {
		name  string
		words []string
	}{
		{"check", []string{"2 = pending changes", "1 = error"}},
		{"status", []string{"Read-only overview", "Exit code: 0"}},
		{"doctor", []string{"live system", "2 = at least one failure"}},
	} {
		cmd, _, err := root.Find([]string{tc.name})
		if err != nil {
			t.Fatal(err)
		}
		for _, word := range tc.words {
			if !strings.Contains(cmd.Long, word) {
				t.Errorf("%s help misses %q", tc.name, word)
			}
		}
	}
}
