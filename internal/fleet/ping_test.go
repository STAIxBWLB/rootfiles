package fleet

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pingStub(t *testing.T, script string) (SSHRunner, string) {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "ssh")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + ShellQuote(calls) + "\n" + script + "\n"
	if err := os.WriteFile(bin, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	return SSHRunner{Binary: bin}, calls
}

func TestPingFailureClasses(t *testing.T) {
	cases := []struct{ name, script, state, next string }{
		{"auth", "echo 'Permission denied (publickey)' >&2; exit 255", "auth-failed", "ssh 'host1'"},
		{"unknown", "echo 'Host key verification failed' >&2; exit 255", "host-key-unknown", "verify it out of band"},
		{"changed", "echo 'REMOTE HOST IDENTIFICATION HAS CHANGED' >&2; exit 255", "host-key-changed", "verify it out of band"},
		{"missing", "echo '/usr/local/bin/rootfiles: not found' >&2; exit 127", "missing", "fleet bootstrap"},
		{"timeout", "echo 'Connection timed out' >&2; exit 255", "timeout", "network"},
		{"sudo", "case \"$*\" in *--version*) echo 'rootfiles version v1.2.3';; *) echo 'sudo: a password is required' >&2; exit 1;; esac", "needs-privilege", "users.fleet_sudo_users"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := pingStub(t, tc.script)
			h := NamedHost{Name: "host1", Host: Host{SSH: "host1"}, Sudo: "nopasswd"}
			got := r.Ping(context.Background(), h, "v1.2.3")
			if got.State != tc.state || !strings.Contains(got.Reason, tc.next) {
				t.Fatalf("result: %+v", got)
			}
		})
	}
}

func TestPingCommandsAndPrivilege(t *testing.T) {
	r, calls := pingStub(t, "case \"$*\" in *--version*) echo 'rootfiles version v1.2.3';; *'id -u'*) echo 0;; *) echo allowed;; esac")
	for _, mode := range []string{"nopasswd", "root", "none"} {
		h := NamedHost{Name: "host1", Host: Host{SSH: "host1"}, Sudo: mode}
		got := r.Ping(context.Background(), h, "v1.2.3")
		if got.State != "ok" || got.Output != "v1.2.3" {
			t.Fatalf("%s: %+v", mode, got)
		}
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	if !strings.Contains(out, "sudo -n -l -- "+readOnlyRemoteCommand("status")) {
		t.Fatalf("sudo command: %s", out)
	}
	if strings.Contains(out, "StrictHostKeyChecking") || strings.Contains(out, "accept-new") {
		t.Fatalf("weakened host key checking: %s", out)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.Contains(line, "--version") && !strings.Contains(line, "sudo -n -l -- "+readOnlyRemoteCommand("status")) && !strings.Contains(line, "id -u") {
			t.Fatalf("unexpected command: %s", line)
		}
	}
	got := r.Ping(context.Background(), NamedHost{Name: "host1", Host: Host{SSH: "host1"}, Sudo: "none"}, "v2.0.0")
	if got.State != "version-skew" {
		t.Fatalf("skew: %+v", got)
	}
}

func TestPingDeadline(t *testing.T) {
	r, _ := pingStub(t, "exec sleep 5")
	r.Timeout = 20 * time.Millisecond
	got := r.Ping(context.Background(), NamedHost{Name: "host1", Host: Host{SSH: "host1"}}, "dev")
	if got.State != "timeout" {
		t.Fatalf("timeout: %+v", got)
	}
}
