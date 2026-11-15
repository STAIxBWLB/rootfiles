package shutdown

import (
	"strings"
	"testing"
	"time"
)

var (
	testNow = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	testAt  = time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
)

func testSchedule() Schedule {
	return Schedule{
		ID:        "20261120T090000Z",
		At:        testAt,
		Action:    ActionPoweroff,
		Message:   "Building power work, back at 22:00.",
		Notices:   []Notice{{Lead: "15m", At: testAt.Add(-15 * time.Minute)}, {Lead: "5m", At: testAt.Add(-5 * time.Minute)}},
		CreatedBy: "ops",
		CreatedAt: testNow,
	}
}

func TestParseAtRequiresAnExplicitOffset(t *testing.T) {
	for _, value := range []string{"2026-11-21T18:00:00", "2026-11-21 18:00:00", "2026-11-21T18:00:00z", "tomorrow"} {
		_, err := ParseAt(value, testNow)
		if err == nil {
			t.Fatalf("--at %q without an explicit offset was accepted", value)
		}
		for _, want := range []string{"--at", "RFC 3339", "explicit offset"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("refusal %q does not name %q", err, want)
			}
		}
	}
}

func TestParseAtAcceptsOffsetsAndReturnsUTC(t *testing.T) {
	for _, tc := range []struct{ value, want string }{
		{"2026-11-21T18:00:00+09:00", "2026-11-21T09:00:00Z"},
		{"2026-11-21T09:00:00Z", "2026-11-21T09:00:00Z"},
		{"2026-11-21T04:00:00-05:00", "2026-11-21T09:00:00Z"},
	} {
		at, err := ParseAt(tc.value, testNow)
		if err != nil {
			t.Fatalf("--at %q: %v", tc.value, err)
		}
		if got := at.Format(time.RFC3339); got != tc.want {
			t.Fatalf("--at %q became %s, want %s", tc.value, got, tc.want)
		}
	}
}

func TestParseAtWindow(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  string
	}{
		{"too soon", "2026-11-20T09:00:30Z", "at least 1 minute ahead"},
		{"already passed", "2026-11-20T08:00:00Z", "at least 1 minute ahead"},
		{"too far", "2026-12-21T09:00:00Z", "at most 30 days ahead"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseAt(tc.value, testNow)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("--at %s: got %v, want a refusal naming %q", tc.value, err, tc.want)
			}
		})
	}
	// The window is inclusive at both ends.
	if _, err := ParseAt("2026-11-20T09:01:00Z", testNow); err != nil {
		t.Fatalf("exactly one minute ahead was refused: %v", err)
	}
	if _, err := ParseAt("2026-12-20T09:00:00Z", testNow); err != nil {
		t.Fatalf("exactly 30 days ahead was refused: %v", err)
	}
}

func TestParseNoticesSkipsPastMomentsAndSorts(t *testing.T) {
	notices, err := ParseNotices("5m,24h,15m", testAt, testNow)
	if err != nil {
		t.Fatal(err)
	}
	// 24h before 2026-11-21T09:00Z is exactly now, so it is still kept.
	want := []struct {
		lead string
		at   time.Time
	}{
		{"24h", testNow},
		{"15m", testAt.Add(-15 * time.Minute)},
		{"5m", testAt.Add(-5 * time.Minute)},
	}
	if len(notices) != len(want) {
		t.Fatalf("notices = %#v, want %d entries", notices, len(want))
	}
	for i, w := range want {
		if notices[i].Lead != w.lead || !notices[i].At.Equal(w.at) {
			t.Fatalf("notice %d = %#v, want lead %s at %s", i, notices[i], w.lead, w.at)
		}
	}
}

func TestParseNoticesDropsPassedAndKeepsDefault(t *testing.T) {
	soon := testNow.Add(20 * time.Minute)
	notices, err := ParseNotices("", soon, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(notices) != 2 || notices[0].Lead != "15m" || notices[1].Lead != "5m" {
		t.Fatalf("default notices near a 20m instant = %#v, want the 15m and 5m pair", notices)
	}
}

func TestParseNoticesRefusals(t *testing.T) {
	for _, tc := range []struct{ value, want string }{
		{"soon", "--notice \"soon\""},
		{"0s", "positive durations"},
		{"-5m", "positive durations"},
		{"5m,5m", "lists 5m twice"},
	} {
		_, err := ParseNotices(tc.value, testAt, testNow)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("--notice %q: got %v, want a refusal naming %q", tc.value, err, tc.want)
		}
	}
}

func TestSanitizeMessageKeepsOnePrintableLine(t *testing.T) {
	got, err := SanitizeMessage("  Building power\twork,\nback at 22:00. \x00 ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Building power work, back at 22:00." {
		t.Fatalf("sanitized message = %q", got)
	}
	long := strings.Repeat("x", MaxMessageRunes+1)
	if _, err := SanitizeMessage(long); err == nil || !strings.Contains(err.Error(), "--message") {
		t.Fatalf("an over-long message: %v", err)
	}
	if _, err := SanitizeMessage(strings.Repeat("x", MaxMessageRunes)); err != nil {
		t.Fatalf("%d characters must be accepted: %v", MaxMessageRunes, err)
	}
}
