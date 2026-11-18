package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/module"
	"github.com/STAIxBWLB/rootfiles/internal/tenant"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

func TestMonitorAndWatchRefuseInvalidFlagsAndNonTTY(t *testing.T) {
	pinInventoryPage(t)
	savedTTY := ui.TerminalAttached
	ui.TerminalAttached = func() bool { return false }
	t.Cleanup(func() { ui.TerminalAttached = savedTTY })

	for _, tc := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{"status watch with json", []string{"status", "--watch", "--json"}, 2, "--watch cannot be combined with --json"},
		{"status watch with output json", []string{"status", "--watch", "-o", "json"}, 2, "--watch cannot be combined with --output"},
		{"status interval without watch", []string{"status", "--interval", "2s"}, 2, "--interval requires --watch"},
		{"monitor interval too short", []string{"monitor", "--interval", "100ms"}, 2, "--interval must be at least 500ms"},
		{"monitor without terminal", []string{"monitor"}, 1, "no terminal for the live dashboard"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := NewRootCmd("test", "test")
			var out, stderr bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&stderr)
			root.SetArgs(tc.args)
			err := executeCommand(context.Background(), root)
			if err == nil {
				t.Fatalf("expected error for %v", tc.args)
			}
			code := 1
			var exit *ExitError
			if errors.As(err, &exit) {
				code = exit.Code
			}
			if code != tc.code {
				t.Fatalf("code=%d, want %d (stderr=%q)", code, tc.code, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("stderr=%q missing %q", stderr.String(), tc.want)
			}
		})
	}
}

