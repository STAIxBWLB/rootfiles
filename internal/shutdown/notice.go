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
		return nil, nil, untouched(err)
	}
	action := actionOrDefault(req.Action)
	message, err := SanitizeMessage(req.Message)
	if err != nil {
		return nil, nil, untouched(err)
	}
	notices, missed, err := ParseNotices(req.Notices, at, now)
	if err != nil {
		return nil, nil, untouched(err)
	}
	bin := binaryPath(req.Binary)
	if err := checkBinary(bin); err != nil {
		return nil, nil, untouched(err)
	}

	existing, err := Load()
	if err != nil {
		return nil, nil, untouched(err)
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
			return nil, nil, untouched(fmt.Errorf("a %s is already scheduled for %s (id %s): pass --replace to replace it, or run 'rootfiles shutdown cancel' first",
				actionVerb(existing.Action), existing.At.UTC().Format(time.RFC3339), existing.ID))
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
	if req.NotifyNow || missed || len(s.Notices) == 0 || s.At.Sub(now) <= NologinLead {
		// A lead time that has already passed still owes users one warning,
		// `fleet shutdown now` asks for it explicitly, and a schedule created
		// inside the five-minute window blocks logins at once: its block timer
		// would be set in the past and would never fire.
		warnings = append(warnings, announce(ctx, r, s, now)...)
	}
	return s, warnings, nil
}

// untouched marks a failure that happened before anything on the host changed,
// so a schedule already recorded there is still in force as it was.
func untouched(err error) error {
	return fmt.Errorf("%w: %w", ErrUntouched, err)
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
	if err := removeUnits(ctx, r, nil); err != nil {
		return nil, nil, fmt.Errorf("%s; the units could not be removed either: %w", firstLine(loadErr), err)
	}
	if err := clearState(r); err != nil {
		return nil, nil, fmt.Errorf("%s; %w", firstLine(loadErr), err)
	}
	warnings := removeNologin(ctx, r, nil)
	return nil, append(warnings, fmt.Sprintf("the stored schedule could not be read (%s), so the units, any login block and the state file were removed by name", firstLine(loadErr))), nil
}

