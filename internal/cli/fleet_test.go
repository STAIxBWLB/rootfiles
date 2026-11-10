package cli

import (
	"bytes"
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
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\ntouch "+fleet.ShellQuote(calls)+"\nexit 0\n"), 0700); err != nil {
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
	fake := []byte("#!/bin/sh\nprintf '%s\\n' \"$7\" > " + fleet.ShellQuote(remoteFile) + "\ntouch " + fleet.ShellQuote(calls) + "\ncat >/dev/null\necho pushed\nexit 0\n")
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

// targetsInventory writes a two-host inventory whose hosts publish a node
// exporter target, so a selection is visible in the rendered JSON.
func targetsInventory(t *testing.T) (inventory string) {
	t.Helper()
	dir := t.TempDir()
	site := filepath.Join(dir, "site.yaml")
	siteConfig := "users:\n  home_base: /home\nmodules:\n  monitoring:\n    node_exporter: true\n"
	if err := os.WriteFile(site, []byte(siteConfig), 0600); err != nil {
		t.Fatal(err)
	}
	inventory = filepath.Join(dir, "fleet.yaml")
	hosts := "hosts:\n" +
		"  one:\n    ssh: 10.0.0.1\n    address: 10.0.0.1\n    config: site.yaml\n" +
		"  two:\n    ssh: 10.0.0.2\n    address: 10.0.0.2\n    config: site.yaml\n"
	if err := os.WriteFile(inventory, []byte(hosts), 0600); err != nil {
		t.Fatal(err)
	}
	return inventory
}

// AC4: fleet targets --host limits the rendered targets to that host.
func TestFleetTargetsHonoursSelection(t *testing.T) {
	inventory := targetsInventory(t)
	render := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		cmd := NewRootCmd("test", "test")
		cmd.SetOut(&out)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(append([]string{"fleet", "targets", "--inventory", inventory}, args...))
		if err := cmd.Execute(); err != nil {
			t.Fatalf("fleet targets %v: %v", args, err)
		}
		return out.String()
	}

	all := render()
	if !strings.Contains(all, "10.0.0.1:9100") || !strings.Contains(all, "10.0.0.2:9100") {
		t.Fatalf("without a selection every host is rendered:\n%s", all)
	}
	one := render("--host", "one")
	if !strings.Contains(one, "10.0.0.1:9100") || strings.Contains(one, "10.0.0.2") {
		t.Fatalf("--host one leaked another host:\n%s", one)
	}

	cmd := NewRootCmd("test", "test")
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"fleet", "targets", "--inventory", inventory, "--host", "nope"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unknown host") {
		t.Fatalf("unknown --host accepted: %v", err)
	}
}

// AC4: a dry run previews a mutation without --yes and opens no SSH.
func TestFleetDryRunNeedsNoYes(t *testing.T) {
	inventory, calls := fleetFlagFixture(t)
	cmd := NewRootCmd("test", "test")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"fleet", "update", "--inventory", inventory, "--all", "--version", "v1.2.3", "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("fleet update --dry-run without --yes: %v", err)
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatal("a dry run opened SSH")
	}
	if !strings.Contains(out.String(), "dry-run") {
		t.Errorf("dry run did not say so:\n%s", out.String())
	}
}

// AC4: fleet schedule enable passes --on-calendar through to each host.
func TestFleetSchedulePassesOnCalendar(t *testing.T) {
	dir := t.TempDir()
	remoteFile := filepath.Join(dir, "remote-command")
	sshDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(sshDir, 0700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$7\" > " + fleet.ShellQuote(remoteFile) + "\ncat >/dev/null\necho scheduled\n"
	if err := os.WriteFile(filepath.Join(sshDir, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", sshDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	inventory := filepath.Join(dir, "fleet.yaml")
	if err := os.WriteFile(inventory, []byte("hosts:\n  h1:\n    ssh: h1\n"), 0600); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCmd("test", "test")
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"fleet", "schedule", "enable", "--inventory", inventory, "--host", "h1",
		"--yes", "--on-calendar", "*-*-* 06:00"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("fleet schedule enable --on-calendar: %v", err)
	}
	remote, err := os.ReadFile(remoteFile)
	if err != nil {
		t.Fatal(err)
	}
	if want := "schedule enable --on-calendar '*-*-* 06:00'"; !strings.Contains(string(remote), want) {
		t.Fatalf("remote command %q does not carry %q", strings.TrimSpace(string(remote)), want)
	}

	// The expression belongs to enable, and must not break out of the unit
	// file or the shell quoting.
	for _, args := range [][]string{
		{"disable", "--on-calendar", "hourly"},
		{"enable", "--on-calendar", "daily\nOnBootSec=1m"},
	} {
		cmd := NewRootCmd("test", "test")
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(append([]string{"fleet", "schedule"}, append(args, "--inventory", inventory, "--host", "h1", "--yes")...))
		if err := cmd.Execute(); err == nil {
			t.Errorf("fleet schedule %v accepted", args)
		}
	}
}

func TestFleetUpdateSkipUnreachablePrintsRerunCommand(t *testing.T) {
	dir := t.TempDir()
	sshDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(sshDir, 0700); err != nil {
		t.Fatal(err)
	}
	contacted := filepath.Join(dir, "contacted")
	script := "#!/bin/sh\n" +
		"[ \"$6\" != h2 ] || { echo 'ssh: connect to host h2 port 22: No route to host' >&2; exit 255; }\n" +
		"case \"$7\" in *'check -o json') printf '{\"modules\":[]}'; exit 0 ;; esac\n" +
		"echo \"$6\" >> " + fleet.ShellQuote(contacted) + "\necho updated\n"
	if err := os.WriteFile(filepath.Join(sshDir, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", sshDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	inventory := filepath.Join(dir, "fleet.yaml")
	if err := os.WriteFile(inventory, []byte("hosts:\n  h1:\n    ssh: h1\n  h2:\n    ssh: h2\n  h3:\n    ssh: h3\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd := NewRootCmd("test", "test")
	cmd.SetOut(io.Discard)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"fleet", "update", "--inventory", inventory, "--all", "--yes", "--version", "v1.2.3", "--skip-unreachable"})
	err := cmd.Execute()
	if exitErr, ok := err.(*ExitError); !ok || exitErr.Code != 2 {
		t.Fatalf("a run with a skipped host must exit 2, got %v", err)
	}
	if got, _ := os.ReadFile(contacted); string(got) != "h1\nh3\n" {
		t.Fatalf("hosts after the unreachable one should still update: %q", got)
	}
	out := stderr.String()
	for _, want := range []string{"fleet update", "--inventory " + fleet.ShellQuote(inventory), "--version 'v1.2.3'", "--yes", "--host h2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rerun command missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "--all") || strings.Contains(out, "--host h1") {
		t.Fatalf("rerun command must select only skipped hosts:\n%s", out)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "state", "rootfiles", "fleet.jsonl"))
	if !strings.Contains(string(log), `"command":"update --skip-unreachable"`) || !strings.Contains(string(log), "skipped-unreachable") {
		t.Fatalf("run log should record the flag and state: %s", log)
	}
}
