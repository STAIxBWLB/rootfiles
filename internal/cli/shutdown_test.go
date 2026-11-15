package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/shutdown"
)

// shutdownFixture fakes root and the global lock and points every path the
// shutdown commands write at a temp dir, with systemctl, docker and wall stubs
// first on PATH so no host command runs.
func shutdownFixture(t *testing.T) string {
	t.Helper()
	oldEUID, oldOS, oldLock, oldAudit := geteuid, nativeHostOS, heldLock, auditLogger
	geteuid = func() int { return 0 }
	nativeHostOS = "linux"
	heldLock, auditLogger = nil, nil
	t.Setenv("ROOTFILES_LOCK_FILE", filepath.Join(t.TempDir(), "lock"))
	t.Setenv("ROOTFILES_LOG_FILE", filepath.Join(t.TempDir(), "audit"))

	dir := t.TempDir()
	oldUnit, oldNologin, oldState := shutdown.UnitDir, shutdown.NologinPath, shutdown.StatePath
	shutdown.UnitDir = filepath.Join(dir, "units")
	shutdown.NologinPath = filepath.Join(dir, "run", "nologin")
	shutdown.StatePath = filepath.Join(dir, "state", "shutdown.json")
	if err := os.MkdirAll(shutdown.UnitDir, 0755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"systemctl": "#!/bin/sh\nexit 0\n",
		"wall":      "#!/bin/sh\ncat > /dev/null\n",
		"docker":    "#!/bin/sh\n[ \"$1\" = ps ] && exit 0\nexit 0\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	t.Cleanup(func() {
		heldLock.Release()
		geteuid, nativeHostOS, heldLock, auditLogger = oldEUID, oldOS, oldLock, oldAudit
		shutdown.UnitDir, shutdown.NologinPath, shutdown.StatePath = oldUnit, oldNologin, oldState
	})
	return dir
}

func runShutdown(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := NewRootCmd("test", "abc")
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	// Execute holds the global lock until the process exits.
	releaseLock := func() {
		heldLock.Release()
		heldLock = nil
	}
	releaseLock()
	err := root.Execute()
	releaseLock()
	return out.String(), errOut.String(), err
}

// AC1: every refusal names the field and the form it expects, and nothing is
// written when a schedule is refused.
func TestShutdownScheduleRefusalsNameTheField(t *testing.T) {
	shutdownFixture(t)
	soon := time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339)
	far := time.Now().Add(40 * 24 * time.Hour).UTC().Format(time.RFC3339)
	plain := time.Now().Add(2 * time.Hour).In(time.FixedZone("KST", 9*3600)).Format("2006-01-02T15:04:05")
	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{"missing", nil, []string{"--at is required", "RFC 3339"}},
		{"no offset", []string{"--at", plain}, []string{"--at", "explicit offset"}},
		{"too soon", []string{"--at", soon}, []string{"--at", "at least 1 minute ahead"}},
		{"too far", []string{"--at", far}, []string{"--at", "at most 30 days ahead"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runShutdown(t, append([]string{"shutdown", "schedule"}, tc.args...)...)
			if err == nil {
				t.Fatal("a bad --at was accepted")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("refusal %q does not name %q", err, want)
				}
			}
			if err := os.MkdirAll(shutdown.UnitDir, 0755); err != nil {
				t.Fatal(err)
			}
			if entries, _ := os.ReadDir(shutdown.UnitDir); len(entries) != 0 {
				t.Fatalf("a refused schedule wrote %v", entries)
			}
		})
	}
}

