package ui

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestHumanRelationASCIIPolicyKeepsMachineBytes(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		name := "locale"
		if explicit {
			name = "flag"
		}
		t.Run(name, func(t *testing.T) {
			cleanOutputEnv(t)
			if !explicit {
				t.Setenv("LC_ALL", "C")
			}
			const original = "Current: old " + Relation + " Target: new\n"
			var human bytes.Buffer
			WriteText(ConfigureWriter(&human, "never", explicit, false), "%s", original)
			if want := "Current: old -> Target: new\n"; human.String() != want {
				t.Fatalf("human=%q want %q", human.String(), want)
			}
			var raw bytes.Buffer
			// Raw commands use the same ConfigureWriter bypass as machine JSON.
			if _, err := io.WriteString(ConfigureWriter(&raw, "never", explicit, true), original); err != nil {
				t.Fatal(err)
			}
			if raw.String() != original {
				t.Fatalf("raw relation bytes changed: %q", raw.String())
			}
			payload := map[string]string{"relation": Relation}
			var machine, baseline bytes.Buffer
			if err := json.NewEncoder(ConfigureWriter(&machine, "always", explicit, true)).Encode(payload); err != nil {
				t.Fatal(err)
			}
			if err := json.NewEncoder(&baseline).Encode(payload); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(machine.Bytes(), baseline.Bytes()) || !strings.Contains(machine.String(), Relation) {
				t.Fatalf("machine bytes changed: %q want %q", machine.String(), baseline.String())
			}
		})
	}
}

func TestTableFlexibleColumnKeepsReadableMinimum(t *testing.T) {
	cleanOutputEnv(t)
	t.Setenv("COLUMNS", "40")
	host := strings.Repeat("h", 32)
	for _, heading := range []string{"reason", strings.Repeat("r", 24)} {
		t.Run(heading, func(t *testing.T) {
			var out bytes.Buffer
			minimum := max(20, ansi.StringWidth(strings.ToUpper(heading)))
			reason := strings.Repeat("r", minimum*2)
			if err := WriteTable(ConfigureWriter(&out, "never", false, false), []string{"host", "state", heading}, [][]string{{host, "ok", reason}}, 2); err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
			if len(lines) != 3 {
				t.Fatalf("reason fragmented across %d lines: %q", len(lines), out.String())
			}
			prefix := 2 + 32 + 2 + len("STATE") + 2
			for _, line := range lines[1:] {
				if got := ansi.StringWidth(line) - prefix; got != minimum {
					t.Fatalf("flexible width=%d want %d: %q", got, minimum, line)
				}
			}
			if !strings.Contains(lines[1], host) || ansi.StringWidth(lines[1]) <= 40 {
				t.Fatalf("narrow table failed to preserve host and overflow: %q", lines[1])
			}
		})
	}
}

type tableFailWriter struct {
	calls, failAt int
	first, later  error
}

func (w *tableFailWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == w.failAt {
		return 0, w.first
	}
	if w.calls > w.failAt {
		return 0, w.later
	}
	return len(p), nil
}

func TestTableReturnsFirstWriteErrorAndStops(t *testing.T) {
	cleanOutputEnv(t)
	for _, failAt := range []int{1, 2} {
		first, later := errors.New("first table failure"), errors.New("later table failure")
		out := &tableFailWriter{failAt: failAt, first: first, later: later}
		err := WriteTable(out, []string{"host", "reason"}, [][]string{{"one", "healthy"}, {"two", "healthy"}}, 1)
		if !errors.Is(err, first) || out.calls != failAt {
			t.Fatalf("failAt=%d error=%v calls=%d", failAt, err, out.calls)
		}
	}
}

func TestWideTableSuccessfulBytesUnchanged(t *testing.T) {
	cleanOutputEnv(t)
	t.Setenv("COLUMNS", "80")
	var out bytes.Buffer
	if err := WriteTable(&out, []string{"host", "state", "reason"}, [][]string{{"node", "ok", "healthy"}}, 2); err != nil {
		t.Fatal(err)
	}
	if want := "  HOST  STATE  REASON\n  node  ok     healthy\n"; out.String() != want {
		t.Fatalf("wide table=%q want %q", out.String(), want)
	}
}

func TestWriteRawKeepsRelationThroughHumanASCIIWriter(t *testing.T) {
	var out bytes.Buffer
	payload := "raw " + Relation + " value without newline"
	WriteRaw(ConfigureWriter(&out, "never", true, false), payload)
	if out.String() != payload {
		t.Fatalf("raw machine payload changed: %q", out.String())
	}
}
