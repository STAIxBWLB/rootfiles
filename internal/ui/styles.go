package ui

import (
	"fmt"

	"charm.land/lipgloss/v2"
)

// Role is the semantic API for CLI styles. Palette entries live only here.
type Role string

const (
	RoleHeader  Role = "header"
	RoleSection Role = "section"
	RoleKey     Role = "key"
	RoleValue   Role = "value"
	RoleSuccess Role = "success"
	RoleWarning Role = "warning"
	RoleError   Role = "error"
	RoleHint    Role = "hint"
	RoleAccent  Role = "accent"
)

var roles = []Role{RoleHeader, RoleSection, RoleKey, RoleValue, RoleSuccess, RoleWarning, RoleError, RoleHint, RoleAccent}

type palette struct {
	background string // reference terminal background; never painted by rootfiles
	foreground string // reference default foreground; values never override it
	colors     map[Role]string
}

var palettes = map[string]palette{
	"ansi":              {colors: map[Role]string{RoleHeader: "4", RoleSection: "5", RoleKey: "4", RoleValue: "", RoleSuccess: "2", RoleWarning: "3", RoleError: "1", RoleHint: "8", RoleAccent: "6"}},
	"tokyo-night-dark":  {background: "#1A1B26", foreground: "#C0CAF5", colors: map[Role]string{RoleHeader: "#7AA2F7", RoleSection: "#BB9AF7", RoleKey: "#7AA2F7", RoleValue: "", RoleSuccess: "#9ECE6A", RoleWarning: "#E0AF68", RoleError: "#F7768E", RoleHint: "#787FAF", RoleAccent: "#7DCFFF"}},
	"tokyo-night-light": {background: "#E1E2E7", foreground: "#343B58", colors: map[Role]string{RoleHeader: "#245DB0", RoleSection: "#7847BD", RoleKey: "#245DB0", RoleValue: "", RoleSuccess: "#326322", RoleWarning: "#765019", RoleError: "#A5263F", RoleHint: "#747C9B", RoleAccent: "#006C86"}},
}

func roleStyle(p palette, role Role) lipgloss.Style {
	style := lipgloss.NewStyle()
	if color := p.colors[role]; color != "" {
		style = style.Foreground(lipgloss.Color(color))
	}
	switch role {
	case RoleHeader, RoleSection, RoleSuccess, RoleWarning, RoleError:
		style = style.Bold(true)
	}
	return style
}

// Semantic roles use the terminal's 16 ANSI colors by default. Stream policy is
// applied by ConfigureWriter, since lipgloss v2 Render does not detect a terminal.
var (
	StyleHeader  = roleStyle(palettes["ansi"], RoleHeader)
	StyleSection = roleStyle(palettes["ansi"], RoleSection)
	StyleKey     = roleStyle(palettes["ansi"], RoleKey)
	StyleValue   = roleStyle(palettes["ansi"], RoleValue)
	StyleSuccess = roleStyle(palettes["ansi"], RoleSuccess)
	StyleWarning = roleStyle(palettes["ansi"], RoleWarning)
	StyleError   = roleStyle(palettes["ansi"], RoleError)
	StyleHint    = roleStyle(palettes["ansi"], RoleHint)
	StyleAccent  = roleStyle(palettes["ansi"], RoleAccent)
)

type styleSet struct{ header, section, key, value, success, warning, failure, hint, accent lipgloss.Style }

func captureStyles() styleSet {
	return styleSet{StyleHeader, StyleSection, StyleKey, StyleValue, StyleSuccess, StyleWarning, StyleError, StyleHint, StyleAccent}
}

func (s styleSet) apply() {
	StyleHeader, StyleSection, StyleKey, StyleValue, StyleSuccess, StyleWarning, StyleError, StyleHint, StyleAccent = s.header, s.section, s.key, s.value, s.success, s.warning, s.failure, s.hint, s.accent
}

func stylesFor(p palette) styleSet {
	return styleSet{
		roleStyle(p, RoleHeader),
		roleStyle(p, RoleSection),
		roleStyle(p, RoleKey),
		roleStyle(p, RoleValue),
		roleStyle(p, RoleSuccess),
		roleStyle(p, RoleWarning),
		roleStyle(p, RoleError),
		roleStyle(p, RoleHint),
		roleStyle(p, RoleAccent),
	}
}

// SetPresentation selects an explicitly requested theme for one command. The
// CLI executes one command per process; the caller defers the restoration even
// on command failure. The second result is an advisory warning; the restore
// function is always usable. Unknown themes fall back to ANSI.
func SetPresentation(getenv func(string) string) (func(), string) {
	envKey := "ROOTFILES_COLOR_THEME"
	name := getenv(envKey)
	if name == "" {
		envKey = "DOT_COLOR_THEME"
		name = getenv(envKey)
	}
	if name == "" {
		name = "ansi"
	}
	p, ok := palettes[name]
	warning := ""
	if !ok {
		warning = fmt.Sprintf("unknown %s %q; using ansi; valid choices: ansi, tokyo-night-dark, tokyo-night-light", envKey, name)
		p = palettes["ansi"]
	}
	previousStyles := captureStyles()
	stylesFor(p).apply()
	return func() { previousStyles.apply() }, warning
}

// RoleStyle returns the current semantic style. Named aliases remain available
// for existing reports; new renderers can use the role vocabulary directly.
func RoleStyle(role Role) lipgloss.Style {
	switch role {
	case RoleHeader:
		return StyleHeader
	case RoleSection:
		return StyleSection
	case RoleKey:
		return StyleKey
	case RoleSuccess:
		return StyleSuccess
	case RoleWarning:
		return StyleWarning
	case RoleError:
		return StyleError
	case RoleHint:
		return StyleHint
	case RoleAccent:
		return StyleAccent
	default:
		return StyleValue
	}
}
