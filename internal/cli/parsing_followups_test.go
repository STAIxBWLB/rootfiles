package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRootAndCheckVerboseHaveSeparateMeanings(t *testing.T) {
	for _, tc := range []struct {
		name         string
		args         []string
		info, report bool
	}{
		{"local-short", []string{"check", "-v"}, false, true},
		{"local-long", []string{"check", "--verbose"}, false, true},
		{"root-prefix-shadowed", []string{"--verbose", "check"}, false, true},
		{"both-local", []string{"--verbose", "check", "-v"}, false, true},
		{"other-command", []string{"status", "--verbose"}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := NewRootCmd("test", "test")
			command := "check"
			if tc.args[0] == "status" {
				command = "status"
			}
			cmd, _, err := root.Find([]string{command})
			if err != nil {
				t.Fatal(err)
			}
			root.PersistentPreRunE = func(*cobra.Command, []string) error { return nil }
			reported := false
			cmd.RunE = func(c *cobra.Command, _ []string) error {
				if c.Name() == "check" {
					reported, _ = c.Flags().GetBool("verbose")
				}
				newLogger(c, true, slog.LevelInfo).Info("fixture diagnostic")
				return nil
			}
			var out, stderr bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&stderr)
			root.SetArgs(tc.args)
			if err := executeCommand(context.Background(), root); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(stderr.String(), "level=INFO") != tc.info || reported != tc.report {
				t.Fatalf("info=%v report=%v stderr=%q", tc.info, reported, stderr.String())
			}
		})
	}
}

func TestUnknownCommandSuggestionsAreUsageErrors(t *testing.T) {
	root := NewRootCmd("test", "test")
	var out, stderr bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&stderr)
	root.SetArgs([]string{"stauts"})
	err := executeCommand(context.Background(), root)
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 2 || !strings.Contains(stderr.String(), "Did you mean this?") || !strings.Contains(stderr.String(), "\tstatus") {
		t.Fatalf("error=%v stderr=%q", err, stderr.String())
	}
	if out.Len() != 0 {
		t.Fatalf("unknown command wrote stdout: %q", out.String())
	}
}

func TestUsageClassificationUsesErrorSource(t *testing.T) {
	for _, args := range [][]string{{"status", "--missing"}, {"--missing=value", "status"}, {"--nosuch", "status"}, {"--color"}} {
		root := NewRootCmd("test", "test")
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(args)
		err := executeCommand(context.Background(), root)
		var exit *ExitError
		if !errors.As(err, &exit) || exit.Code != 2 {
			t.Fatalf("parse %v returned %v", args, err)
		}
	}
	for _, argumentError := range []bool{false, true} {
		root := NewRootCmd("test", "test")
		cmd := &cobra.Command{Use: "fixture", RunE: func(*cobra.Command, []string) error { return fmt.Errorf("unknown command while loading cached data") }}
		if argumentError {
			cmd.Args = func(*cobra.Command, []string) error { return fmt.Errorf("input rejected without trigger words") }
		}
		root.AddCommand(cmd)
		installOutputBoundary(cmd)
		root.PersistentPreRunE = func(*cobra.Command, []string) error { return nil }
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs([]string{"fixture"})
		err := executeCommand(context.Background(), root)
		var exit *ExitError
		typed := errors.As(err, &exit)
		if argumentError && (!typed || exit.Code != 2) {
			t.Fatalf("argument source not usage: %v", err)
		}
		if !argumentError && typed {
			t.Fatalf("operational text misclassified as usage: %v", err)
		}
	}
}

func TestBareParentsStillPrintHelp(t *testing.T) {
	for _, args := range [][]string{nil, {"user"}, {"gpu"}, {"fleet"}, {"--help", "apply"}} {
		root := NewRootCmd("test", "test")
		var out, stderr bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&stderr)
		root.SetArgs(args)
		if err := executeCommand(context.Background(), root); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !strings.Contains(out.String(), "Usage:") || stderr.Len() != 0 {
			t.Fatalf("%v: stdout=%q stderr=%q", args, out.String(), stderr.String())
		}
	}
}

func TestRequiredFlagValidationKeepsExitOne(t *testing.T) {
	root := NewRootCmd("test", "test")
	cmd := &cobra.Command{Use: "fixture", RunE: func(*cobra.Command, []string) error { return nil }}
	cmd.Flags().String("needed", "", "Required fixture value")
	if err := cmd.MarkFlagRequired("needed"); err != nil {
		t.Fatal(err)
	}
	root.AddCommand(cmd)
	installOutputBoundary(cmd)
	root.PersistentPreRunE = func(*cobra.Command, []string) error { return nil }
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fixture"})
	err := executeCommand(context.Background(), root)
	var exit *ExitError
	if err == nil || errors.As(err, &exit) {
		t.Fatalf("required flag error changed exit class: %v", err)
	}
}

func TestOperationalFlagAccessErrorIsNotUsage(t *testing.T) {
	root := NewRootCmd("test", "test")
	cmd := &cobra.Command{Use: "fixture", RunE: func(c *cobra.Command, _ []string) error {
		_, err := c.Flags().GetString("missing-internal-setting")
		return err
	}}
	root.AddCommand(cmd)
	installOutputBoundary(cmd)
	root.PersistentPreRunE = func(*cobra.Command, []string) error { return nil }
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fixture"})
	err := executeCommand(context.Background(), root)
	var exit *ExitError
	if err == nil || errors.As(err, &exit) {
		t.Fatalf("operational pflag access error changed exit class: %v", err)
	}
}

func TestGeneratedCompletionKeepsGlobalFlagArityAndWriter(t *testing.T) {
	for _, flags := range [][]string{{"--ascii"}, {"--verbose"}, {"--color", "never"}} {
		root := NewRootCmd("test", "test")
		var stdout, stderr bytes.Buffer
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		args := append([]string{"completion"}, flags...)
		root.SetArgs(append(args, "bash"))
		if err := executeCommand(context.Background(), root); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(stdout.String(), "# bash completion") || stderr.Len() != 0 {
			t.Fatalf("completion flags=%v output=%q stderr=%q", flags, stdout.String(), stderr.String())
		}
	}
}

func TestFleetLeafFlagBeforeSubcommandStillParses(t *testing.T) {
	root := NewRootCmd("test", "test")
	cmd, _, err := root.Find([]string{"fleet", "targets"})
	if err != nil {
		t.Fatal(err)
	}
	inventory := filepath.Join(t.TempDir(), "inventory.yaml")
	parsed := ""
	cmd.RunE = func(c *cobra.Command, _ []string) error { parsed, _ = c.Flags().GetString("inventory"); return nil }
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"fleet", "--inventory", inventory, "targets"})
	if err := executeCommand(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if parsed != inventory {
		t.Fatalf("inventory=%q want %q", parsed, inventory)
	}
}

func TestGeneratedCompletionArgumentValidationIsUsage(t *testing.T) {
	root := NewRootCmd("test", "test")
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"completion", "bash", "extra"})
	err := executeCommand(context.Background(), root)
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 2 || !exit.Usage {
		t.Fatalf("generated argument error lost its source: %v", err)
	}
}
