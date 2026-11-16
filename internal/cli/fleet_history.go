package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/STAIxBWLB/rootfiles/internal/fleet"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
	"github.com/spf13/cobra"
)

// newFleetLogCmd reads the controller's own run log back. Every rollout appends
// to it, so this answers "what happened" without logging in to a host.
func newFleetLogCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "log",
		Short: "Show recorded fleet rollouts, newest first",
		Long: `Show the rollouts this controller has recorded.

Every fleet mutation appends one line to rootfiles/fleet.jsonl in the state
directory: $XDG_STATE_HOME/rootfiles/fleet.jsonl, else
~/.local/state/rootfiles/fleet.jsonl. This reads that file back instead of
asking the hosts, so it needs no SSH and no inventory. A missing log is an
empty result, not an error; a corrupt line is reported with its line number
and skipped.`,
		Example: `  rootfiles fleet log --limit 20
  rootfiles fleet log --host web1 --since 7d`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			filters, err := historyFiltersFromFlags(c)
			if err != nil {
				return err
			}
			format, err := outputFormat(c)
			if err != nil {
				return err
			}
			entries, warnings, err := fleet.ReadFleetHistory("", filters)
			if err != nil {
				return err
			}
			for _, warning := range warnings {
				ui.WriteWarning(c.ErrOrStderr(), warning, "")
			}
			out := c.OutOrStdout()
			if format == "json" {
				return writeHistoryJSON(out, entries)
			}
			ui.WriteHeader(out, c.CommandPath())
			if len(entries) == 0 {
				// An empty log stays a result, not an error: the header and one
				// hint line say the file held no rollout yet.
				ui.WriteHint(out, "no recorded rollouts")
				return nil
			}
			return writeHistoryTable(out, entries, filters.Host)
		},
	}
	c.Flags().String("host", "", "Only runs that touched this host (a filter, not a host selection)")
	c.Flags().String("op", "", "Only runs of this operation (e.g. apply, update)")
	c.Flags().String("since", "", "Only runs this recent: a duration (24h, 7d) or a date (2026-11-01, UTC)")
	c.Flags().Int("limit", 0, "Print at most this many runs, newest first (0 = every run)")
	addOutputFlag(c)
	return c
}

func historyFiltersFromFlags(c *cobra.Command) (fleet.HistoryFilters, error) {
	var filters fleet.HistoryFilters
	filters.Host, _ = c.Flags().GetString("host")
	filters.Op, _ = c.Flags().GetString("op")
	since, _ := c.Flags().GetString("since")
	filters.Limit, _ = c.Flags().GetInt("limit")
	if filters.Limit < 0 {
		return filters, withNext(
			fmt.Errorf("--limit must be 0 or greater (got %d)", filters.Limit),
			"omit --limit to print every run, or pass a positive count")
	}
	at, err := parseSince(since, time.Now())
	if err != nil {
		return filters, err
	}
	filters.Since = at
	return filters, nil
}

// parseSince accepts the windows operators read a run log by: a duration
// (24h), whole days (7d, which time.ParseDuration rejects), an ISO date, or an
// RFC 3339 timestamp.
func parseSince(value string, now time.Time) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	if days, ok := strings.CutSuffix(value, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil && n > 0 {
			return now.AddDate(0, 0, -n), nil
		}
	}
	if window, err := time.ParseDuration(value); err == nil {
		if window < 0 {
			return time.Time{}, withNext(
				fmt.Errorf("--since %q goes forward in time", value),
				"pass the window to look back over, such as 24h")
		}
		return now.Add(-window), nil
	}
	for _, layout := range []string{"2006-01-02", time.RFC3339} {
		if at, err := time.Parse(layout, value); err == nil {
			return at, nil
		}
	}
	return time.Time{}, withNext(
		fmt.Errorf("--since %q is neither a duration nor a date", value),
		"use a window such as 24h or 7d, or a date such as 2026-11-01")
}

// writeHistoryJSON reprints the stored lines: --json is the record as written,
// never a re-encoding.
func writeHistoryJSON(w io.Writer, entries []fleet.HistoryEntry) error {
	for _, entry := range entries {
		if _, err := w.Write(append(entry.Line, '\n')); err != nil {
			return err
		}
	}
	return nil
}

func writeHistoryTable(w io.Writer, entries []fleet.HistoryEntry, host string) error {
	rows := make([][]string, 0, len(entries))
	for _, entry := range entries {
		record := entry.Record
		at := record.Time.UTC().Format(time.RFC3339)
		op := record.Command
		if op == "" {
			op = "-"
		}
		for _, result := range record.Hosts {
			if host != "" && result.Host != host {
				continue
			}
			rows = append(rows, []string{
				at, op, result.Host, result.State,
				strconv.Itoa(result.ExitCode),
				strings.ReplaceAll(result.Reason, "\n", " "),
			})
		}
	}
	return ui.WriteTable(w, []string{"TIME", "OP", "HOST", "STATE", "EXIT", "REASON"}, rows, 5)
}
