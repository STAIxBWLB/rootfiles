package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/charmbracelet/lipgloss"
)

func init() {
	// Fix the profile so status golden bytes do not depend on the terminal.
	lipgloss.SetColorProfile(0)
}

func TestNewStatusCmd_Basics(t *testing.T) {
	cmd := newStatusCmd()
	if cmd.Use != "status" {
		t.Errorf("Use = %q, want status", cmd.Use)
	}
	if cmd.RunE == nil {
		t.Error("RunE is nil")
	}
}

func TestStatusCmd_RegisteredOnRoot(t *testing.T) {
	root := NewRootCmd("test", "abc")
	sub, _, err := root.Find([]string{"status"})
	if err != nil {
		t.Fatalf("find status: %v", err)
	}
	if sub.Name() != "status" {
		t.Errorf("subcommand name = %q, want status", sub.Name())
	}
}

func TestStatusCmd_RendersAllSections(t *testing.T) {
	_, site, _ := statusContractFixture(t, "populated")
	root := NewRootCmd("test", "abc")
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"status", "--config", site})

	if err := root.Execute(); err != nil {
		t.Fatalf("status execute: %v", err)
	}

	out := buf.String()
	// Every section header must appear so downstream scripts / readers can
	// count on a stable shape. If a section is dropped, this test fails.
	for _, section := range []string{
		"rootfiles status",
		"▸ System",
		"▸ Profile",
		"▸ Modules",
		"▸ GPU Allocations",
		"▸ Tunnel",
		"▸ Users",
	} {
		if !strings.Contains(out, section) {
			t.Errorf("status output missing section %q\n--- got ---\n%s", section, out)
		}
	}
}

// #45 AC1, AC3: after a recorded profile apply with --home-base, status reads
// the kept copy, so it reports the override home base and claims provenance,
// while config_path stays the recorded one (empty for a profile apply) and
// the copy path never leaks into the report.
func TestStatus_UsesAppliedCopy(t *testing.T) {
	oldDetect := detectSystem
	detectSystem = func() (*config.SystemInfo, error) { return &config.SystemInfo{OS: "ubuntu", Version: "24.04"}, nil }
	t.Cleanup(func() { detectSystem = oldDetect })
	noHostCommands(t)
	base := t.TempDir()
	fingerprint := recordProfileApply(t, base)
	root := NewRootCmd("test", "abc")
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"status", "-o", "json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var r statusReport
	if err := json.Unmarshal(buf.Bytes(), &r); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, buf.String())
	}
	if r.Users.HomeBase != base {
		t.Errorf("home base = %q, want the applied override %q", r.Users.HomeBase, base)
	}
	if r.AppliedConfigSHA256 != fingerprint {
		t.Errorf("applied_config_sha256 = %q, want the recorded %q", r.AppliedConfigSHA256, fingerprint)
	}
	if r.Profile != "dgx" || r.ConfigPath != "" {
		t.Errorf("reported target = %q %q, want the recorded profile dgx", r.Profile, r.ConfigPath)
	}
	if r.ConfigErr != "" || r.HomeBaseAmbiguous {
		t.Errorf("config_error = %q, home_base_ambiguous = %v; want a clean load", r.ConfigErr, r.HomeBaseAmbiguous)
	}
	if strings.Contains(buf.String(), "applied-config.yaml") {
		t.Error("the copy path leaked into the report")
	}
}
