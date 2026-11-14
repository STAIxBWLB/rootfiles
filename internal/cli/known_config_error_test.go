package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/fleet"
)

func TestFatalConfigLoadNextSteps(t *testing.T) {
	t.Setenv("ROOTFILES_STATE_DIR", t.TempDir())
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	f := filepath.Join(t.TempDir(), "bad site's config.yaml")
	if err := os.WriteFile(f, []byte("extends: minimal\nssh:\n  port: 70000\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"check", "--json"}, {"apply", "--dry-run", "--yes"},
		{"gpu", "list"},
	} {
		for _, ascii := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/ascii=%t", strings.Join(args, "_"), ascii), func(t *testing.T) {
				root := NewRootCmd("test", "abc")
				var out, stderr bytes.Buffer
				root.SetOut(&out)
				root.SetErr(&stderr)
				argv := append(append([]string{}, args...), "--config", f, "--color=never")
				marker := "→ "
				if ascii {
					argv = append(argv, "--ascii")
					marker = "-> "
				}
				root.SetArgs(argv)
				err := executeCommand(context.Background(), root)
				var exit *ExitError
				if !errors.As(err, &exit) || exit.Code != 1 {
					t.Fatalf("error = %v, want exit 1", err)
				}
				lines := strings.Split(strings.TrimSuffix(stderr.String(), "\n"), "\n")
				if len(lines) != 2 || !strings.HasPrefix(lines[0], "rootfiles: ") ||
					!strings.Contains(lines[0], "ssh.port") ||
					strings.TrimSpace(lines[1]) != marker+"rootfiles config validate --config "+fleet.ShellQuote(f) {
					t.Fatalf("unexpected diagnostics: %q", stderr.String())
				}
				if out.Len() != 0 {
					t.Fatalf("fatal config load wrote stdout: %q", out.String())
				}
			})
		}
	}
}

func TestUnknownProfileNextStep(t *testing.T) {
	t.Setenv("ROOTFILES_STATE_DIR", t.TempDir())
	for _, args := range [][]string{{"check", "--json"}, {"config", "show"}, {"config", "validate"}} {
		for _, overrides := range [][]string{nil, {"--home-base", "/fixture/home"}, {"--vlan-address", "invalid"}} {
			t.Run(strings.Join(append(append([]string{}, args...), overrides...), "_"), func(t *testing.T) {
				root := NewRootCmd("test", "abc")
				var out, stderr bytes.Buffer
				root.SetOut(&out)
				root.SetErr(&stderr)
				argv := append(append([]string{}, args...), "--profile", "not-a-profile", "--ascii", "--color=never")
				root.SetArgs(append(argv, overrides...))
				err := executeCommand(context.Background(), root)
				var exit *ExitError
				if !errors.As(err, &exit) || exit.Code != 1 {
					t.Fatalf("error = %v, want exit 1", err)
				}
				if out.Len() != 0 || strings.Count(stderr.String(), "\n") != 2 ||
					!strings.Contains(stderr.String(), "available profiles: "+strings.Join(config.AvailableProfiles(), ", ")) ||
					!strings.Contains(stderr.String(), "-> rootfiles --help\n") {
					t.Fatalf("stdout=%q stderr=%q", out.String(), stderr.String())
				}
			})
		}
	}
}

func TestConfigInspectionDoesNotSuggestRepeatingValidation(t *testing.T) {
	t.Setenv("ROOTFILES_STATE_DIR", t.TempDir())
	f := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(f, []byte("extends: minimal\nssh:\n  port: 70000\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"show", "validate"} {
		t.Run(action, func(t *testing.T) {
			root := NewRootCmd("test", "abc")
			var out, stderr bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&stderr)
			root.SetArgs([]string{"config", action, "--config", f, "--ascii", "--color=never"})
			err := executeCommand(context.Background(), root)
			var exit *ExitError
			if err == nil || (errors.As(err, &exit) && (exit.Code != 1 || exit.Next != "")) ||
				out.Len() != 0 || !strings.Contains(stderr.String(), "ssh.port") ||
				strings.Contains(stderr.String(), "rootfiles config validate") || strings.Contains(stderr.String(), "->") {
				t.Fatalf("error=%v stdout=%q stderr=%q", err, out.String(), stderr.String())
			}
		})
	}
}

func TestWithNextPreservesCauseAndProfileTarget(t *testing.T) {
	cause := errors.New("loading config: invalid setting\n  another detail")
	err := configLoadError(NewRootCmd("test", "abc"), cause, "minimal", "")
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 1 || !errors.Is(err, cause) ||
		exit.Next != "rootfiles config validate --profile 'minimal'" || strings.Contains(exit.Msg, "\n") {
		t.Fatalf("error = %#v", err)
	}
	if withNext(nil, "unused") != nil {
		t.Fatal("withNext(nil) must preserve success")
	}
}

func TestFatalProfileLoadNextStep(t *testing.T) {
	t.Setenv("ROOTFILES_STATE_DIR", t.TempDir())
	t.Setenv("ROOTFILES_HOME_BASE", "relative-home")
	root := NewRootCmd("test", "abc")
	var out, stderr bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&stderr)
	root.SetArgs([]string{"check", "--profile", "minimal", "--json", "--ascii", "--color=never"})
	err := executeCommand(context.Background(), root)
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 1 || out.Len() != 0 ||
		strings.Count(stderr.String(), "\n") != 2 ||
		!strings.Contains(stderr.String(), "-> rootfiles config validate --profile 'minimal'\n") {
		t.Fatalf("error=%v stdout=%q stderr=%q", err, out.String(), stderr.String())
	}
}

func TestConfigLoadOverridesDoNotSuggestIncompleteValidation(t *testing.T) {
	t.Setenv("ROOTFILES_STATE_DIR", t.TempDir())
	for _, args := range [][]string{{"check", "--json"}, {"apply", "--dry-run", "--yes"}, {"tunnel", "setup", "--dry-run"}} {
		for _, flag := range []string{"vlan-address", "home-base"} {
			t.Run(strings.Join(args, "_")+"/"+flag, func(t *testing.T) {
				root := NewRootCmd("test", "test")
				var stdout, stderr bytes.Buffer
				root.SetOut(&stdout)
				root.SetErr(&stderr)
				argv := append(append([]string{}, args...), "--profile", "minimal", "--"+flag, "invalid", "--ascii", "--color=never")
				root.SetArgs(argv)
				err := executeCommand(context.Background(), root)
				var exit *ExitError
				if err == nil || (errors.As(err, &exit) && (exit.Code != 1 || exit.Next != "")) ||
					stdout.Len() != 0 || strings.Count(stderr.String(), "\n") != 1 ||
					!strings.HasPrefix(stderr.String(), "rootfiles: ") || !strings.Contains(stderr.String(), "invalid") ||
					strings.Contains(stderr.String(), "->") {
					t.Fatalf("error=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
				}
			})
		}
	}
}