func TestMonitorAndWatchCollectSnapshotsHostFree(t *testing.T) {
	pinInventoryPage(t)
	rootDir, site, _ := statusContractFixture(t, "populated")
	pinReportClock(t)
	t.Chdir(rootDir)
	stubGPUStatus(t, rootDir)
	stubMIGStatus(t, rootDir)
	stubTenantReports(t, rootDir)
	stubFleetStatusReads(t, rootDir)
	savedVitals, savedDoctor := hostVitalsCollector, doctorChecks
	t.Cleanup(func() { hostVitalsCollector, doctorChecks = savedVitals, savedDoctor })
	hostVitalsCollector = func(*module.RunContext) module.HostVitals {
		return module.HostVitals{Load1: 0.5, LoadKnown: true, MemTotalMiB: 1024, MemUsedMiB: 512, MemUsedPct: 50}
	}
	doctorChecks = func(context.Context, *module.RunContext) []module.Finding {
		return []module.Finding{{Level: "ok", Check: "fixture", Detail: "synthetic"}}
	}

	var capturedCfg ui.DashboardConfig
	var capturedSnap ui.DashboardSnapshot
	savedRun := runDashboard
	runDashboard = func(ctx context.Context, _ io.Writer, cfg ui.DashboardConfig, refresh ui.RefreshFunc) error {
		capturedCfg = cfg
		snap, err := refresh(ctx)
		if err != nil {
			return err
		}
		capturedSnap = snap
		return nil
	}
	t.Cleanup(func() { runDashboard = savedRun })

	for _, tc := range []struct {
		name string
		args []string
		mode ui.DashboardMode
	}{
		{"monitor", []string{"monitor", "--config", site, "--interval", "1s"}, ui.ModeHost},
		{"top alias", []string{"top", "--config", site}, ui.ModeHost},
		{"status watch", []string{"status", "--config", site, "--watch"}, ui.ModeHost},
		{"gpu status watch", []string{"gpu", "status", "--config", site, "--watch"}, ui.ModeGPU},
		{"tenant status watch", []string{"tenant", "status", "--config", site, "--watch"}, ui.ModeTenant},
		{"fleet status watch", []string{"fleet", "status", "--inventory", fleetFixtureInventory, "--all", "--watch", "--view", "gpu"}, ui.ModeFleet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capturedCfg = ui.DashboardConfig{}
			capturedSnap = ui.DashboardSnapshot{}
			root := NewRootCmd("test", "test")
			var out, stderr bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&stderr)
			root.SetArgs(tc.args)
			if err := executeCommand(context.Background(), root); err != nil {
				t.Fatalf("%v failed: %v (stderr=%s)", tc.args, err, stderr.String())
			}
			if capturedCfg.Mode != tc.mode {
				t.Fatalf("mode=%s, want %s", capturedCfg.Mode, tc.mode)
			}
			if capturedSnap.UpdatedAt.IsZero() {
				t.Fatalf("snapshot UpdatedAt is zero")
			}
			if want := "1 of 3 host reads failed; see the REASON column"; tc.mode == ui.ModeFleet && capturedSnap.Err != want {
				t.Fatalf("fleet warning=%q, want %q", capturedSnap.Err, want)
			}
			model := ui.NewDashboardModel(context.Background(), ui.DashboardConfig{
				Title:    capturedCfg.Title,
				Mode:     capturedCfg.Mode,
				Interval: 2 * time.Second,
				Width:    80,
				Height:   24,
			}, capturedSnap, nil)
			if view := model.View(); !strings.Contains(view, "rootfiles") {
				t.Fatalf("rendered view missing header:\n%s", view)
			}
		})
	}

	// A failed GPU read is reported; a host without GPUs is not an error.
	for _, gpuErr := range []error{errors.New("fixture allocation database unreadable"), module.ErrNoGPUDevices} {
		gpuStatusCollector = func(context.Context, *module.RunContext) (*module.GPUStatusReport, error) { return nil, gpuErr }
		root := NewRootCmd("test", "test")
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		root.SetArgs([]string{"monitor", "--config", site})
		if err := executeCommand(context.Background(), root); err != nil {
			t.Fatalf("monitor with GPU error %v: %v", gpuErr, err)
		}
		reported := strings.Contains(capturedSnap.Err, "GPU status unavailable: "+gpuErr.Error())
		if want := !errors.Is(gpuErr, module.ErrNoGPUDevices); reported != want {
			t.Fatalf("GPU error %q reported=%v, want %v (snapshot error %q)", gpuErr, reported, want, capturedSnap.Err)
		}
	}

	// A GPU module filtered out is omitted, not shown as absent; a MIG read
	// failure reaches the warning line.
	stubGPUStatus(t, rootDir)
	migStatusCollector = func(context.Context, *module.RunContext) ([]module.MIGGPU, error) {
		return nil, errors.New("fixture nvidia-smi failed")
	}
	for _, tc := range []struct {
		args []string
		ok   func() bool
	}{
		{[]string{"monitor", "--config", site, "--module", "tenant"}, func() bool { return capturedSnap.GPUState == ui.SectionOmitted }},
		{[]string{"gpu", "status", "--config", site, "--watch"}, func() bool {
			return capturedSnap.GPUState == ui.SectionRead && strings.Contains(capturedSnap.Err, "MIG status unavailable: fixture nvidia-smi failed")
		}},
	} {
		root := NewRootCmd("test", "test")
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		root.SetArgs(tc.args)
		if err := executeCommand(context.Background(), root); err != nil || !tc.ok() {
			t.Fatalf("%v: err=%v snapshot GPU state=%v error=%q", tc.args, err, capturedSnap.GPUState, capturedSnap.Err)
		}
	}
}

