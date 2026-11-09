package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// scheduleStatusFixture writes the timer unit and the reports the status
// command reads, and stubs systemctl so no host command runs.
func scheduleStatusFixture(t *testing.T, timerUnit, enabled, active string) (stateDir string) {
	t.Helper()
	units := t.TempDir()
	if timerUnit != "" {
		if err := os.WriteFile(filepath.Join(units, scheduleName+".timer"), []byte(timerUnit), 0644); err != nil {
			t.Fatal(err)
		}
	}
	oldUnits := scheduleUnitDir
	scheduleUnitDir = units
	t.Cleanup(func() { scheduleUnitDir = oldUnits })

	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in\n" +
		"is-enabled) echo " + enabled + " ;;\n" +
		"is-active) echo " + active + " ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	stateDir = t.TempDir()
	t.Setenv("ROOTFILES_STATE_DIR", stateDir)
	return stateDir
}

func runScheduleStatusCmd(t *testing.T) string {
	t.Helper()
	var out bytes.Buffer
	root := NewRootCmd("test", "abc")
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"schedule", "status", "--profile", "minimal"})
	if err := root.Execute(); err != nil {
		t.Fatalf("schedule status: %v", err)
	}
	return out.String()
}

// AC4: schedule status reports the timer state and its reports without
// changing anything.
func TestScheduleStatusReportsTimerState(t *testing.T) {
	stateDir := scheduleStatusFixture(t,
		"# Managed by rootfiles-v2\n[Timer]\nOnCalendar=hourly\nRandomizedDelaySec=15m\n",
		"enabled", "active")
	report := filepath.Join(stateDir, "check.json")
	if err := os.WriteFile(report, []byte(`{"modules":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(report, old, old); err != nil {
		t.Fatal(err)
	}

	out := runScheduleStatusCmd(t)
	for _, want := range []string{"installed", "enabled", "active", "OnCalendar:", "hourly", "check.json", "3 h old", "doctor.json", "missing"} {
		if !strings.Contains(out, want) {
			t.Errorf("schedule status missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "stale") {
		t.Errorf("a three-hour-old report is not stale:\n%s", out)
	}
}

// The monitoring hub alerts past 48 hours; schedule status says so first.
func TestScheduleStatusMarksOldReportsStale(t *testing.T) {
	stateDir := scheduleStatusFixture(t, "[Timer]\nOnCalendar=daily\n", "enabled", "active")
	report := filepath.Join(stateDir, "check.json")
	if err := os.WriteFile(report, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(report, old, old); err != nil {
		t.Fatal(err)
	}
	if out := runScheduleStatusCmd(t); !strings.Contains(out, "3 d old") || !strings.Contains(out, "stale") {
		t.Errorf("72-hour-old report not marked stale:\n%s", out)
	}
}

// Without the units the report names the command that installs them.
func TestScheduleStatusWithoutTimer(t *testing.T) {
	scheduleStatusFixture(t, "", "disabled", "inactive")
	out := runScheduleStatusCmd(t)
	if !strings.Contains(out, "not installed") || !strings.Contains(out, "schedule enable") {
		t.Errorf("schedule status:\n%s", out)
	}
}

// schedule status reads the host; it must not take the root and lock that
// schedule enable/disable take.
func TestScheduleStatusIsNotMutating(t *testing.T) {
	root := NewRootCmd("test", "abc")
	for _, path := range [][]string{{"schedule", "status"}, {"schedule", "enable"}, {"schedule", "disable"}} {
		cmd, _, err := root.Find(path)
		if err != nil {
			t.Fatalf("find %v: %v", path, err)
		}
		mutating := cmd.Annotations[annotMutates] == "true"
		want := path[1] != "status"
		if mutating != want {
			t.Errorf("%v: mutating = %v, want %v", path, mutating, want)
		}
	}
}

func TestValidateOnCalendar(t *testing.T) {
	for _, ok := range []string{"daily", "hourly", "*-*-* 06:00", "Mon..Fri 09:00:00"} {
		if err := validateOnCalendar(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", " ", "daily\nOnBootSec=1m", "it's"} {
		if err := validateOnCalendar(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