// removeSchedule undoes one schedule: the nologin it wrote, the units and the
// state file, and (when tellSessions is set) the cancellation notice. The state
// file stays when a unit could not be stopped, so the schedule is still on
// record and can be cancelled again.
func removeSchedule(ctx context.Context, env Env, s *Schedule, tellSessions bool) ([]string, error) {
	r := env.Runner
	if err := removeUnits(ctx, r, s); err != nil {
		return nil, err
	}
	if err := clearState(r); err != nil {
		return nil, err
	}
	warnings := removeNologin(ctx, r, s)
	if tellSessions {
		warnings = append(warnings, broadcast(ctx, r, CancelText(*s))...)
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

// unitFileNames lists what a schedule wrote. With a known schedule the notice
// units come from its own list; without one every notice unit the accepted
// maximum allows is listed, because the state file is what would have said how
// many there were.
func unitFileNames(s *Schedule) []string {
	names := []string{UnitName + ".service", UnitName + ".timer", blockUnit() + ".service", blockUnit() + ".timer"}
	for i := 0; i < noticeCount(s); i++ {
		names = append(names, noticeUnit(i)+".service", noticeUnit(i)+".timer")
	}
	return names
}

// removeUnits stops the timers and deletes the unit files. A timer systemd can
// still hold fires after its files are gone, so the stop must be confirmed
// rather than assumed: a failing disable, a timer still active afterwards, or a
// query that cannot answer all leave the schedule in force. A host with no
// systemctl has nothing armed.
func removeUnits(ctx context.Context, r *exec.Runner, s *Schedule) error {
	if r.CommandExists("systemctl") {
		for _, timer := range TimerNames(s) {
			disableErr := error(nil)
			if _, err := r.Run(ctx, "systemctl", "disable", "--now", timer); err != nil {
				disableErr = err
			}
			switch queryTimer(ctx, r, timer) {
			case timerInactive:
				continue
			case timerActive:
				if disableErr != nil {
					return fmt.Errorf("cannot stop %s: %w; the schedule is still in force, so restore systemd access and run 'rootfiles shutdown cancel' again", timer, disableErr)
				}
				return fmt.Errorf("%s is still active after disable --now: the schedule is still in force, so run 'systemctl stop %s' and then 'rootfiles shutdown cancel' again", timer, timer)
			default:
				if disableErr != nil {
					return fmt.Errorf("cannot stop %s, and systemd does not answer whether it is still armed: %w; the schedule may still be in force, so restore systemd access and run 'rootfiles shutdown cancel' again", timer, disableErr)
				}
				return fmt.Errorf("systemd does not answer whether %s is still armed: the schedule may still be in force, so run 'systemctl is-active %s' and then 'rootfiles shutdown cancel' again", timer, timer)
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

// timerState is what systemd answers about a timer. timerUnknown is the answer
// that must never be read as "not running": an unreachable manager or a missing
// systemctl says nothing about a timer that may still be armed.
type timerState int

const (
	timerUnknown timerState = iota
	timerActive
	timerInactive
)

// queryTimer reads a timer's state. `is-active` exits 3 for inactive and 4 for
// not-found, which are answers; any other failure is the query not answering.
func queryTimer(ctx context.Context, r *exec.Runner, timer string) timerState {
	res, err := r.Query(ctx, "systemctl", "is-active", timer)
	if res != nil && (res.ExitCode == 3 || res.ExitCode == 4) {
		return timerInactive
	}
	if err != nil || res == nil {
		return timerUnknown
	}
	if strings.TrimSpace(res.Stdout) == "active" {
		return timerActive
	}
	return timerInactive
}

// removeNologin deletes /run/nologin where this feature wrote it: inside tenant
// containers, and on the host as legacy cleanup. This build blocks logins in
// tenants only, but an earlier build wrote the host file, so a host file
// carrying this feature's marker is still ours to delete while a foreign one is
// never touched. With no schedule (an unreadable state file) a file carrying
// the marker for any id is ours.
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

// blockLogins writes /run/nologin in every running tenant container whose sshd
// PAM stack honours pam_nologin, and nowhere else. The host's own /run/nologin
// is never written: it would close host sshd, and the controller needs host sshd
// to stay reachable so `fleet shutdown cancel` can always disarm a run it just
// armed. Those who need the warning are the ones logging in to the tenants
// anyway. A tenant file this feature did not write is someone else's maintenance
// lock: it stays, and it stays after the cancel too.
func blockLogins(ctx context.Context, r *exec.Runner, s *Schedule) []string {
	var warnings []string
	text := NologinText(*s)
	containers, err := tenantContainers(ctx, r)
	if err != nil {
		return append(warnings, firstLine(err))
	}
	for _, c := range containers {
		active, err := pamNologinActive(ctx, r, c)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: cannot inspect pam_nologin: %s", c, firstLine(err)))
			continue
		}
		if !active {
			continue
		}
		state, detail := inspectContainerNologin(ctx, r, c)
		switch state {
		case nologinForeign:
			warnings = append(warnings, fmt.Sprintf("%s: %s already exists and was not written by rootfiles: left it in place", c, containerNologin))
			continue
		case nologinUnknown:
			// Writing over a file we could not read would truncate a login block
			// this feature did not write, so an unknown state is not an absent one.
			warnings = append(warnings, fmt.Sprintf("%s: cannot inspect %s (%s): left it in place rather than overwrite a login block rootfiles did not write", c, containerNologin, detail))
			continue
		}
		if _, err := r.RunInput(ctx, text, "docker", "exec", "-i", c, "sh", "-c", "cat > "+containerNologin); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: cannot block logins: %s", c, firstLine(err)))
		}
	}
	return warnings
}

// TenantLoginStatus reports only what can be established from local container
// inspection. Unknown counts prevent an unreadable container from being called
// unblocked. Foreign nologin files still block logins and count as blocked.
type TenantLoginStatus struct {
	Checked int    `json:"checked"`
	Blocked int    `json:"blocked"`
	Unknown int    `json:"unknown"`
	Error   string `json:"error,omitempty"`
}

// InspectTenantLogins reads running tenant container state without changing it.
func InspectTenantLogins(ctx context.Context, r *exec.Runner) TenantLoginStatus {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var report TenantLoginStatus
	containers, err := tenantContainers(ctx, r)
	if err != nil {
		report.Unknown = 1
		report.Error = firstLine(err)
		return report
	}
	for _, c := range containers {
		probe, err := r.Query(ctx, "docker", "exec", c, "grep", "-qs", "pam_nologin", "/etc/pam.d/sshd")
		if err != nil {
			if probe != nil && probe.ExitCode == 1 {
				continue
			}
			report.Unknown++
			continue
		}
		report.Checked++
		status, _ := inspectContainerNologin(ctx, r, c)
		switch status {
		case nologinOurs, nologinForeign:
			report.Blocked++
		case nologinUnknown:
			report.Unknown++
		}
	}
	return report
}

// nologinState is what a tenant's login block file is: this feature's, someone
// else's, absent, or unreadable.
type nologinState int

const (
	nologinUnknown nologinState = iota
	nologinAbsent
	nologinOurs
	nologinForeign
)

// inspectContainerNologin reads the container's block file. A failed read is
// only treated as absence when a separate existence probe says the file is not
// there, because `cat >` would otherwise truncate a file we failed to read.
func inspectContainerNologin(ctx context.Context, r *exec.Runner, container string) (nologinState, string) {
	res, err := r.Query(ctx, "docker", "exec", container, "cat", containerNologin)
	if err == nil {
		if strings.Contains(res.Stdout, nologinMarker) {
			return nologinOurs, ""
		}
		return nologinForeign, ""
	}
	if probe, _ := r.Query(ctx, "docker", "exec", container, "test", "-e", containerNologin); probe != nil && probe.ExitCode == 1 {
		return nologinAbsent, ""
	}
	return nologinUnknown, firstLine(err)
}

// pamNologinActive reports whether the container's sshd PAM stack loads
// pam_nologin, so an unwritable container is left alone rather than reported.
func pamNologinActive(ctx context.Context, r *exec.Runner, container string) (bool, error) {
	res, err := r.Query(ctx, "docker", "exec", container, "grep", "-qs", "pam_nologin", "/etc/pam.d/sshd")
	if err == nil {
		return true, nil
	}
	if res != nil && res.ExitCode == 1 && strings.TrimSpace(res.Stderr) == "" {
		return false, nil
	}
	return false, err
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
