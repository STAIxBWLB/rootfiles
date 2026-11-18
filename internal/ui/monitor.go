package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// DashboardMode selects which sections a live dashboard renders.
type DashboardMode string

const (
	ModeHost   DashboardMode = "host"
	ModeGPU    DashboardMode = "gpu"
	ModeTenant DashboardMode = "tenant"
	ModeFleet  DashboardMode = "fleet"
)

// DashboardConfig configures a live monitoring session.
type DashboardConfig struct {
	Title    string
	Mode     DashboardMode
	Interval time.Duration
	Width    int
	Height   int
	Color    bool
	ASCII    bool
}

// VitalSummary holds host-level load, memory, uptime and shutdown state.
type VitalSummary struct {
	CPUCores    int
	Load1       float64
	Load5       float64
	Load15      float64
	LoadKnown   bool
	MemUsedMiB  uint64
	MemTotalMiB uint64
	MemUsedPct  float64
	Uptime      string
	Shutdown    string
}

// MountDashRow represents one filesystem row in the dashboard.
type MountDashRow struct {
	Path    string
	UsedPct float64
	FreeGiB float64
	Level   string // ok | warn | fail
}

// GPUDashRow represents one GPU device in the dashboard.
type GPUDashRow struct {
	Index      int
	Model      string
	MemUsed    string
	MemTotal   string
	MemPct     float64
	UtilPct    float64
	UtilKnown  bool // false when nvidia-smi reported N/A; never shown as 0%
	TempC      string
	PowerW     string
	Assigned   string
	Restricted bool
}

// MIGDashRow represents one GPU's MIG mode and instance summary.
type MIGDashRow struct {
	Index     string
	Name      string
	Mode      string
	Instances []string
}

// TenantDashRow represents one tenant container in the dashboard.
type TenantDashRow struct {
	Name    string
	State   string
	SSHPort int
	GPUs    string
	Spec    string
	Volume  string
}

// FindingDashRow represents one doctor finding in the dashboard.
type FindingDashRow struct {
	Level  string // ok | warn | fail | skip
	Check  string
	Detail string
}

// FleetDashRow represents one host in a controller fleet dashboard.
type FleetDashRow struct {
	Host     string
	State    string
	Reason   string
	Shutdown string
	GPUs     string
	Users    string
}

// DashboardSnapshot holds one point-in-time read-only snapshot for the TUI.
type DashboardSnapshot struct {
	UpdatedAt        time.Time
	Hostname         string
	OS               string
	Profile          string
	Vitals           VitalSummary
	Mounts           []MountDashRow
	ModulesSatisfied int
	ModulesTotal     int
	PendingModules   []string
	GPUs             []GPUDashRow
	MIG              []MIGDashRow
	Tenants          []TenantDashRow
	Commitment       string
	Overcommitted    bool
	Findings         []FindingDashRow
	Fleet            []FleetDashRow
	FleetView        string
	Err              string
	GPUState         SectionState
	TenantState      SectionState
}

// SectionState says what a section's rows mean, so a filtered or failed read
// never renders as "none". The zero value omits the section.
type SectionState int

const (
	SectionOmitted SectionState = iota // not collected: filtered out or not applicable
	SectionRead                        // collected; no rows means none exist
	SectionFailed                      // the read failed; Err says why
)

// RefreshFunc collects a fresh DashboardSnapshot.
type RefreshFunc func(ctx context.Context) (DashboardSnapshot, error)

type tickMsg time.Time

type snapshotMsg struct {
	snap DashboardSnapshot
	err  error
}

// DashboardModel is the Bubble Tea model for live server and fleet monitoring.
type DashboardModel struct {
	ctx        context.Context
	cfg        DashboardConfig
	refresh    RefreshFunc
	snap       DashboardSnapshot
	inFlight   bool
	activePane int
	cursor     int
	showHelp   bool
	lines      *lineCounter // set during View
	focusLine  int          // output line fitHeight keeps on screen
}

