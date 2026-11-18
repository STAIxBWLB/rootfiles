package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/exec"
	"github.com/STAIxBWLB/rootfiles/internal/module"
	"github.com/STAIxBWLB/rootfiles/internal/tenant"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

// TestTenantStatusNamesOvercommittedCapacity keeps the capacity risk in words.
// The warn marker is a glyph a pipe drops, so a reader who never sees colour or
// a UTF-8 locale still has to learn that filling every volume would not fit.
func TestTenantStatusNamesOvercommittedCapacity(t *testing.T) {
	report := func(over bool) *tenant.StatusReport {
		return &tenant.StatusReport{
			Rows: []tenant.StatusRow{{
				Tenant: "lab", State: "running", SSHPort: 2201, GPUs: "-", Spec: "current",
				Volume: "12G/200G 6%", Image: "ubuntu:24.04",
			}},
			Commitment:    "volumes commit 300G, 12G allocated, 1.2T free on /srv/tenants",
			Overcommitted: over,
		}
	}
	for _, tc := range []struct {
		name string
		over bool
	}{
		{"within free space", false},
		{"overcommitted", true},
	} {
		var out bytes.Buffer
		// ASCII decoration, as a script matching a piped page sees it.
		if err := renderTenantStatus(ui.ConfigureWriter(&out, "never", true, false), report(tc.over)); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "volumes commit 300G, 12G allocated, 1.2T free on /srv/tenants") {
			t.Errorf("%s: commitment missing:\n%s", tc.name, out.String())
		}
		if got, want := strings.Contains(out.String(), "overcommitted: volumes commit 300G"), tc.over; got != want {
			t.Errorf("%s: overcommitted label present=%v, want %v:\n%s", tc.name, got, want, out.String())
		}
	}
}

// A failed tenant read keeps its next step under the table (#179).
func TestTenantStatusErrorRowNamesNextStep(t *testing.T) {
	var out bytes.Buffer
	report := &tenant.StatusReport{Rows: []tenant.StatusRow{{
		Tenant: "lab", Error: "error: docker inspect tenant-lab: Cannot connect to the Docker daemon",
		Detail: []string{"lab: run `docker inspect tenant-lab` for the full Docker error"},
	}}}
	if err := renderTenantStatus(ui.ConfigureWriter(&out, "never", true, false), report); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Cannot connect to the Docker daemon", "lab: run `docker inspect tenant-lab`"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q:\n%s", want, out.String())
		}
	}
}

// TestTenantOpsStatusFailureIsAudited keeps a failed collection on the audit
// path: `tenant ops` promises every attempt is recorded, so the status branch
// shares the common record instead of returning early.
func TestTenantOpsStatusFailureIsAudited(t *testing.T) {
	// The action clears every ROOTFILES_* variable before it reads the host's
	// applied config; this test restores the environment it inherited.
	saved := os.Environ()
	t.Cleanup(func() {
		os.Clearenv()
		for _, kv := range saved {
			key, value, _ := strings.Cut(kv, "=")
			_ = os.Setenv(key, value)
		}
	})
	previousAudit, previousEnv, previousStatus := tenant.AuditPath, tenantOpsEnv, tenantStatusCollector
	tenant.AuditPath = filepath.Join(t.TempDir(), "tenant-audit.jsonl")
	t.Cleanup(func() {
		tenant.AuditPath, tenantOpsEnv, tenantStatusCollector = previousAudit, previousEnv, previousStatus
	})

	dir := t.TempDir()
	m := &tenant.Manager{
		Runner:  exec.NewRunner(false, slog.New(slog.NewTextHandler(io.Discard, nil))),
		Root:    dir,
		SpecDir: dir,
		UnitDir: dir,
		Out:     &bytes.Buffer{},
	}
	spec := &tenant.Spec{
		Name: "est", Image: "ubuntu:24.04", SSHPort: 2201,
		Volume: tenant.Volume{Size: "10G"},
		Users:  []tenant.User{{Name: "est", UID: 1000, GID: 1000}},
	}
	if err := m.SaveSpec(spec); err != nil {
		t.Fatal(err)
	}
	tenantOpsEnv = func(*cobra.Command) (*module.RunContext, *tenant.Manager, error) {
		return nil, m, nil
	}
	tenantStatusCollector = func(*tenant.Manager, context.Context, []string, bool) (*tenant.StatusReport, error) {
		return nil, errors.New("fixture collection failed")
	}

	cmd := newTenantOpsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.RunE(cmd, []string{"est", "status"})
	if err == nil || !strings.Contains(err.Error(), "fixture collection failed") {
		t.Fatalf("status failure not returned: %v", err)
	}
	body, readErr := os.ReadFile(tenant.AuditPath)
	if readErr != nil {
		t.Fatalf("failed status left no audit record: %v", readErr)
	}
	if !strings.Contains(string(body), `"result":"error: fixture collection failed"`) {
		t.Fatalf("audit record does not name the failure: %s", body)
	}
	if !strings.Contains(string(body), `"action":"ops status"`) {
		t.Fatalf("audit record does not name the action: %s", body)
	}
}
