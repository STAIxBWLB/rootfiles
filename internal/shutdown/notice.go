package shutdown

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/STAIxBWLB/rootfiles/internal/exec"
)

// containerNologin is the block file inside a tenant container. It is fixed:
// a test override of the host path must not reach into the containers.
const containerNologin = "/run/nologin"

// ScheduleRun writes the units and the state file for one host, then enables
// the timers. Warnings are non-fatal (a notice that could not be delivered);
// an error means the schedule is not in force.
func ScheduleRun(ctx context.Context, env Env, req Request) (*Schedule, []string, error) {
	r := env.Runner
	now := env.now()
	at := req.At.UTC().Truncate(time.Second)
	if err := ValidateAt(at, now); err != nil {
		return nil, nil, err
	}
	action := actionOrDefault(req.Action)
	message, err := SanitizeMessage(req.Message)
	if err != nil {
		return nil, nil, err
	}
	notices, missed, err := ParseNotices(req.Notices, at, now)
	if err != nil {
		return nil, nil, err
	}
	bin := binaryPath(req.Binary)
	if err := checkBinary(bin); err != nil {
		return nil, nil, err
	}

	existing, err := Load()
	if err != nil {
		return nil, nil, err
	}
	var warnings []string
	if Expired(existing, now) {
		// The moment came and went, and the record and units outlived it. They
		// are leftovers rather than a schedule, so they go without --replace and
		// without telling sessions that a stop was cancelled.
		leftover, err := removeSchedule(ctx, env, existing, false)
		warnings = append(warnings, leftover...)
		if err != nil {
			return nil, warnings, err
		}
		existing = nil
	}
	if existing != nil {
		if !req.Replace {
			return nil, nil, fmt.Errorf("a %s is already scheduled for %s (id %s): pass --replace to replace it, or run 'rootfiles shutdown cancel' first",
				actionVerb(existing.Action), existing.At.UTC().Format(time.RFC3339), existing.ID)
		}
		replaced, err := removeSchedule(ctx, env, existing, false)
		warnings = append(warnings, replaced...)
		if err != nil {
			return nil, warnings, fmt.Errorf("cannot replace the existing schedule: %w", err)
		}
	}

	s := &Schedule{
		ID:        newID(now),
		At:        at,
		Action:    action,
		Message:   message,
		Notices:   notices,
		CreatedBy: approvedBy(),
		CreatedAt: now,
	}
	changed, err := installUnits(ctx, r, Units(bin, *s))
	if err != nil {
		return nil, warnings, err
	}
	// The state file lands before the timers are enabled, so a host can always
	// cancel what it wrote even when enabling fails.
	if err := save(r, *s); err != nil {
		return nil, warnings, err
	}
	if changed {
		if _, err := r.Run(ctx, "systemctl", "daemon-reload"); err != nil {
			return s, warnings, fmt.Errorf("units written but systemd is not reachable: %w; run systemctl daemon-reload after restoring access, then 'rootfiles shutdown cancel' if the schedule is wrong", err)
		}
	}
	for _, timer := range enableOrder(s) {
		if _, err := r.Run(ctx, "systemctl", "enable", "--now", timer); err != nil {
			return s, warnings, fmt.Errorf("cannot enable %s: %w; the schedule is recorded, so run 'rootfiles shutdown cancel' to remove it before retrying", timer, err)
		}
	}
	if req.NotifyNow || missed || len(s.Notices) == 0 {
		// A lead time that has already passed still owes users one warning, and
		// `fleet shutdown now` asks for it explicitly.
		warnings = append(warnings, announce(ctx, r, s, now)...)
	}
	return s, warnings, nil
}

// announce sends one notice and, inside the 5-minute window, blocks new
// logins first so they stop before the host goes down.
func announce(ctx context.Context, r *exec.Runner, s *Schedule, now time.Time) []string {
	var warnings []string
	if s.At.Sub(now) <= NologinLead {
		warnings = append(warnings, blockLogins(ctx, r, s)...)
	}
	return append(warnings, broadcast(ctx, r, NoticeText(*s, now))...)
}

