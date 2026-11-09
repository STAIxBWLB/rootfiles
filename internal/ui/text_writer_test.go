package ui

import (
	"bytes"
	"testing"
)

func TestPlainWriterHelpersKeepPayloadBytes(t *testing.T) {
	var out bytes.Buffer
	WriteText(&out, "%-5s %d", "name", 7)
	WriteLine(&out)
	WriteRaw(&out, "100% <&>\n")
	WriteLine(&out, "alice")
	if got, want := out.String(), "name  7\n100% <&>\nalice\n"; got != want {
		t.Fatalf("payload bytes = %q, want %q", got, want)
	}
}
