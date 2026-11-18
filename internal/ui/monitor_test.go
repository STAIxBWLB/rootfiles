package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
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
			{Index: 0, Model: "NVIDIA H100 80GB HBM3", MemUsed: "40960", MemTotal: "81920", MemPct: 50, UtilPct: 80, UtilKnown: true, TempC: "64", PowerW: "320", Assigned: "alice"},
			{Index: 2, Model: "NVIDIA H100 80GB HBM3", MemUsed: "0", MemTotal: "81920", Assigned: "bob"},
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
	t.Setenv("COLUMNS", "60") // long reasons would wrap if tables wrapped
	snap := DashboardSnapshot{UpdatedAt: time.Date(2026, 11, 18, 9, 30, 0, 0, time.UTC)}
	for i := 0; i < 40; i++ {
		snap.Fleet = append(snap.Fleet, FleetDashRow{Host: fmt.Sprintf("host%02d", i), State: "ok", Reason: strings.Repeat("long reason ", 8)})
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

// MIG lines follow the GPU rows in the selection order, so MIG content
// taller than the terminal can be scrolled into view.
func TestMonitorModel_MIGLinesAreSelectable(t *testing.T) {
	cleanOutputEnv(t)
	snap := DashboardSnapshot{GPUState: SectionRead, GPUs: []GPUDashRow{{Index: 0, Model: "A100"}, {Index: 1, Model: "A100"}}}
	for g := 0; g < 2; g++ {
		mig := MIGDashRow{Index: strconv.Itoa(g), Name: "A100", Mode: "Enabled"}
		for i := 0; i < 7; i++ {
			mig.Instances = append(mig.Instances, fmt.Sprintf("gpu%d-instance%d", g, i))
		}
		snap.MIG = append(snap.MIG, mig)
	}
	var m tea.Model = NewDashboardModel(context.Background(), DashboardConfig{Mode: ModeGPU, Width: 80, Height: 12}, snap, nil)
	for i := 0; i < 40; i++ {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	if view := strings.Join(lines, "\n"); len(lines) > 12 || !strings.Contains(view, ">gpu1-instance6") {
		t.Fatalf("%d lines, want <= 12 with the last MIG instance selected:\n%s", len(lines), view)
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

// The warning line wraps at the dashboard width instead of being cut, so a
// long cause keeps its next step (#179).
func TestMonitorModel_WarningWrapsAtWidth(t *testing.T) {
	cleanOutputEnv(t)
	warning := "2 of 3 tenant reads failed; first, ml: docker inspect tenant-ml: Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?; run `docker inspect tenant-ml` for the full Docker error"
	view := ansi.Strip(NewDashboardModel(context.Background(), DashboardConfig{Mode: ModeTenant, Width: 80, Height: 60}, DashboardSnapshot{Err: warning}, nil).View())
	for _, line := range strings.Split(view, "\n") {
		if ansi.StringWidth(line) > 80 || strings.HasSuffix(line, "...") {
			t.Fatalf("line cut or wider than 80: %q\n%s", line, view)
		}
	}
	if joined := strings.Join(strings.Fields(view), " "); !strings.Contains(joined, "run `docker inspect tenant-ml` for the full Docker error") {
		t.Fatalf("next step lost:\n%s", view)
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

// An unknown utilization reading is never shown as a measured 0%.
func TestMonitorModel_UnknownUtilizationIsNotZero(t *testing.T) {
	cleanOutputEnv(t)
	snap := DashboardSnapshot{GPUState: SectionRead, GPUs: []GPUDashRow{{Index: 0, Model: "fixture", MemUsed: "0", MemTotal: "1024"}}}
	view := ansi.Strip(NewDashboardModel(context.Background(), DashboardConfig{Mode: ModeGPU, Height: 40}, snap, nil).View())
	if strings.Contains(view, "0%") {
		t.Fatalf("unknown utilization rendered as a percentage:\n%s", view)
	}
}

// A disk gauge takes its color from the doctor level, not fixed thresholds.
func TestMonitorModel_DiskBarUsesDoctorLevel(t *testing.T) {
	cleanOutputEnv(t)
	m := NewDashboardModel(context.Background(), DashboardConfig{Mode: ModeHost, Color: true, Height: 60}, DashboardSnapshot{Mounts: []MountDashRow{{Path: "/", UsedPct: 75, Level: "ok"}}}, nil)
	plain := m.renderPlainBar(75, 10)
	if view := m.View(); !strings.Contains(view, StyleSuccess.Render(plain)) || strings.Contains(view, StyleWarning.Render(plain)) {
		t.Fatalf("75%% disk at doctor level ok not drawn as ok:\n%q", view)
	}
}

// Quitting waits out a refresh still in flight, and a refresh that starts
// after quit never reaches the collector.
func TestJoinableRefreshWaitsAndBlocksLateStarts(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	calls := 0
	guarded, stop := joinable(func(context.Context) (DashboardSnapshot, error) {
		calls++
		close(started)
		<-release
		return DashboardSnapshot{}, nil
	})
	go func() { _, _ = guarded(context.Background()) }()
	<-started
	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("stop returned while a refresh was running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-stopped
	if _, err := guarded(context.Background()); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("refresh after stop: err=%v calls=%d, want canceled and one call", err, calls)
	}
}

// #201: the default fleet table shows the vitals and GPU columns a host
// reports, Enter opens the selected host through OpenHost, a failed host
// dashboard stays on the warning line until r, and the footer names the key.
func TestMonitorModel_FleetVitalsAndOpenHost(t *testing.T) {
	cleanOutputEnv(t)
	snap := DashboardSnapshot{Fleet: []FleetDashRow{
		{Host: "gpu01", State: "ok", Load: "2.1", Mem: "34%", Disk: "61% /data", GPUs: "8x H100, 3 assigned"},
		{Host: "gpu02", State: "ok"},
	}}
	var opened []string
	var cmd *exec.Cmd
	cfg := DashboardConfig{Mode: ModeFleet, Width: 120, Height: 24, OpenHost: func(host string) *exec.Cmd {
		opened = append(opened, host)
		cmd = exec.Command("true")
		return cmd
	}}
	var m tea.Model = NewDashboardModel(context.Background(), cfg, snap, nil)
	view := ansi.Strip(m.View())
	for _, want := range []string{"LOAD", "MEM", "DISK", "GPUS", "61% /data", "8x H100, 3 assigned", "enter host"} {
		if !strings.Contains(view, want) {
			t.Fatalf("fleet view lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "tab pane") || strings.Contains(view, "SHUTDOWN") {
		t.Fatalf("fleet view offers a second pane or an empty column:\n%s", view)
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if _, run := m.Update(tea.KeyMsg{Type: tea.KeyEnter}); run == nil || len(opened) != 1 || opened[0] != "gpu02" {
		t.Fatalf("enter: cmd=%v opened=%v", run, opened)
	}
	m, _ = m.Update(hostClosedMsg{host: "gpu02", err: errors.New("exit status 255")})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "gpu02: host dashboard exited: exit status 255") {
		t.Fatalf("failed host dashboard not on the warning line:\n%s", view)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if view := ansi.Strip(m.View()); strings.Contains(view, "host dashboard exited") {
		t.Fatalf("r kept the stale warning:\n%s", view)
	}

	// Without OpenHost, Enter does nothing; the host dashboard keeps its panes.
	bare := NewDashboardModel(context.Background(), DashboardConfig{Mode: ModeFleet, Width: 120}, DashboardSnapshot{Fleet: []FleetDashRow{{Host: "gpu02", State: "ok"}}}, nil)
	if _, run := bare.Update(tea.KeyMsg{Type: tea.KeyEnter}); run != nil {
		t.Fatal("enter without OpenHost returned a command")
	}
	if view := ansi.Strip(bare.View()); strings.Contains(view, "enter") || strings.Contains(view, "LOAD") {
		t.Fatalf("bare fleet view offers enter or an empty vitals column:\n%s", view)
	}
	if view := ansi.Strip(NewDashboardModel(context.Background(), DashboardConfig{Mode: ModeHost, Width: 120}, sampleSnapshot(), nil).View()); !strings.Contains(view, "tab pane") {
		t.Fatalf("host view lost the pane key:\n%s", view)
	}
}

// #201 review: in 80 columns a failure reason stays whole beside long vitals
// and GPU cells, which are what the terminal edge cuts instead.
func TestMonitorModel_FleetReasonSurvivesNarrowTerminal(t *testing.T) {
	cleanOutputEnv(t)
	snap := DashboardSnapshot{Fleet: []FleetDashRow{
		{Host: "gpu01", State: "ok", Load: "212.4", Mem: "97%", Disk: "93% /srv/tenants/data", GPUs: "3x RTX PRO 6000 Blackwell Server Edition, 2 assigned"},
		{Host: "gpu02", State: "unreachable", Reason: "ssh connection failed"},
	}}
	view := ansi.Strip(NewDashboardModel(context.Background(), DashboardConfig{Mode: ModeFleet, Width: 80, Height: 24}, snap, nil).View())
	if !strings.Contains(view, "ssh connection failed") {
		t.Fatalf("80-column fleet view cut the failure reason:\n%s", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if ansi.StringWidth(line) > 80 {
			t.Fatalf("line wider than 80 columns: %q", line)
		}
	}
}

// #201 review: a failed host session keeps the remote output on screen until
// Enter; a session that ends cleanly returns at once.
func TestHostSessionHoldsFailedOutput(t *testing.T) {
	for _, tc := range []struct {
		script  string
		wantErr bool
	}{{"echo remote cause; exit 3", true}, {"echo fine", false}} {
		var out bytes.Buffer
		input := strings.NewReader("\n")
		s := &hostSession{host: "gpu01", cmd: exec.Command("sh", "-c", tc.script)}
		s.SetStdin(input)
		s.SetStdout(&out)
		s.SetStderr(&out)
		err := s.Run()
		held := strings.Contains(out.String(), "gpu01: host dashboard exited: exit status 3; press Enter to return to the fleet")
		if (err != nil) != tc.wantErr || held != tc.wantErr {
			t.Fatalf("%q: err=%v held=%v out=%q", tc.script, err, held, out.String())
		}
	}
}
