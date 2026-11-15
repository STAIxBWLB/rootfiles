// Package shutdown schedules one host poweroff or reboot at an absolute UTC
// instant, with advance notices to host sessions and tenant containers.
// Every write goes through exec.Runner so dry runs and backups apply.
package shutdown

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/STAIxBWLB/rootfiles/internal/exec"
	"github.com/STAIxBWLB/rootfiles/internal/state"
)

const (
	// UnitName is the base of the timer/service pair, matching the report
	// timer's rootfiles- prefix so `systemctl list-timers` groups them.
	UnitName = "rootfiles-shutdown"

	ActionPoweroff = "poweroff"
	ActionReboot   = "reboot"

	// MinLead and MaxLead bound the accepted distance to the instant: a host
	// needs a moment to write and enable units, and an unbounded horizon lets
	// a typo power the fleet off months later.
	MinLead = time.Minute
	MaxLead = 30 * 24 * time.Hour

	// DefaultNotices is the notice schedule used when --notice is omitted.
	DefaultNotices = "24h,1h,15m,5m"

	// MaxNotices bounds the notice list. A cancel that has to find the units
	// without a readable state file scans up to this many, so the bound is what
	// makes that scan complete rather than a guess.
	MaxNotices = 8

	// NologinLead is when new logins stop being accepted.
	NologinLead = 5 * time.Minute

	// MaxMessageRunes bounds the operator's reason in the stored schedule.
	MaxMessageRunes = 300

	// DefaultBinary is the installed path the notice units run, matching the
	// fleet controller's fixed remote binary.
	DefaultBinary = "/usr/local/bin/rootfiles"

	nologinMarker = "rootfiles-shutdown: "
)

// Locations the feature owns; tests override them so nothing touches a host.
var (
	UnitDir     = "/etc/systemd/system"
	NologinPath = "/run/nologin"
	// StatePath overrides the state file, which lives in state.Dir() when empty.
	StatePath = ""
)

// ErrUntouched marks a schedule attempt that failed before it changed anything
// on the host: no unit written and no state saved, so a schedule that was
// already recorded there is still in force exactly as it was. The fleet
// controller reads this to leave such a host out of its rollback.
var ErrUntouched = errors.New("nothing was changed")

// Notice is one advance warning: the lead time the operator asked for and the
// absolute instant it fires.
type Notice struct {
	Lead string    `json:"lead"`
	At   time.Time `json:"at"`
}

