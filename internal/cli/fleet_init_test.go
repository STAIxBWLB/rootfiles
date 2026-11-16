package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/fleet"
)

func runFleetOnboarding(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, stderr bytes.Buffer
	cmd := NewRootCmd("test", "test")
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetArgs(append([]string{"fleet"}, args...))
	err := cmd.Execute()
	return out.String(), stderr.String(), err
}

func TestFleetInitSkeletonAndOverwrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	path := filepath.Join(home, ".config", "rootfiles", "fleet.yaml")
	if _, _, err := runFleetOnboarding(t, "init"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("file mode: %v %v", info, err)
	}
	inv, err := fleet.LoadInventory(path)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Hosts["example-host.example"].SSH != "example-host.example" {
		t.Fatalf("example: %+v", inv.Hosts)
	}
	if _, _, err := runFleetOnboarding(t, "init"); err == nil {
		t.Fatal("existing inventory accepted")
	}
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runFleetOnboarding(t, "init", "--overwrite"); err != nil {
		t.Fatal(err)
	}
	backups, err := filepath.Glob(path + ".backup-*")
	if err != nil || len(backups) != 1 {
		t.Fatalf("backup: %v %v", backups, err)
	}
	old, err := os.ReadFile(backups[0])
	if err != nil || string(old) != "original" {
		t.Fatalf("backup content: %q %v", old, err)
	}
}

func TestFleetInitDryRunAndImport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	sshdir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshdir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshdir, "config"), []byte("Host gpu1 *.example bad_name\nMatch all\n User nobody\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, stderr, err := runFleetOnboarding(t, "init", "--from-ssh-config", "--group", "gpu", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "bad_name") || !strings.Contains(stderr, "*.example") {
		t.Fatalf("skips: %s", stderr)
	}
	path := filepath.Join(home, ".config", "rootfiles", "fleet.yaml")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote file: %v", err)
	}
	if _, _, err := runFleetOnboarding(t, "init", "--from-ssh-config", "--group", "gpu"); err != nil {
		t.Fatal(err)
	}
	if _, err := fleet.LoadInventory(path); err != nil {
		t.Fatalf("written import: %v", err)
	}
	inv, err := fleet.ParseInventory([]byte(out), path)
	if err != nil {
		t.Fatal(err)
	}
	if got := inv.Hosts["gpu1"]; got.SSH != "gpu1" || len(got.Groups) != 1 || got.Groups[0] != "gpu" {
		t.Fatalf("import: %+v", got)
	}
}

func TestFleetHostsNeverCallsSSH(t *testing.T) {
	inventory, calls := fleetFlagFixture(t)
	out, _, err := runFleetOnboarding(t, "hosts", "--inventory", inventory, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []fleetHostRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 || rows[0].Name != "host1" || rows[0].Sudo != "nopasswd" {
		t.Fatalf("output: %s: %v", out, err)
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatalf("ssh invoked: %v", err)
	}
	if err := os.WriteFile(inventory, []byte("hosts: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runFleetOnboarding(t, "hosts", "--inventory", inventory); err == nil {
		t.Fatal("invalid inventory accepted")
	}
}

func TestFleetReadNoSelectionHintsSameInventory(t *testing.T) {
	inventory, calls := fleetFlagFixture(t)
	_, _, err := runFleetOnboarding(t, "status", "--inventory", inventory)
	if err == nil || !strings.Contains(err.Error(), "rootfiles fleet hosts --inventory "+fleet.ShellQuote(inventory)) {
		t.Fatalf("hint: %v", err)
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatalf("ssh invoked: %v", err)
	}
}

func TestFleetOnboardingHelpWidth(t *testing.T) {
	for _, name := range []string{"init", "hosts"} {
		out, _, err := runFleetOnboarding(t, name, "--help")
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(out, "\n") {
			if len(line) > 80 {
				t.Errorf("%s help exceeds 80 columns (%d): %s", name, len(line), line)
			}
		}
	}
}

// closedStdout stands in for a pipe the reader closed: every write fails.
type closedStdout struct{}

func (closedStdout) Write([]byte) (int, error) { return 0, errors.New("fixture: stdout closed") }

// TestFleetInitReportsASuccessWriteFailure keeps the exit code honest when the
// inventory is created but its confirmation cannot be printed: `fleet init`'s
// only stdout is that line, so a failed write is the command's error, not 0.
func TestFleetInitReportsASuccessWriteFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	path := filepath.Join(home, ".config", "rootfiles", "fleet.yaml")
	if _, _, err := runFleetOnboarding(t, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "--inventory", filepath.Join(home, "created.yaml")},
		{"init", "--overwrite"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd := NewRootCmd("test", "test")
			cmd.SetOut(closedStdout{})
			cmd.SetErr(io.Discard)
			cmd.SetArgs(append([]string{"fleet"}, args...))
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), "stdout closed") {
				t.Fatalf("a failed confirmation write returned %v, want the write error", err)
			}
		})
	}
}
