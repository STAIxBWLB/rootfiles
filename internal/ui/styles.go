package ui

import "charm.land/lipgloss/v2"

// Semantic roles use the terminal's 16 ANSI colors. Stream policy is applied
// by ConfigureWriter, since lipgloss v2 Render does not detect a terminal.
var (
	StyleHeader  = lipgloss.NewStyle().Foreground(lipgloss.Color("4")).Bold(true)
	StyleSection = lipgloss.NewStyle().Foreground(lipgloss.Color("5")).Bold(true)
	StyleKey     = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	StyleValue   = lipgloss.NewStyle()
	StyleSuccess = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)
	StyleWarning = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)
	StyleError   = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	StyleHint    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	StyleAccent  = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
)
