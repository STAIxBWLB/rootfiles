package module

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

type fleetSudoTestFileInfo struct {
	name string
	mode os.FileMode
	uid  uint32
}

func (i fleetSudoTestFileInfo) Name() string       { return i.name }
func (i fleetSudoTestFileInfo) Size() int64        { return 0 }
func (i fleetSudoTestFileInfo) Mode() os.FileMode  { return i.mode }
func (i fleetSudoTestFileInfo) ModTime() time.Time { return time.Time{} }
func (i fleetSudoTestFileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i fleetSudoTestFileInfo) Sys() any           { return &syscall.Stat_t{Uid: i.uid} }

const fleetSudoTestListing = `User alice may run the following commands on this host:
    (root) NOPASSWD: /usr/local/bin/rootfiles status -o json, /usr/local/bin/rootfiles check -o json, /usr/local/bin/rootfiles doctor -o json
`

func setFleetSudoTestState(t *testing.T, sudoListing string) string {
	t.Helper()
	oldDir, oldBinary, oldLstat, oldLookup := sudoersDir, fleetSudoBinaryPath, fleetSudoLstat, lookupFleetSudoUser
	sudoersDir = t.TempDir()
	fleetSudoBinaryPath = fleetSudoBinaryDefault
	lookupFleetSudoUser = func(name string) (*user.User, error) {
		if name == "missing" {
			return nil, user.UnknownUserError(name)
		}
		return &user.User{Name: name, Uid: "1000", Gid: "1000"}, nil
	}
	trusted := map[string]os.FileInfo{
		"/":                    fleetSudoTestFileInfo{name: "/", mode: os.ModeDir | 0755, uid: 0},
		"/usr":                 fleetSudoTestFileInfo{name: "usr", mode: os.ModeDir | 0755, uid: 0},
		"/usr/local":           fleetSudoTestFileInfo{name: "local", mode: os.ModeDir | 0755, uid: 0},
		"/usr/local/bin":       fleetSudoTestFileInfo{name: "bin", mode: os.ModeDir | 0755, uid: 0},
		fleetSudoBinaryDefault: fleetSudoTestFileInfo{name: "rootfiles", mode: 0755, uid: 0},
	}
	fleetSudoLstat = func(path string) (os.FileInfo, error) {
		if info, ok := trusted[filepath.Clean(path)]; ok {
			return info, nil
		}
		info, err := oldLstat(path)
		if err != nil || filepath.Clean(path) != fleetSudoersPath() || info.Mode()&os.ModeSymlink != 0 {
			return info, err
		}
		return fleetSudoTestFileInfo{name: info.Name(), mode: info.Mode(), uid: 0}, nil
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	writeCommand := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	writeCommand("visudo", "printf 'visudo %s\\n' \"$*\" >> "+logPath+"\nexit 0\n")
	sudoBody := "printf 'sudo %s\\n' \"$*\" >> " + logPath + "\n"
	if sudoListing != "" {
		sudoBody += "cat <<'LISTING'\n" + sudoListing + "LISTING\n"
	}
	writeCommand("sudo", sudoBody+"exit 0\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() {
		sudoersDir, fleetSudoBinaryPath, fleetSudoLstat, lookupFleetSudoUser = oldDir, oldBinary, oldLstat, oldLookup
	})
	return logPath
}

func TestFleetSudoersContentIsExactAndStable(t *testing.T) {
	content, err := fleetSudoersContent([]string{"zoe", "alice"})
	if err != nil {
		t.Fatal(err)
	}
	want := "# Managed by rootfiles-v2\n" +
		"Cmnd_Alias ROOTFILES_FLEET_READONLY = /usr/local/bin/rootfiles status -o json, /usr/local/bin/rootfiles check -o json, /usr/local/bin/rootfiles doctor -o json\n" +
		"alice,zoe ALL=(root) NOPASSWD: ROOTFILES_FLEET_READONLY\n"
	if string(content) != want {
		t.Fatalf("sudoers content = %q, want %q", content, want)
	}
	if strings.Contains(string(content), "*") || strings.Contains(string(content), "--config") {
		t.Fatalf("sudoers content has a wildcard or config flag: %s", content)
	}
	if _, err := fleetSudoersContent([]string{"alice", "alice"}); err == nil {
		t.Fatal("duplicate users must fail")
	}
	if _, err := fleetSudoersContent([]string{"bad user"}); err == nil {
		t.Fatal("invalid usernames must fail")
	}
}

