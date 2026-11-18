package ui

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestUnattendedPromptDefaults(t *testing.T) {
	saved := TerminalAttached
	TerminalAttached = func() bool { return false }
	t.Cleanup(func() { TerminalAttached = saved })

	if got, err := Confirm("Continue?", true); err != nil || !got {
		t.Fatalf("Confirm() = (%v, %v), want (true, nil)", got, err)
	}
	if got, err := ConfirmDefault("Disable root?", false, true); err != nil || got {
		t.Fatalf("ConfirmDefault() = (%v, %v), want (false, nil)", got, err)
	}
	if got, err := Select("Profile", []string{"minimal", "dgx"}, "dgx", true); err != nil || got != "dgx" {
		t.Fatalf("Select() = (%q, %v), want (\"dgx\", nil)", got, err)
	}
	if got, err := Input("Timezone", "Asia/Seoul", true); err != nil || got != "Asia/Seoul" {
		t.Fatalf("Input() = (%q, %v), want (\"Asia/Seoul\", nil)", got, err)
	}
	if got, err := InputInt("SSH port", 22, true); err != nil || got != 22 {
		t.Fatalf("InputInt() = (%d, %v), want (22, nil)", got, err)
	}
	if got, err := InputIntSlice("Ports", []int{80, 443}, true); err != nil || !slices.Equal(got, []int{80, 443}) {
		t.Fatalf("InputIntSlice() = (%v, %v), want ([80 443], nil)", got, err)
	}
}

func TestInteractivePromptsRefuseWithoutTerminal(t *testing.T) {
	saved := TerminalAttached
	TerminalAttached = func() bool { return false }
	t.Cleanup(func() { TerminalAttached = saved })

	for _, call := range []struct {
		name string
		fn   func() error
	}{
		{"Confirm", func() error { _, err := Confirm("Continue?", false); return err }},
		{"ConfirmDefault", func() error { _, err := ConfirmDefault("Disable root?", true, false); return err }},
		{"Select", func() error { _, err := Select("Profile", []string{"dgx"}, "dgx", false); return err }},
		{"Input", func() error { _, err := Input("Timezone", "UTC", false); return err }},
		{"InputInt", func() error { _, err := InputInt("SSH port", 22, false); return err }},
		{"InputIntSlice", func() error { _, err := InputIntSlice("Ports", []int{22}, false); return err }},
	} {
		t.Run(call.name, func(t *testing.T) {
			err := call.fn()
			if !errors.Is(err, ErrNoTerminal) {
				t.Fatalf("expected ErrNoTerminal, got %v", err)
			}
			if !strings.Contains(err.Error(), "--yes") {
				t.Fatalf("error missing --yes recovery hint: %v", err)
			}
		})
	}
}

// feedStdin replaces stdin with a pipe holding input, then closed.
func feedStdin(t *testing.T, input string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() { // input may exceed the pipe buffer; closing r ends a blocked write
		_, _ = w.WriteString(input)
		_ = w.Close()
	}()
	saved := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = saved; _ = r.Close() })
}

// Under TERM=dumb a typed answer or an empty line keeps today's behavior,
// and a prompt that reads to the end of stdin fails instead of taking the
// default, including after a rejected answer.
func TestDumbTerminalPromptsNeedAnAnswer(t *testing.T) {
	t.Setenv("TERM", "dumb")
	for _, tc := range []struct {
		input string
		def   bool
		want  bool
	}{
		{"y\n", false, true},
		{"n\n", true, false},
		{"\n", true, true},
		{"\n", false, false},
	} {
		feedStdin(t, tc.input)
		if got, err := ConfirmDefault("Apply?", tc.def, false); err != nil || got != tc.want {
			t.Fatalf("input %q default %v: (%v, %v), want (%v, nil)", tc.input, tc.def, got, err, tc.want)
		}
	}
	feedStdin(t, "lab\n")
	if got, err := Input("Name", "default", false); err != nil || got != "lab" {
		t.Fatalf("Input = (%q, %v), want (\"lab\", nil)", got, err)
	}
	feedStdin(t, "1\n")
	if got, err := Select("Profile", []string{"minimal", "dgx"}, "dgx", false); err != nil || got != "minimal" {
		t.Fatalf("Select = (%q, %v), want (\"minimal\", nil)", got, err)
	}
	// Each prompt reads only its own line, so answers piped together all count.
	feedStdin(t, "y\nn\n")
	first, err1 := ConfirmDefault("First?", false, false)
	second, err2 := ConfirmDefault("Second?", true, false)
	if err1 != nil || err2 != nil || !first || second {
		t.Fatalf("piped answers: (%v, %v), (%v, %v), want (true, nil), (false, nil)", first, err1, second, err2)
	}

	for _, tc := range []struct {
		name   string
		inputs []string
		fn     func() error
	}{
		{"ConfirmDefault", []string{"", "  ", "y", "maybe", "maybe\n"}, func() error { _, err := ConfirmDefault("Apply this configuration?", true, false); return err }},
		{"Select", []string{"", "maybe\n", "99\n", "maybe"}, func() error { _, err := Select("Profile", []string{"minimal", "dgx"}, "dgx", false); return err }},
		{"Input", []string{"", "lab"}, func() error { _, err := Input("Timezone", "UTC", false); return err }},
	} {
		for _, input := range tc.inputs {
			feedStdin(t, input)
			if err := tc.fn(); !errors.Is(err, ErrNoAnswer) || !strings.Contains(err.Error(), "--yes") {
				t.Fatalf("%s with stdin %q: err=%v, want ErrNoAnswer naming --yes", tc.name, input, err)
			}
		}
	}
}

// An answer over the line limit and a failing stdin each name their own
// cause instead of "stdin ended".
func TestDumbTerminalPromptNamesWhyThereIsNoAnswer(t *testing.T) {
	t.Setenv("TERM", "dumb")
	feedStdin(t, strings.Repeat("y", 70000)+"\n")
	if _, err := ConfirmDefault("Apply?", true, false); err == nil || !strings.Contains(err.Error(), "answer longer than 16384 bytes") || errors.Is(err, ErrNoAnswer) {
		t.Fatalf("overlong answer: err=%v, want the length limit", err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	_ = r.Close() // reads now fail instead of reaching EOF
	saved := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = saved })
	if _, err := Input("Token", "", false); err == nil || !strings.Contains(err.Error(), "reading the answer from stdin") || errors.Is(err, ErrNoAnswer) {
		t.Fatalf("failing stdin: err=%v, want the read error", err)
	}
}
