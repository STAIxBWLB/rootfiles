package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fleetLogFixture writes a run log under a temporary XDG state dir and returns
// the stored lines, so assertions can compare --json output byte for byte.
func fleetLogFixture(t *testing.T, lines []string) {
	t.Helper()
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	dir := filepath.Join(state, "rootfiles")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "fleet.jsonl"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

// runFleetLog executes `fleet log` with args and returns stdout and stderr.
func runFleetLog(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := NewRootCmd("test", "test")
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(append([]string{"fleet", "log"}, args...))
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func historyRecord(t *testing.T, at time.Time, command string, hosts ...string) string {
	t.Helper()
	results := make([]map[string]any, 0, len(hosts))
	for _, host := range hosts {
		results = append(results, map[string]any{
			"host": host, "command": command, "state": "ok", "exit_code": 0,
		})
	}
	line, err := json.Marshal(map[string]any{
		"time": at.UTC(), "operator": "operator", "command": command, "hosts": results,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(line)
}

// AC1: `fleet log --json` reprints the stored records unchanged, newest first.
func TestFleetLogJSONReprintsStoredLines(t *testing.T) {
	old := historyRecord(t, time.Now().Add(-72*time.Hour), "apply", "web1", "web2")
	recent := historyRecord(t, time.Now().Add(-2*time.Hour), "update", "web1")
	fleetLogFixture(t, []string{old, recent})

	out, errOut, err := runFleetLog(t, "--json")
	if err != nil {
		t.Fatalf("fleet log --json: %v", err)
	}
	if errOut != "" {
		t.Fatalf("stderr = %q, want none", errOut)
	}
	if got, want := out, recent+"\n"+old+"\n"; got != want {
		t.Fatalf("--json output is not the stored bytes, newest first:\n got %s\nwant %s", got, want)
	}
}

// AC1: the text report is newest first and filters narrow it by host and by
// operation.
func TestFleetLogTextOrderAndFilters(t *testing.T) {
	old := historyRecord(t, time.Now().Add(-72*time.Hour), "apply", "web1", "web2")
	recent := historyRecord(t, time.Now().Add(-2*time.Hour), "update", "web1")
	failed, err := json.Marshal(map[string]any{
		"time": time.Now().Add(-48 * time.Hour).UTC(), "operator": "operator", "command": "apply",
		"hosts": []map[string]any{{
			"host": "web3", "command": "apply", "state": "error",
			"reason": "module apply failed", "exit_code": 1,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	fleetLogFixture(t, []string{old, recent, string(failed)})

	out, _, err := runFleetLog(t)
	if err != nil {
		t.Fatalf("fleet log: %v", err)
	}
	// A failing row is why the reason column exists; an all-empty column is
	// dropped by the table renderer, as in every other report here.
	if !strings.Contains(out, "TIME") || !strings.Contains(out, "REASON") || !strings.Contains(out, "module apply failed") {
		t.Fatalf("text report is not the run table with reasons:\n%s", out)
	}
	update, apply := strings.Index(out, "update"), strings.Index(out, "apply")
	if update < 0 || apply < 0 || update > apply {
		t.Fatalf("runs are not newest first:\n%s", out)
	}
	if strings.Count(out, "web2") != 1 {
		t.Fatalf("every host row prints once:\n%s", out)
	}

	only, _, err := runFleetLog(t, "--host", "web2")
	if err != nil {
		t.Fatalf("fleet log --host web2: %v", err)
	}
	if !strings.Contains(only, "web2") || strings.Contains(only, "web1") || strings.Contains(only, "update") {
		t.Fatalf("--host filter leaked other rows:\n%s", only)
	}

	byOp, _, err := runFleetLog(t, "--op", "update")
	if err != nil {
		t.Fatalf("fleet log --op update: %v", err)
	}
	if !strings.Contains(byOp, "update") || strings.Contains(byOp, "apply") {
		t.Fatalf("--op filter leaked another operation:\n%s", byOp)
	}

	limited, _, err := runFleetLog(t, "--limit", "1")
	if err != nil {
		t.Fatalf("fleet log --limit 1: %v", err)
	}
	if !strings.Contains(limited, "update") || strings.Contains(limited, "apply") {
		t.Fatalf("--limit 1 kept more than the newest run:\n%s", limited)
	}
}

// --since takes the windows an operator reads a log by: hours, whole days, and
// a bare date.
func TestFleetLogSinceWindows(t *testing.T) {
	old := historyRecord(t, time.Now().Add(-5*24*time.Hour), "apply", "web1")
	recent := historyRecord(t, time.Now().Add(-2*time.Hour), "update", "web1")
	fleetLogFixture(t, []string{old, recent})

	for _, tc := range []struct {
		since string
		want  []string
	}{
		{"24h", []string{"update"}},
		{"2d", []string{"update"}},
		{"7d", []string{"update", "apply"}},
	} {
		out, _, err := runFleetLog(t, "--since", tc.since)
		if err != nil {
			t.Fatalf("fleet log --since %s: %v", tc.since, err)
		}
		for _, want := range tc.want {
			if !strings.Contains(out, want) {
				t.Fatalf("--since %s dropped %q:\n%s", tc.since, want, out)
			}
		}
		if len(tc.want) == 1 && strings.Contains(out, "apply") {
			t.Fatalf("--since %s kept a run outside the window:\n%s", tc.since, out)
		}
	}

	past := time.Now().Add(-30 * 24 * time.Hour).Format("2006-01-02")
	out, _, err := runFleetLog(t, "--since", past)
	if err != nil {
		t.Fatalf("fleet log --since %s: %v", past, err)
	}
	if !strings.Contains(out, "update") {
		t.Fatalf("a date before both runs kept neither:\n%s", out)
	}

	if _, _, err := runFleetLog(t, "--since", "yesterday-ish"); err == nil || !strings.Contains(err.Error(), "neither a duration nor a date") {
		t.Fatalf("unreadable --since accepted: %v", err)
	}
	if _, _, err := runFleetLog(t, "--since", "-24h"); err == nil || !strings.Contains(err.Error(), "forward in time") {
		t.Fatalf("negative --since accepted: %v", err)
	}
	if _, _, err := runFleetLog(t, "--limit", "-1"); err == nil || !strings.Contains(err.Error(), "--limit must be 0 or greater") {
		t.Fatalf("negative --limit accepted: %v", err)
	}
}

// A missing log is an empty result, and a corrupt line is named and skipped so
// one bad write cannot hide the rest of the file.
func TestFleetLogMissingAndCorruptLog(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)

	out, errOut, err := runFleetLog(t)
	if err != nil {
		t.Fatalf("missing log returned an error: %v", err)
	}
	if out != " rootfiles fleet log\n  no recorded rollouts\n" || errOut != "" {
		t.Fatalf("missing log printed %q / %q, want a header and one empty-log hint", out, errOut)
	}

	dir := filepath.Join(state, "rootfiles")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	good := historyRecord(t, time.Now().Add(-time.Hour), "apply", "web1")
	body := good + "\n" + `{"time":` + "\n" + historyRecord(t, time.Now().Add(-2*time.Hour), "update", "web1") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "fleet.jsonl"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}

	out, errOut, err = runFleetLog(t)
	if err != nil {
		t.Fatalf("corrupt line failed the command: %v", err)
	}
	if !strings.Contains(errOut, "line 2") {
		t.Fatalf("stderr does not name the corrupt line: %q", errOut)
	}
	if !strings.Contains(out, "apply") || !strings.Contains(out, "update") {
		t.Fatalf("the good lines were dropped:\n%s", out)
	}
}

// fleet log reads the controller's own file: it opens no SSH and needs no
// inventory, so a PATH holding no tools must still work.
func TestFleetLogNeedsNoSSHOrInventory(t *testing.T) {
	fleetLogFixture(t, []string{historyRecord(t, time.Now(), "apply", "web1")})
	empty := t.TempDir()
	t.Setenv("PATH", empty)
	out, _, err := runFleetLog(t)
	if err != nil {
		t.Fatalf("fleet log needed a tool it should not: %v", err)
	}
	if !strings.Contains(out, "web1") {
		t.Fatalf("fleet log printed no run:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(empty, "ssh")); !os.IsNotExist(err) {
		t.Fatal("the test wrote an ssh stub")
	}
}