// lineCounter counts the lines View has written, so a renderer can mark
// where its selected row lands.
type lineCounter struct {
	w io.Writer
	n int
}

func (c *lineCounter) Write(p []byte) (int, error) {
	c.n += bytes.Count(p, []byte("\n"))
	return c.w.Write(p)
}

// markFocus asks fitHeight to keep the line rowsAhead below the next one
// written on screen.
func (m *DashboardModel) markFocus(rowsAhead int) {
	m.focusLine = m.lines.n + rowsAhead
}

// NewDashboardModel constructs a DashboardModel with initial snapshot state.
func NewDashboardModel(ctx context.Context, cfg DashboardConfig, initial DashboardSnapshot, refresh RefreshFunc) *DashboardModel {
	if cfg.Width <= 0 {
		cfg.Width = 80
	}
	if cfg.Height <= 0 {
		cfg.Height = 24
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 2 * time.Second
	}
	return &DashboardModel{
		ctx:     ctx,
		cfg:     cfg,
		refresh: refresh,
		snap:    initial,
	}
}

func (m *DashboardModel) Init() tea.Cmd {
	return m.scheduleTick()
}

func (m *DashboardModel) scheduleTick() tea.Cmd {
	interval := m.cfg.Interval
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m *DashboardModel) triggerRefresh() tea.Cmd {
	if m.refresh == nil || m.inFlight {
		return nil
	}
	m.inFlight = true
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	fn := m.refresh
	return func() tea.Msg {
		snap, err := fn(ctx)
		return snapshotMsg{snap: snap, err: err}
	}
}

func (m *DashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.cfg.Width = msg.Width
		}
		if msg.Height > 0 {
			m.cfg.Height = msg.Height
		}
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "r":
			return m, m.triggerRefresh()
		case "tab":
			m.activePane = (m.activePane + 1) % m.paneCount()
			m.cursor = 0
			return m, nil
		case "shift+tab":
			m.activePane = (m.activePane - 1 + m.paneCount()) % m.paneCount()
			m.cursor = 0
			return m, nil
		case "down", "j":
			if maxIdx := m.activeRowCount() - 1; m.cursor < maxIdx {
				m.cursor++
			}
			return m, nil
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		case "?":
			m.showHelp = !m.showHelp
			return m, nil
		}
	case tickMsg:
		if m.inFlight {
			return m, m.scheduleTick()
		}
		return m, tea.Batch(m.scheduleTick(), m.triggerRefresh())
	case snapshotMsg:
		m.inFlight = false
		if msg.err != nil {
			// Rows on screen stay labelled with the time they were collected.
			m.snap.Err = msg.err.Error()
		} else {
			m.snap = msg.snap
			if m.snap.UpdatedAt.IsZero() {
				m.snap.UpdatedAt = time.Now()
			}
		}
		if maxIdx := m.activeRowCount() - 1; maxIdx >= 0 && m.cursor > maxIdx {
			m.cursor = maxIdx
		}
		return m, nil
	}
	return m, nil
}

func (m *DashboardModel) paneCount() int {
	switch m.cfg.Mode {
	case ModeHost:
		return 3
	default:
		return 1
	}
}

func (m *DashboardModel) activeRowCount() int {
	switch m.cfg.Mode {
	case ModeGPU:
		return len(m.snap.GPUs)
	case ModeTenant:
		return len(m.snap.Tenants)
	case ModeFleet:
		return len(m.snap.Fleet)
	default:
		switch m.activePane {
		case 0:
			return len(m.snap.GPUs)
		case 1:
			return len(m.snap.Tenants)
		default:
			return len(m.shownFindings())
		}
	}
}

