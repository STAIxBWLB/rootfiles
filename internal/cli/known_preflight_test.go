package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/state"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

func knownPreflightFixture(t *testing.T) {
	t.Helper()
	previousUID, previousOS, previousArgs := geteuid, nativeHostOS, os.Args
	geteuid = func() int { return 0 }
	nativeHostOS = "linux"
	t.Cleanup(func() { geteuid, nativeHostOS, os.Args = previousUID, previousOS, previousArgs })
	dir := t.TempDir()
	t.Setenv("ROOTFILES_STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv("ROOTFILES_LOCK_FILE", filepath.Join(dir, "rootfiles.lock"))
	t.Setenv("ROOTFILES_LOG_FILE", filepath.Join(dir, "audit"))
	t.Setenv("PATH", t.TempDir())
	t.Setenv("LC_ALL", "C.UTF-8")
}

func executeKnownPreflight(t *testing.T, args []string, ascii bool, wantNext string) error {
	t.Helper()
	args = append(append([]string(nil), args...), "--color=never")
	if ascii {
		args = append(args, "--ascii")
	}
	os.Args = append([]string{"/fixture/rootfiles"}, args...)
	root := NewRootCmd("test", "test")
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	err := executeCommand(context.Background(), root)
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 1 || exit.Usage {
		t.Fatalf("expected operation exit 1, got %v, stderr=%q", err, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("preflight polluted stdout: %q", stdout.String())
	}
	lines := strings.Split(strings.TrimSuffix(stderr.String(), "\n"), "\n")
	prefix := "  " + ui.MarkNext + " "
	if ascii {
		prefix = "  -> "
	}
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "rootfiles: ") || lines[1] != prefix+wantNext {
		t.Fatalf("expected two diagnostic lines and next %q: %q", wantNext, stderr.String())
	}
	if strings.Contains(lines[0], "(try:") || strings.ContainsRune(stderr.String(), 27) {
		t.Fatalf("inline fix or unexpected color: %q", stderr.String())
	}
	return err
}

func TestKnownRootPreflightQuotesOriginalArgs(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		t.Run(strconv.FormatBool(ascii), func(t *testing.T) {
			knownPreflightFixture(t)
			geteuid = func() int { return 1000 }
			next := "sudo rootfiles 'apply' '--config' '/tmp/team'\\''s config.yaml' '--color=never'"
			if ascii {
				next += " '--ascii'"
			}
			executeKnownPreflight(t, []string{"apply", "--config", "/tmp/team's config.yaml"}, ascii, next)
		})
	}
}

func TestKnownRootPreflightNeverReplaysSecrets(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		next string
	}{
		{"token flag", []string{"apply", "--tunnel-token", "fixture-secret"}, "sudo rootfiles apply --help"},
		{"inline token", []string{"apply", "--tunnel-token=fixture-secret"}, "sudo rootfiles apply --help"},
		{"password", []string{"user", "passwd", "alice", "--password", "fixture-secret"}, "sudo rootfiles user passwd --help"},
		{"tunnel positional", []string{"tunnel", "setup", "fixture-secret"}, "sudo rootfiles tunnel setup --help"},
		{"literal mask", []string{"user", "passwd", "alice", "--password", "***"}, "sudo rootfiles user passwd --help"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			knownPreflightFixture(t)
			geteuid = func() int { return 1000 }
			err := executeKnownPreflight(t, tc.args, true, tc.next)
			var exit *ExitError
			errors.As(err, &exit)
			if strings.Contains(exit.Error()+exit.Next, "fixture-secret") || strings.Contains(exit.Next, "***") {
				t.Fatalf("secret or mask exposed: %+v", exit)
			}
		})
	}
}

func TestKnownLinuxAndRefusedFlagNextSteps(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		t.Run("linux/"+strconv.FormatBool(ascii), func(t *testing.T) {
			knownPreflightFixture(t)
			nativeHostOS = "darwin"
			executeKnownPreflight(t, []string{"apply"}, ascii, "rootfiles fleet --help")
			if _, err := os.Stat(state.LockPath()); !os.IsNotExist(err) {
				t.Fatalf("non-Linux preflight reached lock: %v", err)
			}
		})
		t.Run("flags/"+strconv.FormatBool(ascii), func(t *testing.T) {
			knownPreflightFixture(t)
			executeKnownPreflight(t, []string{"schedule", "status", "--profile", "minimal"}, ascii, "remove --profile from this command")
		})
		t.Run("json-flags/"+strconv.FormatBool(ascii), func(t *testing.T) {
			knownPreflightFixture(t)
			executeKnownPreflight(t, []string{"status", "--json", "--vlan-address", "203.0.113.10/32"}, ascii, "remove --vlan-address from this command")
		})
	}
}

func TestKnownLockPreflightPreservesTypedError(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		t.Run(strconv.FormatBool(ascii), func(t *testing.T) {
			knownPreflightFixture(t)
			lock, err := state.Acquire()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(lock.Release)
			err = executeKnownPreflight(t, []string{"apply"}, ascii, "ps -p "+strconv.Itoa(os.Getpid()))
			var held *state.LockHeldError
			if !errors.As(err, &held) || held.PID != os.Getpid() || held.Path != state.LockPath() {
				t.Fatalf("typed lock error lost: %v", err)
			}
		})
	}
}
