package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"log/slog"
	"strings"
	"testing"
)

func TestStandardJSONAliasesKeepBytes(t *testing.T) {
	for _, command := range []string{"status", "check", "doctor", "fleet status", "fleet check", "fleet doctor", "fleet apply", "fleet update", "fleet bootstrap", "fleet schedule"} {
		t.Run(command, func(t *testing.T) {
			root := NewRootCmd("test", "test")
			cmd, _, err := root.Find(strings.Fields(command))
			if err != nil {
				t.Fatal(err)
			}
			if cmd.Flags().Lookup("json") == nil {
				t.Fatal("missing --json")
			}
			payload := map[string]string{"value": "<&> 한글"}
			var old, alias bytes.Buffer
			encode := func(w *bytes.Buffer) {
				f, err := outputFormat(cmd)
				if err != nil || f != "json" {
					t.Fatalf("format=%s err=%v", f, err)
				}
				if strings.HasPrefix(command, "fleet ") {
					err = writeJSONEscaping(w, payload, true)
				} else {
					err = writeJSON(w, payload)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			cmd.Flags().Set("output", "json")
			encode(&old)
			cmd.Flags().Lookup("output").Changed = false
			cmd.Flags().Set("output", "text")
			cmd.Flags().Lookup("output").Changed = false
			cmd.Flags().Set("json", "true")
			encode(&alias)
			if !bytes.Equal(old.Bytes(), alias.Bytes()) {
				t.Fatalf("alias changed bytes: %q vs %q", old.String(), alias.String())
			}
		})
	}
}

func TestStandardJSONDoesNotClaimBackupDestination(t *testing.T) {
	root := NewRootCmd("test", "test")
	for _, name := range []string{"backup", "user backup"} {
		cmd, _, _ := root.Find(strings.Fields(name))
		if cmd.Flags().Lookup("json") != nil {
			t.Fatalf("%s acquired an unrelated report flag", name)
		}
	}
}

func TestStandardUsageAndFailureDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
		text string
	}{
		{[]string{"not-a-command"}, 2, "unknown command"},
		{[]string{"status", "--not-a-flag"}, 2, "unknown flag"},
		{[]string{"gpu", "assign"}, 2, "arg"},
		{[]string{"status", "--color", "invalid"}, 2, "--color"},
		{[]string{"status", "--json", "--output", "text"}, 2, "conflicts"},
		{[]string{"config", "show", "--config", "/nonexistent/rootfiles-test.yaml"}, 1, "reading config"},
	} {
		root := NewRootCmd("test", "test")
		var out, stderr bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&stderr)
		root.SetArgs(tc.args)
		err := executeCommand(context.Background(), root)
		code := 1
		var exit *ExitError
		if errors.As(err, &exit) {
			code = exit.Code
		}
		if err == nil || code != tc.code {
			t.Fatalf("%v: code=%d err=%v", tc.args, code, err)
		}
		if !strings.Contains(stderr.String(), "rootfiles: ") || !strings.Contains(stderr.String(), tc.text) {
			t.Fatalf("%v: stderr=%q", tc.args, stderr.String())
		}
		if tc.code == 2 && (!strings.Contains(stderr.String(), "Usage: rootfiles") || !strings.Contains(stderr.String(), "for details.")) {
			t.Fatalf("missing usage: %q", stderr.String())
		}
		if out.Len() != 0 {
			t.Fatalf("failure polluted stdout: %q", out.String())
		}
	}
}

func TestStandardLogsRequireVerbose(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		root := NewRootCmd("test", "test")
		cmd, _, _ := root.Find([]string{"check"})
		var stderr bytes.Buffer
		cmd.SetErr(&stderr)
		if verbose {
			cmd.Flags().Set("verbose", "true")
		}
		logger := newLogger(cmd, true, slog.LevelInfo)
		logger.Info("query", "cmd", "fixture")
		if strings.Contains(stderr.String(), "level=INFO") != verbose {
			t.Fatalf("verbose=%v stderr=%q", verbose, stderr.String())
		}
	}
}

func TestStandardHelpUsesStdout(t *testing.T) {
	root := NewRootCmd("test", "test")
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"--help", "--color=never"})
	if err := executeCommand(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Usage:") || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestStandardMainReportStreamsAndWidth(t *testing.T) {
	for _, scenario := range []struct {
		color, force, noColor string
		json                  bool
		esc                   bool
	}{
		{"never", "", "", false, false}, {"always", "", "", false, true},
		{"auto", "1", "", false, true}, {"auto", "1", "1", false, false},
		{"always", "1", "", true, false},
	} {
		t.Run(scenario.color+scenario.force+scenario.noColor+fmt.Sprint(scenario.json), func(t *testing.T) {
			t.Setenv("LC_ALL", "C.UTF-8")
			t.Setenv("COLUMNS", "80")
			t.Setenv("FORCE_COLOR", scenario.force)
			t.Setenv("NO_COLOR", scenario.noColor)
			t.Setenv("CLICOLOR_FORCE", "")
			t.Setenv("CLICOLOR", "")
			_, site, _ := statusContractFixture(t, "populated")
			root := NewRootCmd("test", "test")
			var out, stderr bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&stderr)
			args := []string{"status", "--config", site, "--color", scenario.color}
			if scenario.json {
				args = append(args, "--json")
			}
			root.SetArgs(args)
			if err := executeCommand(context.Background(), root); err != nil {
				t.Fatal(err)
			}
			if strings.ContainsRune(out.String(), 27) != scenario.esc {
				t.Fatalf("escape policy output=%q", out.String())
			}
			if scenario.json {
				if !json.Valid(out.Bytes()) {
					t.Fatalf("invalid JSON: %q", out.String())
				}
			} else {
				for _, line := range strings.Split(out.String(), "\n") {
					if width := ansi.StringWidth(line); width > 80 {
						t.Fatalf("line width=%d: %q", width, line)
					}
				}
			}
			if stderr.Len() != 0 {
				t.Fatalf("unexpected diagnostics: %s", stderr.String())
			}
		})
	}
}