func (m *DashboardModel) View() string {
	var raw bytes.Buffer
	colorMode := "never"
	if m.cfg.Color {
		colorMode = "always"
	}
	m.lines, m.focusLine = &lineCounter{w: &raw}, 0
	w := ConfigureWriter(m.lines, colorMode, m.cfg.ASCII, false)
	width := m.cfg.Width
	if width <= 0 {
		width = 80
	}

	title := m.cfg.Title
	if title == "" {
		title = "rootfiles monitor"
	}
	stamp := ""
	if !m.snap.UpdatedAt.IsZero() {
		stamp = "  " + m.snap.UpdatedAt.Local().Format("15:04:05")
	}
	WriteHeader(w, title+stamp)

	if m.snap.Err != "" {
		WriteBullet(w, WarnMark(), m.snap.Err)
	}

	switch m.cfg.Mode {
	case ModeGPU:
		m.renderGPUSection(w, width, true)
		m.renderMIGSection(w)
	case ModeTenant:
		m.renderTenantSection(w, width, true)
	case ModeFleet:
		m.renderFleetSection(w, width)
	default:
		m.renderHostVitalsSection(w, width)
		m.renderGPUSection(w, width, m.activePane == 0)
		m.renderTenantSection(w, width, m.activePane == 1)
		m.renderFindingsSection(w, width, m.activePane == 2)
	}

	WriteLine(w)
	if m.showHelp {
		WriteHint(w, "keys: q/esc quit  r refresh  tab switch pane  j/k or up/down select  ? hide help")
	} else {
		WriteHint(w, fmt.Sprintf("every %s  (q quit, r refresh, tab pane, ? keys)", m.cfg.Interval))
	}
	return fitHeight(clampLinesWidth(raw.String(), width), m.cfg.Height, m.focusLine)
}

// noWrap keeps every dashboard table row on one line, so a row's index is
// its line offset for markFocus; clampLinesWidth truncates what does not fit.
const noWrap = -1

// fitHeight keeps the title and footer lines and scrolls the body so the
// focus line stays on screen.
// ponytail: stateless centering on one line; a stateful viewport if long sections need paging.
func fitHeight(text string, height, focus int) string {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if height <= 0 || len(lines) <= height {
		return text
	}
	if height < 3 {
		return strings.Join(lines[:height], "\n")
	}
	body, room := lines[1:len(lines)-1], height-2
	start := min(max(0, focus-1-room/2), len(body)-room)
	visible := append([]string{lines[0]}, body[start:start+room]...)
	return strings.Join(append(visible, lines[len(lines)-1]), "\n")
}

func (m *DashboardModel) renderHostVitalsSection(w io.Writer, width int) {
	WriteSection(w, "System & Vitals")
	hostLine := FirstNonEmpty(m.snap.Hostname, "localhost")
	if m.snap.OS != "" {
		hostLine += " (" + m.snap.OS + ")"
	}
	if m.snap.Profile != "" {
		hostLine += "  profile=" + m.snap.Profile
	}
	WriteKV(w, "Host", truncateCells(hostLine, max(20, width-18)))

	v := m.snap.Vitals
	if v.LoadKnown {
		loadPct := 0.0
		if v.CPUCores > 0 {
			loadPct = math.Min(100, (v.Load1/float64(v.CPUCores))*100)
		}
		bar := m.renderBar(loadPct, 10)
		cores := ""
		if v.CPUCores > 0 {
			cores = fmt.Sprintf(" (%d cores)", v.CPUCores)
		}
		WriteKV(w, "Load", fmt.Sprintf("%s %.2f %.2f %.2f%s", bar, v.Load1, v.Load5, v.Load15, cores))
	}
	if v.MemTotalMiB > 0 {
		bar := m.renderBar(v.MemUsedPct, 10)
		WriteKV(w, "Memory", fmt.Sprintf("%s %.0f%% (%d/%d MiB)", bar, v.MemUsedPct, v.MemUsedMiB, v.MemTotalMiB))
	}
	if v.Uptime != "" {
		WriteKV(w, "Uptime", v.Uptime)
	}
	if v.Shutdown != "" {
		WriteKV(w, "Shutdown", StyleWarning.Render(v.Shutdown))
	}
	if m.snap.ModulesTotal > 0 {
		modText := fmt.Sprintf("%d/%d satisfied", m.snap.ModulesSatisfied, m.snap.ModulesTotal)
		if len(m.snap.PendingModules) > 0 {
			modText += " (pending: " + strings.Join(m.snap.PendingModules, ", ") + ")"
		}
		WriteKV(w, "Modules", truncateCells(modText, max(20, width-18)))
	}
	for _, mt := range m.snap.Mounts {
		bar := m.renderLevelBar(mt.UsedPct, 10, mt.Level)
		WriteKV(w, "Disk "+mt.Path, fmt.Sprintf("%s %.0f%% (%.0f GiB free)", bar, mt.UsedPct, mt.FreeGiB))
	}
}

