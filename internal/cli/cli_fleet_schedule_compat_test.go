package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// These are the native commands sent by the existing fleet controller and
// scheduled service. Exercise parsing and reader declarations without running
// their host actions or weakening their real root/lock preflight.
func TestExistingFleetAndScheduleNativeWireCommandsAccepted(t *testing.T) {
	cases := []struct {
		command []string
		flags   []string
	}{
		{[]string{"apply"}, []string{"--yes", "--config", "/etc/rootfiles/fleet/fixture.yaml"}},
		{[]string{"apply"}, []string{"--yes", "--dry-run", "--config", "-"}},
		{[]string{"update"}, []string{"--version", "v1.2.3"}},
		{[]string{"schedule", "enable"}, nil},
		{[]string{"schedule", "disable"}, nil},
		{[]string{"schedule", "enable"}, []string{"--config", "/etc/rootfiles/fleet/fixture.yaml", "--profile", "minimal", "--home-base", "/srv/home"}},
		{[]string{"schedule", "disable"}, []string{"--config", "/etc/rootfiles/fleet/fixture.yaml", "--profile", "minimal", "--home-base", "/srv/home"}},
		{[]string{"status"}, []string{"-o", "json"}},
		{[]string{"check"}, []string{"-o", "json"}},
		{[]string{"doctor"}, []string{"-o", "json"}},
		{[]string{"check"}, []string{"-o", "prometheus"}},
		{[]string{"doctor"}, []string{"-o", "prometheus"}},
	}
	for _, tc := range cases {
		t.Run(strings.Join(append(append([]string{}, tc.command...), tc.flags...), " "), func(t *testing.T) {
			root := NewRootCmd("test", "test")
			cmd, _, err := root.Find(tc.command)
			if err != nil {
				t.Fatal(err)
			}
			reached := false
			root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error { return rejectUnusedNativeFlags(cmd) }
			cmd.RunE = func(*cobra.Command, []string) error { reached = true; return nil }
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetArgs(append(append([]string{}, tc.command...), tc.flags...))
			if err := root.Execute(); err != nil || !reached {
				t.Fatalf("existing wire command refused: reached=%t error=%v", reached, err)
			}
		})
	}
}

func TestScheduleStatusNonRootShowsReportsWithoutInstalledTimer(t *testing.T) {
	stateDir := scheduleStatusFixture(t, "", "disabled", "inactive")
	oldEUID := geteuid
	geteuid = func() int { return 1000 }
	t.Cleanup(func() { geteuid = oldEUID })
	lock := filepath.Join(stateDir, "lock")
	t.Setenv("ROOTFILES_LOCK_FILE", lock)
	if err := os.WriteFile(filepath.Join(stateDir, "check.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := runScheduleStatusCmd(t)
	for _, want := range []string{"not installed", "Service:", "check.json", "old", "doctor.json", "missing"} {
		if !strings.Contains(out, want) {
			t.Errorf("schedule status missing %q: %s", want, out)
		}
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("read-only schedule status acquired a lock: %v", err)
	}
}

func TestFleetTargetsExplicitEmptySelectionDoesNotRenderAll(t *testing.T) {
	inventory := targetsInventory(t)
	for _, selection := range [][]string{{"--host="}, {"--group="}, {"--all=false"}, {"--group", "no-such-group"}} {
		var out bytes.Buffer
		root := NewRootCmd("test", "test")
		root.SetOut(&out)
		root.SetErr(io.Discard)
		root.SetArgs(append([]string{"fleet", "targets", "--inventory", inventory}, selection...))
		if err := root.Execute(); err == nil || out.Len() != 0 || !strings.Contains(err.Error(), "from the inventory, or use --all") {
			t.Fatalf("selection %v lacks safe refusal/remedy: error=%v output=%s", selection, err, out.String())
		}
	}
}

func TestScheduleStatusPreservesNonzeroSystemctlState(t *testing.T) {
	scheduleStatusFixture(t, "[Timer]\nOnCalendar=hourly\n", "disabled", "inactive")
	script := "#!/bin/sh\ncase $1 in is-enabled) echo disabled; exit 1;; is-active) echo inactive; exit 3;; esac\n"
	if err := os.WriteFile(filepath.Join(os.Getenv("PATH"), "systemctl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scheduleUnitDir, scheduleName+".service"), []byte("[Service]\nType=oneshot\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := runScheduleStatusCmd(t)
	for _, want := range []string{"Enabled:", "disabled", "Active:", "inactive", "OnCalendar:", "hourly", "Service:", "check.json", "doctor.json"} {
		if !strings.Contains(out, want) {
			t.Errorf("schedule status missing %q: %s", want, out)
		}
	}
}

func TestFleetTargetsGroupAndHostSelectorsFormUnion(t *testing.T) {
	inventory := targetsInventory(t)
	data, err := os.ReadFile(inventory)
	if err != nil {
		t.Fatal(err)
	}
	content := strings.ReplaceAll(string(data), "  one:\n", "  one:\n    groups: [gpu]\n")
	content = strings.ReplaceAll(content, "  two:\n", "  two:\n    groups: [storage]\n")
	if err := os.WriteFile(inventory, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		flags []string
		count int
	}{
		{[]string{"--group", "gpu"}, 1},
		{[]string{"--group", "storage", "--host", "one"}, 2},
	} {
		var out bytes.Buffer
		root := NewRootCmd("test", "test")
		root.SetOut(&out)
		root.SetErr(io.Discard)
		root.SetArgs(append([]string{"fleet", "targets", "--inventory", inventory}, tc.flags...))
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		var groups []struct {
			Labels map[string]string `json:"labels"`
		}
		if err := json.Unmarshal(out.Bytes(), &groups); err != nil {
			t.Fatal(err)
		}
		if len(groups) != tc.count || groups[0].Labels["host"] != "one" {
			t.Fatalf("selection %v returned %s", tc.flags, out.String())
		}
	}
}

func TestScheduleStatusRejectsUnusedTargetInputs(t *testing.T) {
	for _, flag := range []string{"--config", "--profile", "--home-base"} {
		root := NewRootCmd("test", "test")
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		root.SetArgs([]string{"schedule", "status", flag, "unused-fixture"})
		if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "does not use "+flag) {
			t.Fatalf("schedule status accepted unused %s: %v", flag, err)
		}
	}
}