// NotifyRun sends one notice. It is what the notice timers run; a schedule
// that was cancelled or has already fired is silently done.
func NotifyRun(ctx context.Context, env Env, lead string) (*Schedule, []string, error) {
	s, err := Load()
	if err != nil || s == nil {
		return nil, nil, err
	}
	now := env.now()
	if !s.At.After(now) {
		return s, nil, nil
	}
	return s, announce(ctx, env.Runner, s, now), nil
}

// BlockRun stops new logins for the stored schedule. The block timer runs it at
// NologinLead before the instant, whatever lead times the notice list holds, so
// a schedule with `--notice 1h` still closes logins five minutes out.
func BlockRun(ctx context.Context, env Env) (*Schedule, []string, error) {
	s, err := LoadForReport(env.now())
	if err != nil || s == nil {
		return nil, nil, err
	}
	return s, blockLogins(ctx, env.Runner, s), nil
}

// CancelRun removes exactly what scheduling and notifying created and tells
// sessions the stop will not happen. Running it again changes nothing.
//
// A cancel that cannot stop or remove a timer fails instead of reporting
// success: systemd can fire a unit whose files are gone, so a schedule that was
// not fully disarmed is still a schedule.
func CancelRun(ctx context.Context, env Env) (*Schedule, []string, error) {
	s, err := Load()
	if err != nil {
		return cancelUnknown(ctx, env, err)
	}
	if s == nil {
		// A previous cancel already removed the record; any leftover units are
		// still ours to clean up.
		if err := removeUnits(ctx, env.Runner, nil); err != nil {
			return nil, nil, err
		}
		return nil, nil, nil
	}
	warnings, err := removeSchedule(ctx, env, s, true)
	return s, warnings, err
}

// cancelUnknown is the recovery path Load's message names: the state file
// cannot be read, so the units are found by name and only a /run/nologin
// carrying this feature's marker is removed.
func cancelUnknown(ctx context.Context, env Env, loadErr error) (*Schedule, []string, error) {
	r := env.Runner
	warnings := removeNologin(ctx, r, nil)
	if err := removeUnits(ctx, r, nil); err != nil {
		return nil, warnings, fmt.Errorf("%s; the units could not be removed either: %w", firstLine(loadErr), err)
	}
	if err := clearState(r); err != nil {
		return nil, warnings, fmt.Errorf("%s; %w", firstLine(loadErr), err)
	}
	return nil, append(warnings, fmt.Sprintf("the stored schedule could not be read (%s), so the units, any login block and the state file were removed by name", firstLine(loadErr))), nil
}

// removeSchedule undoes one schedule: the nologin it wrote, the units and the
// state file, and (when tellSessions is set) the cancellation notice. The state
// file stays when a unit could not be stopped, so the schedule is still on
// record and can be cancelled again.
func removeSchedule(ctx context.Context, env Env, s *Schedule, tellSessions bool) ([]string, error) {
	r := env.Runner
	warnings := removeNologin(ctx, r, s)
	if tellSessions {
		warnings = append(warnings, broadcast(ctx, r, CancelText(*s))...)
	}
	if err := removeUnits(ctx, r, s); err != nil {
		return warnings, err
	}
	if err := clearState(r); err != nil {
		return warnings, err
	}
	return warnings, nil
}

func installUnits(ctx context.Context, r *exec.Runner, units []Unit) (bool, error) {
	if err := r.MkdirAll(UnitDir, 0755); err != nil {
		return false, fmt.Errorf("cannot create %s: %w; check the unit directory permissions before scheduling", UnitDir, err)
	}
	changed := false
	for _, u := range units {
		path := filepath.Join(UnitDir, u.Name)
		current, err := r.ReadFile(path)
		if err == nil && exec.ManagedContentEqual(string(current), u.Content) {
			continue
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return changed, fmt.Errorf("cannot read shutdown unit %s: %w; repair its permissions before scheduling", path, err)
		}
		if err := r.WriteFile(path, []byte(u.Content), 0644); err != nil {
			return changed, fmt.Errorf("cannot write shutdown unit %s: %w; check the unit directory and backup permissions before retrying", path, err)
		}
		changed = true
	}
	return changed, nil
}

