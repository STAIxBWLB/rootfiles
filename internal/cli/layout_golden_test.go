package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/STAIxBWLB/rootfiles/internal/exec"
	"github.com/STAIxBWLB/rootfiles/internal/module"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

// TestHumanLayoutGolden pins the layout grammar of the human report families
// the CLI renders host-free: one colour-off golden at 80 columns, and one
// colour-on golden naming the roles every row carries. Run it with -update to
// rewrite the goldens after a reviewed wording change.
func TestHumanLayoutGolden(t *testing.T) {
	for _, tc := range layoutCases() {
		t.Run(tc.name, func(t *testing.T) {
			layoutPinWidth(t)
			// Resolved before the render: a captured report chdirs into its
			// fixture, and the golden still belongs to the package directory.
			goldenDir, err := filepath.Abs(filepath.Join("testdata", "layout-t3"))
			if err != nil {
				t.Fatal(err)
			}
			for _, mode := range []struct{ suffix, color string }{
				{"text", "never"},
				{"color", "always"},
			} {
				out := tc.render(t, mode.color)
				golden := filepath.Join(goldenDir, tc.name+"."+mode.suffix+".golden")
				layoutAssertGrammar(t, tc.name, mode.suffix, out)
				if *layoutUpdate {
					if err := os.MkdirAll(goldenDir, 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(golden, []byte(out), 0644); err != nil {
						t.Fatal(err)
					}
					continue
				}
				want, err := os.ReadFile(golden)
				if err != nil {
					t.Fatalf("read golden %s: %v", golden, err)
				}
				if out != string(want) {
					t.Errorf("%s %s layout changed\n--- golden\n%s\n--- current\n%s", tc.name, mode.suffix, want, out)
				}
			}
		})
	}
}

// layoutUpdate rewrites the goldens instead of comparing them:
// `go test ./internal/cli -run TestHumanLayoutGolden -update`.
var layoutUpdate = flag.Bool("update", false, "rewrite the layout goldens")

// layoutPinWidth sets the stream environment every case is pinned in: 80
// columns, UTF-8 markers, and colour left to the explicit mode.
func layoutPinWidth(t *testing.T) {
	t.Helper()
	t.Setenv("LC_ALL", "C.UTF-8")
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "")
	t.Setenv("CLICOLOR", "")
	t.Setenv("COLUMNS", "80")
}

// layoutAssertGrammar checks the invariants a golden alone would let through.
func layoutAssertGrammar(t *testing.T, name, mode, out string) {
	t.Helper()
	if strings.HasSuffix(out, "\n\n") || !strings.HasSuffix(out, "\n") {
		t.Fatalf("%s %s must end with exactly one newline: %q", name, mode, out)
	}
	previousBlank := false
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if width := ansi.StringWidth(line); width > 80 {
			t.Errorf("%s %s line is %d columns: %q", name, mode, width, line)
		}
		if strings.TrimSpace(ansi.Strip(line)) == "" {
			if previousBlank {
				t.Errorf("%s %s has two blank lines in a row", name, mode)
			}
			previousBlank = true
			continue
		}
		previousBlank = false
	}
	if mode == "text" && strings.ContainsRune(out, 27) {
		t.Errorf("%s text golden carries an escape byte", name)
	}
	if mode == "color" && !strings.ContainsRune(out, 27) {
		t.Errorf("%s color golden carries no escape byte", name)
	}
}

type layoutCase struct {
	name   string
	render func(t *testing.T, color string) string
}

// layoutCases holds one entry per output family: the native reports through
// their command, and each renderer that takes its data from a fixture.
func layoutCases() []layoutCase {
	return []layoutCase{
		{name: "check", render: func(t *testing.T, color string) string {
			return layoutCommand(t, color, "check")
		}},
		{name: "doctor", render: func(t *testing.T, color string) string {
			return layoutDoctorText(t, color)
		}},
		{name: "rollback-list", render: func(t *testing.T, color string) string {
			var buf bytes.Buffer
			layoutMust(t, renderRollbackList(ui.ConfigureWriter(&buf, color, false, false), "rootfiles rollback", layoutBackups()))
			return buf.String()
		}},
		{name: "gpu-mig", render: func(t *testing.T, color string) string {
			var buf bytes.Buffer
			renderMIGStatus(ui.ConfigureWriter(&buf, color, false, false), layoutMIG())
			return buf.String()
		}},
		{name: "user-du", render: func(t *testing.T, color string) string {
			var buf bytes.Buffer
			layoutMust(t, renderHomeUsage(ui.ConfigureWriter(&buf, color, false, false), "rootfiles user du", layoutUsage()))
			return buf.String()
		}},
		{name: "controller-status", render: func(t *testing.T, color string) string {
			var buf bytes.Buffer
			layoutMust(t, renderControllerStatus(ui.ConfigureWriter(&buf, color, false, false), layoutSnapshot()))
			return buf.String()
		}},
		// Every report the inventory captures through a host-free fixture gets
		// its golden here, so the page and the golden measure one rendering.
		{name: "fleet-status", render: layoutCaptured("rootfiles fleet status")},
		{name: "fleet-check", render: layoutCaptured("rootfiles fleet check")},
		{name: "fleet-doctor", render: layoutCaptured("rootfiles fleet doctor")},
		{name: "fleet-hosts", render: layoutCaptured("rootfiles fleet hosts")},
		{name: "fleet-log", render: layoutCaptured("rootfiles fleet log")},
		{name: "fleet-ping", render: layoutCaptured("rootfiles fleet ping")},
		{name: "fleet-shutdown-status", render: layoutCaptured("rootfiles fleet shutdown status")},
		{name: "gpu-status", render: layoutCaptured("rootfiles gpu status")},
		{name: "gpu-mig-status", render: layoutCaptured("rootfiles gpu mig status")},
		{name: "user-audit", render: layoutCaptured("rootfiles user audit")},
		{name: "user-key-list", render: layoutCaptured("rootfiles user key list")},
		{name: "shutdown-status", render: layoutCaptured("rootfiles shutdown status")},
		{name: "tunnel-status", render: layoutCaptured("rootfiles tunnel status")},
		{name: "update-check", render: layoutCaptured("rootfiles update")},
	}
}

