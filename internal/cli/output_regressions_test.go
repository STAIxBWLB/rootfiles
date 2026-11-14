package cli

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/module"
)

func TestDoctorSummaryUsesASCIIForEveryMarker(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		name := "locale"
		if explicit {
			name = "flag"
		}
		t.Run(name, func(t *testing.T) {
			_, site, _ := statusContractFixture(t, "populated")
			t.Setenv("LC_ALL", "")
			t.Setenv("LC_CTYPE", "")
			t.Setenv("LANG", "C")
			if explicit {
				t.Setenv("LC_ALL", "C.UTF-8")
			}
			previous := doctorChecks
			doctorChecks = func(context.Context, *module.RunContext) []module.Finding {
				return []module.Finding{
					{Check: "failure", Level: module.LevelFail, Detail: "fixture failure"},
					{Check: "warning", Level: module.LevelWarn, Detail: "fixture warning"},
					{Check: "healthy", Level: module.LevelOK, Detail: "fixture healthy"},
					{Check: "unknown", Level: module.LevelSkip, Detail: "fixture unknown"},
				}
			}
			t.Cleanup(func() { doctorChecks = previous })
			root := NewRootCmd("test", "test")
			var out, stderr bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&stderr)
			args := []string{"doctor", "--config", site, "--color=never"}
			if explicit {
				args = append(args, "--ascii")
			}
			root.SetArgs(args)
			err := executeCommand(context.Background(), root)
			var exit *ExitError
			if !errors.As(err, &exit) || exit.Code != exitDrift {
				t.Fatalf("doctor error=%v stderr=%q", err, stderr.String())
			}
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			summary := lines[len(lines)-1]
			for _, r := range summary {
				if r > 127 {
					t.Fatalf("non-ASCII summary marker in %q", summary)
				}
			}
			for _, token := range []string{"x  1 failed", "!  1 warnings", "ok 1 ok", "?  1 unknown"} {
				if !strings.Contains(summary, token) {
					t.Errorf("missing %q in summary %q", token, summary)
				}
			}
		})
	}
}

func TestDryRunRunnerShowsCommandWithoutVerbose(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	root := NewRootCmd("test", "test")
	cmd, _, err := root.Find([]string{"apply"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.ParseFlags([]string{"--dry-run"}); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	runner := newRunner(cmd, true)
	result, err := runner.Run(context.Background(), "rootfiles-fixture-command", "--would-change", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if result.Command != "rootfiles-fixture-command --would-change fixture" {
		t.Fatalf("unexpected dry-run result: %+v", result)
	}
	if text := stderr.String(); !strings.Contains(text, "msg=dry-run") || !strings.Contains(text, "cmd=\"rootfiles-fixture-command --would-change fixture\"") {
		t.Fatalf("dry-run command missing without verbose: %q", text)
	}
	stderr.Reset()
	if err := cmd.Flags().Set("dry-run", "false"); err != nil {
		t.Fatal(err)
	}
	readCmd, _, err := root.Find([]string{"status"})
	if err != nil {
		t.Fatal(err)
	}
	readCmd.SetErr(&stderr)
	logger := newLogger(readCmd, true, slog.LevelInfo)
	logger.Info("query", "cmd", "fixture read")
	if stderr.Len() != 0 {
		t.Fatalf("ordinary logger leaked INFO diagnostics: %q", stderr.String())
	}
}
