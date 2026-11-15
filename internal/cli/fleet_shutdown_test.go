package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/STAIxBWLB/rootfiles/internal/fleet"
)

// fleetShutdownFixture writes an inventory and points the run log and PATH at
// temp dirs. PATH holds no ssh, so a run that tries to connect fails loudly.
func fleetShutdownFixture(t *testing.T, body string) string {
	t.Helper()
	inventory := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(inventory, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	return inventory
}

func runFleetCmd(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := NewRootCmd("test", "test")
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), errOut.String(), err
}

func TestFleetShutdownRefusesBadInputBeforeConnecting(t *testing.T) {
	inventory := fleetShutdownFixture(t, "hosts:\n  a:\n    ssh: a\n")
	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{"--in below a minute", []string{"now", "--in", "30s"}, []string{"--in 30s", "at least 1 minute"}},
		{"--in beyond the window", []string{"now", "--in", "744h"}, []string{"at most 30 days"}},
		{"missing --at", []string{"schedule"}, []string{"--at is required", "RFC 3339"}},
		{"--at without an offset", []string{"schedule", "--at", "2026-11-21T18:00:00"}, []string{"explicit offset"}},
		{"--at too soon", []string{"schedule", "--at", time.Now().Add(10 * time.Second).UTC().Format(time.RFC3339)}, []string{"at least 1 minute ahead"}},
		{"message too long", []string{"schedule", "--at", time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339), "--message", strings.Repeat("x", 301)}, []string{"--message", "300"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"fleet", "shutdown"}, tc.args...)
			args = append(args, "--inventory", inventory, "--all", "--yes")
			_, _, err := runFleetCmd(t, args...)
			if err == nil {
				t.Fatal("bad input was accepted")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("refusal %q does not name %q", err, want)
				}
			}
			// PATH has no ssh: a reachable error proves nothing was attempted.
			if strings.Contains(err.Error(), "executable file not found") {
				t.Fatalf("the command connected before refusing: %v", err)
			}
		})
	}
}

func TestFleetShutdownDryRunSchedulesTheSameInstantAndLogsIt(t *testing.T) {
	now := time.Now()
	at := now.Add(2 * time.Hour).UTC().Truncate(time.Second)
	inventory := fleetShutdownFixture(t, "hosts:\n  web1:\n    ssh: web1\n  web2:\n    ssh: web2\n")
	out, _, err := runFleetCmd(t, "fleet", "shutdown", "schedule",
		"--inventory", inventory, "--all", "--dry-run",
		"--at", at.Format(time.RFC3339), "--notice", "15m", "--message", "Building power work")
	if err != nil {
		t.Fatalf("dry-run schedule: %v\n%s", err, out)
	}
	if !strings.Contains(out, "dry-run: would schedule poweroff for "+at.Format(time.RFC3339)) {
		t.Fatalf("dry run did not name the instant:\n%s", out)
	}
	for _, host := range []string{"web1", "web2"} {
		if !strings.Contains(hostRow(t, out, host), "dry-run: would schedule") {
			t.Fatalf("%s is missing from the preview:\n%s", host, out)
		}
	}

	log := filepath.Join(os.Getenv("XDG_STATE_HOME"), "rootfiles", "fleet.jsonl")
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("the run was not logged: %v", err)
	}
	var record fleet.RunRecord
	if err := json.Unmarshal(bytes.TrimSpace(data), &record); err != nil {
		t.Fatalf("run log: %v", err)
	}
	want := "shutdown schedule " + at.Format(time.RFC3339) + " poweroff --message 'Building power work'"
	if record.Command != want {
		t.Fatalf("run log command = %q, want %q", record.Command, want)
	}
	if len(record.Hosts) != 2 {
		t.Fatalf("run log hosts = %#v", record.Hosts)
	}
}

// scheduledInstant reads the instant out of a preview row.
func scheduledInstant(t *testing.T, table, host string) string {
	t.Helper()
	row := hostRow(t, table, host)
	_, rest, found := strings.Cut(row, " for ")
	if !found || len(rest) < len("2006-01-02T15:04:05Z") {
		t.Fatalf("no instant in the %s row: %q", host, row)
	}
	return rest[:len("2006-01-02T15:04:05Z")]
}

// AC7: `now` gives every host the same instant, computed once from --in.
func TestFleetShutdownNowSharesOneInstant(t *testing.T) {
	inventory := fleetShutdownFixture(t, "hosts:\n  web1:\n    ssh: web1\n  web2:\n    ssh: web2\n")
	out, _, err := runFleetCmd(t, "fleet", "shutdown", "now", "--in", "2m",
		"--inventory", inventory, "--all", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run now: %v\n%s", err, out)
	}
	first, second := scheduledInstant(t, out, "web1"), scheduledInstant(t, out, "web2")
	if first != second {
		t.Fatalf("hosts got different instants: %s and %s", first, second)
	}
	at, err := time.Parse(time.RFC3339, first)
	if err != nil {
		t.Fatalf("preview instant %q: %v", first, err)
	}
	if lead := time.Until(at); lead < time.Minute || lead > 3*time.Minute {
		t.Fatalf("--in 2m produced a %s lead", lead)
	}
}

