package ui

import (
	"errors"
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
