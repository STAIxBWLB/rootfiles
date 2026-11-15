package shutdown

import (
	"fmt"
	"strings"
	"time"
)

// Unit is one rendered systemd unit file: a name under UnitDir and its text.
type Unit struct {
	Name    string
	Content string
}

// TimerNames lists the timers a schedule installs, in the order a cancel stops
// them: the poweroff first, because it is the one that must not fire. Without a
// schedule every notice timer the cap allows is listed too, because a recovery
// from an unreadable state file has no way to know how many were written.
func TimerNames(s *Schedule) []string {
	names := []string{UnitName + ".timer", blockUnit() + ".timer"}
	for i := 0; i < noticeCount(s); i++ {
		names = append(names, noticeUnit(i)+".timer")
	}
	return names
}

// noticeCount is how many notice units a schedule owns: its own list when the
// record is readable, and the accepted maximum when it is not.
func noticeCount(s *Schedule) int {
	if s == nil {
		return MaxNotices
	}
	return len(s.Notices)
}

// enableOrder is TimerNames reversed: the harmless timers are enabled first, so
// a failure before the last step leaves no poweroff armed.
func enableOrder(s *Schedule) []string {
	names := TimerNames(s)
	for i, j := 0, len(names)-1; i < j; i, j = i+1, j-1 {
		names[i], names[j] = names[j], names[i]
	}
	return names
}

func noticeUnit(index int) string {
	return fmt.Sprintf("%s-notice-%d", UnitName, index+1)
}

func blockUnit() string {
	return UnitName + "-block"
}

// accuracy keeps a timer to the second. systemd's one-minute default lets each
// host pick its own moment inside that window, which is exactly the drift the
// shared instant exists to prevent; the five-minute login block would be late
// by the same amount.
const accuracy = "AccuracySec=1s"

// calendar renders the absolute instant in the form systemd accepts with an
// explicit zone, so the host's own time zone cannot move the moment.
func calendar(at time.Time) string {
	return at.UTC().Format("2006-01-02 15:04:05") + " UTC"
}

// Units renders the timer/service pair for the poweroff and one pair per
// notice. Persistent=false everywhere: a host that boots after the instant
// must never act on a moment that has already passed.
func Units(bin string, s Schedule) []Unit {
	bin = binaryPath(bin)
	verb := actionVerb(s.Action)
	units := []Unit{
		{Name: UnitName + ".service", Content: fmt.Sprintf(`# Managed by rootfiles
[Unit]
Description=rootfiles scheduled %[1]s

[Service]
Type=oneshot
ExecStart=/bin/sh -c 'systemctl %[2]s'
`, verb, systemctlVerb(s.Action))},
		{Name: UnitName + ".timer", Content: fmt.Sprintf(`# Managed by rootfiles
[Unit]
Description=rootfiles scheduled %[1]s at %[2]s

[Timer]
OnCalendar=%[3]s
%[4]s
# A late boot must not act on an instant that has already passed.
Persistent=false

[Install]
WantedBy=timers.target
`, verb, s.At.UTC().Format(time.RFC3339), calendar(s.At), accuracy)},
		{Name: blockUnit() + ".service", Content: fmt.Sprintf(`# Managed by rootfiles
[Unit]
Description=rootfiles shutdown login block

[Service]
Type=oneshot
ExecStart=%[1]s shutdown block
`, bin)},
		{Name: blockUnit() + ".timer", Content: fmt.Sprintf(`# Managed by rootfiles
[Unit]
Description=rootfiles shutdown login block at %[1]s

[Timer]
OnCalendar=%[2]s
%[3]s
Persistent=false

[Install]
WantedBy=timers.target
`, s.At.Add(-NologinLead).UTC().Format(time.RFC3339), calendar(s.At.Add(-NologinLead)), accuracy)},
	}
	for i, n := range s.Notices {
		name := noticeUnit(i)
		units = append(units,
			Unit{Name: name + ".service", Content: fmt.Sprintf(`# Managed by rootfiles
[Unit]
Description=rootfiles shutdown notice (%[1]s before)

[Service]
Type=oneshot
ExecStart=%[2]s shutdown notify --notice '%[1]s'
`, n.Lead, bin)},
			Unit{Name: name + ".timer", Content: fmt.Sprintf(`# Managed by rootfiles
[Unit]
Description=rootfiles shutdown notice (%[1]s before) at %[2]s

[Timer]
OnCalendar=%[3]s
%[4]s
Persistent=false

[Install]
WantedBy=timers.target
`, n.Lead, n.At.UTC().Format(time.RFC3339), calendar(n.At), accuracy)},
		)
	}
	return units
}

func systemctlVerb(action string) string {
	if actionOrDefault(action) == ActionReboot {
		return "reboot"
	}
	return "poweroff"
}

// NoticeText is what users see: what happens, the instant in UTC and in the
// host's own zone, the time left and the operator's reason.
func NoticeText(s Schedule, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Scheduled %s: this host will %s at %s (%s).\n",
		actionOrDefault(s.Action), actionVerb(s.Action),
		s.At.UTC().Format("2006-01-02 15:04:05")+" UTC",
		s.At.Local().Format("2006-01-02 15:04:05 MST"))
	if left := s.At.Sub(now); left > 0 {
		fmt.Fprintf(&b, "About %s left.\n", TimeLeft(left))
	} else {
		b.WriteString("The scheduled moment has passed; the host stops as soon as systemd acts.\n")
	}
	if s.Message != "" {
		fmt.Fprintf(&b, "%s\n", s.Message)
	}
	if s.At.Sub(now) <= NologinLead {
		b.WriteString("Tenant logins may be blocked. Save your work now.\n")
	}
	return b.String()
}

// CancelText tells sessions the scheduled stop will not happen.
func CancelText(s Schedule) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Cancelled: the %s scheduled for %s will not happen. Tenant login restrictions from this schedule are being removed.\n",
		actionVerb(s.Action), s.At.UTC().Format("2006-01-02 15:04:05")+" UTC")
	if s.Message != "" {
		fmt.Fprintf(&b, "It was announced as: %s\n", s.Message)
	}
	return b.String()
}

// NologinText is written to /run/nologin; it carries the marker cancel uses to
// remove only the file this feature wrote.
func NologinText(s Schedule) string {
	return fmt.Sprintf("Scheduled %s at %s. %s%s\n",
		actionVerb(s.Action), s.At.UTC().Format("2006-01-02 15:04:05")+" UTC",
		nologinMarker, s.ID)
}

// WroteNologin reports whether content is a file this feature wrote for id.
func WroteNologin(content, id string) bool {
	return strings.Contains(content, nologinMarker+id)
}

// TimeLeft renders the remaining time the way notices and status reports
// both show it.
func TimeLeft(d time.Duration) string {
	d = d.Round(time.Minute)
	if d < time.Minute {
		return "under a minute"
	}
	if d < time.Hour {
		minutes := int(d.Minutes())
		return fmt.Sprintf("%d minute%s", minutes, plural(minutes))
	}
	hours := int(d.Hours())
	if minutes := int(d.Minutes()) % 60; minutes != 0 {
		return fmt.Sprintf("%dh%02dm", hours, minutes)
	}
	return fmt.Sprintf("%d hour%s", hours, plural(hours))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
