package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

// This file holds the five conformance checks of CLI output standard v1 as
// tests over the whole tree rather than one sample command: colour off and on,
// a machine or plain stream without an escape byte, pages that fit 80 columns,
// usage errors that exit 2, and a non-UTF-8 locale that selects ASCII
// decoration. The help lint over the same tree is TestHelpStyleAcrossTree, and
// the pinned bytes of each report family are TestHumanLayoutGolden.

// TestConformanceColorOnlyDecorates holds the colour rule in both directions:
// a page rendered with colour on says exactly what the colour-off page says
// once the escapes are removed, and --color=always decorates help and reports
// without changing the words a person reads.
func TestConformanceColorOnlyDecorates(t *testing.T) {
	pinInventoryPage(t)
	walkCommands(NewRootCmd("dev", "test"), func(c *cobra.Command) {
		plain := renderHelpInColor(c, "never")
		decorated := renderHelpInColor(c, "always")
		if ansi.Strip(decorated) != plain {
			t.Errorf("%s: colour changes the words a person reads", c.CommandPath())
		}
		if !strings.ContainsRune(decorated, 27) {
			t.Errorf("%s: colour-on help carries no escape", c.CommandPath())
		}
	})
	for _, command := range measuredReports() {
		t.Run(command, func(t *testing.T) {
			root := conformanceFixture(t)
			plain := conformanceRender(t, root, command, "--color", "never")
			decorated := conformanceRender(t, root, command, "--color", "always")
			if ansi.Strip(decorated) != plain {
				t.Errorf("colour changes the words a person reads:\n--- colour off\n%s\n--- colour on, stripped\n%s",
					plain, ansi.Strip(decorated))
			}
			if !strings.ContainsRune(decorated, 27) {
				t.Error("the colour-on capture carries no escape, so this comparison proves nothing")
			}
		})
	}
}

// TestConformanceMachineAndPlainStreamsCarryNoEscape holds the three stream
// policies that must never decorate: NO_COLOR (which wins over a forced
// colour), --json, and the plain stream an ambient FORCE_COLOR would otherwise
// decorate - that last one is checked from the other side, so the check cannot
// pass by never decorating anything.
func TestConformanceMachineAndPlainStreamsCarryNoEscape(t *testing.T) {
	for _, command := range measuredReports() {
		t.Run(command, func(t *testing.T) {
			root := conformanceFixture(t)

			t.Setenv("NO_COLOR", "1")
			t.Setenv("FORCE_COLOR", "1")
			if out := conformanceRender(t, root, command); strings.ContainsRune(out, 27) {
				t.Error("NO_COLOR let an escape through")
			}

			t.Setenv("NO_COLOR", "")
			t.Setenv("FORCE_COLOR", "1")
			if out := conformanceRender(t, root, command); !strings.ContainsRune(out, 27) {
				t.Error("FORCE_COLOR decorated nothing, so the machine-output check proves nothing")
			}

			if !declaresJSON(t, command) {
				return
			}
			t.Setenv("FORCE_COLOR", "")
			machine := conformanceRender(t, root, command, "--json")
			if strings.ContainsRune(machine, 27) {
				t.Error("--json carried an escape")
			}
			if strings.HasPrefix(machine, " rootfiles ") {
				t.Error("--json carried the human header line")
			}
			if glyphs := decorationGlyphs(machine); len(glyphs) > 0 {
				t.Errorf("--json carried decoration %q", glyphs)
			}
		})
	}
}

// TestConformancePagesFitEightyColumns measures every help page and every
// captured report against the 80-column budget. Help width does not follow
// COLUMNS: the narrowest stream still gets the page the widest one does.
func TestConformancePagesFitEightyColumns(t *testing.T) {
	pinInventoryPage(t)
	for _, columns := range []string{"80", "40"} {
		t.Setenv("COLUMNS", columns)
		walkCommands(NewRootCmd("dev", "test"), func(c *cobra.Command) {
			for _, line := range strings.Split(renderHelpInColor(c, "never"), "\n") {
				if width := ansi.StringWidth(line); width > InventoryWidth {
					t.Errorf("%s: COLUMNS=%s help line is %d columns: %q", c.CommandPath(), columns, width, line)
				}
			}
		})
	}
	for _, command := range measuredReports() {
		t.Run(command, func(t *testing.T) {
			root := conformanceFixture(t)
			for _, line := range strings.Split(conformanceRender(t, root, command), "\n") {
				if width := ansi.StringWidth(line); width > InventoryWidth {
					t.Errorf("line is %d columns: %q", width, line)
				}
			}
		})
	}
}

