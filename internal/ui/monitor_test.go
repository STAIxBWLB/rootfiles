package ui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func sampleSnapshot() DashboardSnapshot {
	return DashboardSnapshot{
		UpdatedAt: time.Date(2026, 11, 18, 9, 30, 0, 0, time.UTC),
		Hostname:  "dgx01",
		OS:        "ubuntu 24.04",
		Profile:   "dgx",
		Vitals: VitalSummary{
			CPUCores:    128,
			Load1:       32.0,
			Load5:       24.5,
			Load15:      16.2,
			LoadKnown:   true,
			MemUsedMiB:  512000,
			MemTotalMiB: 1024000,
			MemUsedPct:  50,
			Uptime:      "14d 3h",
			Shutdown:    "poweroff at 2026-11-21 18:00",
		},
		Mounts: []MountDashRow{
			{Path: "/", UsedPct: 42, FreeGiB: 500, Level: "ok"},
			{Path: "/raid/home", UsedPct: 85, FreeGiB: 1200, Level: "warn"},
		},
		ModulesSatisfied: 12,
		ModulesTotal:     14,
		PendingModules:   []string{"ssh", "monitoring"},
		GPUs: []GPUDashRow{
			{Index: 0, Model: "NVIDIA H100 80GB HBM3", MemUsed: "40960", MemTotal: "81920", MemPct: 50, UtilPct: 80, TempC: "64", PowerW: "320", Assigned: "alice"},
			{Index: 1, Model: "NVIDIA H100 80GB HBM3", Restricted: true, Assigned: "tenant:lab"},
		},
		Tenants: []TenantDashRow{
			{Name: "lab", State: "running", SSHPort: 2201, GPUs: "1", Spec: "ok", Volume: "120G/500G"},
		},
		Commitment: "500 GiB committed of 1200 GiB free",
		Findings: []FindingDashRow{
			{Level: "ok", Check: "ssh access", Detail: "key login for root"},
			{Level: "warn", Check: "disk /raid/home", Detail: "85% used"},
		},
		Fleet: []FleetDashRow{
			{Host: "gpu01", State: "ok", GPUs: "8x H100 (alice:0,1)"},
			{Host: "gpu02", State: "drift", Reason: "2 modules pending", GPUs: "4x A100"},
		},
	}
}

func TestMonitorModel_Render80ColumnsAndModes(t *testing.T) {
	cleanOutputEnv(t)
	snap := sampleSnapshot()

	for _, mode := range []DashboardMode{ModeHost, ModeGPU, ModeTenant, ModeFleet} {
		for _, color := range []bool{false, true} {
			for _, ascii := range []bool{false, true} {
				t.Run(string(mode), func(t *testing.T) {
					model := NewDashboardModel(context.Background(), DashboardConfig{
						Title:    "rootfiles monitor",
						Mode:     mode,
						Interval: 2 * time.Second,
						Width:    80,
						Height:   24,
						Color:    color,
						ASCII:    ascii,
					}, snap, nil)
					view := model.View()
					for _, line := range strings.Split(view, "\n") {
						if w := ansi.StringWidth(line); w > 80 {
							t.Errorf("mode=%s color=%v ascii=%v line width %d > 80: %q", mode, color, ascii, w, line)
						}
					}
					if !color && strings.ContainsRune(view, 27) {
						t.Errorf("mode=%s color=false emitted ANSI escape: %q", mode, view)
					}
					if color && !strings.ContainsRune(view, 27) {
						t.Errorf("mode=%s color=true emitted no ANSI escape: %q", mode, view)
					}
					plain := ansi.Strip(view)
					if ascii && strings.ContainsAny(plain, MarkOK+MarkFail+MarkWarn+MarkSection+"█░") {
						t.Errorf("mode=%s ascii=true kept Unicode glyph in:\n%s", mode, plain)
					}
				})
			}
		}
	}
}

func TestMonitorModel_KeyNavigationAndSingleInFlightRefresh(t *testing.T) {
	cleanOutputEnv(t)
	calls := 0
	model := NewDashboardModel(context.Background(), DashboardConfig{
		Title:    "rootfiles monitor",
		Mode:     ModeHost,
		Interval: time.Second,
		Width:    80,
		Height:   24,
	}, sampleSnapshot(), func(context.Context) (DashboardSnapshot, error) {
		calls++
		return sampleSnapshot(), nil
	})

	// Tab cycles active pane, j/k moves cursor, ? toggles help.
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	m := updated.(*DashboardModel)
	if m.cursor != 1 {
		t.Fatalf("cursor=%d, want 1", m.cursor)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(*DashboardModel)
	if m.activePane != 1 || m.cursor != 0 {
		t.Fatalf("activePane=%d cursor=%d, want 1, 0", m.activePane, m.cursor)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = updated.(*DashboardModel)
	if !strings.Contains(m.View(), "keys: q/esc quit") {
		t.Fatalf("help footer not shown:\n%s", m.View())
	}

	// First tick starts a refresh; a second tick while inFlight does not start a duplicate.
	updated, cmd := m.Update(tickMsg(time.Now()))
	m = updated.(*DashboardModel)
	if !m.inFlight || cmd == nil {
		t.Fatal("expected inFlight=true after tick")
	}
	updated, _ = m.Update(tickMsg(time.Now()))
	m = updated.(*DashboardModel)
	if calls != 0 {
		t.Fatalf("refresh called synchronously before cmd execution: %d", calls)
	}
	// Deliver snapshotMsg to clear inFlight.
	updated, _ = m.Update(snapshotMsg{snap: sampleSnapshot()})
	m = updated.(*DashboardModel)
	if m.inFlight {
		t.Fatal("expected inFlight=false after snapshotMsg")
	}
}

func TestRunDashboard_RefusesWithoutTerminal(t *testing.T) {
	saved := TerminalAttached
	TerminalAttached = func() bool { return false }
	t.Cleanup(func() { TerminalAttached = saved })

	var out bytes.Buffer
	err := RunDashboard(context.Background(), &out, DashboardConfig{Title: "rootfiles monitor"}, nil)
	if !errors.Is(err, ErrNoTerminal) {
		t.Fatalf("expected ErrNoTerminal, got %v", err)
	}
}