// Without a home base the dashboard withholds the GPU and tenant databases,
// as status does, instead of reading them under /home.
func TestMonitorWithholdsDatabasesWithoutHomeBase(t *testing.T) {
	pinInventoryPage(t)
	rootDir, site, _ := statusContractFixture(t, "unknown-home")
	t.Chdir(rootDir)
	saved := struct {
		run     func(context.Context, io.Writer, ui.DashboardConfig, ui.RefreshFunc) error
		vitals  func(*module.RunContext) module.HostVitals
		doctor  func(context.Context, *module.RunContext) []module.Finding
		gpuRead func(context.Context, *module.RunContext) (*module.GPUStatusReport, error)
	}{runDashboard, hostVitalsCollector, doctorChecks, gpuStatusCollector}
	t.Cleanup(func() {
		runDashboard, hostVitalsCollector, doctorChecks, gpuStatusCollector = saved.run, saved.vitals, saved.doctor, saved.gpuRead
	})
	hostVitalsCollector = func(*module.RunContext) module.HostVitals { return module.HostVitals{} }
	doctorChecks = func(context.Context, *module.RunContext) []module.Finding { return nil }
	gpuStatusCollector = func(context.Context, *module.RunContext) (*module.GPUStatusReport, error) {
		t.Fatal("GPU database read without a home base")
		return nil, nil
	}
	var snap ui.DashboardSnapshot
	runDashboard = func(ctx context.Context, _ io.Writer, _ ui.DashboardConfig, refresh ui.RefreshFunc) error {
		var err error
		snap, err = refresh(ctx)
		return err
	}
	root := NewRootCmd("test", "test")
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"monitor", "--config", site})
	if err := executeCommand(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if snap.GPUState != ui.SectionFailed || snap.TenantState != ui.SectionFailed || snap.Err == "" {
		t.Fatalf("GPU state=%v tenant state=%v error=%q, want both failed with the config error", snap.GPUState, snap.TenantState, snap.Err)
	}
}

// CollectStatus already formats a spec error as "error: <cause>".
func TestTenantDashRowsKeepCollectorErrorText(t *testing.T) {
	rows := toTenantDashRows([]tenant.StatusRow{{Tenant: "lab", Error: "error: spec unreadable"}})
	if rows[0].State != "error: spec unreadable" {
		t.Fatalf("state=%q", rows[0].State)
	}
}

// Single-resource watches keep stderr off the alternate screen: diagnostics
// reach the warning line, and the tenant trust check runs on every refresh.
func TestWatchCapturesStderrAndRechecksTenantTrust(t *testing.T) {
	pinInventoryPage(t)
	rootDir, site, _ := statusContractFixture(t, "populated")
	t.Chdir(rootDir)
	stubMIGStatus(t, rootDir)
	stubTenantReports(t, rootDir)
	savedRun, savedGPU := runDashboard, gpuStatusCollector
	t.Cleanup(func() { runDashboard, gpuStatusCollector = savedRun, savedGPU })
	gpuStatusCollector = func(_ context.Context, rc *module.RunContext) (*module.GPUStatusReport, error) {
		_, _ = io.WriteString(rc.Err, "rootfiles: warning: fixture metadata is not root-owned\n")
		return &module.GPUStatusReport{}, nil
	}
	var snap ui.DashboardSnapshot
	var refreshErr error
	runDashboard = func(ctx context.Context, _ io.Writer, _ ui.DashboardConfig, refresh ui.RefreshFunc) error {
		snap, refreshErr = refresh(ctx)
		return nil
	}

	run := func(args ...string) string {
		t.Helper()
		root := NewRootCmd("test", "test")
		var stderr bytes.Buffer
		root.SetOut(io.Discard)
		root.SetErr(&stderr)
		root.SetArgs(args)
		if err := executeCommand(context.Background(), root); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return stderr.String()
	}

	if stderr := run("gpu", "status", "--config", site, "--watch"); strings.Contains(stderr, "fixture metadata") {
		t.Fatalf("diagnostic reached the terminal: %q", stderr)
	}
	if !strings.Contains(snap.Err, "warning: fixture metadata is not root-owned") {
		t.Fatalf("warning line=%q, want the captured diagnostic", snap.Err)
	}

	calls := 0
	tenantReportEnv = func(*cobra.Command) (*module.RunContext, *tenant.Manager, error) {
		if calls++; calls > 1 {
			return nil, nil, errors.New("refusing tenant root fixture")
		}
		return nil, &tenant.Manager{}, nil
	}
	run("tenant", "status", "--config", site, "--watch")
	if refreshErr == nil || !strings.Contains(refreshErr.Error(), "refusing tenant root fixture") {
		t.Fatalf("refresh error=%v, want the trust refusal", refreshErr)
	}

	got := diagnosticLines("time=t level=INFO msg=exec\nrootfiles: warning: kept\nrootfiles: warning: kept\n")
	if len(got) != 1 || got[0] != "warning: kept" {
		t.Fatalf("diagnosticLines=%q, want only the warning", got)
	}
}

