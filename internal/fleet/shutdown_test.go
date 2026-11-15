package fleet

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func shutdownHost(name, sudo string) NamedHost {
	return NamedHost{Name: name, Host: Host{SSH: name}, Sudo: sudo}
}

// writeShutdownSSH stubs the three remote commands a schedule run sends:
// the clock probe, the schedule read-back and the schedule itself. override
// runs before the defaults and may exit for one host.
func writeShutdownSSH(t *testing.T, calls, override string) string {
	t.Helper()
	now := strconv.FormatInt(time.Now().Unix(), 10)
	return writeFakeSSH(t, `#!/bin/sh
printf '%s|%s\n' "$6" "$7" >> '`+ShellQuote(calls)+`'
`+override+`
case "$7" in
  "date -u +%s") printf '%s\n' '`+now+`' ;;
  *"shutdown status -o json") printf '%s\n' '{}' ;;
  *"shutdown schedule"*) printf '%s\n' 'schedule written' ;;
  *"shutdown cancel"*) printf '%s\n' 'cancelled' ;;
  *) printf '%s\n' 'unexpected remote command' >&2; exit 1 ;;
esac
`)
}

func shutdownCalls(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no remote command was sent: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func TestFleetShutdownSendsOneIdenticalInstantToEveryHost(t *testing.T) {
	calls := filepath.Join(t.TempDir(), "calls")
	bin := writeShutdownSSH(t, calls, "")
	hosts := []NamedHost{shutdownHost("a", "nopasswd"), shutdownHost("b", "root"), shutdownHost("c", "none")}
	at := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)

	got := ShutdownSchedule(context.Background(), SSHRunner{Binary: bin}, hosts,
		ShutdownRequest{At: at, Notices: "15m,5m", Message: "Building power work"}, false)
	if len(got) != 3 {
		t.Fatalf("results = %#v", got)
	}
	for _, r := range got {
		if r.State != "ok" {
			t.Fatalf("host %s was not scheduled: %#v", r.Host, r)
		}
	}

	instant := "--at " + ShellQuote(at.Format(time.RFC3339))
	scheduled := 0
	probes := 0
	for _, line := range shutdownCalls(t, calls) {
		_, remote, _ := strings.Cut(line, "|")
		switch {
		case strings.HasSuffix(remote, "date -u +%s"):
			probes++
			if strings.Contains(remote, "sudo") {
				t.Fatalf("the clock probe used sudo: %q", remote)
			}
		case strings.Contains(remote, "shutdown schedule"):
			scheduled++
			for _, want := range []string{instant, "--notice '15m,5m'", "--message 'Building power work'"} {
				if !strings.Contains(remote, want) {
					t.Fatalf("schedule command lacks %q: %q", want, remote)
				}
			}
		}
	}
	if probes != 3 || scheduled != 3 {
		t.Fatalf("probes=%d schedules=%d calls:\n%s", probes, scheduled, strings.Join(shutdownCalls(t, calls), "\n"))
	}
	// The read-only sudoers boundary stays untouched: the nopasswd host gets
	// exactly its declared privilege, and a root login needs none.
	for _, line := range shutdownCalls(t, calls) {
		switch {
		case strings.HasPrefix(line, "a|") && strings.Contains(line, "shutdown status -o json"):
			if !strings.HasPrefix(line, "a|sudo -n /usr/local/bin/rootfiles") {
				t.Fatalf("the nopasswd host lost its declared privilege: %q", line)
			}
		case strings.HasPrefix(line, "b|") && strings.Contains(line, "shutdown"):
			if strings.Contains(line, "sudo") {
				t.Fatalf("a root login was given sudo: %q", line)
			}
		}
	}
}

func TestFleetShutdownRefusesClockSkewBeforeWritingAnything(t *testing.T) {
	calls := filepath.Join(t.TempDir(), "calls")
	skewed := strconv.FormatInt(time.Now().Add(45*time.Second).Unix(), 10)
	bin := writeShutdownSSH(t, calls, `if [ "$7" = "date -u +%s" ] && [ "$6" = "slow" ]; then printf '%s\n' '`+skewed+`'; exit 0; fi`)
	hosts := []NamedHost{shutdownHost("slow", "root"), shutdownHost("fine", "root")}

	got := ShutdownSchedule(context.Background(), SSHRunner{Binary: bin}, hosts,
		ShutdownRequest{At: time.Now().Add(2 * time.Hour)}, false)
	if got[0].State != "error" {
		t.Fatalf("a 45s clock skew was accepted: %#v", got[0])
	}
	// The measurement is taken between two wall-clock reads, so the reported
	// value lands just under the 45s the stub prints.
	for _, want := range []string{"clock skew is 4", "ahead of the controller", "slow", "30s"} {
		if !strings.Contains(got[0].Reason, want) {
			t.Fatalf("skew refusal %q does not name %q", got[0].Reason, want)
		}
	}
	if got[1].State != "skipped" || !strings.Contains(got[1].Reason, "pre-flight failed on slow") {
		t.Fatalf("the healthy host was not held back: %#v", got[1])
	}
	for _, line := range shutdownCalls(t, calls) {
		if strings.Contains(line, "shutdown schedule") {
			t.Fatalf("a host was scheduled despite the failed pre-flight: %q", line)
		}
	}
}

