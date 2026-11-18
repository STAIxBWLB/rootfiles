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
	if _, err := w.WriteString(input); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	saved := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = saved; _ = r.Close() })
}

// Under TERM=dumb a typed answer or an empty line keeps today's behavior,
// and stdin that ends before an answer fails instead of taking the default.
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
		{"y", false, true}, // an answer without a trailing newline still counts
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

	for _, input := range []string{"", "  ", "maybe\n"} {
		for _, call := range []struct {
			name string
			fn   func() error
		}{
			{"ConfirmDefault", func() error { _, err := ConfirmDefault("Apply this configuration?", true, false); return err }},
			{"Select", func() error { _, err := Select("Profile", []string{"minimal", "dgx"}, "dgx", false); return err }},
			{"Input", func() error { _, err := Input("Timezone", "UTC", false); return err }},
		} {
			if call.name != "ConfirmDefault" && input == "maybe\n" {
				continue // only a confirm rejects "maybe" and asks again
			}
			feedStdin(t, input)
			err := call.fn()
			if !errors.Is(err, ErrNoAnswer) || !strings.Contains(err.Error(), "--yes") {
				t.Fatalf("%s with stdin %q: err=%v, want ErrNoAnswer naming --yes", call.name, input, err)
			}
		}
	}
}