// #196: only a missing inventory gets the fleet init step; a missing host
// config inside an existing inventory must not suggest replacing it.
func TestControllerMonitorInitHintOnlyForMissingInventory(t *testing.T) {
	saved := nativeHostOS
	nativeHostOS = "darwin"
	t.Cleanup(func() { nativeHostOS = saved })
	dir := t.TempDir()
	existing := filepath.Join(dir, "fleet.yaml")
	if err := os.WriteFile(existing, []byte("hosts:\n  gpu01:\n    ssh: gpu01\n    config: fleet/gpu01.yaml\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		inventory string
		wantInit  bool
	}{{filepath.Join(dir, "missing.yaml"), true}, {existing, false}} {
		root := NewRootCmd("test", "test")
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		root.SetArgs([]string{"monitor", "--inventory", tc.inventory})
		err := executeCommand(context.Background(), root)
		next := ""
		if exit := (*ExitError)(nil); errors.As(err, &exit) {
			next = exit.Next
		}
		if err == nil || strings.Contains(next, "fleet init") != tc.wantInit {
			t.Fatalf("%s: err=%v next=%q, want init step %v", tc.inventory, err, next, tc.wantInit)
		}
	}
}

// A controller monitors the fleet, so the host-only --module filter is refused
// instead of silently ignored.
func TestControllerMonitorRefusesModuleFilter(t *testing.T) {
	saved := nativeHostOS
	nativeHostOS = "darwin"
	t.Cleanup(func() { nativeHostOS = saved })
	root := NewRootCmd("test", "test")
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"monitor", "--module", "gpu"})
	if err := executeCommand(context.Background(), root); err == nil || !strings.Contains(err.Error(), "--module is only used on a Linux host") {
		t.Fatalf("err=%v, want the controller refusal", err)
	}
}

// A failed tenant read is counted on the warning line, which also carries the
// first cause whole with its next step; its row keeps the cause.
func TestTenantDashboardCountsFailedReads(t *testing.T) {
	saved := tenantStatusCollector
	t.Cleanup(func() { tenantStatusCollector = saved })
	tenantStatusCollector = func(*tenant.Manager, context.Context, []string, bool) (*tenant.StatusReport, error) {
		return &tenant.StatusReport{Rows: []tenant.StatusRow{
			{Tenant: "lab", State: "running"},
			{Tenant: "ml", Error: "error: docker inspect tenant-ml: permission denied",
				Detail: []string{"ml: run `docker inspect tenant-ml` for the full Docker error"}},
			{Tenant: "web", Error: "error: load spec: unexpected EOF"},
		}}, nil
	}
	snap, err := collectTenantDashboardSnapshot(context.Background(), &tenant.Manager{}, []string{"lab", "ml", "web"}, false)
	want := "2 of 3 tenant reads failed; first, ml: docker inspect tenant-ml: permission denied; run `docker inspect tenant-ml` for the full Docker error"
	if err != nil || snap.Err != want || snap.Tenants[1].State != "error: docker inspect tenant-ml: permission denied" {
		t.Fatalf("err=%v warning=%q rows=%+v", err, snap.Err, snap.Tenants)
	}
}
