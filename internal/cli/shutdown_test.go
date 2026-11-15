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
	for _, want := range []string{"Scheduled shutdown", "poweroff", at.Format(time.RFC3339), "Building power work", "30m before", "Blocked", "tenant login blocks start"} {
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
		Blocked   *bool              `json:"logins_blocked"`
		Units     bool               `json:"units_installed"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &report); err != nil {
		t.Fatalf("status JSON: %v\n%s", err, jsonOut)
	}
	if report.Scheduled == nil || !report.Scheduled.At.Equal(at) || report.Scheduled.Action != shutdown.ActionPoweroff {
		t.Fatalf("status JSON schedule = %+v", report.Scheduled)
	}
	if report.Next == nil || report.Next.Lead != "30m" || !report.Units || report.Blocked == nil || *report.Blocked {
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
	// collectStatus reads systemctl, ip, cloudflared and the account list, so the
	// test runs on a private PATH with those seams stubbed: no host tool and no
	// real account data.
	noHostCommands(t)

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

// AC5: an expired record is not a schedule any more, and a state file that
// cannot be read is reported instead of reading as "nothing scheduled".
func TestStatusIgnoresAnExpiredScheduleAndReportsAnUnreadableOne(t *testing.T) {
	saved := shutdown.StatePath
	shutdown.StatePath = filepath.Join(t.TempDir(), "shutdown.json")
	t.Cleanup(func() { shutdown.StatePath = saved })
	noHostCommands(t)

	sys := &config.SystemInfo{OS: "ubuntu"}
	cfg := config.Fallback(sys, "")
	rc := readOnlyRunContext(cfg, sys, slog.New(slog.NewTextHandler(io.Discard, nil)))

	expired := shutdown.Schedule{
		ID: "20261119T090000Z", At: time.Now().Add(-time.Hour).UTC(), Action: shutdown.ActionPoweroff,
		Notices: []shutdown.Notice{{Lead: "15m", At: time.Now().Add(-75 * time.Minute).UTC()}},
	}
	encoded, err := json.Marshal(expired)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shutdown.StatePath, encoded, 0644); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(collectStatus(context.Background(), rc, sys, "minimal", "", nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "scheduled_shutdown") {
		t.Fatalf("an expired record was reported as a schedule:\n%s", data)
	}

	if err := os.WriteFile(shutdown.StatePath, []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(collectStatus(context.Background(), rc, sys, "minimal", "", nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"scheduled_shutdown_error"`) {
		t.Fatalf("an unreadable state file read as nothing scheduled:\n%s", data)
	}
	if strings.Contains(string(data), `"scheduled_shutdown":`) {
		t.Fatalf("an unreadable state file also claimed a schedule:\n%s", data)
	}
}