func (m *DashboardModel) renderGPUSection(w io.Writer, _ int, focused bool) {
	title := "GPUs"
	if focused && m.cfg.Mode == ModeHost {
		title += " [active]"
	}
	if m.snap.GPUState == SectionOmitted {
		return
	}
	if focused {
		m.markFocus(1)
	}
	WriteSection(w, title)
	if len(m.snap.GPUs) == 0 {
		if m.snap.GPUState == SectionFailed {
			WriteHint(w, "GPU read failed; see the warning above")
		} else {
			WriteHint(w, "no NVIDIA GPUs detected")
		}
		return
	}
	rows := make([][]string, 0, len(m.snap.GPUs))
	for i, g := range m.snap.GPUs {
		idx := strconv.Itoa(g.Index)
		if focused && i == m.cursor {
			idx = ">" + idx
		}
		memCell, utilCell, thermCell := "-", "-", "-"
		if !g.Restricted {
			if g.MemUsed != "" && g.MemTotal != "" {
				memCell = fmt.Sprintf("%s/%sM", g.MemUsed, g.MemTotal)
			}
			if g.UtilKnown {
				utilCell = fmt.Sprintf("%s %.0f%%", m.renderPlainBar(g.UtilPct, 6), g.UtilPct)
			}
			if g.TempC != "" {
				thermCell = g.TempC + "C"
			}
			if g.PowerW != "" {
				thermCell += "/" + g.PowerW + "W"
			}
		}
		owner := FirstNonEmpty(g.Assigned, "-")
		rows = append(rows, []string{idx, truncateCells(g.Model, 18), memCell, utilCell, thermCell, owner})
	}
	if focused {
		m.markFocus(1 + m.cursor)
	}
	_ = WriteTable(w, []string{"GPU", "MODEL", "VRAM", "UTIL", "TEMP/PWR", "OWNER"}, rows, noWrap)
}

func (m *DashboardModel) renderMIGSection(w io.Writer) {
	if len(m.snap.MIG) == 0 {
		return
	}
	WriteSection(w, "MIG")
	for _, g := range m.snap.MIG {
		WriteKV(w, "GPU "+g.Index, g.Name+"  MIG "+g.Mode)
		for _, inst := range g.Instances {
			WriteBullet(w, StyleHint.Render(MarkAbsent), inst)
		}
	}
}

func (m *DashboardModel) renderTenantSection(w io.Writer, _ int, focused bool) {
	failed := m.snap.TenantState == SectionFailed
	if m.snap.TenantState == SectionOmitted || (m.cfg.Mode == ModeHost && len(m.snap.Tenants) == 0 && !failed) {
		return
	}
	title := "Tenants"
	if focused && m.cfg.Mode == ModeHost {
		title += " [active]"
	}
	if focused {
		m.markFocus(1)
	}
	WriteSection(w, title)
	if len(m.snap.Tenants) == 0 {
		if failed {
			WriteHint(w, "tenant read failed; see the warning above")
		} else {
			WriteHint(w, "no tenant specs configured")
		}
		return
	}
	rows := make([][]string, 0, len(m.snap.Tenants))
	for i, t := range m.snap.Tenants {
		name := t.Name
		if focused && i == m.cursor {
			name = ">" + name
		}
		port := "-"
		if t.SSHPort > 0 {
			port = strconv.Itoa(t.SSHPort)
		}
		rows = append(rows, []string{name, t.State, port, FirstNonEmpty(t.GPUs, "-"), FirstNonEmpty(t.Spec, "-"), FirstNonEmpty(t.Volume, "-")})
	}
	if focused {
		m.markFocus(1 + m.cursor)
	}
	_ = WriteTable(w, []string{"TENANT", "STATE", "SSH", "GPUS", "SPEC", "VOLUME"}, rows, noWrap)
	if m.snap.Commitment != "" {
		marker, text := OKMark(), m.snap.Commitment
		if m.snap.Overcommitted {
			marker, text = WarnMark(), "overcommitted: "+m.snap.Commitment
		}
		WriteBullet(w, marker, text)
	}
}

