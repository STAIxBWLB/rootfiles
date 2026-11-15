package shutdown

import (
	"strings"
	"testing"
	"time"
)

func TestUnitsRenderTheInstantAndNeverPersist(t *testing.T) {
	s := testSchedule()
	units := Units("", s)
	byName := map[string]string{}
	for _, u := range units {
		byName[u.Name] = u.Content
	}
	wantService := `# Managed by rootfiles
[Unit]
Description=rootfiles scheduled power off

[Service]
Type=oneshot
ExecStart=/bin/sh -c 'systemctl poweroff'
`
	if got := byName["rootfiles-shutdown.service"]; got != wantService {
		t.Fatalf("service unit:\n%s\nwant:\n%s", got, wantService)
	}
	wantTimer := `# Managed by rootfiles
[Unit]
Description=rootfiles scheduled power off at 2026-11-21T09:00:00Z

[Timer]
OnCalendar=2026-11-21 09:00:00 UTC
# A late boot must not act on an instant that has already passed.
Persistent=false

[Install]
WantedBy=timers.target
`
	if got := byName["rootfiles-shutdown.timer"]; got != wantTimer {
		t.Fatalf("timer unit:\n%s\nwant:\n%s", got, wantTimer)
	}
	wantNoticeService := `# Managed by rootfiles
[Unit]
Description=rootfiles shutdown notice (15m before)

[Service]
Type=oneshot
ExecStart=/usr/local/bin/rootfiles shutdown notify --notice '15m'
`
	if got := byName["rootfiles-shutdown-notice-1.service"]; got != wantNoticeService {
		t.Fatalf("notice service:\n%s\nwant:\n%s", got, wantNoticeService)
	}
	noticeTimer := byName["rootfiles-shutdown-notice-2.timer"]
	for _, want := range []string{"OnCalendar=2026-11-21 08:55:00 UTC", "Persistent=false", "(5m before)"} {
		if !strings.Contains(noticeTimer, want) {
			t.Fatalf("second notice timer lacks %q:\n%s", want, noticeTimer)
		}
	}
}

func TestUnitsRebootAndZoneIndependence(t *testing.T) {
	s := testSchedule()
	s.Action = ActionReboot
	// The same instant expressed in another zone renders the same calendar.
	shifted := s
	zone := time.FixedZone("KST", 9*60*60)
	shifted.At = s.At.In(zone)
	if got, want := Units("", shifted)[1].Content, Units("", s)[1].Content; got != want {
		t.Fatalf("the host zone moved the unit:\n%s\nwant:\n%s", got, want)
	}
	units := Units("", s)
	if !strings.Contains(units[0].Content, "systemctl reboot") {
		t.Fatalf("reboot service:\n%s", units[0].Content)
	}
	if !strings.Contains(units[1].Content, "scheduled reboot") {
		t.Fatalf("reboot timer:\n%s", units[1].Content)
	}
}

func TestUnitsDropPersistenceIfEitherUnitChanges(t *testing.T) {
	// A guard for the two guardrails together: an absolute UTC calendar and
	// no catch-up after a late boot.
	for _, u := range Units("", testSchedule()) {
		if strings.Contains(u.Content, "OnCalendar") && !strings.Contains(u.Content, " UTC\n") {
			t.Fatalf("%s is not pinned to UTC:\n%s", u.Name, u.Content)
		}
		if strings.Contains(u.Content, "[Timer]") && !strings.Contains(u.Content, "Persistent=false") {
			t.Fatalf("%s would catch up after a late boot:\n%s", u.Name, u.Content)
		}
	}
}

func TestTimerNamesFollowTheNoticeOrder(t *testing.T) {
	names := TimerNames(&Schedule{Notices: []Notice{{Lead: "15m"}, {Lead: "5m"}}})
	want := []string{"rootfiles-shutdown.timer", "rootfiles-shutdown-notice-1.timer", "rootfiles-shutdown-notice-2.timer"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("timers = %v, want %v", names, want)
	}
	if got := TimerNames(nil); len(got) != 1 || got[0] != "rootfiles-shutdown.timer" {
		t.Fatalf("timers without a schedule = %v", got)
	}
}

func TestNoticeTextCarriesUTCZoneTimeLeftAndMessage(t *testing.T) {
	s := testSchedule()
	text := NoticeText(s, testAt.Add(-15*time.Minute))
	local := s.At.Local().Format("2006-01-02 15:04:05 MST")
	for _, want := range []string{
		"Scheduled poweroff: this host will power off at 2026-11-21 09:00:00 UTC",
		"(" + local + ")",
		"About 15 minutes left.",
		"Building power work, back at 22:00.",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("notice text lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "New logins are blocked") {
		t.Fatalf("a 15m notice announced the login block:\n%s", text)
	}
	late := NoticeText(s, testAt.Add(-2*time.Minute))
	if !strings.Contains(late, "New logins are blocked") {
		t.Fatalf("a notice inside the 5m window did not announce the block:\n%s", late)
	}
}

func TestCancelTextNamesTheInstantAndTheReason(t *testing.T) {
	text := CancelText(testSchedule())
	for _, want := range []string{"Cancelled", "2026-11-21 09:00:00 UTC", "Building power work"} {
		if !strings.Contains(text, want) {
			t.Fatalf("cancellation text lacks %q:\n%s", want, text)
		}
	}
}

func TestWroteNologinOnlyOwnsItsOwnFile(t *testing.T) {
	s := testSchedule()
	own := NologinText(s)
	if !WroteNologin(own, s.ID) {
		t.Fatalf("the marker for %s is not recognised:\n%s", s.ID, own)
	}
	if WroteNologin(own, "20261119T000000Z") {
		t.Fatal("another schedule's file was claimed")
	}
	if WroteNologin("System is going down NOW!", s.ID) {
		t.Fatal("a foreign /run/nologin was claimed")
	}
}