// The status report must not depend on an ambient toolchain: a host with a
// failing systemctl and ip answers the same way as one without them.
func TestStatusScheduleFieldsSurviveFailingAmbientTools(t *testing.T) {
	saved := shutdown.StatePath
	shutdown.StatePath = filepath.Join(t.TempDir(), "shutdown.json")
	t.Cleanup(func() { shutdown.StatePath = saved })
	noHostCommands(t)
	failing := t.TempDir()
	for _, name := range []string{"systemctl", "ip", "cloudflared"} {
		body := "#!/bin/sh\nexit 1\n"
		if err := os.WriteFile(filepath.Join(failing, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", failing)

	sys := &config.SystemInfo{OS: "ubuntu"}
	cfg := config.Fallback(sys, "")
	rc := readOnlyRunContext(cfg, sys, slog.New(slog.NewTextHandler(io.Discard, nil)))
	report := collectStatus(context.Background(), rc, sys, "minimal", "", nil, nil)
	if report.ScheduledShutdown != nil || report.ScheduledShutdownError != "" {
		t.Fatalf("a failing toolchain invented a schedule state: %+v / %q", report.ScheduledShutdown, report.ScheduledShutdownError)
	}
}

// A --replace must not tell sessions a live stop was cancelled.
func TestShutdownReplaceDoesNotAnnounceACancellation(t *testing.T) {
	dir := shutdownFixture(t)
	wall := filepath.Join(dir, "wall.log")
	bin := filepath.Join(dir, "bin")
	if err := os.WriteFile(filepath.Join(bin, "wall"), []byte("#!/bin/sh\ncat >> '"+wall+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	if _, _, err := runShutdown(t, "shutdown", "schedule", "--at", at.Format(time.RFC3339), "--notice", "15m"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runShutdown(t, "shutdown", "schedule", "--at", at.Add(time.Hour).Format(time.RFC3339), "--notice", "15m", "--replace"); err != nil {
		t.Fatalf("--replace: %v", err)
	}
	if data, err := os.ReadFile(wall); err == nil && strings.Contains(string(data), "Cancelled") {
		t.Fatalf("--replace told sessions the stop was cancelled:\n%s", data)
	}
}

// The login-block command is internal, like notify, and closes logins for the
// stored schedule.
func TestShutdownBlockIsHiddenAndBlocksLogins(t *testing.T) {
	shutdownFixture(t)
	at := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	if _, _, err := runShutdown(t, "shutdown", "schedule", "--at", at.Format(time.RFC3339), "--notice", "1h"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runShutdown(t, "shutdown", "block")
	if err != nil || !strings.Contains(out, "tenant login block attempted") {
		t.Fatalf("block = %q, %v", out, err)
	}
	if _, err := os.Stat(shutdown.NologinPath); !os.IsNotExist(err) {
		t.Fatalf("block touched the host nologin file: %v", err)
	}
	help, _, err := runShutdown(t, "shutdown", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(help, "\n  block") {
		t.Fatalf("the internal block command is listed in help:\n%s", help)
	}
}

// An unreadable state file cannot block the cancel its own error message names.
func TestShutdownCancelRecoversFromAnUnreadableState(t *testing.T) {
	shutdownFixture(t)
	at := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	if _, _, err := runShutdown(t, "shutdown", "schedule", "--at", at.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shutdown.StatePath, []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}
	jsonOut, _, err := runShutdown(t, "shutdown", "status", "-o", "json")
	if err != nil {
		t.Fatalf("status on an unreadable state failed instead of reporting it: %v", err)
	}
	if !strings.Contains(jsonOut, "scheduled_shutdown_error") {
		t.Fatalf("status JSON hid the unreadable state:\n%s", jsonOut)
	}
	out, errOut, err := runShutdown(t, "shutdown", "cancel")
	if err != nil {
		t.Fatalf("cancel could not recover: %v\n%s", err, errOut)
	}
	if !strings.Contains(out, "leftover") || !strings.Contains(errOut, "could not be read") {
		t.Fatalf("cancel = %q, %q; want the leftovers named on stdout and the cause on stderr", out, errOut)
	}
	if entries, _ := os.ReadDir(shutdown.UnitDir); len(entries) != 0 {
		t.Fatalf("recovery cancel left %v", entries)
	}
}

func TestShutdownStatusTenantStateAndUnknown(t *testing.T) {
	dir := shutdownFixture(t)
	at := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	if _, _, err := runShutdown(t, "shutdown", "schedule", "--at", at.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	script := "#!/bin/sh\ncase \"$1:$3\" in ps:*) echo tenant-a;; exec:grep) exit 0;; exec:cat) echo maintenance;; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	out, _, err := runShutdown(t, "shutdown", "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Blocked *bool                      `json:"logins_blocked"`
		Tenant  shutdown.TenantLoginStatus `json:"tenant_logins"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil || report.Blocked == nil || !*report.Blocked || report.Tenant.Blocked != 1 {
		t.Fatalf("blocked status: %s %v", out, err)
	}
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nexit 2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	out, _, err = runShutdown(t, "shutdown", "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil || report.Blocked != nil || report.Tenant.Unknown == 0 {
		t.Fatalf("unknown status: %s %v", out, err)
	}
}

func TestStatusJSONDoesNotInspectDocker(t *testing.T) {
	fixture, site, _ := statusContractFixture(t, "populated")
	marker := filepath.Join(fixture, "docker-called")
	bin := filepath.Join(fixture, "bin")
	stub := "#!/bin/sh\nprintf called > '" + marker + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	previous := shutdown.StatePath
	shutdown.StatePath = filepath.Join(fixture, "shutdown.json")
	t.Cleanup(func() { shutdown.StatePath = previous })
	for _, scheduled := range []bool{false, true} {
		if scheduled {
			s := shutdown.Schedule{ID: "fixture", At: time.Now().Add(time.Hour), Action: shutdown.ActionPoweroff}
			data, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(shutdown.StatePath, data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		var out bytes.Buffer
		cmd := NewRootCmd("test", "fixture")
		cmd.SetOut(&out)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"status", "--config", site, "-o", "json"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("scheduled=%t: %v", scheduled, err)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("scheduled=%t: status invoked docker: %v", scheduled, err)
		}
	}
}
