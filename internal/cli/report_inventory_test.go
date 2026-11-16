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

	"github.com/STAIxBWLB/rootfiles/internal/module"
)

// captureReports renders every report the inventory marks measured - colour
// off, through its host-free fixture - and returns the text each one wrote. A
// report whose fixture stopped working fails here rather than silently dropping
// out of the page.
func captureReports(t *testing.T) map[string]string {
	t.Helper()
	root, _, _ := statusContractFixture(t, "populated")
	writeBackupManifest(t)
	savedNow, savedChecks := checkNow, doctorChecks
	t.Cleanup(func() { checkNow, doctorChecks = savedNow, savedChecks })
	checkNow = func() time.Time { return time.Unix(1700000000, 0) }
	doctorChecks = fixtureDoctorChecks

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
		// The fixture's own absolute path is normalized the way the byte
		// contracts do it: a temporary directory's length differs per machine,
		// and this page records a shape, not that path.
		captured[report.Command] = strings.ReplaceAll(runReportText(t, report.Command), root, "<fixture>")
	}
	return captured
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

func runReportText(t *testing.T, command string) string {
	t.Helper()
	root := NewRootCmd("inventory", "inventory")
	args := strings.Fields(strings.TrimPrefix(command, root.Name()+" "))
	if needsConfigFixture(command) {
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

// needsConfigFixture marks the two captured reports that read no target config.
func needsConfigFixture(command string) bool {
	switch command {
	case "rootfiles rollback", "rootfiles schedule status":
		return false
	}
	return true
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