func (m *DashboardModel) renderFindingsSection(w io.Writer, width int, focused bool) {
	if len(m.snap.Findings) == 0 {
		return
	}
	title := "Health checks"
	if focused && m.cfg.Mode == ModeHost {
		title += " [active]"
	}
	if focused {
		m.markFocus(1)
	}
	WriteSection(w, title)
	counts := map[string]int{}
	for _, f := range m.snap.Findings {
		counts[f.Level]++
	}
	for i, f := range m.shownFindings() {
		marker := WarnMark()
		if f.Level == "fail" {
			marker = FailMark()
		}
		text := f.Check + ": " + f.Detail
		if focused && i == m.cursor {
			m.markFocus(0)
			text = ">" + text
		}
		WriteBullet(w, marker, truncateCells(text, max(20, width-6)))
	}
	WriteSummary(w, []SummaryCount{
		{Marker: FailMark(), Count: counts["fail"], Label: "failed"},
		{Marker: WarnMark(), Count: counts["warn"], Label: "warnings"},
		{Marker: OKMark(), Count: counts["ok"], Label: "ok"},
	})
}

// shownFindings are the findings the health pane lists and selects from.
func (m *DashboardModel) shownFindings() []FindingDashRow {
	var shown []FindingDashRow
	for _, f := range m.snap.Findings {
		if f.Level != "ok" && f.Level != "skip" {
			shown = append(shown, f)
		}
	}
	return shown
}

func (m *DashboardModel) renderFleetSection(w io.Writer, _ int) {
	WriteSection(w, fmt.Sprintf("Fleet (%d host(s))", len(m.snap.Fleet)))
	if len(m.snap.Fleet) == 0 {
		if m.snap.Err != "" {
			WriteHint(w, "fleet read failed; see the warning above")
		} else {
			WriteHint(w, "no hosts selected")
		}
		return
	}
	rows := make([][]string, 0, len(m.snap.Fleet))
	counts := map[string]int{}
	for i, h := range m.snap.Fleet {
		switch h.State {
		case "ok":
			counts["ok"]++
		case "drift", "findings":
			counts["warn"]++
		default:
			counts["fail"]++
		}
		host := h.Host
		if i == m.cursor {
			host = ">" + host
		}
		switch m.snap.FleetView {
		case "gpu":
			rows = append(rows, []string{host, h.State, FirstNonEmpty(h.GPUs, "-"), FirstNonEmpty(h.Reason, "-")})
		case "users":
			rows = append(rows, []string{host, h.State, FirstNonEmpty(h.Users, "-"), FirstNonEmpty(h.Reason, "-")})
		default:
			rows = append(rows, []string{host, h.State, FirstNonEmpty(h.Shutdown, "-"), FirstNonEmpty(h.Reason, "-")})
		}
	}
	m.markFocus(1 + m.cursor)
	switch m.snap.FleetView {
	case "gpu":
		_ = WriteTable(w, []string{"HOST", "STATE", "GPUS", "REASON"}, rows, noWrap)
	case "users":
		_ = WriteTable(w, []string{"HOST", "STATE", "USERS", "REASON"}, rows, noWrap)
	default:
		_ = WriteTable(w, []string{"HOST", "STATE", "SHUTDOWN", "REASON"}, rows, noWrap)
	}
	WriteSummary(w, []SummaryCount{
		{Marker: FailMark(), Count: counts["fail"], Label: "error"},
		{Marker: WarnMark(), Count: counts["warn"], Label: "drift"},
		{Marker: OKMark(), Count: counts["ok"], Label: "ok"},
	})
}

