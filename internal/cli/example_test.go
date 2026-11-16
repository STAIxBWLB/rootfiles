package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// TestExamplesAreRunnableWithoutAHost holds every example to what a person can
// copy into a shell and run: it starts at the program, names flags the command
// registers, passes as many positionals as Args accepts, carries every flag the
// command reference marks required, and holds no shell metacharacter that would
// be read as redirection instead of as an argument.
func TestExamplesAreRunnableWithoutAHost(t *testing.T) {
	pinInventoryPage(t)
	required := requiredFlagsByCommand(t)
	root := NewRootCmd("dev", "test")
	checked := 0
	walkCommands(root, func(c *cobra.Command) {
		if strings.TrimSpace(c.Example) == "" {
			return
		}
		for _, line := range exampleInvocations(c.Example) {
			checked++
			if err := checkExample(c, line, required); err != nil {
				t.Errorf("%s: %s: %v", c.CommandPath(), line, err)
			}
		}
	})
	if checked < 80 {
		t.Fatalf("checked %d examples, fewer than the tree has", checked)
	}
}

// exampleInvocations splits an Example field into single invocations. A line
// ending in a backslash continues on the next one, which is how an invocation
// too long for the 80-column help budget stays valid when copied whole.
func exampleInvocations(example string) []string {
	var out, pending []string
	for _, raw := range strings.Split(example, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasSuffix(line, `\`) {
			pending = append(pending, strings.TrimSpace(strings.TrimSuffix(line, `\`)))
			continue
		}
		if len(pending) > 0 {
			line = strings.Join(append(pending, line), " ")
			pending = nil
		}
		out = append(out, line)
	}
	if len(pending) > 0 {
		out = append(out, strings.Join(pending, " "))
	}
	return out
}

func checkExample(c *cobra.Command, line string, required map[string][]string) error {
	if strings.ContainsAny(line, "<>|;") {
		return fmt.Errorf("holds a shell metacharacter; quote the value instead")
	}
	words := shellWords(line)
	if len(words) == 0 {
		return fmt.Errorf("empty example")
	}
	root := c.Root()
	usedSudo := words[0] == "sudo"
	if usedSudo {
		words = words[1:]
	}
	if len(words) == 0 || words[0] != root.Name() {
		return fmt.Errorf("does not start with %q", root.Name())
	}
	invoked, _, err := root.Find(words[1:])
	if err != nil {
		return fmt.Errorf("does not resolve: %v", err)
	}
	if invoked.CommandPath() != c.CommandPath() && !strings.HasPrefix(invoked.CommandPath(), c.CommandPath()+" ") {
		return fmt.Errorf("invokes %s, which is not %s or below it", invoked.CommandPath(), c.CommandPath())
	}
	if usedSudo && runsAsOperator(invoked.CommandPath()) {
		return fmt.Errorf("uses sudo, but %s runs as the operator", invoked.CommandPath())
	}
	path := strings.Fields(invoked.CommandPath())[1:]
	rest := words[1+len(path):]
	flags, positionals, err := splitExampleArgs(invoked, rest)
	if err != nil {
		return err
	}
	if want := minArgs(invoked); len(positionals) < want {
		return fmt.Errorf("passes %d arguments, but %s needs %d", len(positionals), invoked.CommandPath(), want)
	}
	if max := maxArgs(invoked); len(positionals) > max {
		return fmt.Errorf("passes %d arguments, but %s accepts %d", len(positionals), invoked.CommandPath(), max)
	}
	for _, name := range required[invoked.CommandPath()] {
		if !flags[name] {
			return fmt.Errorf("omits --%s, which the command reference marks required", name)
		}
	}
	return nil
}

// runsAsOperator names the commands sudo would actively harm: the fleet
// controller and the reports that also run on one. Sudo changes HOME, the
// inventory they read and the SSH identity they use, so a copyable example
// leaves it to the native host mutations.
func runsAsOperator(path string) bool {
	if path == "rootfiles status" || path == "rootfiles doctor" {
		return true
	}
	for _, word := range strings.Fields(path) {
		if word == "fleet" {
			return true
		}
	}
	return false
}

// splitExampleArgs separates flags from positionals without running anything,
// so an unregistered flag or a missing value is caught here and not on a host.
func splitExampleArgs(c *cobra.Command, args []string) (map[string]bool, []string, error) {
	lookup := func(name string) *pflag.Flag {
		if f := c.Flags().Lookup(name); f != nil {
			return f
		}
		return c.InheritedFlags().Lookup(name)
	}
	shorthand := func(name string) *pflag.Flag {
		if f := c.Flags().ShorthandLookup(name); f != nil {
			return f
		}
		return c.InheritedFlags().ShorthandLookup(name)
	}
	seen := map[string]bool{}
	var positionals []string
	for i := 0; i < len(args); i++ {
		token := args[i]
		switch {
		case strings.HasPrefix(token, "--"):
			name, _, hasValue := strings.Cut(strings.TrimPrefix(token, "--"), "=")
			flag := lookup(name)
			if flag == nil {
				return nil, nil, fmt.Errorf("--%s is not a flag of %s", name, c.CommandPath())
			}
			seen[name] = true
			if !hasValue && flag.NoOptDefVal == "" {
				if i+1 >= len(args) {
					return nil, nil, fmt.Errorf("--%s needs a value", name)
				}
				i++
			}
		case strings.HasPrefix(token, "-") && len(token) == 2:
			flag := shorthand(strings.TrimPrefix(token, "-"))
			if flag == nil {
				return nil, nil, fmt.Errorf("%s is not a shorthand of %s", token, c.CommandPath())
			}
			seen[flag.Name] = true
			if flag.NoOptDefVal == "" {
				if i+1 >= len(args) {
					return nil, nil, fmt.Errorf("%s needs a value", token)
				}
				i++
			}
		default:
			positionals = append(positionals, token)
		}
	}
	return seen, positionals, nil
}

// shellWords splits an invocation the way a shell would: quoted spans stay one
// word, so a value with a space is not counted as a second argument.
func shellWords(line string) []string {
	var out []string
	var current strings.Builder
	var quote rune
	started := false
	flush := func() {
		if started || current.Len() > 0 {
			out = append(out, current.String())
			current.Reset()
			started = false
		}
	}
	for _, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			started = true
		case r == ' ' || r == '\t':
			flush()
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return out
}

// maxArgs is the most positionals Args accepts, found by asking it up to a
// point every command here stays under.
func maxArgs(cmd *cobra.Command) int {
	if cmd.Args == nil {
		return 16
	}
	most := 0
	for n := 0; n <= 16; n++ {
		if cmd.Args(cmd, make([]string, n)) == nil {
			most = n
		}
	}
	return most
}

// requiredFlagsByCommand reads the `required` markers out of the command
// reference, so an example cannot omit a flag the operator's table says is
// mandatory.
func requiredFlagsByCommand(t *testing.T) map[string][]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "commands.md"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	path := ""
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "### rootfiles"); ok {
			path = "rootfiles " + strings.TrimSpace(rest)
			continue
		}
		if path == "" || !strings.HasPrefix(line, "| `--") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < 4 || strings.TrimSpace(cells[2]) != "required" {
			continue
		}
		for _, token := range strings.Split(cells[1], ",") {
			name := strings.Trim(strings.TrimSpace(token), "`")
			if strings.HasPrefix(name, "--") {
				out[path] = append(out[path], strings.TrimPrefix(name, "--"))
			}
		}
	}
	if len(out) < 5 {
		t.Fatalf("read %d commands with required flags out of the reference", len(out))
	}
	return out
}