// AC9: the controller's own host is refused unless the operator says so.
func TestFleetShutdownRefusesThisControllerUnlessAllowed(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		t.Skip("no hostname to match")
	}
	inventory := fleetShutdownFixture(t, "hosts:\n  controller:\n    ssh: "+hostname+"\n  web1:\n    ssh: web1\n")
	at := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)

	_, _, err = runFleetCmd(t, "fleet", "shutdown", "schedule",
		"--inventory", inventory, "--all", "--dry-run", "--at", at)
	if err == nil {
		t.Fatal("scheduling the controller's own host was allowed")
	}
	for _, want := range []string{"includes this controller", "controller"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not name %q", err, want)
		}
	}
	// The recovery flag is the printed next step, not part of the message.
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || !strings.Contains(exitErr.Next, "--include-self") {
		t.Fatalf("refusal does not offer --include-self: %#v", err)
	}
	if _, _, err := runFleetCmd(t, "fleet", "shutdown", "schedule",
		"--inventory", inventory, "--all", "--dry-run", "--at", at, "--include-self"); err != nil {
		t.Fatalf("--include-self was still refused: %v", err)
	}
	// A selection without the controller is unaffected.
	if _, _, err := runFleetCmd(t, "fleet", "shutdown", "schedule",
		"--inventory", inventory, "--host", "web1", "--dry-run", "--at", at); err != nil {
		t.Fatalf("a remote host was refused: %v", err)
	}
}

const scheduledStatusPayload = `{"hostname":"web1","scheduled_shutdown":{"id":"20261120T090000Z","at":"2026-11-21T09:00:00Z","action":"poweroff","created_by":"ops","created_at":"2026-11-20T09:00:00Z"}}`

// fleetScheduleFixture serves one host with a schedule and one without.
func fleetScheduleFixture(t *testing.T, called *atomic.Int32) string {
	t.Helper()
	previous := readFleetHost
	t.Cleanup(func() { readFleetHost = previous })
	readFleetHost = func(_ fleet.SSHRunner, _ context.Context, h fleet.NamedHost, op string) fleet.Result {
		if called != nil {
			called.Add(1)
		}
		payload := json.RawMessage(plainStatusPayload)
		if h.Name == "web1" {
			payload = json.RawMessage(scheduledStatusPayload)
		}
		return fleet.Result{Host: h.Name, Command: op, State: "ok", Remote: payload}
	}
	inventory := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(inventory, []byte("hosts:\n  web1:\n    ssh: web1\n  web2:\n    ssh: web2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return inventory
}

// AC8: the SHUTDOWN column appears in fleet status and in fleet shutdown
// status, and the JSON report keeps its existing shape.
func TestFleetStatusShowsTheScheduledShutdown(t *testing.T) {
	inventory := fleetScheduleFixture(t, nil)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"fleet status", []string{"fleet", "status"}},
		{"fleet shutdown status", []string{"fleet", "shutdown", "status"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, _, err := runFleetCmd(t, append(tc.args, "--inventory", inventory, "--all")...)
			if err != nil {
				t.Fatalf("%s: %v\n%s", tc.name, err, out)
			}
			if !strings.Contains(out, "SHUTDOWN") {
				t.Fatalf("%s has no SHUTDOWN column:\n%s", tc.name, out)
			}
			if row := hostRow(t, out, "web1"); !strings.Contains(row, "2026-11-21T09:00Z poweroff") {
				t.Fatalf("%s does not show the schedule: %q", tc.name, row)
			}
			if row := hostRow(t, out, "web2"); !strings.Contains(row, "-") {
				t.Fatalf("%s does not show the empty cell for web2: %q", tc.name, row)
			}
		})
	}

	out, _, err := runFleetCmd(t, "fleet", "status", "--inventory", inventory, "--all", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("status JSON: %v\n%s", err, out)
	}
	if len(report) != 1 || report["hosts"] == nil {
		t.Fatalf("the status JSON gained a controller-side key: %v", report)
	}
}

func TestFleetShutdownCancelDryRunReachesEveryHost(t *testing.T) {
	inventory := fleetShutdownFixture(t, "hosts:\n  web1:\n    ssh: web1\n  web2:\n    ssh: web2\n")
	out, _, err := runFleetCmd(t, "fleet", "shutdown", "cancel", "--inventory", inventory, "--all", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run cancel: %v\n%s", err, out)
	}
	for _, host := range []string{"web1", "web2"} {
		if !strings.Contains(hostRow(t, out, host), "would cancel") {
			t.Fatalf("%s is missing from the preview:\n%s", host, out)
		}
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("XDG_STATE_HOME"), "rootfiles", "fleet.jsonl"))
	if err != nil || !strings.Contains(string(data), `"command":"shutdown cancel"`) {
		t.Fatalf("cancel was not logged: %q err=%v", data, err)
	}
}