func TestShutdownScheduleWritesAndReportsThroughStatus(t *testing.T) {
	shutdownFixture(t)
	at := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	out, _, err := runShutdown(t, "shutdown", "schedule",
		"--at", at.Format(time.RFC3339), "--notice", "30m", "--message", "Building power work")
	if err != nil {
		t.Fatalf("schedule: %v\n%s", err, out)
	}
	if !strings.Contains(out, at.Format(time.RFC3339)) || !strings.Contains(out, "poweroff") {
		t.Fatalf("schedule did not report the instant and the action:\n%s", out)
	}
	for _, name := range []string{"rootfiles-shutdown.service", "rootfiles-shutdown.timer", "rootfiles-shutdown-notice-1.timer"} {
		if _, err := os.Stat(filepath.Join(shutdown.UnitDir, name)); err != nil {
			t.Fatalf("schedule did not write %s: %v", name, err)
		}
	}

	text, _, err := runShutdown(t, "shutdown", "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Scheduled shutdown", "poweroff", at.Format(time.RFC3339), "Building power work", "30m before", "Blocked"} {
		if !strings.Contains(text, want) {
			t.Fatalf("status text lacks %q:\n%s", want, text)
		}
	}

	jsonOut, _, err := runShutdown(t, "shutdown", "status", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Scheduled *shutdown.Schedule `json:"scheduled_shutdown"`
		Next      *shutdown.Notice   `json:"next_notice"`
		Blocked   bool               `json:"logins_blocked"`
		Units     bool               `json:"units_installed"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &report); err != nil {
		t.Fatalf("status JSON: %v\n%s", err, jsonOut)
	}
	if report.Scheduled == nil || !report.Scheduled.At.Equal(at) || report.Scheduled.Action != shutdown.ActionPoweroff {
		t.Fatalf("status JSON schedule = %+v", report.Scheduled)
	}
	if report.Next == nil || report.Next.Lead != "30m" || !report.Units || report.Blocked {
		t.Fatalf("status JSON details = %+v", report)
	}

	// A second schedule is refused and names --replace.
	_, _, err = runShutdown(t, "shutdown", "schedule", "--at", at.Add(time.Hour).Format(time.RFC3339))
	if err == nil || !strings.Contains(err.Error(), "--replace") {
		t.Fatalf("second schedule: %v", err)
	}
	if _, _, err := runShutdown(t, "shutdown", "cancel"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if entries, _ := os.ReadDir(shutdown.UnitDir); len(entries) != 0 {
		t.Fatalf("cancel left %v", entries)
	}
	out, _, err = runShutdown(t, "shutdown", "cancel")
	if err != nil || !strings.Contains(out, "nothing scheduled") {
		t.Fatalf("second cancel = %q, %v", out, err)
	}
}

func TestShutdownNotifyIsHiddenAndSendsWhatTheTimersWould(t *testing.T) {
	shutdownFixture(t)
	at := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Second)
	if _, _, err := runShutdown(t, "shutdown", "schedule", "--at", at.Format(time.RFC3339), "--notice", "5m"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runShutdown(t, "shutdown", "notify", "--notice", "15m")
	if err != nil || !strings.Contains(out, "notice sent") {
		t.Fatalf("notify = %q, %v", out, err)
	}
	help, _, err := runShutdown(t, "shutdown", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(help, "notify") {
		t.Fatalf("the internal notify command is listed in help:\n%s", help)
	}
}

// AC5: the field is present only when a schedule exists, and the text report
// stays quiet otherwise.
func TestStatusReportCarriesTheScheduleOnlyWhenScheduled(t *testing.T) {
	saved := shutdown.StatePath
	shutdown.StatePath = filepath.Join(t.TempDir(), "shutdown.json")
	t.Cleanup(func() { shutdown.StatePath = saved })

	sys := &config.SystemInfo{OS: "ubuntu"}
	cfg := config.Fallback(sys, "")
	rc := readOnlyRunContext(cfg, sys, slog.New(slog.NewTextHandler(io.Discard, nil)))

	report := collectStatus(context.Background(), rc, sys, "minimal", "", nil, nil)
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "scheduled_shutdown") {
		t.Fatalf("status JSON claimed a schedule:\n%s", data)
	}

	scheduled := shutdown.Schedule{
		ID:        "20261120T090000Z",
		At:        time.Now().Add(2 * time.Hour).UTC(),
		Action:    shutdown.ActionReboot,
		Message:   "Kernel work",
		CreatedBy: "ops",
		CreatedAt: time.Now().UTC(),
	}
	encoded, err := json.Marshal(scheduled)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shutdown.StatePath, encoded, 0644); err != nil {
		t.Fatal(err)
	}

	report = collectStatus(context.Background(), rc, sys, "minimal", "", nil, nil)
	data, err = json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"scheduled_shutdown"`) || !strings.Contains(string(data), "Kernel work") {
		t.Fatalf("status JSON lost the schedule:\n%s", data)
	}
	var out bytes.Buffer
	renderStatusText(&out, report, "minimal", nil)
	if !strings.Contains(out.String(), "Scheduled shutdown") || !strings.Contains(out.String(), "reboot") {
		t.Fatalf("status text does not name the schedule:\n%s", out.String())
	}
}
