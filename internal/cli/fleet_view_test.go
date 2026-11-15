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

	"github.com/STAIxBWLB/rootfiles/internal/fleet"
)

// The two payloads a host's own `status -o json` returns: one with a GPU
// section and one without, which is what a host that does not run the gpu
// module reports.
const (
	gpuStatusPayload   = `{"hostname":"web1","gpu":{"version":1,"total_gpus":8,"gpu_model":"A100","allocations":[{"username":"alice","gpus":[0,1],"method":"env","updated_at":"2026-11-01T00:00:00Z"}]},"users":{"home_base":"/home","managed":3,"system":5}}`
	plainStatusPayload = `{"hostname":"web2","users":{"home_base":"/srv/home","managed":1,"system":4}}`
)

func fleetViewResults() []fleet.Result {
	return []fleet.Result{
		{Host: "web1", Command: "status", State: "ok", Remote: json.RawMessage(gpuStatusPayload)},
		{Host: "web2", Command: "status", State: "ok", Remote: json.RawMessage(plainStatusPayload)},
	}
}

// fleetViewFixture serves those payloads through the read seam and writes the
// inventory they are selected from. called counts host reads, so a test can
// prove a rejected --view never opened one.
func fleetViewFixture(t *testing.T, called *atomic.Int32) string {
	t.Helper()
	previous := readFleetHost
	t.Cleanup(func() { readFleetHost = previous })
	readFleetHost = func(_ fleet.SSHRunner, _ context.Context, h fleet.NamedHost, op string) fleet.Result {
		if called != nil {
			called.Add(1)
		}
		for _, result := range fleetViewResults() {
			if result.Host == h.Name {
				return result
			}
		}
		return fleet.Result{Host: h.Name, Command: op, State: "unreachable", Reason: "no fixture for this host"}
	}
	inventory := filepath.Join(t.TempDir(), "fleet.yaml")
	body := "hosts:\n  web1:\n    ssh: web1\n  web2:\n    ssh: web2\n"
	if err := os.WriteFile(inventory, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return inventory
}

func runFleetStatus(t *testing.T, inventory string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := NewRootCmd("test", "test")
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(append([]string{"fleet", "status", "--inventory", inventory, "--all"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func hostRow(t *testing.T, table, host string) string {
	t.Helper()
	for _, line := range strings.Split(table, "\n") {
		if strings.Contains(line, host) {
			return line
		}
	}
	t.Fatalf("no row for %s:\n%s", host, table)
	return ""
}

// AC2: --view gpu and --view users render the payloads the hosts already
// returned, and a host without the section shows "-" instead of an error.
func TestFleetStatusViewsRenderReportedPayloads(t *testing.T) {
	inventory := fleetViewFixture(t, nil)

	gpu, err := runFleetStatus(t, inventory, "--view", "gpu")
	if err != nil {
		t.Fatalf("fleet status --view gpu: %v", err)
	}
	row := hostRow(t, gpu, "web1")
	for _, want := range []string{"A100", "alice:0,1 (env)"} {
		if !strings.Contains(row, want) {
			t.Fatalf("gpu row lost %q: %s", want, row)
		}
	}
	if !strings.Contains(row, "8") {
		t.Fatalf("gpu row lost the device count: %s", row)
	}
	// web2 runs no gpu module: its columns are "-", and the command still
	// succeeded for every host (no error, exit 0).
	missing := hostRow(t, gpu, "web2")
	if !strings.Contains(missing, "-") || strings.Contains(missing, "A100") {
		t.Fatalf("a host without a gpu section did not render as missing: %s", missing)
	}

	users, err := runFleetStatus(t, inventory, "--view", "users")
	if err != nil {
		t.Fatalf("fleet status --view users: %v", err)
	}
	for host, want := range map[string][]string{
		"web1": {"/home", "3", "5"},
		"web2": {"/srv/home", "1", "4"},
	} {
		row := hostRow(t, users, host)
		for _, value := range want {
			if !strings.Contains(row, value) {
				t.Fatalf("users row for %s lost %q: %s", host, value, row)
			}
		}
	}
}

// AC2: --json adds the per-host objects under the view's own key.
func TestFleetStatusViewJSONAddsOneKey(t *testing.T) {
	inventory := fleetViewFixture(t, nil)

	gpuJSON, err := runFleetStatus(t, inventory, "--view", "gpu", "--json")
	if err != nil {
		t.Fatalf("fleet status --view gpu --json: %v", err)
	}
	var doc struct {
		Hosts []fleet.Result `json:"hosts"`
		GPU   map[string]struct {
			TotalGPUs   int    `json:"total_gpus"`
			GPUModel    string `json:"gpu_model"`
			Allocations []struct {
				Username string `json:"username"`
			} `json:"allocations"`
		} `json:"gpu"`
	}
	if err := json.Unmarshal([]byte(gpuJSON), &doc); err != nil {
		t.Fatalf("gpu view JSON: %v\n%s", err, gpuJSON)
	}
	if len(doc.Hosts) != 2 {
		t.Fatalf("hosts = %d, want both hosts", len(doc.Hosts))
	}
	if len(doc.GPU) != 1 || doc.GPU["web1"].TotalGPUs != 8 || doc.GPU["web1"].GPUModel != "A100" {
		t.Fatalf("gpu section = %#v, want only web1's report", doc.GPU)
	}

	usersJSON, err := runFleetStatus(t, inventory, "--view", "users", "--json")
	if err != nil {
		t.Fatalf("fleet status --view users --json: %v", err)
	}
	var usersDoc struct {
		Users map[string]struct {
			HomeBase string `json:"home_base"`
			Managed  int    `json:"managed"`
		} `json:"users"`
	}
	if err := json.Unmarshal([]byte(usersJSON), &usersDoc); err != nil {
		t.Fatalf("users view JSON: %v\n%s", err, usersJSON)
	}
	if len(usersDoc.Users) != 2 || usersDoc.Users["web2"].Managed != 1 {
		t.Fatalf("users section = %#v, want both hosts", usersDoc.Users)
	}
}

// AC4: the default report keeps the bytes writeFleetResults has always
// produced, for text and for JSON.
func TestFleetStatusDefaultReportBytesUnchanged(t *testing.T) {
	inventory := fleetViewFixture(t, nil)
	results := fleetViewResults()

	for _, format := range []string{"text", "json"} {
		var want bytes.Buffer
		if err := writeFleetResults(&want, results, format); err != nil {
			t.Fatal(err)
		}
		args := []string{}
		if format == "json" {
			args = append(args, "--json")
		}
		got, err := runFleetStatus(t, inventory, args...)
		if err != nil {
			t.Fatalf("fleet status (%s): %v", format, err)
		}
		if got != want.String() {
			t.Fatalf("default %s report changed:\n got %q\nwant %q", format, got, want.String())
		}
	}
}

// A mistyped view fails before the inventory loads, so no SSH happens; --view
// exists on status only.
func TestFleetStatusViewIsValidatedBeforeConnecting(t *testing.T) {
	var called atomic.Int32
	inventory := fleetViewFixture(t, &called)

	_, err := runFleetStatus(t, inventory, "--view", "cpu")
	if err == nil || !strings.Contains(err.Error(), `--view "cpu" is not a known fleet view`) {
		t.Fatalf("unknown --view accepted: %v", err)
	}
	// The next step is a field, not part of Error(): that is where the fix lives.
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || !strings.Contains(exitErr.Next, "gpu or users") {
		t.Fatalf("the error carries no next step naming the views: %#v", err)
	}
	if called.Load() != 0 {
		t.Fatalf("a rejected --view opened %d host reads", called.Load())
	}

	cmd := NewRootCmd("test", "test")
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"fleet", "check", "--inventory", inventory, "--all", "--view", "gpu"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unknown flag: --view") {
		t.Fatalf("fleet check accepted --view: %v", err)
	}
}
