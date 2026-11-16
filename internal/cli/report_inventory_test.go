package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/fleet"
	"github.com/STAIxBWLB/rootfiles/internal/module"
	"github.com/STAIxBWLB/rootfiles/internal/tenant"
)

// captureReports renders every report the inventory marks measured - colour
// off, through its host-free fixture - and returns the text each one wrote. A
// report whose fixture stopped working fails here rather than silently dropping
// out of the page.
func captureReports(t *testing.T) map[string]string {
	t.Helper()
	root, _, _ := statusContractFixture(t, "populated")
	writeBackupManifest(t)
	pinReportClock(t)
	// The page records an 80-column budget, and a piped report only wraps when
	// COLUMNS says so; without it a table would be measured unwrapped.
	t.Setenv("COLUMNS", "80")

	// `--config site.yaml` is relative because the fixture root is the working
	// directory: an absolute path would wrap inside a key/value row and make the
	// measured width depend on the length of the temporary directory.
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	// The caller reads the committed page right after this returns, so the
	// working directory goes back here rather than at test cleanup.
	defer func() { _ = os.Chdir(original) }()

	captured := map[string]string{}
	for _, report := range ReportInventory {
		if report.State != ReportMeasured {
			continue
		}
		captured[report.Command] = strings.ReplaceAll(reportText(t, root, report.Command), root, "<fixture>")
	}
	return captured
}

// reportText renders one captured report inside root, through the fixture the
// inventory records for it. Both the page and the layout golden read the same
// helper, so neither can drift from the other.
func reportText(t *testing.T, root, command string, extra ...string) string {
	t.Helper()
	capture := reportCaptures[command]
	if capture.setup != nil {
		capture.setup(t, root)
	}
	return runReportText(t, command, append(append([]string{}, capture.args...), extra...))
}

// pinReportClock freezes the two clocks the captured reports read, so a page
// and a golden do not move with the day.
func pinReportClock(t *testing.T) {
	t.Helper()
	savedNow, savedChecks := checkNow, doctorChecks
	t.Cleanup(func() { checkNow, doctorChecks = savedNow, savedChecks })
	checkNow = func() time.Time { return time.Unix(1700000000, 0) }
	doctorChecks = fixtureDoctorChecks
}

