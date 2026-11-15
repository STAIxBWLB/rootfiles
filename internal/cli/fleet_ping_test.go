package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFleetPingReadOnlySelectionAndOutput(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	inventory := filepath.Join(dir, "fleet.yaml")
	if err := os.WriteFile(inventory, []byte("hosts:\n  host1:\n    ssh: host1\n    sudo: none\n"), 0600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + calls + "'\necho 'rootfiles version test'\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	var out bytes.Buffer
	cmd := NewRootCmd("test", "test")
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"fleet", "ping", "--inventory", inventory, "--all", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"state": "ok"`) {
		t.Fatalf("output: %s", out.String())
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "/usr/local/bin/rootfiles --version") {
		t.Fatalf("calls: %s", data)
	}
}

func TestFleetPingFindingsExitTwo(t *testing.T) {
	dir := t.TempDir()
	inventory := filepath.Join(dir, "fleet.yaml")
	if err := os.WriteFile(inventory, []byte("hosts:\n  host1:\n    ssh: host1\n    sudo: none\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\necho 'Permission denied (publickey)' >&2\nexit 255\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	var out bytes.Buffer
	cmd := NewRootCmd("test", "test")
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"fleet", "ping", "--inventory", inventory, "--all", "--json"})
	err := cmd.Execute()
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 2 || !strings.Contains(out.String(), "auth-failed") {
		t.Fatalf("output %s, error %v", out.String(), err)
	}
}