// maxNoticeUnits bounds the scan a cancel without a readable state file makes
// for notice units; a notice list that long is already absurd.
const maxNoticeUnits = 16

// removeUnits stops the timers and deletes the unit files. A timer systemd
// still holds can fire after its files are gone, so a failing stop or reload is
// an error, not a warning; a host with no systemctl has nothing armed.
func removeUnits(ctx context.Context, r *exec.Runner, s *Schedule) error {
	if r.CommandExists("systemctl") {
		for _, timer := range TimerNames(s) {
			if _, err := r.Run(ctx, "systemctl", "disable", "--now", timer); err != nil && timerActive(ctx, r, timer) {
				return fmt.Errorf("cannot stop %s: %w; the schedule is still in force, so restore systemd access and run 'rootfiles shutdown cancel' again", timer, err)
			}
		}
	}
	removed := false
	for _, name := range unitFileNames(s) {
		path := filepath.Join(UnitDir, name)
		err := r.Remove(path)
		if err == nil {
			removed = true
			continue
		}
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		return fmt.Errorf("cannot remove shutdown unit %s: %w; the schedule is still in force, so remove it by hand once the host is reachable", path, err)
	}
	if removed && r.CommandExists("systemctl") {
		if _, err := r.Run(ctx, "systemctl", "daemon-reload"); err != nil {
			return fmt.Errorf("the unit files are gone but systemd still holds them: %w; run 'systemctl daemon-reload' and then 'rootfiles shutdown cancel' again", err)
		}
	}
	return nil
}

// unitFileNames lists what a schedule wrote. With a known schedule the notice
// units come from its own list; without one they are found by name, because the
// state file is what would have said how many there were.
func unitFileNames(s *Schedule) []string {
	names := []string{UnitName + ".service", UnitName + ".timer", blockUnit() + ".service", blockUnit() + ".timer"}
	if s != nil {
		for i := range s.Notices {
			names = append(names, noticeUnit(i)+".service", noticeUnit(i)+".timer")
		}
		return names
	}
	for i := 0; i < maxNoticeUnits; i++ {
		names = append(names, noticeUnit(i)+".service", noticeUnit(i)+".timer")
	}
	return names
}

// timerActive asks systemd whether a timer is still running. `is-active` exits
// non-zero for every state but active, so an error means "not running".
func timerActive(ctx context.Context, r *exec.Runner, timer string) bool {
	res, err := r.Query(ctx, "systemctl", "is-active", timer)
	return err == nil && strings.TrimSpace(res.Stdout) == "active"
}

// removeNologin deletes /run/nologin only where this feature wrote it, on the
// host and inside tenant containers. With no schedule (an unreadable state
// file) a file carrying this feature's marker for any id is ours.
func removeNologin(ctx context.Context, r *exec.Runner, s *Schedule) []string {
	var warnings []string
	ours := func(content string) bool {
		if s != nil {
			return WroteNologin(content, s.ID)
		}
		return strings.Contains(content, nologinMarker)
	}
	if data, err := r.ReadFile(NologinPath); err == nil && ours(string(data)) {
		if err := r.Remove(NologinPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			warnings = append(warnings, fmt.Sprintf("cannot remove %s: %v", NologinPath, firstLine(err)))
		}
	}
	containers, err := tenantContainers(ctx, r)
	if err != nil {
		return append(warnings, firstLine(err))
	}
	for _, c := range containers {
		res, err := r.Query(ctx, "docker", "exec", c, "cat", containerNologin)
		if err != nil || !ours(res.Stdout) {
			continue
		}
		if _, err := r.Run(ctx, "docker", "exec", c, "rm", "-f", containerNologin); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: cannot remove %s: %s", c, containerNologin, firstLine(err)))
		}
	}
	return warnings
}

