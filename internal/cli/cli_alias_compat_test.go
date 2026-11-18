package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/module"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

func aliasCLIFixture(t *testing.T) {
	t.Helper()
	noHostCommands(t)
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("ROOTFILES_STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv("ROOTFILES_PROFILE", "minimal")
	t.Setenv("ROOTFILES_USER", "")
	t.Setenv("ROOTFILES_SSH_PUBKEY", "")
	t.Setenv("ROOTFILES_TUNNEL_TOKEN", "")
	t.Setenv("ROOTFILES_VLAN_ADDRESS", "")
	t.Setenv("ROOTFILES_HOME_BASE", filepath.Join(dir, "home"))
	oldDetect := detectSystem
	detectSystem = func() (*config.SystemInfo, error) { return &config.SystemInfo{OS: "ubuntu", Version: "24.04"}, nil }
	t.Cleanup(func() { detectSystem = oldDetect })
}

func executeAliasCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var output bytes.Buffer
	root := NewRootCmd("test", "test")
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs(append(args, "--dry-run", "--yes"))
	err := root.Execute()
	return output.String(), err
}

func TestCLIUserAddKeyAliasesAndUsernameFallback(t *testing.T) {
	for _, tc := range []struct {
		name, username, pubkey string
		args                   []string
	}{
		{"canonical", "alice", "canonical-key", []string{"alice", "--ssh-pubkey", "canonical-key"}},
		{"username fallback", "flag-user", "canonical-key", []string{"--user", "flag-user", "--ssh-pubkey", "canonical-key"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aliasCLIFixture(t)
			old := addCLIUser
			calls := 0
			addCLIUser = func(_ context.Context, rc *module.RunContext, username string, keys, groups []string, noDocker bool) error {
				calls++
				if username != tc.username || !reflect.DeepEqual(keys, []string{tc.pubkey}) || !rc.DryRun {
					t.Fatalf("wrong user add inputs: username=%q keys=%q dry=%t", username, keys, rc.DryRun)
				}
				return nil
			}
			t.Cleanup(func() { addCLIUser = old })
			if _, err := executeAliasCLI(t, append([]string{"user", "add"}, tc.args...)...); err != nil || calls != 1 {
				t.Fatalf("user add calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestCLIUserAddWithoutNameNamesCauseAndFix(t *testing.T) {
	aliasCLIFixture(t)
	saved := ui.TerminalAttached
	ui.TerminalAttached = func() bool { return false }
	t.Cleanup(func() { ui.TerminalAttached = saved })
	root := NewRootCmd("test", "test")
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs([]string{"user", "add", "--dry-run"})
	var exit *ExitError
	if err := root.Execute(); !errors.As(err, &exit) || !strings.Contains(exit.Msg, "no terminal") || !strings.Contains(exit.Next, "--user") {
		t.Fatalf("user add without a name or terminal: err=%v", err)
	}
}

func TestCLIUserBackupDestination(t *testing.T) {
	aliasCLIFixture(t)
	want := filepath.Join(t.TempDir(), "users.json")
	old := backupCLIUsers
	got := ""
	backupCLIUsers = func(_ *module.RunContext, output string) error { got = output; return nil }
	t.Cleanup(func() { backupCLIUsers = old })
	if _, err := executeAliasCLI(t, "user", "backup", "--file", want); err != nil || got != want {
		t.Fatalf("user backup destination=%q err=%v, want %q", got, err, want)
	}
}

// The spellings deprecated until v0.22.0 are gone: each is an unknown flag
// (exit 2), never a silent alias (#187).
func TestCLIRemovedSpellingsAreUnknownFlags(t *testing.T) {
	for _, args := range [][]string{
		{"backup", "-o", "/srv/old"},
		{"backup", "--output", "/srv/old"},
		{"user", "backup", "-o", "/root/users.json"},
		{"user", "backup", "--output", "/root/users.json"},
		{"user", "add", "alice", "--pubkey", "fixture-key"},
		{"config", "init", "--force"},
		{"user", "passwd", "alice", "--suffix", "fixture-suffix"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			aliasCLIFixture(t)
			_, err := executeAliasCLI(t, args...)
			var exit *ExitError
			if !errors.As(err, &exit) || exit.Code != 2 || !strings.Contains(exit.Msg, "unknown") {
				t.Fatalf("err = %v, want an unknown-flag usage error", err)
			}
		})
	}
}

func TestCLITunnelInstallAndSetupPreserveConfigAndOverrides(t *testing.T) {
	for _, tc := range []struct {
		name, command, configToken, wantToken string
		tokenFile                             bool
		args                                  []string
		vlan                                  string
	}{
		{"setup token file", "setup", "", "", true, nil, "192.0.2.1/32"},
		{"install token file", "install", "", "", true, nil, "192.0.2.1/32"},
		{"install configured token", "install", "configured-token", "configured-token", false, nil, "192.0.2.1/32"},
		{"setup positional wins", "setup", "configured-token", "positional-token", false, []string{"positional-token", "--tunnel-token", "flag-token"}, "192.0.2.1/32"},
		{"install accepts token", "install", "configured-token", "positional-token", false, []string{"positional-token"}, "192.0.2.1/32"},
		{"install flag override", "install", "configured-token", "flag-token", false, []string{"--tunnel-token", "flag-token", "--vlan-address", "203.0.113.10/32"}, "203.0.113.10/32"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aliasCLIFixture(t)
			dir := t.TempDir()
			tokenFile := filepath.Join(dir, "tunnel-token")
			if err := os.WriteFile(tokenFile, []byte("private-file-token"), 0600); err != nil {
				t.Fatal(err)
			}
			credential := "tunnel_token: " + tc.configToken
			if tc.tokenFile {
				credential = "tunnel_token_file: " + tokenFile
			}
			site := filepath.Join(dir, "site.yaml")
			content := "extends: minimal\nmodules:\n  cloudflared:\n    enabled: true\n    " + credential + "\n    private_network:\n      enabled: true\n      interface: vlan0\n      address: 192.0.2.1/32\n"
			if err := os.WriteFile(site, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			old := applyCloudflaredSetup
			calls := 0
			applyCloudflaredSetup = func(_ context.Context, rc *module.RunContext) (*module.ApplyResult, error) {
				calls++
				got := rc.Config.Modules.Cloudflared
				if got.TunnelToken != tc.wantToken || got.PrivateNetwork.Address != tc.vlan || !got.PrivateNetwork.Enabled {
					t.Fatalf("wrong tunnel inputs: token=%q vlan=%q enabled=%t", got.TunnelToken, got.PrivateNetwork.Address, got.PrivateNetwork.Enabled)
				}
				if tc.tokenFile && got.TunnelTokenFile != tokenFile {
					t.Fatalf("configured token file dropped: %q", got.TunnelTokenFile)
				}
				return &module.ApplyResult{}, nil
			}
			t.Cleanup(func() { applyCloudflaredSetup = old })
			args := append([]string{"tunnel", tc.command}, tc.args...)
			args = append(args, "--config", site)
			printed, err := executeAliasCLI(t, args...)
			if err != nil || calls != 1 {
				t.Fatalf("tunnel calls=%d err=%v", calls, err)
			}
			for _, secret := range []string{"private-file-token", "configured-token", "positional-token", "flag-token"} {
				if strings.Contains(printed, secret) {
					t.Fatalf("setup output exposed credential %q", secret)
				}
			}
			if !strings.Contains(printed, "already current") {
				t.Fatalf("converged setup reported changes: %q", printed)
			}
		})
	}
}

func TestCLICommandAliasesKeepMutationGateAndReaders(t *testing.T) {
	for _, pair := range [][3]string{{"tenant", "passwd", "reset"}, {"tunnel", "setup", "install"}} {
		t.Run(pair[0], func(t *testing.T) {
			root := NewRootCmd("test", "test")
			canonical, _, err := root.Find([]string{pair[0], pair[1]})
			if err != nil {
				t.Fatal(err)
			}
			alias, _, err := root.Find([]string{pair[0], pair[2]})
			if err != nil || alias != canonical || alias.Annotations[annotMutates] != "true" {
				t.Fatalf("alias lost canonical command/mutation gate: %v", err)
			}
			parent, _, _ := root.Find([]string{pair[0]})
			count := 0
			for _, cmd := range parent.Commands() {
				if cmd.Name() == pair[1] || cmd.Name() == pair[2] {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("registered %d duplicate commands", count)
			}
			for _, flag := range []string{"profile", "config", "home-base"} {
				if !commandReadsRootFlag(alias, flag) {
					t.Fatalf("alias lost --%s reader", flag)
				}
			}
			if pair[0] == "tunnel" {
				for _, flag := range []string{"tunnel-token", "vlan-address"} {
					if !commandReadsRootFlag(alias, flag) {
						t.Fatalf("install alias lost --%s reader", flag)
					}
				}
			}
		})
	}
}

func commandReadsRootFlag(cmd *cobra.Command, flag string) bool {
	for current := cmd; current != nil; current = current.Parent() {
		if current.Annotations[annotReadsRootFlag+flag] == "true" {
			return true
		}
	}
	return false
}

func TestCLIUserAddKeepsInheritedPrefixInputs(t *testing.T) {
	aliasCLIFixture(t)
	old := addCLIUser
	calls := 0
	addCLIUser = func(_ context.Context, _ *module.RunContext, username string, keys, groups []string, noDocker bool) error {
		calls++
		if username != "alice" || !reflect.DeepEqual(keys, []string{"prefix-key"}) {
			t.Fatalf("inherited prefix inputs were shadowed: username=%q keys=%q", username, keys)
		}
		return nil
	}
	t.Cleanup(func() { addCLIUser = old })
	if printed, err := executeAliasCLI(t, "--ssh-pubkey", "prefix-key", "--user", "alice", "user", "add"); err != nil || calls != 1 {
		t.Fatalf("prefix user add calls=%d err=%v output=%q", calls, err, printed)
	}
}

func TestCLIAliasesStillRejectUnsupportedRockyMutations(t *testing.T) {
	for _, args := range [][]string{{"tunnel", "install"}, {"tenant", "reset", "demo", "alice"}} {
		t.Run(strings.Join(args[:2], " "), func(t *testing.T) {
			aliasCLIFixture(t)
			t.Setenv("ROOTFILES_PROFILE", "rocky")
			detectSystem = func() (*config.SystemInfo, error) { return &config.SystemInfo{OS: "rocky", Version: "9.5"}, nil }
			old := applyCloudflaredSetup
			applyCloudflaredSetup = func(context.Context, *module.RunContext) (*module.ApplyResult, error) {
				t.Fatal("unsupported alias reached cloudflared mutation")
				return nil, nil
			}
			t.Cleanup(func() { applyCloudflaredSetup = old })
			_, err := executeAliasCLI(t, args...)
			if err == nil || !strings.Contains(err.Error(), "unsupported") {
				t.Fatalf("unsupported alias was admitted: %v", err)
			}
			stateDir := os.Getenv("ROOTFILES_STATE_DIR")
			if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
				t.Fatalf("unsupported alias wrote state: %v", err)
			}
		})
	}
}
