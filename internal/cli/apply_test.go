package cli

import (
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