func TestValidateFleetSudoBinaryRejectsUntrustedAncestorsAndFiles(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		mode os.FileMode
		uid  uint32
		want string
	}{
		{name: "writable parent", path: "/usr/local/bin", mode: os.ModeDir | 0777, uid: 0, want: "writable"},
		{name: "symlink parent", path: "/usr/local", mode: os.ModeSymlink | 0777, uid: 0, want: "symlinks"},
		{name: "non-root binary", path: fleetSudoBinaryDefault, mode: 0755, uid: 1000, want: "owned by root"},
		{name: "writable binary", path: fleetSudoBinaryDefault, mode: 0777, uid: 0, want: "writable"},
		{name: "non-executable binary", path: fleetSudoBinaryDefault, mode: 0644, uid: 0, want: "not executable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setFleetSudoTestState(t, "")
			base := fleetSudoLstat
			fleetSudoLstat = func(path string) (os.FileInfo, error) {
				if filepath.Clean(path) == tc.path {
					return fleetSudoTestFileInfo{name: filepath.Base(path), mode: tc.mode, uid: tc.uid}, nil
				}
				return base(path)
			}
			if err := validateFleetSudoBinary(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateFleetSudoBinary error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestUsersModuleEmptyFleetSudoDoesNotTouchUnrelatedSudoers(t *testing.T) {
	setFleetSudoTestState(t, "")
	unrelated := filepath.Join(sudoersDir, "alice")
	if err := os.WriteFile(unrelated, []byte("alice ALL=(ALL) NOPASSWD:ALL\n"), 0440); err != nil {
		t.Fatal(err)
	}
	rc := newRealRC(t)
	check, err := NewUsersModule().Check(context.Background(), rc)
	if err != nil || !check.Satisfied {
		t.Fatalf("Check empty fleet list = %+v, %v; want satisfied", check, err)
	}
	result, err := NewUsersModule().Apply(context.Background(), rc)
	if err != nil || result.Changed {
		t.Fatalf("Apply empty fleet list = %+v, %v; want no change", result, err)
	}
	managed := fleetSudoersPath()
	if err := os.WriteFile(managed, []byte("old rootfiles rule\n"), 0440); err != nil {
		t.Fatal(err)
	}
	check, err = NewUsersModule().Check(context.Background(), rc)
	if err != nil || check.Satisfied {
		t.Fatalf("Check existing rootfiles rule = %+v, %v; want removal", check, err)
	}
	result, err = NewUsersModule().Apply(context.Background(), rc)
	if err != nil || !result.Changed {
		t.Fatalf("Apply removing rootfiles rule = %+v, %v; want changed", result, err)
	}
	if _, err := os.Lstat(managed); !os.IsNotExist(err) {
		t.Fatalf("rootfiles-owned rule remains after empty config: %v", err)
	}
	if got, err := os.ReadFile(unrelated); err != nil || string(got) != "alice ALL=(ALL) NOPASSWD:ALL\n" {
		t.Fatalf("unrelated sudoers changed: %q, %v", got, err)
	}
}

func TestUsersModuleFleetSudoInstallVerifyAndIdempotency(t *testing.T) {
	calls := setFleetSudoTestState(t, fleetSudoTestListing)
	rc := newRealRC(t)
	rc.Config.Users.FleetSudoUsers = []string{"alice"}
	m := NewUsersModule()

	check, err := m.Check(context.Background(), rc)
	if err != nil || check.Satisfied {
		t.Fatalf("initial Check = %+v, %v; want pending install", check, err)
	}
	result, err := m.Apply(context.Background(), rc)
	if err != nil || !result.Changed {
		t.Fatalf("Apply = %+v, %v; want changed", result, err)
	}
	content, err := os.ReadFile(fleetSudoersPath())
	if err != nil {
		t.Fatal(err)
	}
	want, _ := fleetSudoersContent([]string{"alice"})
	if string(content) != string(want) {
		t.Fatalf("installed sudoers = %q, want %q", content, want)
	}
	if mode, err := os.Stat(fleetSudoersPath()); err != nil || mode.Mode().Perm() != 0440 {
		t.Fatalf("installed mode = %v, %v; want 0440", mode, err)
	}
	callData, err := os.ReadFile(calls)
	if err != nil || !strings.Contains(string(callData), "visudo -cf ") || !strings.Contains(string(callData), "sudo -l -U alice") {
		t.Fatalf("expected validation and effect verification calls, got %q, %v", callData, err)
	}
	check, err = m.Check(context.Background(), rc)
	if err != nil || !check.Satisfied {
		t.Fatalf("second Check = %+v, %v; want satisfied", check, err)
	}
	result, err = m.Apply(context.Background(), rc)
	if err != nil || result.Changed {
		t.Fatalf("second Apply = %+v, %v; want idempotent", result, err)
	}
}

func TestUsersModuleFleetSudoMissingUserFails(t *testing.T) {
	setFleetSudoTestState(t, "")
	rc := newDryRunRC(t)
	rc.Config.Users.FleetSudoUsers = []string{"missing"}
	if _, err := NewUsersModule().Check(context.Background(), rc); err == nil {
		t.Fatal("Check must fail when a fleet sudo user is missing and undeclared")
	}
}

func TestUsersModuleFleetSudoDeclaredAccountMayBeCreatedFirst(t *testing.T) {
	setFleetSudoTestState(t, "")
	rc := newDryRunRC(t)
	rc.Config.Users.FleetSudoUsers = []string{"missing"}
	rc.Config.Users.Accounts = []config.AccountConfig{{Name: "missing"}}
	if _, err := NewUsersModule().Check(context.Background(), rc); err != nil {
		t.Fatalf("Check should allow an account declared for the same Apply: %v", err)
	}
}

func TestFleetSudoApplyFailsWhenEffectiveRuleIsAbsent(t *testing.T) {
	setFleetSudoTestState(t, "User alice may run the following commands on this host:\n    (root) PASSWD: /usr/local/bin/rootfiles status -o json\n")
	rc := newRealRC(t)
	rc.Config.Users.FleetSudoUsers = []string{"alice"}
	if _, err := NewUsersModule().Apply(context.Background(), rc); err == nil {
		t.Fatal("Apply must fail when sudo -l does not confirm the NOPASSWD rule")
	}
}

func TestUsersModuleFleetSudoApplyMissingUserFails(t *testing.T) {
	setFleetSudoTestState(t, "")
	rc := newRealRC(t)
	rc.Config.Users.FleetSudoUsers = []string{"missing"}
	if _, err := NewUsersModule().Apply(context.Background(), rc); err == nil {
		t.Fatal("Apply must fail when a fleet sudo user is missing")
	}
	if _, err := os.Lstat(fleetSudoersPath()); !os.IsNotExist(err) {
		t.Fatalf("missing-user failure wrote the sudoers drop-in: %v", err)
	}
}

func TestFleetSudoDropInRejectsSymlink(t *testing.T) {
	setFleetSudoTestState(t, "")
	link := fleetSudoersPath()
	if err := os.Symlink(filepath.Join(sudoersDir, "missing-target"), link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fleetSudoDropInInfo(); err == nil {
		t.Fatal("sudoers drop-in symlink must be rejected")
	}
}

func TestFleetSudoMissingUserLookupErrorIsPropagated(t *testing.T) {
	setFleetSudoTestState(t, "")
	lookupFleetSudoUser = func(string) (*user.User, error) { return nil, errors.New("NSS unavailable") }
	if err := validateFleetSudoUsers([]string{"alice"}, nil, false); err == nil || !strings.Contains(err.Error(), "alice") {
		t.Fatalf("lookup failure = %v, want user-specific error", err)
	}
}
