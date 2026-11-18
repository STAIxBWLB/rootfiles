package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/STAIxBWLB/rootfiles/internal/fleet"
	"github.com/STAIxBWLB/rootfiles/internal/module"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

// fleetViews are the fleet-wide sections `fleet status --view` can add. Each is
// read from the status payload the host already returned for the same SSH call,
// so a view costs no remote command and the sudoers rule is unchanged.
const fleetViewHelp = "gpu or users"

// fleetGPUView is the `gpu` section of a host's status report.
type fleetGPUView struct {
	TotalGPUs   int    `json:"total_gpus"`
	GPUModel    string `json:"gpu_model"`
	Allocations []struct {
		Username string `json:"username"`
		GPUs     []int  `json:"gpus"`
		Method   string `json:"method"`
	} `json:"allocations"`
}

// fleetUsersView is the `users` section of a host's status report.
type fleetUsersView struct {
	HomeBase string `json:"home_base"`
	Managed  int    `json:"managed"`
	System   int    `json:"system"`
}

// decodeGPUView reads the GPU section of a host's report. ok is false when the
// host did not report one: a failed status carries no payload, and a host that
// does not run the gpu module leaves the section out.
func decodeGPUView(result fleet.Result) (*fleetGPUView, bool) {
	if len(result.Remote) == 0 {
		return nil, false
	}
	var payload struct {
		GPU *fleetGPUView `json:"gpu"`
	}
	if err := json.Unmarshal(result.Remote, &payload); err != nil || payload.GPU == nil {
		return nil, false
	}
	return payload.GPU, true
}

// decodeVitalsView reads a host's vitals; a host on a release before they
// joined the status report has none.
func decodeVitalsView(result fleet.Result) (*module.HostVitals, bool) {
	if len(result.Remote) == 0 {
		return nil, false
	}
	var payload struct {
		Vitals *module.HostVitals `json:"vitals"`
	}
	if err := json.Unmarshal(result.Remote, &payload); err != nil || payload.Vitals == nil {
		return nil, false
	}
	return payload.Vitals, true
}

// decodeSystemGPUs reads the GPUs a host detected, which its status report
// carries whether or not the host keeps an allocation ledger.
func decodeSystemGPUs(result fleet.Result) (int, string, bool) {
	if len(result.Remote) == 0 {
		return 0, "", false
	}
	var payload struct {
		System *struct {
			GPUCount int    `json:"gpu_count"`
			GPUModel string `json:"gpu_model"`
		} `json:"system"`
	}
	if err := json.Unmarshal(result.Remote, &payload); err != nil || payload.System == nil || payload.System.GPUCount == 0 {
		return 0, "", false
	}
	return payload.System.GPUCount, payload.System.GPUModel, true
}

func decodeUsersView(result fleet.Result) (*fleetUsersView, bool) {
	if len(result.Remote) == 0 {
		return nil, false
	}
	var payload struct {
		Users *fleetUsersView `json:"users"`
	}
	if err := json.Unmarshal(result.Remote, &payload); err != nil || payload.Users == nil {
		return nil, false
	}
	return payload.Users, true
}

// knownFleetView reports whether --view names a section this build renders. It
// is checked before the inventory loads, so a typo fails before any SSH.
func knownFleetView(view string) bool {
	return view == "gpu" || view == "users"
}

// writeFleetView renders one fleet-wide view beside the per-host report. It is
// reached only when --view is set, so the default status text and JSON stay as
// they were.
func writeFleetView(w io.Writer, results []fleet.Result, view, format string) error {
	switch view {
	case "gpu":
		if format == "json" {
			perHost := make(map[string]*fleetGPUView, len(results))
			for _, result := range results {
				if payload, ok := decodeGPUView(result); ok {
					perHost[result.Host] = payload
				}
			}
			return writeJSONEscaping(w, map[string]any{"hosts": results, "gpu": perHost}, true)
		}
		rows := make([][]string, 0, len(results))
		for _, result := range results {
			row := []string{result.Host, result.State, "-", "-", "-"}
			if payload, ok := decodeGPUView(result); ok {
				row[2] = strconv.Itoa(payload.TotalGPUs)
				row[3] = payload.GPUModel
				row[4] = allocationsCell(payload)
			}
			rows = append(rows, row)
		}
		return ui.WriteTable(w, []string{"HOST", "STATE", "GPUS", "MODEL", "ALLOCATIONS"}, rows, 4)
	case "users":
		if format == "json" {
			perHost := make(map[string]*fleetUsersView, len(results))
			for _, result := range results {
				if payload, ok := decodeUsersView(result); ok {
					perHost[result.Host] = payload
				}
			}
			return writeJSONEscaping(w, map[string]any{"hosts": results, "users": perHost}, true)
		}
		rows := make([][]string, 0, len(results))
		for _, result := range results {
			row := []string{result.Host, result.State, "-", "-", "-"}
			if payload, ok := decodeUsersView(result); ok {
				row[2] = payload.HomeBase
				row[3] = strconv.Itoa(payload.Managed)
				row[4] = strconv.Itoa(payload.System)
			}
			rows = append(rows, row)
		}
		return ui.WriteTable(w, []string{"HOST", "STATE", "HOME_BASE", "MANAGED", "SYSTEM"}, rows, 2)
	}
	return withNext(
		fmt.Errorf("--view %q is not a known fleet view", view),
		"choose "+fleetViewHelp+" (rootfiles fleet status --help)")
}

// allocationsCell renders assignments as user:gpus so one row stays one line.
func allocationsCell(payload *fleetGPUView) string {
	cells := make([]string, 0, len(payload.Allocations))
	for _, allocation := range payload.Allocations {
		gpus := make([]string, len(allocation.GPUs))
		for i, index := range allocation.GPUs {
			gpus[i] = strconv.Itoa(index)
		}
		cell := allocation.Username + ":" + strings.Join(gpus, ",")
		if allocation.Method != "" {
			cell += " (" + allocation.Method + ")"
		}
		cells = append(cells, cell)
	}
	return strings.Join(cells, "; ")
}
