package fleet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// historyFixture writes a run log that mixes operations, hosts and times, with
// a corrupt line in the middle and an unknown field on one record.
func historyFixture(t *testing.T) (string, []string) {
	t.Helper()
	lines := []string{
		`{"time":"2026-11-10T10:00:00Z","operator":"yj","command":"apply","hosts":[{"host":"web1","command":"apply","state":"ok","exit_code":0},{"host":"web2","command":"apply","state":"error","reason":"boom","exit_code":1}]}`,
		`{"time":"2026-11-12T08:00:00Z","operator":"yj","command":"update","hosts":[{"host":"web1","command":"update","state":"apply-pending","exit_code":0}]}`,
		`{"time":"2026-11-11T09:30:00Z","operator":"yj","command":"targets --push 10.0.0.1:9100","hosts":[{"host":"web3","command":"targets","state":"ok","exit_code":0}],"note":"written by a newer rootfiles"}`,
		`{"time":`,
		`{"time":"2026-11-13T00:00:00Z","operator":"yj","command":"bootstrap","hosts":[{"host":"web2","command":"bootstrap","state":"ok","exit_code":0}]}`,
	}
	path := filepath.Join(t.TempDir(), "fleet.jsonl")
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path, lines
}

func historyHosts(entries []HistoryEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Record.Command)
	}
	return out
}

// AC1: the run log is read newest first, filters narrow it, and the stored
// bytes survive for --json. A corrupt line is reported and skipped.
func TestReadFleetHistoryFiltersAndOrder(t *testing.T) {
	path, lines := historyFixture(t)
	since, err := time.Parse(time.RFC3339, "2026-11-11T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		filters HistoryFilters
		want    []string
	}{
		{"all newest first", HistoryFilters{}, []string{"bootstrap", "update", "targets --push 10.0.0.1:9100", "apply"}},
		{"host web1", HistoryFilters{Host: "web1"}, []string{"update", "apply"}},
		{"host web2", HistoryFilters{Host: "web2"}, []string{"bootstrap", "apply"}},
		{"op apply", HistoryFilters{Op: "apply"}, []string{"apply"}},
		{"op matches the command's first word", HistoryFilters{Op: "targets"}, []string{"targets --push 10.0.0.1:9100"}},
		{"since", HistoryFilters{Since: since}, []string{"bootstrap", "update", "targets --push 10.0.0.1:9100"}},
		{"limit keeps the newest", HistoryFilters{Limit: 2}, []string{"bootstrap", "update"}},
		{"unknown host matches nothing", HistoryFilters{Host: "web9"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries, warnings, err := ReadFleetHistory(path, tc.filters)
			if err != nil {
				t.Fatal(err)
			}
			if got := historyHosts(entries); strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("order = %v, want %v", got, tc.want)
			}
			// The fixture's one bad line is reported whatever the filter keeps.
			if len(warnings) != 1 || !strings.Contains(warnings[0], "line 4") {
				t.Fatalf("warnings = %v, want one naming line 4", warnings)
			}
		})
	}

	entries, _, err := ReadFleetHistory(path, HistoryFilters{Op: "targets"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("targets filter returned %d entries, want 1", len(entries))
	}
	// Byte identity is the contract behind --json: a field this build does not
	// know must survive, which re-encoding a decoded record would drop.
	if got, want := string(entries[0].Line), lines[2]; got != want {
		t.Fatalf("stored line changed:\n got %s\nwant %s", got, want)
	}
}

func TestReadFleetHistoryMissingLogIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.jsonl")
	entries, warnings, err := ReadFleetHistory(path, HistoryFilters{})
	if err != nil {
		t.Fatalf("missing log returned an error: %v", err)
	}
	if len(entries) != 0 || len(warnings) != 0 {
		t.Fatalf("missing log = %d entries, %v warnings, want none", len(entries), warnings)
	}
}

func TestReadFleetHistoryDefaultsToTheAppendedLog(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	path := filepath.Join(state, "rootfiles", "fleet.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	results := []Result{{Host: "web1", Command: "apply", State: "ok"}}
	if err := AppendRunLog("", "apply", results); err != nil {
		t.Fatal(err)
	}
	entries, warnings, err := ReadFleetHistory("", HistoryFilters{})
	if err != nil || len(warnings) != 0 {
		t.Fatalf("read back the appended log: %v %v", err, warnings)
	}
	if len(entries) != 1 || entries[0].Record.Command != "apply" || len(entries[0].Record.Hosts) != 1 {
		t.Fatalf("entry = %#v, want the single appended apply record", entries)
	}
	// What AppendRunLog writes is what --json reprints.
	if !strings.HasSuffix(string(entries[0].Line), "}") || strings.HasSuffix(string(entries[0].Line), "\n") {
		t.Fatalf("stored line = %q, want one JSON object without its newline", entries[0].Line)
	}
}
