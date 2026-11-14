package ui

import (
	"github.com/charmbracelet/x/ansi"
	"strings"
)

const (
	MarkOK      = "✓"
	MarkFail    = "✗"
	MarkWarn    = "⚠"
	MarkAbsent  = "-"
	MarkUnknown = "?"
	MarkPending = "~"
	MarkActive  = "*"
	MarkNext    = "→"
	MarkSection = "▸"
)

var asciiReplacer = stringsReplacer()

func stringsReplacer() *strings.Replacer {
	return strings.NewReplacer(MarkOK, "ok", MarkFail, "x", MarkWarn, "!", MarkNext, "->", MarkSection, ">")
}

// MarkerCell reserves two cells for every semantic marker.
func MarkerCell(marker string, ascii bool) string {
	if ascii {
		marker = asciiReplacer.Replace(marker)
	}
	if n := 2 - ansi.StringWidth(marker); n > 0 {
		marker += strings.Repeat(" ", n)
	}
	return marker
}

func OKMark() string      { return StyleSuccess.Render(MarkOK) }
func FailMark() string    { return StyleError.Render(MarkFail) }
func PendingMark() string { return StyleHint.Render(MarkPending) }
func WarnMark() string    { return StyleWarning.Render(MarkWarn) }
func NextMark() string    { return StyleAccent.Render(MarkNext) }