// blockLogins writes /run/nologin on the host and in every running tenant
// container whose sshd PAM stack honours pam_nologin. A /run/nologin this
// feature did not write is someone else's maintenance lock: it stays, and it
// stays after the cancel too.
func blockLogins(ctx context.Context, r *exec.Runner, s *Schedule) []string {
	var warnings []string
	text := NologinText(*s)
	switch existing, err := r.ReadFile(NologinPath); {
	case err == nil && !strings.Contains(string(existing), nologinMarker):
		warnings = append(warnings, fmt.Sprintf("%s already exists and was not written by rootfiles: left it in place", NologinPath))
	case err != nil && !errors.Is(err, os.ErrNotExist):
		warnings = append(warnings, fmt.Sprintf("cannot read %s: %s", NologinPath, firstLine(err)))
	default:
		if err := r.MkdirAll(filepath.Dir(NologinPath), 0755); err != nil {
			warnings = append(warnings, fmt.Sprintf("cannot create %s: %s", filepath.Dir(NologinPath), firstLine(err)))
		} else if err := r.WriteFile(NologinPath, []byte(text), 0644); err != nil {
			warnings = append(warnings, fmt.Sprintf("cannot write %s: %s", NologinPath, firstLine(err)))
		}
	}
	containers, err := tenantContainers(ctx, r)
	if err != nil {
		return append(warnings, firstLine(err))
	}
	for _, c := range containers {
		if !pamNologinActive(ctx, r, c) {
			continue
		}
		if existing, err := r.Query(ctx, "docker", "exec", c, "cat", containerNologin); err == nil && !strings.Contains(existing.Stdout, nologinMarker) {
			warnings = append(warnings, fmt.Sprintf("%s: %s already exists and was not written by rootfiles: left it in place", c, containerNologin))
			continue
		}
		if _, err := r.RunInput(ctx, text, "docker", "exec", "-i", c, "sh", "-c", "cat > "+containerNologin); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: cannot block logins: %s", c, firstLine(err)))
		}
	}
	return warnings
}

// pamNologinActive reports whether the container's sshd PAM stack loads
// pam_nologin, so an unwritable container is left alone rather than reported.
func pamNologinActive(ctx context.Context, r *exec.Runner, container string) bool {
	_, err := r.Query(ctx, "docker", "exec", container, "grep", "-qs", "pam_nologin", "/etc/pam.d/sshd")
	return err == nil
}

// tenantContainers lists running tenant containers by name.
func tenantContainers(ctx context.Context, r *exec.Runner) ([]string, error) {
	res, err := r.Query(ctx, "docker", "ps", "--format", "{{.Names}}")
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w", err)
	}
	var names []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		if name := strings.TrimSpace(line); strings.HasPrefix(name, "tenant-") {
			names = append(names, name)
		}
	}
	return names, nil
}

// broadcast writes text to host sessions and to every running tenant
// container. A container without wall is a warning: one broken tenant must
// never stop the notice reaching the others.
func broadcast(ctx context.Context, r *exec.Runner, text string) []string {
	var warnings []string
	if _, err := r.RunInput(ctx, text, "wall"); err != nil {
		warnings = append(warnings, "host sessions: "+firstLine(err))
	}
	containers, err := tenantContainers(ctx, r)
	if err != nil {
		return append(warnings, firstLine(err))
	}
	for _, c := range containers {
		if _, err := r.RunInput(ctx, text, "docker", "exec", "-i", c, "wall"); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: wall: %s", c, firstLine(err)))
		}
	}
	return warnings
}

// firstLine keeps a journal warning to the line that explains it.
func firstLine(err error) string {
	line := strings.TrimSpace(strings.SplitN(err.Error(), "\n", 2)[0])
	if line == "" {
		return "failed"
	}
	return line
}
