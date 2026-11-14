package ui

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

// terminalFD is a seam for host-free per-stream conformance tests.
var terminalFD = term.IsTerminal

// Terminal reports whether this writer targets a terminal, including wrappers.
func Terminal(w io.Writer) bool {
	if out, ok := w.(*outputWriter); ok {
		return Terminal(out.Writer)
	}
	fd, ok := w.(interface{ Fd() uintptr })
	return ok && terminalFD(fd.Fd())
}

// ResolveColor applies standard v1 precedence independently for each stream.
func ResolveColor(w io.Writer, mode string) bool {
	switch mode {
	case "always":
		return true
	case "never":
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	force := os.Getenv("FORCE_COLOR")
	cliForce := os.Getenv("CLICOLOR_FORCE")
	if (force != "" && force != "0") || (cliForce != "" && cliForce != "0") {
		return true
	}
	if force == "0" || os.Getenv("CLICOLOR") == "0" {
		return false
	}
	return Terminal(w) && os.Getenv("TERM") != "dumb"
}

// ASCIIMarkers follows locale precedence rather than guessing from the terminal.
func ASCIIMarkers(explicit bool) bool {
	if explicit {
		return true
	}
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if locale := os.Getenv(name); locale != "" {
			locale = strings.ToLower(locale)
			return !strings.Contains(locale, "utf-8") && !strings.Contains(locale, "utf8")
		}
	}
	return true
}

// OutputWidth returns the terminal width, or an explicit COLUMNS on a pipe.
func OutputWidth(w io.Writer) int {
	if out, ok := w.(*outputWriter); ok {
		return OutputWidth(out.Writer)
	}
	if fd, ok := w.(interface{ Fd() uintptr }); ok && Terminal(w) {
		if width, _, err := term.GetSize(fd.Fd()); err == nil {
			return width
		}
	}
	width, _ := strconv.Atoi(os.Getenv("COLUMNS"))
	if width < 0 {
		return 0
	}
	return width
}

// Only semantic markers in a row prefix are substituted; glyphs in values stay verbatim.
var asciiLineMarkers = regexp.MustCompile("(?m)^[ \t]*(?:\x1b\\[[0-9;]*m)*(?:✓|✗|⚠|→|▸)(?:\x1b\\[[0-9;]*m)*(?: |$)")

// WrapText wraps only free text chosen by the caller. Tables must preserve their
// id/state columns and pass their flexible cell here with its available width.
func WrapText(text string, width int) string {
	if width <= 0 {
		return text
	}
	return ansi.Wrap(text, width, "")
}

type outputWriter struct {
	io.Writer
	color bool
	ascii bool
}

// ConfigureWriter owns decoration at the final stream boundary. Machine output
// bypasses every transformation, including wrapping and ASCII substitution.
func ConfigureWriter(w io.Writer, mode string, ascii, json bool) io.Writer {
	if old, ok := w.(*outputWriter); ok {
		w = old.Writer
	}
	if json {
		return w
	}
	return &outputWriter{w, ResolveColor(w, mode), ASCIIMarkers(ascii)}
}

func (w *outputWriter) Write(p []byte) (int, error) {
	s := string(p)
	if !w.color {
		s = ansi.Strip(s)
	}
	if w.ascii {
		s = asciiLineMarkers.ReplaceAllStringFunc(s, func(line string) string {
			return asciiReplacer.Replace(line)
		})
	}
	n, err := io.WriteString(w.Writer, s)
	if err == nil && n != len(s) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func displayWriter(w io.Writer) io.Writer {
	if _, ok := w.(*outputWriter); ok {
		return w
	}
	return ConfigureWriter(w, "auto", false, false)
}

// WriteError prints only a supplied fix. Marker presence depends on the error
// stream's TTY, independently of whether explicit color was requested.
func WriteError(w io.Writer, msg, next string) {
	tty := Terminal(w)
	out := displayWriter(w)
	prefix := ""
	if tty {
		prefix = FailMark() + " "
	}
	fmt.Fprintln(out, prefix+"rootfiles: "+msg)
	if next != "" {
		fmt.Fprintln(out, "  "+StyleAccent.Render(MarkNext+" "+next))
	}
}
