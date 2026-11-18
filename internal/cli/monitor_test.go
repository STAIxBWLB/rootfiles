package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/rootfiles/internal/module"
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