// Schedule is the single stored schedule for a host.
type Schedule struct {
	ID        string    `json:"id"`
	At        time.Time `json:"at"`
	Action    string    `json:"action"`
	Message   string    `json:"message,omitempty"`
	Notices   []Notice  `json:"notices"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// Request is what a caller asks for; At is already an absolute instant.
type Request struct {
	At        time.Time
	Action    string
	Message   string
	Notices   string // comma-separated lead times; empty uses DefaultNotices
	Replace   bool
	NotifyNow bool   // broadcast one notice immediately, as `fleet shutdown now` does
	Binary    string // rootfiles path the notice units run; empty uses DefaultBinary
}

// Env carries the runner and the clock, so tests pin both.
type Env struct {
	Runner *exec.Runner
	Now    func() time.Time
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

// ParseAt reads an RFC 3339 instant that carries an explicit offset and checks
// it against the accepted window.
func ParseAt(value string, now time.Time) (time.Time, error) {
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, fmt.Errorf("--at %q: use RFC 3339 with an explicit offset, such as 2026-11-21T18:00:00Z or 2026-11-21T18:00:00+09:00, so a host's own time zone cannot move the moment", value)
	}
	at = at.UTC().Truncate(time.Second)
	if err := ValidateAt(at, now); err != nil {
		return time.Time{}, err
	}
	return at, nil
}

// ValidateAt checks an already-parsed instant against the accepted window.
func ValidateAt(at, now time.Time) error {
	lead := at.Sub(now)
	switch {
	case lead < MinLead:
		return fmt.Errorf("--at %s is %s from now: must be at least 1 minute ahead so every host is scheduled before the moment",
			at.UTC().Format(time.RFC3339), rounded(lead))
	case lead > MaxLead:
		return fmt.Errorf("--at %s is %s from now: must be at most 30 days ahead",
			at.UTC().Format(time.RFC3339), rounded(lead))
	}
	return nil
}

// ParseNotices turns the operator's lead times into absolute notice instants,
// dropping the ones whose moment has already passed. It also reports whether
// any lead was dropped: a schedule written inside a lead time has already
// missed a warning users were promised, so the caller owes them one now.
func ParseNotices(value string, at, now time.Time) ([]Notice, bool, error) {
	if strings.TrimSpace(value) == "" {
		value = DefaultNotices
	}
	seen := make(map[string]bool)
	out := make([]Notice, 0, 4)
	missed := false
	for _, token := range strings.Split(value, ",") {
		lead := strings.TrimSpace(token)
		if lead == "" {
			continue
		}
		d, err := time.ParseDuration(lead)
		if err != nil || d <= 0 {
			return nil, false, fmt.Errorf("--notice %q: use positive durations such as %s", lead, DefaultNotices)
		}
		if seen[lead] {
			return nil, false, fmt.Errorf("--notice lists %s twice: each lead time appears once", lead)
		}
		seen[lead] = true
		moment := at.Add(-d)
		if moment.Before(now) {
			missed = true
			continue
		}
		out = append(out, Notice{Lead: lead, At: moment})
	}
	if len(out) > MaxNotices {
		return nil, false, fmt.Errorf("--notice lists %d lead times: at most %d are accepted, so a cancel without a readable state file can still find every unit this wrote", len(out), MaxNotices)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, missed, nil
}

// SanitizeMessage reduces the operator's reason to one line of printable text.
func SanitizeMessage(value string) (string, error) {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ')
		case unicode.IsPrint(r):
			b.WriteRune(r)
		}
	}
	cleaned := strings.Join(strings.Fields(b.String()), " ")
	if n := utf8.RuneCountInString(cleaned); n > MaxMessageRunes {
		return "", fmt.Errorf("--message is %d characters: keep the reason to %d or fewer", n, MaxMessageRunes)
	}
	return cleaned, nil
}

// StatePathFor returns the state file, honouring the test override.
func StatePathFor() string {
	if StatePath != "" {
		return StatePath
	}
	return filepath.Join(state.Dir(), "shutdown.json")
}

// Load reads the stored schedule; a missing file is no schedule, not an error.
func Load() (*Schedule, error) {
	data, err := os.ReadFile(StatePathFor())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read scheduled shutdown %s: %w; check its permissions before scheduling or cancelling", StatePathFor(), err)
	}
	var s Schedule
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("scheduled shutdown %s is not valid JSON: %w; run 'rootfiles shutdown cancel' to remove it", StatePathFor(), err)
	}
	if s.ID == "" || s.At.IsZero() {
		return nil, fmt.Errorf("scheduled shutdown %s is incomplete; run 'rootfiles shutdown cancel' to remove it", StatePathFor())
	}
	s.Action = actionOrDefault(s.Action)
	return &s, nil
}

// Expired reports whether a stored schedule's instant has passed. The record
// and its units can outlive the moment (a host that booted afterwards keeps
// both, and Persistent=false stops them from firing), so an expired record is
// leftovers, not a schedule.
func Expired(s *Schedule, now time.Time) bool {
	return s != nil && !s.At.After(now)
}

// LoadForReport reads the stored schedule for a report: an expired record is
// not a schedule, so it reads as none. Scheduling and cancelling still see it
// through Load, so the next run cleans up what it left behind.
func LoadForReport(now time.Time) (*Schedule, error) {
	s, err := Load()
	if err != nil || Expired(s, now) {
		return nil, err
	}
	return s, nil
}

func save(r *exec.Runner, s Schedule) error {
	path := StatePathFor()
	if err := r.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("cannot create %s: %w; check the state directory permissions before scheduling", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := r.WriteFile(tmp, append(data, '\n'), 0644); err != nil {
		return fmt.Errorf("cannot write %s: %w; check the state directory and backup permissions before retrying", tmp, err)
	}
	if err := r.Rename(tmp, path); err != nil {
		return fmt.Errorf("cannot replace %s: %w; check the state directory permissions before retrying", path, err)
	}
	return nil
}

func clearState(r *exec.Runner) error {
	err := r.Remove(StatePathFor())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cannot remove %s: %w; remove it by hand once the host is reachable", StatePathFor(), err)
	}
	return nil
}

func actionOrDefault(action string) string {
	if action == ActionReboot {
		return ActionReboot
	}
	return ActionPoweroff
}

// actionVerb is what the notice tells users will happen.
func actionVerb(action string) string {
	if actionOrDefault(action) == ActionReboot {
		return "reboot"
	}
	return "power off"
}

func binaryPath(bin string) string {
	if strings.TrimSpace(bin) == "" {
		return DefaultBinary
	}
	return bin
}

func checkBinary(bin string) error {
	if strings.ContainsAny(bin, " \t\n\r'\"") {
		return fmt.Errorf("cannot schedule: the rootfiles binary path %q contains whitespace or quotes, which a systemd unit cannot run; install rootfiles at a path without them", bin)
	}
	return nil
}

func approvedBy() string {
	for _, key := range []string{"SUDO_USER", "USER", "LOGNAME"} {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return "unknown"
}

func newID(now time.Time) string {
	return now.UTC().Format("20060102T150405Z")
}

func rounded(d time.Duration) string {
	if d < 0 {
		return "in the past"
	}
	return d.Round(time.Second).String()
}
