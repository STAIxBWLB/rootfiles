package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

func TestNewCheckCmd_Basics(t *testing.T) {
	cmd := newCheckCmd()
	if cmd.Use != "check" {
		t.Errorf("Use = %q, want check", cmd.Use)
	}
	if cmd.RunE == nil {
		t.Error("RunE is nil")
	}
	if f := cmd.Flags().Lookup("verbose"); f == nil {
		t.Error("check is missing --verbose flag")
	} else if f.Shorthand != "v" {
		t.Errorf("verbose shorthand = %q, want v", f.Shorthand)
	}
}

func TestCheck_JSONOutputAndExitCode(t *testing.T) {
	previous := detectSystem
	detectSystem = func() (*config.SystemInfo, error) { return &config.SystemInfo{OS: "ubuntu", Version: "22.04"}, nil }
	t.Cleanup(func() { detectSystem = previous })
	t.Setenv("ROOTFILES_STATE_DIR", t.TempDir())
	root := NewRootCmd("test", "abc")
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"check", "--profile", "base", "-o", "json"})
	err := root.Execute()

	var report checkReport
	if jerr := json.Unmarshal(buf.Bytes(), &report); jerr != nil {
		t.Fatalf("output is not JSON: %v\n%s", jerr, buf.String())
	}
	if report.Profile != "base" || len(report.Modules) != 3 {
		t.Errorf("unexpected report: %+v", report)
	}
	var exitErr *ExitError
	switch {
	case report.Satisfied && err != nil:
		t.Errorf("satisfied check should exit 0, got %v", err)
	case !report.Satisfied && (!errors.As(err, &exitErr) || exitErr.Code != exitDrift):
		t.Errorf("drift should exit %d, got %v", exitDrift, err)
	}
}

func TestCheck_RejectsUnknownOutput(t *testing.T) {
	root := NewRootCmd("test", "abc")
	root.SetArgs([]string{"check", "--profile", "base", "-o", "yaml"})
	if err := root.Execute(); err == nil {
		t.Error("unknown output format should fail")
	}
}

// #45 AC1: after a recorded profile apply with --home-base, check reads the
// kept copy, so it claims provenance (the fingerprint covers home_base, so a
// bare dgx load with /raid/home could not match) and reports the recorded
// target, not the copy path. The module filter is locale: command-level
// check tests run host-safe modules (as TestCheck_JSONOutputAndExitCode does
// with base), and the users check reads the host's fleet sudoers drop-in,
// which a non-root user cannot stat on Ubuntu (/etc/sudoers.d is 0750).
func TestCheck_UsesAppliedCopy(t *testing.T) {
	fingerprint := recordProfileApply(t, "/home")
	root := NewRootCmd("test", "abc")
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"check", "--module", "locale", "-o", "json"})
	err := root.Execute()

	var report checkReport
	if jerr := json.Unmarshal(buf.Bytes(), &report); jerr != nil {
		t.Fatalf("output is not JSON: %v (execute: %v)\n%s", jerr, err, buf.String())
	}
	if report.AppliedConfigSHA256 != fingerprint {
		t.Errorf("applied_config_sha256 = %q, want the recorded %q", report.AppliedConfigSHA256, fingerprint)
	}
	if report.Profile != "dgx" || report.ConfigPath != "" {
		t.Errorf("reported target = %q %q, want the recorded profile dgx", report.Profile, report.ConfigPath)
	}
	if len(report.Modules) != 1 || report.Modules[0].Name != "locale" {
		t.Fatalf("modules = %+v, want only locale", report.Modules)
	}
	if strings.Contains(buf.String(), "applied-config.yaml") {
		t.Error("the copy path leaked into the report")
	}
	var exitErr *ExitError
	switch {
	case report.Satisfied && err != nil:
		t.Errorf("satisfied check should exit 0, got %v", err)
	case !report.Satisfied && (!errors.As(err, &exitErr) || exitErr.Code != exitDrift):
		t.Errorf("drift should exit %d, got %v", exitDrift, err)
	}
}
