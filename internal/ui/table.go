package ui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// WriteTable renders borderless report rows. Non-flexible columns are capped
// at 32 cells and shortened visibly, preserving identifiers as single cells.
// Callers must offer --json or a detail command for the complete values.
func WriteTable(w io.Writer, headings []string, rows [][]string, flexible int) {
	columns := make([]int, 0, len(headings))
	for i := range headings {
		present := false
		for _, row := range rows {
			if i < len(row) && row[i] != "" && row[i] != "-" {
				present = true
				break
			}
		}
		if present {
			columns = append(columns, i)
		}
	}
	if len(columns) == 0 {
		return
	}
	widths := make(map[int]int, len(columns))
	for _, i := range columns {
		widths[i] = min(32, ansi.StringWidth(headings[i]))
		for _, row := range rows {
			if i < len(row) {
				widths[i] = max(widths[i], min(32, ansi.StringWidth(row[i])))
			}
		}
	}
	limit := OutputWidth(w)
	if limit > 0 {
		fixed := 2 + 2*(len(columns)-1)
		for _, i := range columns {
			if i != flexible {
				fixed += widths[i]
			}
		}
		if _, exists := widths[flexible]; exists {
			widths[flexible] = max(1, limit-fixed)
		}
	} else if _, exists := widths[flexible]; exists {
		for _, row := range rows {
			if flexible < len(row) {
				widths[flexible] = max(widths[flexible], ansi.StringWidth(row[flexible]))
			}
		}
	}
	out := displayWriter(w)
	printRow := func(row []string, heading bool) {
		cells := make(map[int][]string, len(columns))
		height := 1
		for _, i := range columns {
			value := ""
			if i < len(row) {
				value = row[i]
			}
			if value == "" {
				value = "-"
			}
			if heading {
				value = strings.ToUpper(value)
			}
			if i == flexible {
				cells[i] = strings.Split(WrapText(value, widths[i]), "\n")
			} else {
				cells[i] = []string{shortenCell(value, widths[i])}
			}
			height = max(height, len(cells[i]))
		}
		for line := 0; line < height; line++ {
			parts := make([]string, 0, len(columns))
			for _, i := range columns {
				value := ""
				if line < len(cells[i]) {
					value = cells[i][line]
				}
				padding := strings.Repeat(" ", max(0, widths[i]-ansi.StringWidth(value)))
				if heading {
					value = StyleKey.Render(value)
				} else if value == "-" {
					value = StyleHint.Render(value)
				}
				parts = append(parts, value+padding)
			}
			fmt.Fprintln(out, "  "+strings.TrimRight(strings.Join(parts, "  "), " "))
		}
	}
	printRow(headings, true)
	for _, row := range rows {
		printRow(row, false)
	}
}

func shortenCell(value string, width int) string {
	if ansi.StringWidth(value) <= width {
		return value
	}
	if width <= 3 {
		return strings.Repeat(".", width)
	}
	// Strip styling only for a shortened value, so no SGR sequence can be split.
	value = ansi.Strip(value)
	left := (width - 3 + 1) / 2
	right := width - 3 - left
	return ansi.Truncate(value, left, "") + "..." + ansi.TruncateLeft(value, ansi.StringWidth(value)-right, "")
}
