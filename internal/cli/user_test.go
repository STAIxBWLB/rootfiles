package cli

import (
	"strings"
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

// user backup names its destination --file; -o/--output keeps working for one
// release and says so.
func TestUserBackupFlagAlias(t *testing.T) {
	cmd := newUserBackupCmd()
	if f := cmd.Flags().Lookup("file"); f == nil {
		t.Fatal("user backup has no --file flag")
	}
	alias := cmd.Flags().Lookup("output")
	if alias == nil || alias.Deprecated == "" || !strings.Contains(alias.Deprecated, "--file") {
		t.Fatalf("--output is not deprecated in favour of --file: %+v", alias)
	}
	if alias.ShorthandDeprecated == "" {
		t.Fatal("-o is not marked deprecated, so its use prints no notice")
	}
}

// The duplicate flags are deprecated with the name of what replaces them, so
// the notice cobra prints is the migration step.
func TestUserFlagDeprecations(t *testing.T) {
	for _, tc := range []struct {
		args        []string
		flag        string
		replacement string
	}{
		{[]string{"user", "add"}, "pubkey", "--ssh-pubkey"},
		{[]string{"user", "passwd"}, "suffix", "--password"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			sub, _, err := NewRootCmd("test", "abc").Find(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			f := sub.Flags().Lookup(tc.flag)
			if f == nil || !strings.Contains(f.Deprecated, tc.replacement) {
				t.Fatalf("%v --%s is not deprecated in favour of %s: %+v", tc.args, tc.flag, tc.replacement, f)
			}
		})
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
	for _, name := range []string{"password", "suffix", "file", "all"} {
		if sub.Flags().Lookup(name) == nil {
			t.Errorf("user passwd missing flag %q", name)
		}
	}
}
