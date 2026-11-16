package cli

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Report states a row of the report-output inventory can carry.
const (
	// ReportMeasured: the audit captured this report's text here, colour off,
	// 80 columns, through a host-free fixture, and measured it.
	ReportMeasured = "measured"
	// ReportRaw: the command prints a payload whose bytes are a contract, so the
	// layout grammar does not apply to it.
	ReportRaw = "raw"
	// ReportOwed: this slice has no host-free seam for it, so its capture and
	// its golden belong to the T3 output slice.
	ReportOwed = "owed"
)

// ReportFacts is what the audit records about one command's human report: the
// file that writes it, the host-free seam a test drives, and what the captured
// text shows when this slice has a seam for it.
type ReportFacts struct {
	Command string
	File    string
	Seam    string
	State   string
	Issues  []string
}

// ReportInventory lists every command whose job is to print a report, with the
// seam its text can be driven through. A row marked owed names the seam it still
// needs: the fleet controller needs an SSH stub, tenant reports a container
// runtime stub, and the native ones read the live account or systemd database.
var ReportInventory = []ReportFacts{
	{Command: "rootfiles status", File: "status.go", Seam: "statusContractFixture", State: ReportMeasured},
	{Command: "rootfiles check", File: "check.go", Seam: "statusContractFixture", State: ReportMeasured},
	{Command: "rootfiles doctor", File: "doctor.go", Seam: "statusContractFixture + doctorChecks", State: ReportMeasured},
	{Command: "rootfiles rollback", File: "rollback.go", Seam: "state.BackupsDir", State: ReportMeasured},
	{Command: "rootfiles schedule status", File: "schedule.go", Seam: "statusContractFixture PATH stubs", State: ReportMeasured},
	{Command: "rootfiles gpu list", File: "gpu.go", Seam: "statusContractFixture allocation DB", State: ReportMeasured},
	{Command: "rootfiles user list", File: "user.go", Seam: "statusContractFixture users DB", State: ReportMeasured},
	{Command: "rootfiles user du", File: "userlife.go", Seam: "statusContractFixture home base", State: ReportMeasured},
	{Command: "rootfiles config show", File: "configcmd.go", Seam: "config.LoadWithHomeBase", State: ReportRaw},
	// A scrape-targets file is consumed by a monitoring hub, so its bytes are a
	// contract rather than a layout this standard may reflow.
	{Command: "rootfiles fleet targets", File: "fleet.go", Seam: "fleet.RenderTargets", State: ReportRaw},

	{Command: "rootfiles gpu status", File: "gpu.go", Seam: "-", State: ReportOwed},
	{Command: "rootfiles gpu mig status", File: "gpu.go", Seam: "-", State: ReportOwed},
	{Command: "rootfiles user audit", File: "userlife.go", Seam: "-", State: ReportOwed},
	{Command: "rootfiles user groups", File: "user.go", Seam: "-", State: ReportOwed},
	{Command: "rootfiles user id", File: "user.go", Seam: "-", State: ReportOwed},
	{Command: "rootfiles user key list", File: "userlife.go", Seam: "-", State: ReportOwed},
	{Command: "rootfiles user quota show", File: "userlife.go", Seam: "-", State: ReportOwed},
	{Command: "rootfiles shutdown status", File: "shutdown.go", Seam: "-", State: ReportOwed},
	{Command: "rootfiles tunnel status", File: "tunnel.go", Seam: "-", State: ReportOwed},
	{Command: "rootfiles update", File: "upgrade.go", Seam: "-", State: ReportOwed},
	{Command: "rootfiles tenant list", File: "tenant.go", Seam: "-", State: ReportOwed},
	{Command: "rootfiles tenant status", File: "tenant.go", Seam: "-", State: ReportOwed},
	{Command: "rootfiles tenant logs", File: "tenant.go", Seam: "-", State: ReportOwed},
	{Command: "rootfiles fleet status", File: "fleet.go", Seam: "readFleetHost + inventory fixture", State: ReportMeasured},
	{Command: "rootfiles fleet check", File: "fleet.go", Seam: "readFleetHost + inventory fixture", State: ReportMeasured},
	{Command: "rootfiles fleet doctor", File: "fleet.go", Seam: "readFleetHost + inventory fixture", State: ReportMeasured},
	{Command: "rootfiles fleet hosts", File: "fleet_init.go", Seam: "inventory fixture", State: ReportMeasured},
	{Command: "rootfiles fleet log", File: "fleet_history.go", Seam: "run log fixture", State: ReportMeasured},
	{Command: "rootfiles fleet ping", File: "fleet_ping.go", Seam: "pingFleetFn + inventory fixture", State: ReportMeasured},
	{Command: "rootfiles fleet shutdown status", File: "fleet_shutdown.go", Seam: "readFleetHost + inventory fixture", State: ReportMeasured},
}

// LayoutIssues names what a captured report shows against the layout grammar.
// It reports only what the bytes can prove: the width of a line, blank-line
// runs, the final newline, an escape sequence in a colour-off capture, a missing
// header and a section that opens without the blank line before it. The rest of
// the grammar - aligned key/value rows, the two-cell marker, a capped table and
// the closing summary line - is judged where a family's bytes are pinned.
func LayoutIssues(text string) []string {
	var out []string
	if text == "" {
		return []string{"no output"}
	}
	if strings.ContainsRune(text, 27) {
		out = append(out, "escape sequence in a colour-off capture")
	}
	body := strings.TrimSuffix(text, "\n")
	lines := strings.Split(body, "\n")
	wide := 0
	for i, line := range lines {
		n := ansi.StringWidth(line)
		if n <= InventoryWidth {
			continue
		}
		wide++
		if wide == 1 {
			out = append(out, fmt.Sprintf("line %d is %d columns", i+1, n))
		}
	}
	if wide > 1 {
		out = append(out, fmt.Sprintf("%d lines are over %d columns", wide, InventoryWidth))
	}
	// A report opens with the header line `ui.WriteHeader` writes: one space,
	// the tool name, then the command path.
	switch {
	case lines[0] == "":
		out = append(out, "starts with a blank line, so no header line")
	case !strings.HasPrefix(lines[0], " rootfiles "):
		out = append(out, "no header line")
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == "" && lines[i-1] == "" {
			out = append(out, fmt.Sprintf("blank line run before line %d", i+1))
			break
		}
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, "\u25b8 ") {
			continue
		}
		if i == 0 || lines[i-1] != "" {
			out = append(out, fmt.Sprintf("section at line %d has no blank line before it", i+1))
			break
		}
	}
	if !strings.HasSuffix(text, "\n") {
		out = append(out, "no final newline")
	} else if strings.HasSuffix(text, "\n\n") {
		out = append(out, "trailing blank line")
	}
	return out
}
