package cli

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/fleet"
	"github.com/STAIxBWLB/rootfiles/internal/module"
)

func TestStatusTextRendersCollectedSnapshotWithoutQueries(t *testing.T) {
	dir, site, _ := statusContractFixture(t, "populated")
	calls := filepath.Join(dir, "probe-calls")
	for _, name := range []string{"systemctl", "ip", "dpkg-query", "cloudflared"} {
		p := filepath.Join(dir, "bin", name)
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.Replace(body, []byte("#!/bin/sh\n"), []byte("#!/bin/sh\nprintf '%s\\n' "+fleet.ShellQuote(name)+" >> "+fleet.ShellQuote(calls)+"\n"), 1)
		if err := os.WriteFile(p, body, 0755); err != nil {
			t.Fatal(err)
		}
	}
	scans := 0
	scan := scanStatusUsers
	scanStatusUsers = func(ctx context.Context, rc *module.RunContext) ([]module.UserMeta, error) {
		scans++
		return scan(ctx, rc)
	}
	sys, _ := detectSystem()
	cfg, err := config.LoadWithHomeBase("", site, sys, "")
	if err != nil {
		t.Fatal(err)
	}
	rc := readOnlyRunContext(cfg, sys, slog.New(slog.NewTextHandler(io.Discard, nil)))
	report := collectStatus(context.Background(), rc, sys, "", site, nil, nil)
	before, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if scans != 1 {
		t.Fatalf("system users scanned %d times", scans)
	}
	// The snapshot, rather than a renderer's fresh queries, must be displayed.
	for _, name := range []string{"users.json", "gpu-allocations.json"} {
		if err := os.WriteFile(filepath.Join(dir, "home", ".rootfiles", name), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", t.TempDir())
	if err := os.Remove(cloudflaredStatusBinary); err != nil {
		t.Fatal(err)
	}
	var text bytes.Buffer
	renderStatusText(&text, report, "", nil)
	for _, want := range []string{"2 GPU(s) across 2 user(s)", "2 user(s)", "cloudflared version 2026.11.10 <fixture>&\"quoted\"", "203.0.113.7/32 on fixture0"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("collected value %q missing:\n%s", want, text.String())
		}
	}
	for _, entry := range report.Modules {
		if !strings.Contains(text.String(), "  "+entry.Name+"\n") {
			t.Errorf("JSON module %q absent from text snapshot", entry.Name)
		}
	}
	after, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || scans != 1 {
		t.Fatal("text rendering repeated a host query")
	}
}

func TestStatusTextSharesUnsupportedCapabilityReport(t *testing.T) {
	for _, filter := range []string{"", "locale"} {
		t.Run("filter="+filter, func(t *testing.T) {
			_, site, _ := statusContractFixture(t, "unsupported-os")
			root := NewRootCmd("status-contract", "fixture")
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(io.Discard)
			args := []string{"status", "--config", site, "-o", "text"}
			if filter != "" {
				args = append(args, "--module", filter)
			}
			root.SetArgs(args)
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "Modules (check failed)") || !strings.Contains(out.String(), "unsupported operating system") || !strings.Contains(out.String(), "Use a supported distribution") {
				t.Fatalf("text lost the collector's capability finding/remedy:\n%s", out.String())
			}
			if strings.Contains(out.String(), "module(s) need attention") || strings.Contains(out.String(), "Modules (none enabled)") {
				t.Fatal("unsupported capability was hidden or modules were evaluated")
			}
		})
	}
}