// TestConformanceUsageErrorsExitTwo holds every runnable command to the
// parsing-error exit code, through each way parsing fails: an unknown flag, an
// unusable flag value, and an unknown subcommand on a group.
func TestConformanceUsageErrorsExitTwo(t *testing.T) {
	pinInventoryPage(t)
	walkCommands(NewRootCmd("dev", "test"), func(c *cobra.Command) {
		args := strings.Fields(strings.TrimPrefix(c.CommandPath(), "rootfiles "))
		if c.Runnable() {
			for _, broken := range [][]string{
				append(append([]string{}, args...), "--zzz-unknown"),
				append(append([]string{}, args...), "--color", "bogus"),
			} {
				assertUsageExit(t, c.CommandPath(), broken)
			}
		}
		if c.HasSubCommands() {
			assertUsageExit(t, c.CommandPath(), append(append([]string{}, args...), "zzz-unknown"))
		}
	})
}

// TestConformanceLocaleSelectsASCIIDecoration checks the locale rule over every
// captured report: a non-UTF-8 locale renders no Unicode marker, and it swaps
// those markers in place instead of dropping or rewrapping the page. An
// explicit --ascii is the same page as that locale.
func TestConformanceLocaleSelectsASCIIDecoration(t *testing.T) {
	for _, command := range measuredReports() {
		t.Run(command, func(t *testing.T) {
			root := conformanceFixture(t)
			t.Setenv("LC_ALL", "C.UTF-8")
			t.Setenv("LANG", "C.UTF-8")
			unicode := conformanceRender(t, root, command)
			t.Setenv("LC_ALL", "C")
			t.Setenv("LANG", "C")
			ascii := conformanceRender(t, root, command)

			if glyphs := decorationGlyphs(ascii); len(glyphs) > 0 {
				t.Errorf("LANG=C kept %q", glyphs)
			}
			if len(decorationGlyphs(unicode)) == 0 {
				return
			}
			if len(strings.Split(ascii, "\n")) != len(strings.Split(unicode, "\n")) {
				t.Error("LANG=C changed the number of lines, so it rewrapped the page instead of swapping markers")
			}
			t.Setenv("LC_ALL", "C.UTF-8")
			t.Setenv("LANG", "C.UTF-8")
			if explicit := conformanceRender(t, root, command, "--ascii"); explicit != ascii {
				t.Error("--ascii and LANG=C render different pages")
			}
		})
	}
}

// measuredReports names every report the inventory marks measured: the surface
// the output checks run over.
func measuredReports() []string {
	var out []string
	for _, report := range ReportInventory {
		if report.State == ReportMeasured {
			out = append(out, report.Command)
		}
	}
	return out
}

// conformanceFixture pins the stream environment a check renders in: 80
// columns, UTF-8 markers, colour left to the check. A check that needs another
// locale or colour policy sets it after calling this.
func conformanceFixture(t *testing.T) string {
	t.Helper()
	layoutPinWidth(t)
	root, _, _ := statusContractFixture(t, "populated")
	writeBackupManifest(t)
	pinReportClock(t)
	t.Chdir(root)
	return root
}

// conformanceRender renders one captured report inside its fixture root,
// normalized the way the inventory page normalizes it.
func conformanceRender(t *testing.T, root, command string, extra ...string) string {
	t.Helper()
	return strings.ReplaceAll(reportText(t, root, command, extra...), root, "<fixture>")
}

// renderHelpInColor renders one command's help page through a writer carrying
// the requested colour policy.
func renderHelpInColor(c *cobra.Command, color string) string {
	var buf bytes.Buffer
	saved := c.OutOrStdout()
	c.SetOut(ui.ConfigureWriter(&buf, color, false, false))
	defer c.SetOut(saved)
	c.HelpFunc()(c, nil)
	return buf.String()
}

// declaresJSON reports whether a command offers the machine format.
func declaresJSON(t *testing.T, command string) bool {
	t.Helper()
	c, _, err := NewRootCmd("dev", "test").Find(strings.Fields(strings.TrimPrefix(command, "rootfiles ")))
	if err != nil {
		t.Fatalf("%s: %v", command, err)
	}
	return c.Flags().Lookup("json") != nil
}

// decorationGlyphs lists the Unicode marker and relation glyphs a page may
// carry. Prose is content: a non-UTF-8 locale may keep its em dashes, but the
// markers a locale substitutes are these.
func decorationGlyphs(s string) []string {
	seen := map[rune]bool{}
	var out []string
	for _, r := range s {
		switch r {
		case '\u25b8', '\u2713', '\u2717', '\u26a0', '\u2192':
			if !seen[r] {
				seen[r] = true
				out = append(out, string(r))
			}
		}
	}
	return out
}

// assertUsageExit runs one malformed invocation and holds it to the parsing
// error contract: exit 2, and an error naming what parsing rejected.
func assertUsageExit(t *testing.T, path string, args []string) {
	t.Helper()
	root := NewRootCmd("dev", "test")
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err := root.Execute()
	if err == nil {
		t.Errorf("%s %q: accepted", path, args)
		return
	}
	coded, ok := err.(*ExitError)
	if !ok {
		t.Errorf("%s %q: %v is not a coded exit", path, args, err)
		return
	}
	if coded.Code != 2 {
		t.Errorf("%s %q: exit %d, want 2", path, args, coded.Code)
	}
	if strings.TrimSpace(coded.Msg) == "" {
		t.Errorf("%s %q: no message", path, args)
	}
}
