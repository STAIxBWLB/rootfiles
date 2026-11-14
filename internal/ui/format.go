package ui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// WriteHeader writes one report title line.
func WriteHeader(w io.Writer, title string) {
	fmt.Fprintln(displayWriter(w), StyleHeader.Render(" "+title))
}

// WriteSection writes a section divider with one leading blank line and the
// canonical "▸ " prefix. Callers pass the section name only (plus an optional
// trailing annotation such as a count — "Modules (8/10 satisfied)").
func WriteSection(w io.Writer, title string) {
	fmt.Fprintln(displayWriter(w))
	fmt.Fprintln(displayWriter(w), StyleSection.Render(MarkSection+" "+title))
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
	if ansi.StringWidth(key+":") > 28 {
		fmt.Fprintf(displayWriter(w), "  %s\n%s\n", StyleKey.Render(key+":"), hangingText(w, "    ", value))
		return
	}
	label := key + ":"
	label += strings.Repeat(" ", max(0, 14-ansi.StringWidth(label)))
	fmt.Fprintln(displayWriter(w), hangingText(w, "  "+StyleKey.Render(label)+"  ", value))
}

// WriteHint writes a single dimmed hint line indented under a section.
// Used for footnote-style messages like "run 'rootfiles check' for details".
func WriteHint(w io.Writer, msg string) {
	fmt.Fprintln(displayWriter(w), hangingText(w, "  ", StyleHint.Render(msg)))
}

// WriteBullet writes an indented "<marker>  <text>" row. Used for module
// status lists and allocation rows where every entry wants the same 2-space
// outer indent as WriteKV but no key-width alignment.
func WriteBullet(w io.Writer, marker, text string) {
	ascii := ASCIIMarkers(false)
	if out, ok := w.(*outputWriter); ok {
		ascii = out.ascii
	}
	fmt.Fprintln(displayWriter(w), hangingText(w, "  "+MarkerCell(marker, ascii)+" ", text))
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
func WriteText(w io.Writer, format string, args ...any) {
	fmt.Fprintf(displayWriter(w), format, args...)
}

// WriteLine writes a plain line; an empty argument list writes a blank line.
func WriteLine(w io.Writer, args ...any) { fmt.Fprintln(displayWriter(w), args...) }

// WriteRaw writes a payload without interpreting format verbs or adding bytes.
func WriteRaw(w io.Writer, text string) { io.WriteString(w, text) }

// hangingText wraps only the free-text part, preserving the label/marker column.
func hangingText(w io.Writer, prefix, text string) string {
	width := OutputWidth(w)
	indent := ansi.StringWidth(prefix)
	if width <= indent {
		return prefix + text
	}
	lines := strings.Split(WrapText(text, width-indent), "\n")
	return prefix + strings.Join(lines, "\n"+strings.Repeat(" ", indent))
}