// layoutCaptured renders a report through the fixture its inventory row names,
// with colour forced by the golden's mode. The fixture root is normalized the
// way the page normalizes it, so a golden carries no temporary path.
func layoutCaptured(command string) func(t *testing.T, color string) string {
	return func(t *testing.T, color string) string {
		t.Helper()
		layoutPinWidth(t)
		root, _, _ := statusContractFixture(t, "populated")
		pinReportClock(t)
		t.Chdir(root)
		out := reportText(t, root, command, "--color", color)
		return strings.ReplaceAll(out, root, "<fixture>")
	}
}

func layoutMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// layoutDoctorText renders doctor's human report from synthetic findings, so
// the golden does not depend on the host's disks, clock or services.
func layoutDoctorText(t *testing.T, color string) string {
	previous := doctorChecks
	t.Cleanup(func() { doctorChecks = previous })
	doctorChecks = func(context.Context, *module.RunContext) []module.Finding {
		return []module.Finding{
			{Check: "disk", Level: module.LevelOK, Detail: "/ at 41% used"},
			{Check: "clock", Level: module.LevelWarn, Detail: "systemd-timesyncd is inactive", Hint: "systemctl enable --now systemd-timesyncd"},
			{Check: "gpu", Level: module.LevelFail, Detail: "nvidia-smi reported no devices"},
			{Check: "reboot", Level: module.LevelSkip, Detail: "no reboot marker"},
		}
	}
	return layoutCommand(t, color, "doctor")
}

// layoutCommand runs one report command against the shared fixture with the
// width and colour policy the golden pins.
func layoutCommand(t *testing.T, color, command string) string {
	t.Helper()
	layoutPinWidth(t)
	previousNow := checkNow
	t.Cleanup(func() { checkNow = previousNow })
	checkNow = func() time.Time { return time.Unix(1700000000, 0) }
	fixtureRoot, _, _ := statusContractFixture(t, "populated")
	// A relative config keeps the printed path short and stable at 80 columns.
	// The cwd is restored here, not only at test cleanup, so the caller can
	// still read its golden.
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(fixtureRoot)
	defer func() {
		if err := os.Chdir(original); err != nil {
			t.Fatal(err)
		}
	}()
	var stdout, stderr bytes.Buffer
	root := NewRootCmd("layout-contract", "fixture")
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{command, "--config", "site.yaml", "--color", color})
	if err := root.Execute(); err != nil {
		var coded *ExitError
		if !errors.As(err, &coded) {
			t.Fatalf("%s wrote a non-report error: %v", command, err)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("%s wrote unexpected stderr: %s", command, stderr.String())
	}
	return stdout.String()
}

func layoutBackups() []exec.BackupManifest {
	return []exec.BackupManifest{
		{ID: "20261101T090413Z", Note: "rootfiles apply --profile dgx", Entries: []exec.BackupEntry{{Path: "/etc/ssh/sshd_config"}, {Path: "/etc/docker/daemon.json"}}},
		{ID: "20261031T220001Z", Note: "sudo rootfiles user add alice with a much longer note that has to stay inside its column", Entries: []exec.BackupEntry{{Path: "/etc/passwd"}}},
		{ID: "20261030T101500Z", Note: "", Entries: nil},
	}
}

func layoutMIG() []module.MIGGPU {
	return []module.MIGGPU{
		{Index: "0", Name: "NVIDIA A100-SXM4-40GB", Current: "Enabled", Pending: "Enabled",
			Instances: []string{"MIG 3g.20gb Device 0 (UUID: MIG-1111)", "MIG 3g.20gb Device 1 (UUID: MIG-2222)"}},
		{Index: "1", Name: "NVIDIA A100-SXM4-40GB", Current: "Disabled", Pending: "Enabled", Instances: nil},
	}
}

func layoutUsage() []module.HomeUsage {
	return []module.HomeUsage{
		{User: "alice", Home: "/home/alice", Bytes: 48_318_382_080},
		{User: "bob", Home: "/home/bob", Bytes: 1_048_576},
		{User: "carol", Home: "/home/carol", Bytes: 0},
	}
}

func layoutSnapshot() controllerSnapshot {
	return controllerSnapshot{
		Mode:      "controller",
		Version:   "v0.21.0",
		Platform:  "darwin",
		Inventory: "/Users/operator/.config/rootfiles/fleet.yaml",
		Hosts:     3,
		Groups:    2,
		Runs: []controllerRun{
			{Host: "gpu01", Time: time.Date(2026, 11, 15, 4, 30, 0, 0, time.UTC), Operation: "update", State: "ok"},
			{Host: "gpu02", Time: time.Date(2026, 11, 14, 22, 5, 0, 0, time.UTC), Operation: "apply --dry-run", State: "dry-run"},
		},
	}
}
