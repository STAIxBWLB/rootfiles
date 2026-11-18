package cli

import (
	"testing"
)

func TestNewUserCmd_Subcommands(t *testing.T) {
	cmd := newUserCmd()
	if cmd.Use != "user" {
		t.Errorf("Use = %q, want user", cmd.Use)
	}

	// Every subcommand documented in README.md and docs/ must be registered.
	// Drift between this list and the actual subcommands will fail the test,
	// which is the point — the doc drift audit showed these were falling out
	// of sync with the docs.
	want := []string{"add", "list", "backup", "restore", "rehome", "id", "groups", "group-add", "group-del", "passwd"}
	have := make(map[string]bool, len(cmd.Commands()))
	for _, sub := range cmd.Commands() {
		have[sub.Name()] = true
	}
	for _, name := range want {
		if !have[name] {
			t.Errorf("user cmd missing subcommand %q", name)
		}
	}
}

func TestUserGroupAddCmd_RequiresGroupsFlag(t *testing.T) {
	// Find the group-add subcommand via NewRootCmd so persistent flags are present.
	root := NewRootCmd("test", "abc")
	sub, _, err := root.Find([]string{"user", "group-add"})
	if err != nil {
		t.Fatalf("find user group-add: %v", err)
	}
	for _, name := range []string{"groups", "docker", "sudo"} {
		if sub.Flags().Lookup(name) == nil {
			t.Errorf("user group-add missing flag %q", name)
		}
	}
}

func TestUserPasswdCmd_HasBatchFlags(t *testing.T) {
	root := NewRootCmd("test", "abc")
	sub, _, err := root.Find([]string{"user", "passwd"})
	if err != nil {
		t.Fatalf("find user passwd: %v", err)
	}
	for _, name := range []string{"password", "file", "all"} {
		if sub.Flags().Lookup(name) == nil {
			t.Errorf("user passwd missing flag %q", name)
		}
	}
}
