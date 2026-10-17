package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/fleet"
)

func TestFleetReadPoolBoundsFakeSSHConcurrency(t *testing.T) {
	dir := t.TempDir()
	active, peak := filepath.Join(dir, "active"), filepath.Join(dir, "peak")
	lock := filepath.Join(dir, "lock")
	for _, p := range []string{active, peak} {
		if err := os.WriteFile(p, []byte("0"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	quote := func(s string) string { return "'" + s + "'" }
	bin := filepath.Join(dir, "ssh")
	script := "#!/bin/sh\n" +
		"lock=" + quote(lock) + "\nactive=" + quote(active) + "\npeak=" + quote(peak) + "\n" +
		"while ! mkdir \"$lock\" 2>/dev/null; do sleep 0.01; done\n" +
		"n=$(cat \"$active\"); n=$((n+1)); echo $n > \"$active\"; m=$(cat \"$peak\"); [ $n -le $m ] || echo $n > \"$peak\"; rmdir \"$lock\"\n" +
		"sleep 0.15\n" +
		"while ! mkdir \"$lock\" 2>/dev/null; do sleep 0.01; done\n" +
		"n=$(cat \"$active\"); n=$((n-1)); echo $n > \"$active\"; rmdir \"$lock\"\n" +
		"printf '{}\\n'\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	hosts := make([]fleet.NamedHost, 4)
	for i := range hosts {
		hosts[i] = fleet.NamedHost{Name: strconv.Itoa(i), Host: fleet.Host{SSH: "fake"}, Sudo: "none"}
	}
	results := runReadPool(context.Background(), fleet.SSHRunner{Binary: bin}, hosts, "status", 2)
	for _, result := range results {
		if result.State != "ok" {
			t.Fatalf("unexpected fake SSH result: %#v", result)
		}
	}
	got, err := os.ReadFile(peak)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(got)))
	if err != nil {
		t.Fatal(err)
	}
	if n > 2 || n < 2 {
		t.Fatalf("peak SSH concurrency = %d, want 2", n)
	}
}

func TestInvalidMutationTimeoutFailsBeforeSSH(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "ssh-called")
	sshDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(sshDir, 0700); err != nil {
		t.Fatal(err)
	}
	ssh := filepath.Join(sshDir, "ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\ntouch "+shellQuoteCLI(calls)+"\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", sshDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	inventory := filepath.Join(dir, "fleet.yaml")
	if err := os.WriteFile(inventory, []byte("hosts:\n  host1:\n    ssh: host1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, timeout := range []string{"0", "25h"} {
		cmd := NewRootCmd("test", "test")
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"fleet", "update", "--inventory", inventory, "--host", "host1", "--yes", "--version", "v1.2.3", "--timeout", timeout})
		if err := cmd.Execute(); err == nil {
			t.Errorf("invalid timeout %q was accepted", timeout)
		}
		if _, err := os.Stat(calls); !os.IsNotExist(err) {
			t.Fatalf("invalid timeout %q invoked SSH", timeout)
		}
	}
}

func TestFleetTargetsPushValidatesPathAndGuardsParentsBeforeWrites(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "ssh-called")
	remoteFile := filepath.Join(dir, "remote-command")
	sshDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(sshDir, 0700); err != nil {
		t.Fatal(err)
	}
	ssh := filepath.Join(sshDir, "ssh")
	fake := []byte("#!/bin/sh\nprintf '%s\\n' \"$7\" > " + shellQuoteCLI(remoteFile) + "\ntouch " + shellQuoteCLI(calls) + "\ncat >/dev/null\necho pushed\nexit 0\n")
	if err := os.WriteFile(ssh, fake, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", sshDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	inventory := filepath.Join(dir, "fleet.yaml")
	site := filepath.Join(dir, "site.yaml")
	writeInventory := func(target string) {
		content := "users:\n  home_base: /home\nmodules:\n  monitoring:\n    enabled: true\n    hub:\n      enabled: true\n      targets_file: " + target + "\n"
		if err := os.WriteFile(site, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(inventory, []byte("hosts:\n  hub:\n    ssh: test-hub\n    config: site.yaml\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runPush := func() error {
		cmd := NewRootCmd("test", "test")
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"fleet", "targets", "--inventory", inventory, "--push", "hub", "--yes"})
		return cmd.Execute()
	}
	writeInventory("/srv/rootfiles/discovery/targets.json")
	if err := runPush(); err != nil {
		t.Fatalf("valid isolated targets path failed: %v", err)
	}
	remote, err := os.ReadFile(remoteFile)
	if err != nil {
		t.Fatal(err)
	}
	command := string(remote)
	parentCheck := strings.Index(command, "if [ -e \"$dir\" ]; then check_dir")
	parentCreate := strings.Index(command, "install -d -o root -g root -m 0755")
	targetCheck := strings.Index(command, "if [ -e \"$target\" ]; then [ -f \"$target\" ]")
	stage := strings.Index(command, "tmp=$(mktemp")
	if parentCheck < 0 || parentCreate < parentCheck || targetCheck < 0 || targetCheck > parentCreate || stage < targetCheck {
		t.Fatalf("target push lacks ordered ownership checks before staging: %s", command)
	}
	if strings.Contains(command, "install -d -m 0755 ") || !strings.Contains(command, "mv -f -- \"$tmp\" \"$target\"") {
		t.Fatalf("target push is not guarded and atomic: %s", command)
	}

	if err := os.Remove(calls); err != nil {
		t.Fatal(err)
	}
	writeInventory("/etc/rootfiles/monitoring/targets.json")
	if err := runPush(); err == nil {
		t.Fatal("target path inside the hub config/secret directory was accepted")
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatalf("invalid discovery path opened SSH: %v", err)
	}
}
