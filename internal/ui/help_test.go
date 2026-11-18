package ui

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

func assertHangingWidth(t *testing.T, output string, width int) {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if got := ansi.StringWidth(line); got > width {
			t.Errorf("line width %d > %d: %q", got, width, line)
		}
	}
	if !utf8.ValidString(output) {
		t.Fatal("wrapping broke UTF-8")
	}
}

func TestWriteHangingWrapsLongWordsAt80(t *testing.T) {
	t.Setenv("COLUMNS", "80")
	var out bytes.Buffer
	prefix := "  item  "
	text := strings.Repeat("x", 200)
	WriteHanging(ConfigureWriter(&out, "never", false, false), prefix, text)
	want := prefix + strings.Repeat("x", 72) + "\n" + strings.Repeat(" ", 8) + strings.Repeat("x", 72) + "\n" + strings.Repeat(" ", 8) + strings.Repeat("x", 56) + "\n"
	if got := out.String(); got != want {
		t.Fatalf("hanging long word=%q, want %q", got, want)
	}
	assertHangingWidth(t, out.String(), 80)
}

func TestWriteHangingUsesStyledUnicodePrefixCells(t *testing.T) {
	t.Setenv("COLUMNS", "80")
	var out bytes.Buffer
	prefix := StyleKey.Render("  이름:  ")
	text := StyleWarning.Render(strings.Repeat("한", 100))
	WriteHanging(ConfigureWriter(&out, "always", false, false), prefix, text)
	got := out.String()
	if !strings.HasPrefix(got, prefix) {
		t.Fatal("styled prefix was replaced or split")
	}
	if !strings.ContainsRune(got, 27) {
		t.Fatal("ANSI styles were dropped")
	}
	plain := ansi.Strip(got)
	lines := strings.Split(strings.TrimSuffix(plain, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatal("wide text did not wrap")
	}
	indent := strings.Repeat(" ", 9)
	if !strings.HasPrefix(lines[1], indent) {
		t.Fatalf("continuation did not use 9 display cells: %q", lines[1])
	}
	var body strings.Builder
	body.WriteString(strings.TrimPrefix(lines[0], "  이름:  "))
	for _, line := range lines[1:] {
		body.WriteString(strings.TrimPrefix(line, indent))
	}
	if body.String() != strings.Repeat("한", 100) {
		t.Fatal("Unicode text was truncated or duplicated")
	}
	assertHangingWidth(t, got, 80)
}

func TestWriteHangingRetainsExplicitLinesAndParagraphs(t *testing.T) {
	t.Setenv("COLUMNS", "80")
	for _, tc := range []struct{ name, text, want string }{
		{"empty", "", "key: \n"},
		{"one line", "first", "key: first\n"},
		{"terminal newline", "first\n", "key: first\n"},
		{"lines and paragraph", "first\nsecond\n\nthird\n", "key: first\n     second\n\n     third\n"},
		{"trailing paragraph", "first\n\n", "key: first\n\n"},
		{"leading paragraph", "\nfirst", "key: \n     first\n"},
		{"CRLF", "first\r\n\r\nsecond", "key: first\n\n     second\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			WriteHanging(ConfigureWriter(&out, "never", false, false), "key: ", tc.text)
			if got := out.String(); got != tc.want {
				t.Fatalf("paragraph output=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestWriteHangingLongPrefixGetsOwnLines(t *testing.T) {
	t.Setenv("COLUMNS", "80")
	for _, tc := range []struct{ name, prefix, text, want string }{
		{"over width", strings.Repeat("p", 100), "body", strings.Repeat("p", 80) + "\n" + strings.Repeat("p", 20) + "\n  body\n"},
		{"no room for wide glyph", strings.Repeat("p", 79), "한글", strings.Repeat("p", 79) + "\n  한글\n"},
		{"empty description", strings.Repeat("p", 100), "", strings.Repeat("p", 80) + "\n" + strings.Repeat("p", 20) + "\n"},
		{"paragraph after label", strings.Repeat("p", 80), "first\n\nsecond", strings.Repeat("p", 80) + "\n  first\n\n  second\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			WriteHanging(ConfigureWriter(&out, "never", false, false), tc.prefix, tc.text)
			if got := out.String(); got != tc.want {
				t.Fatalf("long prefix output=%q, want %q", got, tc.want)
			}
			assertHangingWidth(t, out.String(), 80)
		})
	}
}

func TestWriteHangingPipeWithoutColumnsDoesNotWrap(t *testing.T) {
	t.Setenv("COLUMNS", "")
	var out bytes.Buffer
	prefix := "key: "
	text := strings.Repeat("x", 120) + "\n\nlast"
	WriteHanging(ConfigureWriter(&out, "never", false, false), prefix, text)
	want := prefix + strings.Repeat("x", 120) + "\n\n     last\n"
	if got := out.String(); got != want {
		t.Fatalf("unrequested pipe wrapping: %q", got)
	}
}