func writeBackupManifest(t *testing.T) {
	t.Helper()
	dir := filepath.Join(os.Getenv("ROOTFILES_STATE_DIR"), "backups", "20261101T090413Z")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(map[string]any{
		"id": "20261101T090413Z",
		"entries": []map[string]any{
			{"path": "/etc/ssh/sshd_config", "kind": "file", "existed": true},
			{"path": "/etc/rootfiles/site.yaml", "kind": "file", "existed": false},
		},
		"note": "fixture apply",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
}

func runReportText(t *testing.T, command string, extra []string) string {
	t.Helper()
	root := NewRootCmd("inventory", "inventory")
	args := strings.Fields(strings.TrimPrefix(command, root.Name()+" "))
	args = append(args, extra...)
	if reportReadsConfig(command) {
		args = append(args, "--config", "site.yaml")
	}
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		if _, ok := err.(*ExitError); !ok {
			t.Fatalf("%s: %v", command, err)
		}
	}
	return out.String()
}

// reportCaptures holds what one captured report needs beyond the shared
// fixture: the arguments that make it print its report instead of a refusal,
// and a setup that adds the inventory, run log, stub binary or replaced seam it
// reads. A command absent here runs with the shared fixture alone.
var reportCaptures = map[string]struct {
	args  []string
	setup func(t *testing.T, root string)
}{
	"rootfiles fleet status":          {args: []string{"--inventory", fleetFixtureInventory, "--all"}, setup: stubFleetStatusReads},
	"rootfiles fleet check":           {args: []string{"--inventory", fleetFixtureInventory, "--all"}, setup: stubFleetCheckReads},
	"rootfiles fleet doctor":          {args: []string{"--inventory", fleetFixtureInventory, "--all"}, setup: stubFleetCheckReads},
	"rootfiles fleet hosts":           {args: []string{"--inventory", fleetFixtureInventory}, setup: writeFleetInventoryFixture},
	"rootfiles fleet targets":         {args: []string{"--inventory", fleetFixtureInventory}, setup: writeFleetInventoryFixture},
	"rootfiles fleet log":             {setup: writeFleetLogFixture},
	"rootfiles fleet ping":            {args: []string{"--inventory", fleetFixtureInventory, "--all"}, setup: stubFleetPing},
	"rootfiles fleet shutdown status": {args: []string{"--inventory", fleetFixtureInventory, "--all"}, setup: stubFleetStatusReads},
	"rootfiles gpu status":            {setup: stubGPUStatus},
	"rootfiles gpu mig status":        {setup: stubMIGStatus},
	"rootfiles user audit":            {setup: stubUserAudit},
	"rootfiles user key list":         {args: []string{"alice"}, setup: stubKeyList},
	"rootfiles tenant list":           {setup: stubTenantReports},
	"rootfiles tenant status":         {setup: stubTenantReports},
	"rootfiles update":                {args: []string{"--check", "--version", "v0.0.1-fixture"}},
}

// fleetFixtureInventory is relative to the fixture root, so the path the page
// records does not depend on this machine's temporary directory.
const fleetFixtureInventory = "fleet.yaml"

// reportReadsConfig marks the reports that take no --config: the fleet reports
// refuse it, a rollback and the timer report read the state directory, shutdown
// status has no target and update reads the release, not a site.
func reportReadsConfig(command string) bool {
	switch {
	case strings.HasPrefix(command, "rootfiles fleet"):
		return false
	case command == "rootfiles rollback", command == "rootfiles schedule status",
		command == "rootfiles shutdown status", command == "rootfiles update":
		return false
	}
	return true
}

// writeFleetInventoryFixture writes the inventory the fleet reads select from.
func writeFleetInventoryFixture(t *testing.T, root string) {
	t.Helper()
	body := "defaults:\n  sudo: nopasswd\n  parallel: 2\n" +
		"hosts:\n" +
		"  gpu01:\n    ssh: gpu01\n    groups: [gpu]\n    config: profiles/dgx.yaml\n" +
		"  gpu02:\n    ssh: gpu02\n    groups: [gpu]\n    config: profiles/dgx.yaml\n" +
		"  web01:\n    ssh: web01\n    groups: [edge]\n"
	if err := os.WriteFile(filepath.Join(root, fleetFixtureInventory), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	// Two hosts name a site config, so the CONFIG column of `fleet hosts` has a
	// real value to render instead of the empty-cell dash.
	if err := os.MkdirAll(filepath.Join(root, "profiles"), 0755); err != nil {
		t.Fatal(err)
	}
	site := "users:\n  home_base: /home\nmodules:\n  system:\n    enabled: true\n"
	if err := os.WriteFile(filepath.Join(root, "profiles", "dgx.yaml"), []byte(site), 0600); err != nil {
		t.Fatal(err)
	}
}

// stubFleetStatusReads answers the read seam with the payloads a status report
// renders, one of them carrying a scheduled shutdown.
func stubFleetStatusReads(t *testing.T, root string) {
	t.Helper()
	writeFleetInventoryFixture(t, root)
	stubFleetReads(t, []fleet.Result{
		{Host: "gpu01", Command: "status", State: "ok", Remote: json.RawMessage(`{"hostname":"gpu01","scheduled_shutdown":{"at":"2026-11-21T09:00:00Z","action":"poweroff"}}`)},
		{Host: "gpu02", Command: "status", State: "ok", Remote: json.RawMessage(`{"hostname":"gpu02"}`)},
		{Host: "web01", Command: "status", State: "error", Reason: "rootfiles is not installed"},
	})
}

// stubFleetCheckReads answers the read seam with the rows a check report counts.
func stubFleetCheckReads(t *testing.T, root string) {
	t.Helper()
	writeFleetInventoryFixture(t, root)
	stubFleetReads(t, []fleet.Result{
		{Host: "gpu01", Command: "check", State: "ok"},
		{Host: "gpu02", Command: "check", State: "error", Reason: "2 module(s) need apply"},
		{Host: "web01", Command: "check", State: "unreachable", Reason: "connect timed out"},
	})
}

// stubFleetPing answers the connectivity report without opening a connection.
func stubFleetPing(t *testing.T, root string) {
	t.Helper()
	writeFleetInventoryFixture(t, root)
	previous := pingFleetFn
	t.Cleanup(func() { pingFleetFn = previous })
	pingFleetFn = func(_ context.Context, _ fleet.SSHRunner, hosts []fleet.NamedHost, _ int, _ string) []fleet.Result {
		results := make([]fleet.Result, 0, len(hosts))
		for _, h := range hosts {
			result := fleet.Result{Host: h.Name, Command: "ping", State: "ok"}
			if h.Name == "web01" {
				result.State = "unreachable"
				result.Reason = "connect timed out"
			}
			results = append(results, result)
		}
		return results
	}
}

// stubFleetReads replaces the read seam for the duration of the capture.
func stubFleetReads(t *testing.T, results []fleet.Result) {
	t.Helper()
	previous := readFleetHost
	t.Cleanup(func() { readFleetHost = previous })
	readFleetHost = func(_ fleet.SSHRunner, _ context.Context, h fleet.NamedHost, _ string) fleet.Result {
		for _, result := range results {
			if result.Host == h.Name {
				return result
			}
		}
		return fleet.Result{Host: h.Name, State: "error", Reason: "no fixture for this host"}
	}
}

// writeFleetLogFixture writes one rollout to the run log `fleet log` reads back.
func writeFleetLogFixture(t *testing.T, _ string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".local", "state", "rootfiles")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	body := `{"time":"2026-11-15T04:30:00Z","command":"apply","hosts":[{"host":"gpu01","state":"ok","exit_code":0}]}` + "\n" +
		`{"time":"2026-11-14T22:05:00Z","command":"update","hosts":[{"host":"web01","state":"error","exit_code":1,"reason":"download failed"}]}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "fleet.jsonl"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

// stubGPUStatus answers the GPU collector with three devices: one measured, one
// assigned, and one a cgroup restriction hides from nvidia-smi.
func stubGPUStatus(t *testing.T, _ string) {
	t.Helper()
	previous := gpuStatusCollector
	t.Cleanup(func() { gpuStatusCollector = previous })
	gpuStatusCollector = func(context.Context, *module.RunContext) (*module.GPUStatusReport, error) {
		return &module.GPUStatusReport{Rows: []module.GPUStatusRow{
			{Index: 0, Model: "NVIDIA A100-SXM4-40GB", MemUsed: "1024", MemTotal: "40960", Util: "12", Assigned: "alice"},
			{Index: 1, Model: "NVIDIA A100-SXM4-40GB", Restricted: true},
			{Index: 2, Model: "NVIDIA A100-SXM4-40GB", MemUsed: "0", MemTotal: "40960", Util: "0", Assigned: "bob"},
		}}, nil
	}
}

// stubMIGStatus answers the MIG collector with the two fixtures the family
// golden already uses.
func stubMIGStatus(t *testing.T, _ string) {
	t.Helper()
	previous := migStatusCollector
	t.Cleanup(func() { migStatusCollector = previous })
	migStatusCollector = func(context.Context, *module.RunContext) ([]module.MIGGPU, error) {
		return layoutMIG(), nil
	}
}

// stubUserAudit answers the audit collector with the three findings it reports.
func stubUserAudit(t *testing.T, _ string) {
	t.Helper()
	previous := userAuditCollector
	t.Cleanup(func() { userAuditCollector = previous })
	userAuditCollector = func(context.Context, *module.RunContext) ([]module.AuditFinding, error) {
		return []module.AuditFinding{
			{User: "alice", Detail: "in users.json but not on this system (rootfiles user restore)"},
			{User: "bob", Detail: "uid 1002 on system, 1042 in users.json (file ownership may be wrong)"},
			{User: "carol", Detail: "account under home_base not tracked in users.json"},
		}, nil
	}
}

// stubKeyList answers the key collector with a long key and a key without a
// comment, so the table has to shorten one cell and dash the other.
func stubKeyList(t *testing.T, _ string) {
	t.Helper()
	previous := keyListCollector
	t.Cleanup(func() { keyListCollector = previous })
	keyListCollector = func(*module.RunContext, string) ([]module.AuthorizedKey, error) {
		return []module.AuthorizedKey{
			{Blob: "AAAAC3NzaC1lZDI1NTE5AAAAIFixtureKeyMaterialForTheGolden", Comment: "alice@laptop"},
			{Blob: "AAAAB3NzaC1yc2EAAAADAQABAAABAQFixtureKeyMaterial"},
		}, nil
	}
}

// stubTenantReports answers the tenant seams: no root-owned data root, no
// container runtime, two specs and the report rows they produce.
func stubTenantReports(t *testing.T, _ string) {
	t.Helper()
	previousEnv, previousList, previousStatus := tenantReportEnv, tenantSpecLister, tenantStatusCollector
	t.Cleanup(func() {
		tenantReportEnv, tenantSpecLister, tenantStatusCollector = previousEnv, previousList, previousStatus
	})
	tenantReportEnv = func(*cobra.Command) (*module.RunContext, *tenant.Manager, error) {
		return nil, &tenant.Manager{}, nil
	}
	tenantSpecLister = func(string) ([]string, error) { return []string{"lab", "ml"}, nil }
	tenantStatusCollector = func(*tenant.Manager, context.Context, []string, bool) (*tenant.StatusReport, error) {
		return &tenant.StatusReport{
			Rows: []tenant.StatusRow{
				{Tenant: "lab", State: "running", SSHPort: 2201, GPUs: "0,1", Spec: "current", Volume: "12G/200G 6%", Image: "ubuntu:24.04"},
				{Tenant: "ml", State: "missing", SSHPort: 2202, GPUs: "-", Spec: "current", Volume: "unmounted/100G", Image: "ubuntu:24.04"},
			},
			Commitment: "volumes commit 300G, 12G allocated, 1.2T free on /srv/tenants",
		}, nil
	}
}

// fixtureDoctorChecks is the finding set the captured doctor report prints, so
// the page does not depend on what this machine's disks and clock are doing.
func fixtureDoctorChecks(context.Context, *module.RunContext) []module.Finding {
	return []module.Finding{{Check: "fixture", Level: module.LevelOK, Detail: "healthy"}}
}

// TestReportInventoryIsMeasured keeps the page from carrying a claim the audit
// cannot repeat: every row names a command that exists and a file that holds it,
// every measured row renders through its fixture, and the issues recorded for it
// are the ones its bytes show.
func TestReportInventoryIsMeasured(t *testing.T) {
	pinInventoryPage(t)
	root := NewRootCmd("dev", "test")
	seen := map[string]bool{}
	for _, report := range ReportInventory {
		if seen[report.Command] {
			t.Errorf("%s: listed twice", report.Command)
		}
		seen[report.Command] = true
		c, _, err := root.Find(strings.Fields(strings.TrimPrefix(report.Command, root.Name()+" ")))
		if err != nil {
			t.Errorf("%s: %v", report.Command, err)
			continue
		}
		if c.CommandPath() != report.Command {
			t.Errorf("%s: resolves to %s", report.Command, c.CommandPath())
		}
		if _, err := os.Stat(report.File); err != nil {
			t.Errorf("%s: names %s: %v", report.Command, report.File, err)
		}
	}
	captured := captureReports(t)
	for _, report := range ReportInventory {
		if report.State == ReportMeasured {
			if _, ok := captured[report.Command]; !ok {
				t.Errorf("%s: measured but not captured", report.Command)
			}
		}
	}
}

// TestLayoutIssuesBite proves the measurement is not vacuous: a report that
// starts with a blank line, runs over 80 columns, leaves a trailing blank line
// or spends two blank lines in a row is named.
func TestLayoutIssuesBite(t *testing.T) {
	if issues := LayoutIssues(" rootfiles fixture\n\n\u25b8 Section\n  ok\n"); len(issues) != 0 {
		t.Fatalf("clean report flagged: %v", issues)
	}
	for _, tc := range []struct{ name, text, want string }{
		{"blank start", "\n\u25b8 Section\n  ok\n", "starts with a blank line"},
		{"no header", "Name: value\n", "no header line"},
		{"width", " rootfiles fixture\n  " + strings.Repeat("x", 80) + "\n", "columns"},
		{"trailing blank", " rootfiles fixture\n  ok\n\n", "trailing blank line"},
		{"no final newline", " rootfiles fixture\n  ok", "no final newline"},
		{"blank run", " rootfiles fixture\n\n\n\u25b8 Section\n  ok\n", "blank line run"},
		{"unspaced section", " rootfiles fixture\n  ok\n\u25b8 Section\n", "no blank line before it"},
		{"escape", " rootfiles fixture\n  \x1b[32mok\x1b[0m\n", "escape sequence"},
		{"no output", "", "no output"},
	} {
		issues := LayoutIssues(tc.text)
		found := false
		for _, issue := range issues {
			if strings.Contains(issue, tc.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: %v does not name %q", tc.name, issues, tc.want)
		}
	}
}