func TestFleetShutdownRefusesAnExistingScheduleWithoutReplace(t *testing.T) {
	stored := `{"scheduled_shutdown":{"at":"2026-11-21T09:00:00Z","action":"poweroff"}}`
	for _, tc := range []struct {
		name    string
		replace bool
		want    string
	}{
		{"refused", false, "--replace"},
		{"replaced", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := filepath.Join(t.TempDir(), "calls")
			bin := writeShutdownSSH(t, calls, `if [ "$7" = "/usr/local/bin/rootfiles shutdown status -o json" ]; then printf '%s\n' '`+stored+`'; exit 0; fi`)
			hosts := []NamedHost{shutdownHost("a", "root")}
			got := ShutdownSchedule(context.Background(), SSHRunner{Binary: bin}, hosts,
				ShutdownRequest{At: time.Now().Add(time.Hour), Replace: tc.replace}, false)
			if tc.want == "" {
				if got[0].State != "ok" {
					t.Fatalf("--replace did not schedule over the existing one: %#v", got[0])
				}
				return
			}
			if got[0].State != "error" || !strings.Contains(got[0].Reason, tc.want) {
				t.Fatalf("an existing schedule was accepted: %#v", got[0])
			}
			for _, want := range []string{"2026-11-21T09:00:00Z", "rootfiles fleet shutdown cancel --host a"} {
				if !strings.Contains(got[0].Reason, want) {
					t.Fatalf("refusal %q does not name %q", got[0].Reason, want)
				}
			}
			for _, line := range shutdownCalls(t, calls) {
				if strings.Contains(line, "shutdown schedule") {
					t.Fatalf("a second schedule was sent anyway: %q", line)
				}
			}
		})
	}
}

func TestFleetShutdownRollsBackWhatItScheduled(t *testing.T) {
	calls := filepath.Join(t.TempDir(), "calls")
	bin := writeShutdownSSH(t, calls, `if [ "$6" = "bad" ] && [ "$7" != "date -u +%s" ] && [ "${7#*shutdown schedule}" != "$7" ]; then printf '%s\n' 'rootfiles: cannot write shutdown unit' >&2; exit 1; fi`)
	hosts := []NamedHost{shutdownHost("first", "root"), shutdownHost("bad", "root"), shutdownHost("later", "root")}

	got := ShutdownSchedule(context.Background(), SSHRunner{Binary: bin}, hosts,
		ShutdownRequest{At: time.Now().Add(time.Hour)}, false)
	if got[0].State != "cancelled" || !strings.Contains(got[0].Reason, "cancelled because bad failed") {
		t.Fatalf("the first host was not rolled back: %#v", got[0])
	}
	if got[1].State != "error" || !strings.Contains(got[1].Reason, "cannot write shutdown unit") {
		t.Fatalf("the failing host: %#v", got[1])
	}
	if got[2].State != "skipped" {
		t.Fatalf("the third host ran after a failure: %#v", got[2])
	}
	// The failing host counts as uncertain: it can have enabled the poweroff
	// timer before a later step failed, so it is cancelled too.
	touched := map[string]bool{}
	for _, line := range shutdownCalls(t, calls) {
		if strings.Contains(line, "shutdown cancel") {
			name, _, _ := strings.Cut(line, "|")
			if name != "first" && name != "bad" {
				t.Fatalf("rollback cancelled a host that was never attempted: %q", line)
			}
			touched[name] = true
		}
	}
	if len(touched) != 2 || !touched["first"] || !touched["bad"] {
		t.Fatalf("rollback cancellations = %v, want the scheduled host and the failing one", touched)
	}
	for _, line := range shutdownCalls(t, calls) {
		if strings.HasPrefix(line, "later|") && strings.Contains(line, "shutdown cancel") {
			t.Fatalf("a host the run never reached was cancelled: %q", line)
		}
	}
}

// AC6: a host that failed after it may have armed its poweroff timer is
// reported as cancelled again, never as safe-by-silence.
func TestFleetShutdownCancelsTheHostThatFailedMidway(t *testing.T) {
	calls := filepath.Join(t.TempDir(), "calls")
	bin := writeShutdownSSH(t, calls, `if [ "${7#*shutdown schedule}" != "$7" ] && [ "$6" = "bad" ]; then printf '%s\n' 'rootfiles: cannot enable rootfiles-shutdown-notice-1.timer' >&2; exit 1; fi`)
	hosts := []NamedHost{shutdownHost("bad", "root")}
	got := ShutdownSchedule(context.Background(), SSHRunner{Binary: bin}, hosts,
		ShutdownRequest{At: time.Now().Add(time.Hour)}, false)
	if got[0].State != "error" {
		t.Fatalf("a failed schedule was reported as %q", got[0].State)
	}
	for _, want := range []string{"cannot enable", "cancelled again", "will not stop on its own"} {
		if !strings.Contains(got[0].Reason, want) {
			t.Fatalf("the failing host's reason %q does not name %q", got[0].Reason, want)
		}
	}
	if !strings.Contains(strings.Join(shutdownCalls(t, calls), "\n"), "bad|/usr/local/bin/rootfiles shutdown cancel") {
		t.Fatalf("the failing host was never cancelled:\n%s", strings.Join(shutdownCalls(t, calls), "\n"))
	}
}

