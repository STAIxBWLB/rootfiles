package fleet

import (
	"os/exec"
	"testing"
)

func TestShellQuoteWireBytesAndRoundTrip(t *testing.T) {
	for _, tt := range []struct{ value, wire string }{
		{"", "''"}, {"a b", "'a b'"}, {"a'b", "'a'\\''b'"},
		{"$HOME; $(false) `false`", "'$HOME; $(false) `false`'"},
		{"line\nnext", "'line\nnext'"}, {"--on-calendar", "'--on-calendar'"},
	} {
		quoted := ShellQuote(tt.value)
		if quoted != tt.wire {
			t.Errorf("quote %q = %q, want %q", tt.value, quoted, tt.wire)
		}
		// The fixed command only prints one quoted synthetic argument.
		out, err := exec.Command("/bin/sh", "-c", "printf '%s' "+quoted).Output()
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != tt.value {
			t.Errorf("round trip = %q, want %q", out, tt.value)
		}
	}
}
