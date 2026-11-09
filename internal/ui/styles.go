package ui

import "github.com/charmbracelet/lipgloss"

// Styles for consistent output across the CLI.
//
// Colour semantics (glyph layer is in markers.go):
//   - StyleHeader   page/report titles — blue, bold, padded
//   - StyleSection  section dividers   — purple, bold
//   - StyleKey      left-side labels in key/value rows — green, padded to 14
//   - StyleValue    right-side values in key/value rows — light blue
//   - StyleSuccess  positive status (MarkOK) — green, bold
//   - StyleWarning  non-fatal attention (MarkWarn) — orange, bold
//   - StyleError    hard failure (MarkFail) — red, bold
//   - StyleHint     secondary info, neutral-missing, (unset) placeholders — gray
//
// lipgloss detects terminal capabilities via termenv, so styles automatically
// degrade to plain text when stdout is not a TTY or NO_COLOR is set. No manual
// gating needed.
var (
	StyleHeader = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7AA2F7")).
			Padding(0, 1)

	StyleSection = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#BB9AF7"))

	StyleKey = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#9ECE6A")).
			Width(14)

	StyleValue = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#C0CAF5"))

	StyleSuccess = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#9ECE6A")).
			Bold(true)

	StyleWarning = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E0AF68")).
			Bold(true)

	StyleError = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F7768E")).
			Bold(true)

	StyleHint = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#565F89"))
)