func (m *DashboardModel) renderBar(pct float64, cells int) string {
	level := "ok"
	switch {
	case pct >= 90:
		level = "fail"
	case pct >= 70:
		level = "warn"
	}
	return m.renderLevelBar(pct, cells, level)
}

// renderLevelBar colors a gauge by a computed severity, so a disk bar agrees
// with the doctor thresholds that produced its level.
func (m *DashboardModel) renderLevelBar(pct float64, cells int, level string) string {
	bar := m.renderPlainBar(pct, cells)
	if !m.cfg.Color {
		return bar
	}
	switch level {
	case "fail":
		return StyleError.Render(bar)
	case "warn":
		return StyleWarning.Render(bar)
	default:
		return StyleSuccess.Render(bar)
	}
}

func (m *DashboardModel) renderPlainBar(pct float64, cells int) string {
	if cells < 4 {
		cells = 4
	}
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(math.Round((pct / 100) * float64(cells)))
	if filled > cells {
		filled = cells
	}
	fillChar, emptyChar := "█", "░"
	if m.cfg.ASCII || !m.cfg.Color {
		fillChar, emptyChar = "#", "."
	}
	return "[" + strings.Repeat(fillChar, filled) + strings.Repeat(emptyChar, cells-filled) + "]"
}

func truncateCells(text string, limit int) string {
	if limit <= 0 || ansi.StringWidth(text) <= limit {
		return text
	}
	if limit <= 3 {
		return ansi.Truncate(text, limit, "")
	}
	return ansi.Truncate(text, limit, "...")
}

func clampLinesWidth(text string, width int) string {
	if width <= 0 {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if ansi.StringWidth(line) > width {
			lines[i] = ansi.Truncate(line, width, "...")
		}
	}
	return strings.Join(lines, "\n")
}

// ErrNoDashboardTerminal is what RunDashboard returns when it cannot draw or
// read keys; --yes does not apply to it.
var ErrNoDashboardTerminal = errors.New("no terminal for the live dashboard; run it from an interactive terminal without redirecting its output")

// DashboardTerminal reports whether the dashboard can draw on out and read
// keys: out is a terminal, input comes from a terminal, and TERM is not dumb
// (the alternate screen needs cursor control). A variable so tests can pin it.
var DashboardTerminal = func(out io.Writer) bool {
	return os.Getenv("TERM") != "dumb" && Terminal(out) && TerminalAttached()
}

// RunDashboard starts the interactive Bubble Tea program on out. It requires
// an attached terminal and cancels cleanly when ctx is cancelled.
func RunDashboard(ctx context.Context, out io.Writer, cfg DashboardConfig, refresh RefreshFunc) error {
	if !DashboardTerminal(out) {
		return fmt.Errorf("%s: %w", FirstNonEmpty(cfg.Title, "rootfiles monitor"), ErrNoDashboardTerminal)
	}
	// Quitting cancels a refresh still in flight (fleet SSH reads, module
	// queries), which Bubble Tea's own context does not reach.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var initial DashboardSnapshot
	if refresh != nil {
		snap, err := refresh(ctx)
		if err != nil {
			initial.Err = err.Error()
		} else {
			initial = snap
			if initial.UpdatedAt.IsZero() {
				initial.UpdatedAt = time.Now()
			}
		}
	}
	model := NewDashboardModel(ctx, cfg, initial, refresh)
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithOutput(teaOutput(out)), tea.WithContext(ctx))
	_, err := p.Run()
	return err
}

// teaOutput hands Bubble Tea the raw stream: it writes cursor and screen
// control sequences that a color-stripping writer would remove, and it sizes
// the screen from the stream's file descriptor. The model applies color itself.
func teaOutput(out io.Writer) io.Writer {
	if o, ok := out.(*outputWriter); ok {
		return o.Writer
	}
	return out
}
