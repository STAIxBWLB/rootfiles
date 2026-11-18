package module

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

func TestCheckUsername(t *testing.T) {
	for _, ok := range []string{"alice", "bob_2", "john.doe", "_svc", "a-b"} {
		if err := checkUsername(ok); err != nil {
			t.Errorf("%q should be valid: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Root", "1abc", "a b", "x ALL=(ALL) ALL", "a:b", "a'b", strings.Repeat("a", 33)} {
		if err := checkUsername(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestSudoersPathAvoidsDots(t *testing.T) {
	if got := filepath.Base(sudoersPath("john.doe")); strings.Contains(got, ".") {
		t.Errorf("sudoers file %q would be ignored by #includedir", got)
	}
}

func TestWriteSudoers(t *testing.T) {
	dir := t.TempDir()
	old := sudoersDir
	sudoersDir = dir
	t.Cleanup(func() { sudoersDir = old })

	rc := newRealRC(t)
	if err := writeSudoers(context.Background(), rc, "alice"); err != nil {
		t.Fatalf("writeSudoers: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "alice"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "alice ALL=(ALL) NOPASSWD:ALL\n" {
		t.Errorf("unexpected sudoers content %q", data)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temp file left behind: %v", entries)
	}

	if err := writeSudoers(context.Background(), rc, "bad user"); err == nil {
		t.Error("invalid username must be rejected before touching sudoers")
	}
}

func TestGeneratePassword(t *testing.T) {
	a, err := generatePassword(16)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := generatePassword(16)
	if len(a) != 16 || a == b {
		t.Errorf("unexpected passwords %q, %q", a, b)
	}
	for _, c := range a {
		if !strings.ContainsRune("abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789", c) {
			t.Errorf("unexpected character %q", c)
		}
	}
}

func TestSetPasswordRejectsSeparators(t *testing.T) {
	rc := newDryRunRC(t)
	if err := setPassword(context.Background(), rc, "alice", "x:y"); err == nil {
		t.Error("':' in password must be rejected (chpasswd field separator)")
	}
	if err := setPassword(context.Background(), rc, "alice", "x\nroot:pw"); err == nil {
		t.Error("newline in password must be rejected")
	}
}

func TestExistingGroups(t *testing.T) {
	// Stub the lookup: host groups differ (macOS has no "root" group).
	old := lookupAccountGroup
	t.Cleanup(func() { lookupAccountGroup = old })
	lookupAccountGroup = func(name string) (*user.Group, error) {
		if name == "staff" {
			return &user.Group{Name: name, Gid: "50"}, nil
		}
		return nil, user.UnknownGroupError(name)
	}
	present, missing := existingGroups([]string{"staff", "staff", "rootfiles-no-such-group", ""})
	if len(present) != 1 || present[0] != "staff" {
		t.Errorf("present = %v", present)
	}
	if len(missing) != 1 || missing[0] != "rootfiles-no-such-group" {
		t.Errorf("missing = %v", missing)
	}
}

// A user given no password gets a random one, fed to chpasswd and printed
// once; a given password is used as is. The suffix scheme is gone (#187).
func TestSetPasswordsGeneratesWhenNoneGiven(t *testing.T) {
	bin := t.TempDir()
	fed := filepath.Join(bin, "stdin")
	stub := "#!/bin/sh\nwhile IFS= read -r line; do printf '%s\\n' \"$line\" >> " + fed + "; done\n"
	if err := os.WriteFile(filepath.Join(bin, "chpasswd"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	var out bytes.Buffer
	rc := &RunContext{Config: &config.Config{}, Runner: guardedTestRunner(t, false, slog.New(slog.NewTextHandler(io.Discard, nil))), Out: &out}
	entries := []PasswordEntry{{Username: "alice"}, {Username: "bob", Password: "given-pass"}}
	if err := SetPasswords(context.Background(), rc, entries, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(fed)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || lines[1] != "bob:given-pass" || !strings.HasPrefix(lines[0], "alice:") || len(lines[0]) != len("alice:")+16 {
		t.Fatalf("chpasswd got %q, want a 16-character alice password and bob's given one", lines)
	}
	generated := strings.TrimPrefix(lines[0], "alice:")
	if !strings.Contains(out.String(), "Password set for alice: "+generated) || strings.Contains(out.String(), "given-pass") {
		t.Fatalf("output %q, want alice's password printed once and bob's never", out.String())
	}
}
