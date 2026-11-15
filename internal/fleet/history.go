package fleet

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"
)

// HistoryEntry pairs one stored run-log line with its decoded record. The line
// is kept byte for byte so `fleet log --json` can reprint what the rollout
// wrote instead of re-encoding a record and dropping fields it does not know.
type HistoryEntry struct {
	Line   []byte
	Record RunRecord
}

// HistoryFilters narrows the run log read. The zero value matches every run.
type HistoryFilters struct {
	// Host keeps runs that touched this host. The entry is still returned
	// whole: filtering a record's hosts would change its stored bytes.
	Host string
	// Op keeps runs whose operation matches: the first word of the recorded
	// command, so "targets" selects "targets --push <hub>".
	Op string
	// Since keeps runs recorded at or after this instant.
	Since time.Time
	// Limit keeps at most this many runs, newest first. Zero keeps all.
	Limit int
}

// ReadFleetHistory reads the controller's run log and returns matching runs
// newest first. An empty path means the log the rollouts append to. A missing
// log is an empty history rather than an error, and a corrupt line is reported
// with its 1-based line number and skipped so one bad write cannot hide the
// rest of the file.
func ReadFleetHistory(path string, filters HistoryFilters) ([]HistoryEntry, []string, error) {
	if path == "" {
		path = defaultRunLog()
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("reading fleet run log %s: %w", path, err)
	}
	defer f.Close()

	type occurrence struct {
		entry HistoryEntry
		index int
	}
	var matched []occurrence
	var warnings []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for line := 1; scanner.Scan(); line++ {
		raw := scanner.Bytes()
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var record RunRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			warnings = append(warnings, fmt.Sprintf("fleet run log %s line %d: %v", path, line, err))
			continue
		}
		if !matchesHistory(filters, record) {
			continue
		}
		matched = append(matched, occurrence{
			entry: HistoryEntry{Line: append([]byte(nil), raw...), Record: record},
			index: line,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, warnings, fmt.Errorf("reading fleet run log %s: %w", path, err)
	}

	// Newest first by the recorded time; two runs written in the same instant
	// keep the later line first.
	sort.SliceStable(matched, func(a, b int) bool {
		if !matched[a].entry.Record.Time.Equal(matched[b].entry.Record.Time) {
			return matched[a].entry.Record.Time.After(matched[b].entry.Record.Time)
		}
		return matched[a].index > matched[b].index
	})
	if filters.Limit > 0 && len(matched) > filters.Limit {
		matched = matched[:filters.Limit]
	}
	entries := make([]HistoryEntry, len(matched))
	for i, m := range matched {
		entries[i] = m.entry
	}
	return entries, warnings, nil
}

func matchesHistory(filters HistoryFilters, record RunRecord) bool {
	if filters.Host != "" && !touchesHost(record, filters.Host) {
		return false
	}
	if filters.Op != "" && historyOperation(record.Command) != filters.Op {
		return false
	}
	if !filters.Since.IsZero() && record.Time.Before(filters.Since) {
		return false
	}
	return true
}

// historyOperation is the recorded command without its arguments: fleet writes
// both `apply` and `targets --push <hub>`, and an operator filters the first.
func historyOperation(command string) string {
	if fields := strings.Fields(command); len(fields) > 0 {
		return fields[0]
	}
	return ""
}

func touchesHost(record RunRecord, host string) bool {
	for _, result := range record.Hosts {
		if result.Host == host {
			return true
		}
	}
	return false
}
