package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

func TestNewApplyCmd_Basics(t *testing.T) {
	cmd := newApplyCmd()
	if cmd.Use != "apply" {
		t.Errorf("Use = %q, want apply", cmd.Use)
	}
	if cmd.RunE == nil {
		t.Error("RunE is nil")
	}
}

func TestApplyCmd_InheritsRootFlags(t *testing.T) {
	root := NewRootCmd("test", "abc")
	apply, _, err := root.Find([]string{"apply"})
	if err != nil {
		t.Fatalf("find apply: %v", err)
	}
	// Apply should inherit persistent flags from root.
	for _, name := range []string{"yes", "dry-run", "force", "profile", "module", "config"} {
		if f := apply.Flags().Lookup(name); f == nil {
			if f = apply.InheritedFlags().Lookup(name); f == nil {
				t.Errorf("apply missing persistent flag %q", name)
			}
		}
	}
}

func TestSecurityDowngrades(t *testing.T) {
	profile := &config.Config{
		SSH:     config.SSHConfig{DisableRootLogin: true, DisablePasswordAuth: true},
		Modules: config.ModulesConfig{Network: config.NetworkConfig{UFW: true}},
	}

	same := *profile
	if d := securityDowngrades(profile, &same); len(d) != 0 {
		t.Errorf("unchanged config should have no downgrades, got %v", d)
	}

	weaker := *profile
	weaker.SSH.DisableRootLogin = false
	weaker.SSH.DisablePasswordAuth = false
	weaker.Modules.Network.UFW = false
	weaker.Users.SudoNopasswd = true
	if d := securityDowngrades(profile, &weaker); len(d) != 4 {
		t.Errorf("expected 4 downgrades, got %v", d)
	}

	// Tightening a setting is never a downgrade.
	loose := &config.Config{}
	if d := securityDowngrades(loose, profile); len(d) != 0 {
		t.Errorf("stricter config should have no downgrades, got %v", d)
	}
}

func TestApplyDryRunUnknownNTPDoesNotFail(t *testing.T) {
	oldDetect := detectSystem
	detectSystem = func() (*config.SystemInfo, error) { return &config.SystemInfo{OS: "ubuntu", Version: "24.04"}, nil }
	t.Cleanup(func() { detectSystem = oldDetect })
	for _, installed := range []bool{false, true} {
		name := "pending packages"
		if installed {
			name = "packages already installed"
		}
		t.Run(name, func(t *testing.T) {
			noHostCommands(t)
			dir := t.TempDir()
			timedatectl := "#!/bin/sh\necho 'fixture has no systemd bus' >&2\nexit 1\n"
			if err := os.WriteFile(filepath.Join(dir, "timedatectl"), []byte(timedatectl), 0755); err != nil {
				t.Fatal(err)
			}
			query := "#!/bin/sh\nexit 1\n"
			if installed {
				query = "#!/bin/sh\nshift 2\nfor pkg do printf '%s install ok installed\\n' \"$pkg\"; done\n"
			}
			if err := os.WriteFile(filepath.Join(dir, "dpkg-query"), []byte(query), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			t.Setenv("ROOTFILES_STATE_DIR", t.TempDir())
			file := filepath.Join(t.TempDir(), "site.yaml")
			contents := "users:\n  home_base: " + t.TempDir() + "\nmodules:\n  security:\n    enabled: true\n    time_sync: true\n"
			if err := os.WriteFile(file, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			root := NewRootCmd("test", "test")
			var output bytes.Buffer
			root.SetOut(&output)
			root.SetErr(&output)
			root.SetArgs([]string{"apply", "--config", file, "--module", "security", "--dry-run", "--yes"})
			if err := root.Execute(); err != nil {
				t.Fatalf("warning-only NTP failure blocks apply --dry-run: %v\n%s", err, output.String())
			}
		})
	}
}