// AC6: an unreadable state file on a host stops the run with the recovery
// command, not with a silent "nothing scheduled".
func TestFleetShutdownRefusesAnUnreadableHostSchedule(t *testing.T) {
	calls := filepath.Join(t.TempDir(), "calls")
	stored := `{"scheduled_shutdown_error":"scheduled shutdown /var/lib/rootfiles/shutdown.json is not valid JSON"}`
	bin := writeShutdownSSH(t, calls, `if [ "$7" = "/usr/local/bin/rootfiles shutdown status -o json" ]; then printf '%s\n' '`+stored+`'; exit 0; fi`)
	hosts := []NamedHost{shutdownHost("a", "root")}
	got := ShutdownSchedule(context.Background(), SSHRunner{Binary: bin}, hosts,
		ShutdownRequest{At: time.Now().Add(time.Hour)}, false)
	if got[0].State != "error" {
		t.Fatalf("an unreadable schedule was accepted: %#v", got[0])
	}
	for _, want := range []string{"cannot be read", "may still be armed", "fleet shutdown cancel --host a --yes"} {
		if !strings.Contains(got[0].Reason, want) {
			t.Fatalf("refusal %q does not name %q", got[0].Reason, want)
		}
	}
	for _, line := range shutdownCalls(t, calls) {
		if strings.Contains(line, "shutdown schedule") {
			t.Fatalf("the host was scheduled over a schedule it could not read: %q", line)
		}
	}
}

func TestFleetShutdownReportsAFailedRollbackCancellation(t *testing.T) {
	calls := filepath.Join(t.TempDir(), "calls")
	bin := writeShutdownSSH(t, calls, `
if [ "${7#*shutdown schedule}" != "$7" ] && [ "$6" = "bad" ]; then printf '%s\n' 'rootfiles: boom' >&2; exit 1; fi
if [ "${7#*shutdown cancel}" != "$7" ]; then printf '%s\n' 'rootfiles: cannot remove shutdown unit' >&2; exit 1; fi
`)
	hosts := []NamedHost{shutdownHost("first", "root"), shutdownHost("bad", "root")}
	got := ShutdownSchedule(context.Background(), SSHRunner{Binary: bin}, hosts,
		ShutdownRequest{At: time.Now().Add(time.Hour)}, false)
	if got[0].State != "cancel-failed" || !strings.Contains(got[0].Reason, "cannot remove shutdown unit") {
		t.Fatalf("a failed rollback cancellation was not reported: %#v", got[0])
	}
	// The failing host's own cancel failed too, so it is named as possibly armed.
	if got[1].State != "cancel-failed" || !strings.Contains(got[1].Reason, "may still stop at the instant") {
		t.Fatalf("the failing host was not reported as possibly armed: %#v", got[1])
	}
}

// AC8: cancel reaches every host and does not stop at the first failure.
func TestFleetShutdownCancelContinuesPastFailures(t *testing.T) {
	calls := filepath.Join(t.TempDir(), "calls")
	bin := writeShutdownSSH(t, calls, `if [ "$6" = "bad" ]; then printf '%s\n' 'rootfiles: cannot read scheduled shutdown' >&2; exit 1; fi`)
	hosts := []NamedHost{shutdownHost("bad", "root"), shutdownHost("second", "root")}

	got := ShutdownCancel(context.Background(), SSHRunner{Binary: bin}, hosts, false)
	if got[0].State != "error" || got[1].State != "ok" {
		t.Fatalf("cancel stopped at the first failure: %#v", got)
	}
	seen := 0
	for _, line := range shutdownCalls(t, calls) {
		if strings.Contains(line, "shutdown cancel") {
			seen++
		}
	}
	if seen != 2 {
		t.Fatalf("cancel reached %d hosts, want 2", seen)
	}
}

func TestFleetShutdownDryRunContactsNoHost(t *testing.T) {
	calls := filepath.Join(t.TempDir(), "calls")
	hosts := []NamedHost{shutdownHost("a", "root"), shutdownHost("b", "root")}
	at := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	got := ShutdownSchedule(context.Background(), SSHRunner{Binary: filepath.Join(t.TempDir(), "no-ssh")}, hosts,
		ShutdownRequest{At: at, Notices: "15m"}, true)
	for _, r := range got {
		if r.State != "ok" || !strings.Contains(r.Reason, "dry-run: would schedule poweroff for "+at.Format(time.RFC3339)) {
			t.Fatalf("dry-run result = %#v", r)
		}
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatal("the dry run contacted a host")
	}
}
