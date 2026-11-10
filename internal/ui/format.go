package ui

import (
	"fmt"
	"io"
)

// WriteHeader writes a styled report title to w with one leading blank line.
// The title is wrapped in spaces so the blue StyleHeader background frames the
// text cleanly on terminals that show it.
func WriteHeader(w io.Writer, title string) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, StyleHeader.Render(" "+title+" "))
}

// WriteSection writes a section divider with one leading blank line and the
// canonical "▸ " prefix. Callers pass the section name only (plus an optional
// trailing annotation such as a count — "Modules (8/10 satisfied)").
func WriteSection(w io.Writer, title string) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, StyleSection.Render("▸ "+title))
}

// WriteKV writes a styled key/value row at the 2-space top-level indent.
// An empty value renders as "(unset)" in the hint style, matching the
// dotfiles convention so mixed CLI muscle-memory works across both tools.
func WriteKV(w io.Writer, key, value string) {
	if value == "" {
		value = StyleHint.Render("(unset)")
	} else {
		value = StyleValue.Render(value)
	}
	fmt.Fprintf(w, "  %s  %s\n", StyleKey.Render(key+":"), value)
}

// WriteHint writes a single dimmed hint line indented under a section.
// Used for footnote-style messages like "run 'rootfiles check' for details".
func WriteHint(w io.Writer, msg string) {
	fmt.Fprintln(w, "  "+StyleHint.Render(msg))
}

// WriteBullet writes an indented "<marker>  <text>" row. Used for module
// status lists and allocation rows where every entry wants the same 2-space
// outer indent as WriteKV but no key-width alignment.
func WriteBullet(w io.Writer, marker, text string) {
	fmt.Fprintf(w, "  %s  %s\n", marker, text)
}

// FirstNonEmpty returns the first nonempty display value without trimming it.
func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// WriteText writes formatted plain text without adding styling or a newline.
// Tables and byte-sensitive payloads retain their caller's format.
func WriteText(w io.Writer, format string, args ...any) { fmt.Fprintf(w, format, args...) }

// WriteLine writes a plain line; an empty argument list writes a blank line.
func WriteLine(w io.Writer, args ...any) { fmt.Fprintln(w, args...) }

// WriteRaw writes a payload without interpreting format verbs or adding bytes.
func WriteRaw(w io.Writer, text string) { io.WriteString(w, text) }
