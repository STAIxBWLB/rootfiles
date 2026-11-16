package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/fleet"
	"github.com/STAIxBWLB/rootfiles/internal/module"
	"github.com/STAIxBWLB/rootfiles/internal/tenant"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

func TestGPUAllocationRowsKeepMarkerAndGutter(t *testing.T) {
	_, site, _ := statusContractFixture(t, "populated")
	root := NewRootCmd("test", "test")
	var out, stderr bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&stderr)
	root.SetArgs([]string{"status", "--config", site, "--color=never"})
	if err := executeCommand(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"  *  alice  0\n", "  *  bob  1\n"} {
		if !strings.Contains(out.String(), line) {
			t.Fatalf("missing allocation row %q:\n%s", line, out.String())
		}
	}
}

func TestTenantAuditWarningUsesCommandErrorPolicy(t *testing.T) {
	old := tenant.AuditPath
	tenant.AuditPath = filepath.Join(t.TempDir(), "missing-parent", "audit.jsonl")
	t.Cleanup(func() { tenant.AuditPath = old })
	root := NewRootCmd("test", "test")
	var out, stderr bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&stderr)
	if err := root.ParseFlags([]string{"--color=never", "--ascii"}); err != nil {
		t.Fatal(err)
	}
	if err := configureOutput(root); err != nil {
		t.Fatal(err)
	}
	recordTenant(root, "fixture", "status", nil)
	// A piped stderr keeps the plain warning line: the marker is TTY-only, so
	// a script matching "rootfiles: " keeps working.
	if !strings.HasPrefix(stderr.String(), "rootfiles: warning: tenant audit log:") || strings.ContainsRune(stderr.String(), 27) || strings.Contains(stderr.String(), "⚠") {
		t.Fatalf("tenant warning bypasses command policy: %q", stderr.String())
	}
	if out.Len() != 0 {
		t.Fatalf("warning polluted stdout: %q", out.String())
	}
}

func TestStatusMetadataWarningUsesCommandErrorPolicy(t *testing.T) {
	_, site, _ := statusContractFixture(t, "populated")
	previous := warnStatusMetadata
	warnStatusMetadata = func(rc *module.RunContext) {
		if rc.Err == nil {
			t.Fatal("status did not supply its command error writer")
		}
		ui.WriteText(rc.Err, ui.MarkWarn+" untrusted rootfiles metadata fixture\n")
	}
	t.Cleanup(func() { warnStatusMetadata = previous })
	root := NewRootCmd("test", "test")
	var out, stderr bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&stderr)
	root.SetArgs([]string{"status", "--config", site, "--color=never", "--ascii", "--json"})
	if err := executeCommand(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stderr.String(), "! untrusted rootfiles metadata") || strings.ContainsRune(stderr.String(), 27) || strings.Contains(stderr.String(), "⚠") {
		t.Fatalf("users warning bypasses command policy: %q", stderr.String())
	}
	if !strings.HasPrefix(out.String(), "{") {
		t.Fatalf("warning polluted machine stdout: %q", out.String())
	}
}

type fleetErrorWriter struct{ err error }

func (w fleetErrorWriter) Write([]byte) (int, error) { return 0, w.err }

func TestFleetResultsReturnOutputErrors(t *testing.T) {
	sentinel := errors.New("fixture output failure")
	for _, format := range []string{"text", "json"} {
		err := writeFleetResults(fleetErrorWriter{sentinel}, []fleet.Result{{Host: "fixture", State: "ok", Reason: "healthy"}}, format)
		if !errors.Is(err, sentinel) {
			t.Fatalf("%s output error=%v", format, err)
		}
	}
}

func TestRootVerboseEnablesReportDiagnostics(t *testing.T) {
	for _, command := range []string{"status", "doctor"} {
		for _, verbose := range []bool{false, true} {
			t.Run(command+fmt.Sprint(verbose), func(t *testing.T) {
				fixture, site, _ := statusContractFixture(t, "populated")
				bin := os.Getenv("PATH")
				if !strings.HasPrefix(bin, fixture+string(os.PathSeparator)) || strings.ContainsRune(bin, os.PathListSeparator) {
					t.Fatalf("unexpected fixture PATH: %q", bin)
				}
				if err := os.WriteFile(filepath.Join(bin, "diagnostic-probe"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
					t.Fatal(err)
				}
				previous := doctorChecks
				doctorChecks = func(ctx context.Context, rc *module.RunContext) []module.Finding {
					if _, err := rc.Runner.Query(ctx, "diagnostic-probe"); err != nil {
						t.Fatal(err)
					}
					return []module.Finding{{Check: "probe", Level: module.LevelOK, Detail: "healthy"}}
				}
				t.Cleanup(func() { doctorChecks = previous })
				root := NewRootCmd("test", "test")
				var out, stderr bytes.Buffer
				root.SetOut(&out)
				root.SetErr(&stderr)
				args := []string{command, "--config", site, "--json"}
				if verbose {
					args = append([]string{"--verbose"}, args...)
				}
				root.SetArgs(args)
				if err := executeCommand(context.Background(), root); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(stderr.String(), "level=INFO") != verbose {
					t.Fatalf("verbose=%v diagnostics=%q", verbose, stderr.String())
				}
				if !strings.HasPrefix(out.String(), "{") {
					t.Fatalf("diagnostics polluted JSON: %q", out.String())
				}
			})
		}
	}
}
