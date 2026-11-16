package cli

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type offlineTransport struct{}

func (offlineTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("offline fixture")
}

func controllerFixture(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	for name, script := range map[string]string{"ssh": "#!/bin/sh\necho 'OpenSSH_fixture' >&2\n", "ssh-add": "#!/bin/sh\necho '2048 fixture'\n"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	inventory := filepath.Join(home, "fleet.yaml")
	if err := os.WriteFile(inventory, []byte("hosts:\n  host1:\n    ssh: host1\n    groups: [gpu]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	oldOS, oldClient := nativeHostOS, http.DefaultClient
	nativeHostOS = "darwin"
	http.DefaultClient = &http.Client{Transport: offlineTransport{}}
	t.Cleanup(func() { nativeHostOS = oldOS; http.DefaultClient = oldClient })
	return home, inventory
}

func runControllerTest(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := NewRootCmd("dev", "test")
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestControllerDoctorAndStatus(t *testing.T) {
	home, inventory := controllerFixture(t)
	out, err := runControllerTest(t, "doctor", "--inventory", inventory, "--json")
	if err != nil || !strings.Contains(out, `"mode": "controller"`) || !strings.Contains(out, `"failed": 0`) {
		t.Fatalf("doctor: %s %v", out, err)
	}
	log := filepath.Join(home, ".local", "state", "rootfiles", "fleet.jsonl")
	if err := os.MkdirAll(filepath.Dir(log), 0700); err != nil {
		t.Fatal(err)
	}
	record := `{"time":"2026-01-01T00:00:00Z","command":"apply","hosts":[{"host":"host1","state":"ok"}]}` + "\n"
	if err := os.WriteFile(log, []byte(record), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = runControllerTest(t, "status", "--inventory", inventory, "--json")
	if err != nil || !strings.Contains(out, `"mode": "controller"`) || !strings.Contains(out, `"host": "host1"`) || strings.Contains(out, `"modules"`) {
		t.Fatalf("status: %s %v", out, err)
	}
}

func TestControllerDoctorMissingInventoryAndIdentity(t *testing.T) {
	home, _ := controllerFixture(t)
	missing := filepath.Join(home, "missing.yaml")
	out, err := runControllerTest(t, "doctor", "--inventory", missing, "--json")
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 2 || !strings.Contains(out, "rootfiles fleet init --inventory") {
		t.Fatalf("doctor: %s %v", out, err)
	}
}

// TestControllerExplicitEmptyInventoryKeepsTheDefault pins the controller half
// of --inventory carrying no default: an empty value reports the per-user
// inventory and succeeds, exactly as an omitted flag does, so the audit changed
// the page and not this report's target or exit code.
func TestControllerExplicitEmptyInventoryKeepsTheDefault(t *testing.T) {
	home, _ := controllerFixture(t)
	perUser := filepath.Join(home, ".config", "rootfiles", "fleet.yaml")
	if err := os.MkdirAll(filepath.Dir(perUser), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(perUser, []byte("hosts:\n  peruser:\n    ssh: peruser\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"status", "--json"}, `"inventory": "` + perUser + `"`},
		{[]string{"status", "--json", "--inventory", ""}, `"inventory": "` + perUser + `"`},
		{[]string{"doctor", "--json", "--inventory", ""}, `"detail": "1 hosts, 0 groups"`},
	} {
		out, err := runControllerTest(t, tc.args...)
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if !strings.Contains(out, tc.want) {
			t.Fatalf("%v reported %s, want %s", tc.args, out, tc.want)
		}
	}
}

func TestControllerIdentityFileAndUnsafeInventory(t *testing.T) {
	home, inventory := controllerFixture(t)
	key := filepath.Join(home, "id_fixture")
	if err := os.WriteFile(key, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, "bin")
	if err := os.WriteFile(filepath.Join(bin, "ssh-keygen"), []byte("#!/bin/sh\necho fixture-public-key\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "ssh-add"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ssh := "#!/bin/sh\ncase \"$1\" in -V) echo OpenSSH_fixture;; -G) echo 'identityfile " + key + "';; *) exit 1;; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(ssh), 0700); err != nil {
		t.Fatal(err)
	}
	out, err := runControllerTest(t, "doctor", "--inventory", inventory, "--json")
	if err != nil || !strings.Contains(out, "SSH identity available") {
		t.Fatalf("identity: %s %v", out, err)
	}
	if err := os.Chmod(inventory, 0666); err != nil {
		t.Fatal(err)
	}
	out, err = runControllerTest(t, "doctor", "--inventory", inventory, "--json")
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 2 || !strings.Contains(out, "writable by group or others") {
		t.Fatalf("permissions: %s %v", out, err)
	}
}

func TestControllerDoesNotRunLinuxCommands(t *testing.T) {
	home, inventory := controllerFixture(t)
	marker := filepath.Join(home, "called")
	for _, name := range []string{"sshd", "systemctl", "apt", "dnf"} {
		stub := "#!/bin/sh\nprintf called >> '" + marker + "'\nexit 1\n"
		if err := os.WriteFile(filepath.Join(home, "bin", name), []byte(stub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range []string{"doctor", "status"} {
		if out, err := runControllerTest(t, command, "--inventory", inventory, "--json"); err != nil {
			t.Fatalf("%s: %s %v", command, out, err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("Linux-only command was run: %v", err)
	}
}
