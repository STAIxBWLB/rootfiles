package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
		GPUState:         SectionRead,
		TenantState:      SectionRead,
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

// A table taller than the terminal scrolls with the selection and keeps the
// title and key footer on screen.
func TestMonitorModel_FitsHeightAndFollowsSelection(t *testing.T) {
	cleanOutputEnv(t)
	snap := DashboardSnapshot{UpdatedAt: time.Date(2026, 11, 18, 9, 30, 0, 0, time.UTC)}
	for i := 0; i < 40; i++ {
		snap.Fleet = append(snap.Fleet, FleetDashRow{Host: fmt.Sprintf("host%02d", i), State: "ok"})
	}
	for _, ascii := range []bool{false, true} {
		var m tea.Model = NewDashboardModel(context.Background(), DashboardConfig{Mode: ModeFleet, Width: 80, Height: 12, ASCII: ascii}, snap, nil)
		for want := 0; want <= 30; want += 30 {
			lines := strings.Split(ansi.Strip(m.View()), "\n")
			view := strings.Join(lines, "\n")
			if len(lines) > 12 || !strings.Contains(lines[0], "rootfiles monitor") || !strings.Contains(lines[len(lines)-1], "q quit") || !strings.Contains(view, fmt.Sprintf(">host%02d", want)) {
				t.Fatalf("ascii=%v cursor=%d: %d lines, want <= 12 with title, footer and selection:\n%s", ascii, want, len(lines), view)
			}
			for i := 0; i < 30; i++ {
				m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
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
	if !errors.Is(err, ErrNoDashboardTerminal) || strings.Contains(err.Error(), "--yes") {
		t.Fatalf("expected ErrNoDashboardTerminal without --yes advice, got %v", err)
	}
	t.Setenv("TERM", "dumb")
	TerminalAttached = func() bool { return true }
	if DashboardTerminal(&out) {
		t.Fatal("dashboard accepted a non-terminal output or TERM=dumb")
	}
}

// A failed or filtered read never renders as an absent resource.
func TestMonitorModel_FailedReadsAreNotEmptyResources(t *testing.T) {
	cleanOutputEnv(t)
	empty := []string{"no NVIDIA GPUs detected", "no tenant specs configured", "no hosts selected"}
	for _, tc := range []struct {
		mode    DashboardMode
		snap    DashboardSnapshot
		want    []string
		without []string
	}{
		{ModeHost, DashboardSnapshot{Err: "GPU status unavailable", GPUState: SectionFailed, TenantState: SectionFailed}, []string{"GPU read failed", "tenant read failed"}, empty},
		{ModeHost, DashboardSnapshot{}, nil, append([]string{"GPUs", "Tenants"}, empty...)},
		{ModeGPU, DashboardSnapshot{Err: "allocation database unreadable"}, []string{"allocation database unreadable"}, empty},
		{ModeTenant, DashboardSnapshot{Err: "spec directory unreadable"}, []string{"spec directory unreadable"}, empty},
		{ModeFleet, DashboardSnapshot{Err: "inventory unreadable"}, []string{"fleet read failed"}, empty},
		{ModeHost, DashboardSnapshot{GPUState: SectionRead}, []string{"no NVIDIA GPUs detected"}, nil},
	} {
		view := ansi.Strip(NewDashboardModel(context.Background(), DashboardConfig{Mode: tc.mode, Height: 60}, tc.snap, nil).View())
		for _, want := range tc.want {
			if !strings.Contains(view, want) {
				t.Errorf("mode=%s: missing %q:\n%s", tc.mode, want, view)
			}
		}
		for _, bad := range tc.without {
			if strings.Contains(view, bad) {
				t.Errorf("mode=%s: unexpected %q:\n%s", tc.mode, bad, view)
			}
		}
	}
}

// A failed refresh keeps the rows on screen labelled with their collection
// time, and Bubble Tea gets the raw stream, not the color-stripping writer.
func TestMonitorModel_FailedRefreshKeepsCollectionTime(t *testing.T) {
	cleanOutputEnv(t)
	snap := sampleSnapshot()
	var m tea.Model = NewDashboardModel(context.Background(), DashboardConfig{Mode: ModeGPU, Height: 60}, snap, nil)
	m, _ = m.Update(snapshotMsg{err: errors.New("allocation database unreadable")})
	view := ansi.Strip(m.View())
	if !strings.Contains(view, snap.UpdatedAt.Local().Format("15:04:05")) || !strings.Contains(view, "allocation database unreadable") {
		t.Fatalf("stale rows lost their collection time:\n%s", view)
	}
	var raw bytes.Buffer
	if got := teaOutput(ConfigureWriter(&raw, "never", true, false)); got != io.Writer(&raw) {
		t.Fatalf("teaOutput kept the decorating writer: %T", got)
	}
}

// The health pane selects among the findings it lists and keeps the
// selection on screen.
func TestMonitorModel_HealthPaneSelectsShownFindings(t *testing.T) {
	cleanOutputEnv(t)
	snap := DashboardSnapshot{Findings: []FindingDashRow{{Level: "ok", Check: "hidden-ok"}}}
	for i := 0; i < 30; i++ {
		snap.Findings = append(snap.Findings, FindingDashRow{Level: "warn", Check: fmt.Sprintf("check%02d", i), Detail: "fixture"})
	}
	var m tea.Model = NewDashboardModel(context.Background(), DashboardConfig{Mode: ModeHost, Width: 80, Height: 12}, snap, nil)
	for _, key := range []tea.KeyMsg{{Type: tea.KeyTab}, {Type: tea.KeyTab}} {
		m, _ = m.Update(key)
	}
	for i := 0; i < 40; i++ {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, ">check29") || len(strings.Split(view, "\n")) > 12 {
		t.Fatalf("health pane selection not shown on a 12-line screen:\n%s", view)
	}
}
