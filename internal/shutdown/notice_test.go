package shutdown

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/rootfiles/internal/exec"
)

// testEnv points every owned path at a temp dir and pins the clock, so no
// test can read or write a real host path.
func testEnv(t *testing.T) (Env, string) {
	t.Helper()
	dir := t.TempDir()
	oldUnit, oldNologin, oldState := UnitDir, NologinPath, StatePath
	UnitDir = filepath.Join(dir, "units")
	NologinPath = filepath.Join(dir, "run", "nologin")
	StatePath = filepath.Join(dir, "state", "shutdown.json")
	if err := os.MkdirAll(UnitDir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { UnitDir, NologinPath, StatePath = oldUnit, oldNologin, oldState })
	return Env{Runner: exec.NewRunner(false, slog.New(slog.NewTextHandler(io.Discard, nil))), Now: func() time.Time { return testNow }}, dir
}

// installFakes puts a docker, wall and systemctl stub first on PATH. docker
// answers from files named <container>.<kind> inside dir.
func installFakes(t *testing.T, dir string) {
	t.Helper()
	fake := filepath.Join(dir, "fake")
	if err := os.MkdirAll(fake, 0755); err != nil {
		t.Fatal(err)
	}
	scripts := map[string]string{
		"docker": `#!/bin/sh
printf '%s\n' "$*" >> '` + dir + `/docker.log'
case "$1" in
ps) cat '` + dir + `/containers' 2>/dev/null ;;
exec)
	shift
	[ "$1" = "-i" ] && shift
	c="$1"; shift
	case "$1" in
	cat) if [ -e '` + dir + `/failcat' ]; then exit 1; fi; cat '` + dir + `/'"$c"'.nologin' 2>/dev/null ;;
	test) [ -e '` + dir + `/'"$c"'.nologin' ] ;;
	grep) [ -e '` + dir + `/'"$c"'.pam' ] ;;
	wall) if [ -e '` + dir + `/'"$c"'.nowall' ]; then exit 1; fi; cat >> '` + dir + `/'"$c"'.wall' ;;
	sh) cat >> '` + dir + `/'"$c"'.nologin' ;;
	esac ;;
esac
`,
		"wall": `#!/bin/sh
cat >> '` + dir + `/host.wall'
`,
		"systemctl": `#!/bin/sh
printf '%s\n' "$*" >> '` + dir + `/systemctl.log'
`,
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(fake, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", fake+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	data, _ := os.ReadFile(path)
	return string(data)
}

func unitNames(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(UnitDir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func request(at time.Time, notices string) Request {
	return Request{At: at, Message: "Building power work, back at 22:00.", Notices: notices, Binary: "/usr/local/bin/rootfiles"}
}

func TestScheduleRunWritesUnitsStateAndTimers(t *testing.T) {
	env, dir := testEnv(t)
	installFakes(t, dir)

	s, warnings, err := ScheduleRun(context.Background(), env, request(testNow.Add(2*time.Hour), "15m,5m"))
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if got, want := len(unitNames(t)), 8; got != want {
		t.Fatalf("units = %v, want the timer/service pair, the login-block pair and two notice pairs", unitNames(t))
	}
	if !s.At.Equal(testNow.Add(2 * time.Hour)) {
		t.Fatalf("schedule instant = %s", s.At)
	}

	data, err := os.ReadFile(StatePath)
	if err != nil {
		t.Fatal(err)
	}
	var stored Schedule
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("state file is not JSON: %v\n%s", err, data)
	}
	if stored.ID != s.ID || !stored.At.Equal(testAt.Add(-22*time.Hour)) || stored.Action != ActionPoweroff || len(stored.Notices) != 2 {
		t.Fatalf("stored schedule = %+v", stored)
	}
	if stored.CreatedBy == "" || stored.CreatedAt.IsZero() {
		t.Fatalf("stored schedule does not record who and when: %+v", stored)
	}
	if !strings.HasSuffix(stored.At.Format(time.RFC3339Nano), "Z") {
		t.Fatalf("stored instant is not UTC: %s", stored.At)
	}

	calls := readLog(t, filepath.Join(dir, "systemctl.log"))
	for _, want := range []string{"daemon-reload", "enable --now rootfiles-shutdown.timer", "enable --now rootfiles-shutdown-block.timer", "enable --now rootfiles-shutdown-notice-1.timer", "enable --now rootfiles-shutdown-notice-2.timer"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("systemd was not asked to %q:\n%s", want, calls)
		}
	}
	if strings.Contains(calls, "poweroff") || strings.Contains(calls, "reboot") {
		t.Fatalf("scheduling ran the action itself:\n%s", calls)
	}
	if wall := readLog(t, filepath.Join(dir, "host.wall")); wall != "" {
		t.Fatalf("a future schedule announced itself immediately: %q", wall)
	}
}

func TestScheduleRunRefusesASecondScheduleUnlessReplaced(t *testing.T) {
	env, dir := testEnv(t)
	installFakes(t, dir)
	if _, _, err := ScheduleRun(context.Background(), env, request(testNow.Add(2*time.Hour), "15m,5m")); err != nil {
		t.Fatal(err)
	}
	_, _, err := ScheduleRun(context.Background(), env, request(testNow.Add(3*time.Hour), "15m,5m"))
	if err == nil {
		t.Fatal("a second schedule was accepted")
	}
	for _, want := range []string{"already scheduled", "2026-11-20T11:00:00Z", "--replace", "rootfiles shutdown cancel"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not name %q", err, want)
		}
	}

	replaced := request(testNow.Add(3*time.Hour), "15m")
	replaced.Replace = true
	if _, _, err := ScheduleRun(context.Background(), env, replaced); err != nil {
		t.Fatalf("--replace failed: %v", err)
	}
	names := unitNames(t)
	if len(names) != 6 {
		t.Fatalf("replace left %v, want the pair, the login-block pair and one notice pair", names)
	}
	for _, gone := range []string{"rootfiles-shutdown-notice-2.service", "rootfiles-shutdown-notice-2.timer"} {
		if _, err := os.Stat(filepath.Join(UnitDir, gone)); !os.IsNotExist(err) {
			t.Fatalf("replace kept %s", gone)
		}
	}
	s, err := Load()
	if err != nil || s == nil || !s.At.Equal(testNow.Add(3*time.Hour)) {
		t.Fatalf("replacement was not stored: %+v err=%v", s, err)
	}
}

func TestScheduleRunSendsOneNoticeWhenEveryLeadPassed(t *testing.T) {
	env, dir := testEnv(t)
	installFakes(t, dir)
	if _, _, err := ScheduleRun(context.Background(), env, request(testNow.Add(2*time.Minute), "")); err != nil {
		t.Fatal(err)
	}
	wall := readLog(t, filepath.Join(dir, "host.wall"))
	if !strings.Contains(wall, "will power off") {
		t.Fatalf("no notice was sent for an instant inside every lead:\n%q", wall)
	}
}

func TestScheduleRunNotifyNowSendsEvenWithFutureNotices(t *testing.T) {
	env, dir := testEnv(t)
	installFakes(t, dir)
	req := request(testNow.Add(24*time.Hour), "")
	req.NotifyNow = true
	if _, _, err := ScheduleRun(context.Background(), env, req); err != nil {
		t.Fatal(err)
	}
	if wall := readLog(t, filepath.Join(dir, "host.wall")); !strings.Contains(wall, "will power off") {
		t.Fatalf("--notify-now sent nothing:\n%q", wall)
	}
}

func TestScheduleRunDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	oldUnit, oldNologin, oldState := UnitDir, NologinPath, StatePath
	UnitDir = filepath.Join(dir, "units")
	NologinPath = filepath.Join(dir, "run", "nologin")
	StatePath = filepath.Join(dir, "state", "shutdown.json")
	t.Cleanup(func() { UnitDir, NologinPath, StatePath = oldUnit, oldNologin, oldState })
	installFakes(t, dir)

	env := Env{Runner: exec.NewRunner(true, slog.New(slog.NewTextHandler(io.Discard, nil))), Now: func() time.Time { return testNow }}
	req := request(testNow.Add(2*time.Minute), "")
	req.NotifyNow = true
	if _, _, err := ScheduleRun(context.Background(), env, req); err != nil {
		t.Fatal(err)
	}
	if names := readLog(t, filepath.Join(dir, "systemctl.log")); names != "" {
		t.Fatalf("dry run called systemd:\n%s", names)
	}
	for _, path := range []string{UnitDir, StatePath, NologinPath, filepath.Join(dir, "host.wall")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("dry run created %s", path)
		}
	}
}

func TestCancelRunRemovesExactlyWhatItWroteAndIsIdempotent(t *testing.T) {
	env, dir := testEnv(t)
	installFakes(t, dir)
	req := request(testNow.Add(3*time.Minute), "5m")
	req.NotifyNow = true
	if _, _, err := ScheduleRun(context.Background(), env, req); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(NologinPath); !os.IsNotExist(err) {
		t.Fatalf("notice touched the host login block: %v", err)
	}

	ctx := context.Background()
	cancelled, warnings, err := CancelRun(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled == nil || cancelled.ID == "" {
		t.Fatal("cancel did not report the schedule it removed")
	}
	for _, w := range warnings {
		if !strings.Contains(w, "docker") {
			t.Fatalf("unexpected cancel warning: %q", w)
		}
	}
	if names := unitNames(t); len(names) != 0 {
		t.Fatalf("cancel left units: %v", names)
	}
	for _, path := range []string{StatePath, NologinPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("cancel left %s", path)
		}
	}
	if wall := readLog(t, filepath.Join(dir, "host.wall")); !strings.Contains(wall, "Cancelled") {
		t.Fatalf("cancel did not tell sessions:\n%q", wall)
	}

	// The second run is a no-op, not an error.
	gone, warnings, err := CancelRun(ctx, env)
	if err != nil || gone != nil || len(warnings) != 0 {
		t.Fatalf("second cancel = %v, %v, %v", gone, warnings, err)
	}
}

func TestCancelRunLeavesAForeignNologin(t *testing.T) {
	env, dir := testEnv(t)
	installFakes(t, dir)
	if err := os.MkdirAll(filepath.Dir(NologinPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(NologinPath, []byte("System is going down NOW!"), 0644); err != nil {
		t.Fatal(err)
	}
	// Even with a schedule recorded, a file another tool wrote is not ours.
	if err := save(env.Runner, testSchedule()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CancelRun(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(NologinPath)
	if err != nil || string(data) != "System is going down NOW!" {
		t.Fatalf("a foreign /run/nologin was changed: %q err=%v", data, err)
	}
}

func TestNotifyBroadcastsToHostAndTenantsAndWarnsOnFailure(t *testing.T) {
	env, dir := testEnv(t)
	installFakes(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "containers"), []byte("tenant-a\ntenant-b\nunrelated\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tenant-b.nowall"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	s := testSchedule()
	s.At = testNow.Add(15 * time.Minute)
	if err := save(env.Runner, s); err != nil {
		t.Fatal(err)
	}

	got, warnings, err := NotifyRun(context.Background(), env, "15m")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != s.ID {
		t.Fatalf("notify did not report the schedule: %#v", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "tenant-b") {
		t.Fatalf("a failing container must be one warning naming it: %v", warnings)
	}
	for _, path := range []string{filepath.Join(dir, "host.wall"), filepath.Join(dir, "tenant-a.wall")} {
		if text := readLog(t, path); !strings.Contains(text, "About 15 minutes left.") {
			t.Fatalf("%s did not receive the notice:\n%s", path, text)
		}
	}
	if text := readLog(t, filepath.Join(dir, "tenant-b.wall")); text != "" {
		t.Fatalf("the failing tenant received a notice anyway: %q", text)
	}
	for _, path := range []string{filepath.Join(dir, "tenant-a.nologin"), filepath.Join(dir, "tenant-b.nologin")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("a 15m notice wrote %s", path)
		}
	}
}

func TestNotifyBlocksLoginsInsideTheFiveMinuteWindow(t *testing.T) {
	env, dir := testEnv(t)
	installFakes(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "containers"), []byte("tenant-a\ntenant-b\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Only tenant-a's sshd honours pam_nologin.
	if err := os.WriteFile(filepath.Join(dir, "tenant-a.pam"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	s := testSchedule()
	s.At = testNow.Add(3 * time.Minute)
	if err := save(env.Runner, s); err != nil {
		t.Fatal(err)
	}

	if _, _, err := NotifyRun(context.Background(), env, "5m"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(NologinPath); !os.IsNotExist(err) {
		t.Fatalf("notify touched the host login block: %v", err)
	}
	if text := readLog(t, filepath.Join(dir, "tenant-a.nologin")); !WroteNologin(text, s.ID) {
		t.Fatalf("tenant-a with pam_nologin was not blocked: %q", text)
	}
	if _, err := os.Stat(filepath.Join(dir, "tenant-b.nologin")); !os.IsNotExist(err) {
		t.Fatal("tenant-b without pam_nologin was blocked")
	}
}

func TestNotifyAfterTheMomentOrWithoutAScheduleDoesNothing(t *testing.T) {
	env, dir := testEnv(t)
	installFakes(t, dir)
	if s, warnings, err := NotifyRun(context.Background(), env, "5m"); s != nil || warnings != nil || err != nil {
		t.Fatalf("notify without a schedule = %v, %v, %v", s, warnings, err)
	}
	past := testSchedule()
	past.At = testNow.Add(-time.Minute)
	if err := save(env.Runner, past); err != nil {
		t.Fatal(err)
	}
	if _, warnings, err := NotifyRun(context.Background(), env, "5m"); err != nil || len(warnings) != 0 {
		t.Fatalf("notify after the moment = %v, %v", warnings, err)
	}
	if wall := readLog(t, filepath.Join(dir, "host.wall")); wall != "" {
		t.Fatalf("a passed moment still announced itself: %q", wall)
	}
}

// AC3: logins close on the block timer's own instant, so a notice list that
// never reaches the five-minute mark still closes them on time.
func TestBlockRunClosesLoginsWhateverTheNoticeList(t *testing.T) {
	env, dir := testEnv(t)
	installFakes(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "containers"), []byte("tenant-a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tenant-a.pam"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ScheduleRun(context.Background(), env, request(testNow.Add(2*time.Hour), "1h")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(NologinPath); !os.IsNotExist(err) {
		t.Fatal("a two-hour schedule blocked logins at once")
	}
	if _, err := os.Stat(filepath.Join(UnitDir, "rootfiles-shutdown-block.timer")); err != nil {
		t.Fatalf("no login-block timer was written: %v", err)
	}
	// The block timer runs `shutdown block` five minutes out, with no notice due.
	later := Env{Runner: env.Runner, Now: func() time.Time { return testNow.Add(115 * time.Minute) }}
	s, warnings, err := BlockRun(context.Background(), later)
	if err != nil || s == nil {
		t.Fatalf("block = %v, %v", s, err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	data, err := os.ReadFile(filepath.Join(dir, "tenant-a.nologin"))
	if err != nil || !WroteNologin(string(data), s.ID) {
		t.Fatalf("tenant block missing: %q err=%v", data, err)
	}
	if _, err := os.Stat(NologinPath); !os.IsNotExist(err) {
		t.Fatalf("block touched the host file: %v", err)
	}
}

// AC3: a lead time that has already passed still owes users one warning.
func TestScheduleRunAnnouncesWhenALeadTimeWasMissed(t *testing.T) {
	env, dir := testEnv(t)
	installFakes(t, dir)
	if _, _, err := ScheduleRun(context.Background(), env, request(testNow.Add(20*time.Minute), "")); err != nil {
		t.Fatal(err)
	}
	if wall := readLog(t, filepath.Join(dir, "host.wall")); !strings.Contains(wall, "will power off") {
		t.Fatalf("a schedule inside its longer lead times sent nothing:\n%q", wall)
	}
	s, err := Load()
	if err != nil || s == nil || len(s.Notices) != 2 {
		t.Fatalf("the leads that are still ahead were not kept: %+v err=%v", s, err)
	}
}

// A schedule whose moment has passed is leftovers: reports omit it, and the
// next schedule cleans it up without --replace and without a cancellation.
func TestScheduleRunCleansAnExpiredLeftoverWithoutReplace(t *testing.T) {
	env, dir := testEnv(t)
	installFakes(t, dir)
	expired := testSchedule()
	expired.At = testNow.Add(-time.Hour)
	expired.Notices = []Notice{{Lead: "15m", At: expired.At.Add(-15 * time.Minute)}, {Lead: "5m", At: expired.At.Add(-5 * time.Minute)}}
	if err := save(env.Runner, expired); err != nil {
		t.Fatal(err)
	}
	if _, err := installUnits(context.Background(), env.Runner, Units("/usr/local/bin/rootfiles", expired)); err != nil {
		t.Fatal(err)
	}
	if s, err := LoadForReport(testNow); err != nil || s != nil {
		t.Fatalf("an expired record still reports as a schedule: %+v err=%v", s, err)
	}
	if _, _, err := ScheduleRun(context.Background(), env, request(testNow.Add(2*time.Hour), "15m")); err != nil {
		t.Fatalf("scheduling over an expired record needed --replace: %v", err)
	}
	loaded, err := Load()
	if err != nil || loaded == nil || !loaded.At.Equal(testNow.Add(2*time.Hour)) || len(loaded.Notices) != 1 {
		t.Fatalf("the new schedule was not stored: %+v err=%v", loaded, err)
	}
	if _, err := os.Stat(filepath.Join(UnitDir, "rootfiles-shutdown-notice-2.timer")); !os.IsNotExist(err) {
		t.Fatal("the expired schedule's second notice unit survived")
	}
	if wall := readLog(t, filepath.Join(dir, "host.wall")); strings.Contains(wall, "Cancelled") {
		t.Fatalf("cleaning an expired record told sessions a live stop was cancelled:\n%q", wall)
	}
}

// AC4/AC6: a cancel that cannot stop a timer fails, keeps the record and keeps
// the units, so the schedule is still on record and can be cancelled again.
func TestCancelRunFailsWhenATimerCannotBeStopped(t *testing.T) {
	env, dir := testEnv(t)
	installFakes(t, dir)
	fail := filepath.Join(dir, "failbin")
	if err := os.MkdirAll(fail, 0755); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\ncase \"$1\" in\nis-active) echo active ;;\ndisable) exit 1 ;;\nesac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(fail, "systemctl"), []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fail+string(os.PathListSeparator)+os.Getenv("PATH"))

	if _, _, err := ScheduleRun(context.Background(), env, request(testNow.Add(2*time.Hour), "15m")); err != nil {
		t.Fatal(err)
	}
	_, _, err := CancelRun(context.Background(), env)
	if err == nil {
		t.Fatal("a cancel whose timer is still active reported success")
	}
	for _, want := range []string{"cannot stop rootfiles-shutdown.timer", "still in force", "rootfiles shutdown cancel"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("cancel error %q does not name %q", err, want)
		}
	}
	if s, loadErr := Load(); loadErr != nil || s == nil {
		t.Fatalf("cancel dropped the record of a schedule it could not stop: %+v err=%v", s, loadErr)
	}
	if names := unitNames(t); len(names) == 0 {
		t.Fatal("cancel removed the units of a schedule it could not stop")
	}
}

// AC4: a unit that cannot be removed leaves the schedule on record.
func TestCancelRunFailsWhenAUnitCannotBeRemoved(t *testing.T) {
	env, dir := testEnv(t)
	installFakes(t, dir)
	if _, _, err := ScheduleRun(context.Background(), env, request(testNow.Add(2*time.Hour), "15m")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(UnitDir, "rootfiles-shutdown.timer")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "busy"), 0755); err != nil {
		t.Fatal(err)
	}
	_, _, err := CancelRun(context.Background(), env)
	if err == nil || !strings.Contains(err.Error(), "cannot remove shutdown unit") {
		t.Fatalf("cancel = %v, want a refusal naming the unit it could not remove", err)
	}
	if s, loadErr := Load(); loadErr != nil || s == nil {
		t.Fatalf("the record was dropped: %+v err=%v", s, loadErr)
	}
}

// AC3: a /run/nologin another tool wrote is a maintenance lock of its own; it
// is reported and left in place, on the host and inside a tenant.
func TestBlockLoginsKeepsAForeignFileOnHostAndTenants(t *testing.T) {
	env, dir := testEnv(t)
	installFakes(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "containers"), []byte("tenant-a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tenant-a.pam"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tenant-a.nologin"), []byte("Maintenance by hand\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(NologinPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(NologinPath, []byte("Maintenance by hand\n"), 0644); err != nil {
		t.Fatal(err)
	}

	req := request(testNow.Add(3*time.Minute), "5m")
	req.NotifyNow = true
	_, warnings, err := ScheduleRun(context.Background(), env, req)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{"tenant-a", "was not written by rootfiles"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("warnings do not name %q:\n%s", want, joined)
		}
	}
	if data, err := os.ReadFile(NologinPath); err != nil || string(data) != "Maintenance by hand\n" {
		t.Fatalf("a foreign host /run/nologin was overwritten: %q err=%v", data, err)
	}
	if text := readLog(t, filepath.Join(dir, "tenant-a.nologin")); text != "Maintenance by hand\n" {
		t.Fatalf("a foreign container /run/nologin was overwritten: %q", text)
	}

	// The cancel must not lift someone else's lock either.
	if _, _, err := CancelRun(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(NologinPath); err != nil || string(data) != "Maintenance by hand\n" {
		t.Fatalf("cancel touched a foreign host /run/nologin: %q err=%v", data, err)
	}
	if text := readLog(t, filepath.Join(dir, "tenant-a.nologin")); text != "Maintenance by hand\n" {
		t.Fatalf("cancel touched a foreign container /run/nologin: %q", text)
	}
}

// AC4: an unreadable state file cannot block the recovery the error names, so
// cancel removes the units by name and the record with them.
func TestCancelRunRecoversFromAnUnreadableState(t *testing.T) {
	env, dir := testEnv(t)
	installFakes(t, dir)
	if _, _, err := ScheduleRun(context.Background(), env, request(testNow.Add(2*time.Hour), "15m")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(StatePath, []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(NologinPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(NologinPath, []byte(NologinText(testSchedule())), 0644); err != nil {
		t.Fatal(err)
	}

	gone, warnings, err := CancelRun(context.Background(), env)
	if err != nil {
		t.Fatalf("cancel could not recover an unreadable state file: %v", err)
	}
	if gone != nil {
		t.Fatalf("cancel reported a schedule it could not read: %+v", gone)
	}
	if names := unitNames(t); len(names) != 0 {
		t.Fatalf("recovery cancel left units: %v", names)
	}
	for _, path := range []string{StatePath, NologinPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("recovery cancel left %s", path)
		}
	}
	if joined := strings.Join(warnings, "\n"); !strings.Contains(joined, "could not be read") {
		t.Fatalf("no warning named the unreadable state: %v", warnings)
	}
}

// AC4/AC6: a cancel must not report success while systemd declines to say
// whether a timer is still armed. An unreachable manager says nothing either
// way, and a timer it still holds fires after its files are gone.
func TestCancelRunFailsWhenSystemdDoesNotAnswer(t *testing.T) {
	env, dir := testEnv(t)
	installFakes(t, dir)
	if _, _, err := ScheduleRun(context.Background(), env, request(testNow.Add(2*time.Hour), "15m")); err != nil {
		t.Fatal(err)
	}
	mute := filepath.Join(dir, "mutebin")
	if err := os.MkdirAll(mute, 0755); err != nil {
		t.Fatal(err)
	}
	// disable --now succeeds, but is-active exits 5: not active, not inactive.
	body := "#!/bin/sh\ncase \"$1\" in\nis-active) exit 5 ;;\nesac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(mute, "systemctl"), []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", mute+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, _, err := CancelRun(context.Background(), env)
	if err == nil || !strings.Contains(err.Error(), "does not answer") {
		t.Fatalf("a cancel that could not verify the timer reported success: %v", err)
	}
	if s, loadErr := Load(); loadErr != nil || s == nil {
		t.Fatalf("the record was removed although the schedule may still be armed: %+v err=%v", s, loadErr)
	}
}

// AC3: a schedule created inside the five-minute window blocks logins at once.
// Its own block timer would sit in the past with Persistent=false and never
// fire, and a lead time that is still ahead leaves nothing for missed to catch.
func TestScheduleRunBlocksLoginsWhenTheScheduleIsInsideTheWindow(t *testing.T) {
	env, dir := testEnv(t)
	installFakes(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "containers"), []byte("tenant-a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tenant-a.pam"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	s, _, err := ScheduleRun(context.Background(), env, request(testNow.Add(2*time.Minute), "1m"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "tenant-a.nologin"))
	if err != nil || !WroteNologin(string(data), s.ID) {
		t.Fatalf("tenant block missing: %q err=%v", data, err)
	}
	if _, err := os.Stat(NologinPath); !os.IsNotExist(err) {
		t.Fatalf("schedule touched the host file: %v", err)
	}
}

// AC3: a tenant block file that cannot be read is not an absent one. Writing
// over it would truncate a login block this feature did not write.
func TestBlockLoginsKeepsAForeignFileWhenTheInspectionFails(t *testing.T) {
	env, dir := testEnv(t)
	installFakes(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "containers"), []byte("tenant-a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tenant-a.pam"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tenant-a.nologin"), []byte("maintenance window\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// The file is there, but reading it fails.
	if err := os.WriteFile(filepath.Join(dir, "failcat"), nil, 0644); err != nil {
		t.Fatal(err)
	}

	_, warnings, err := ScheduleRun(context.Background(), env, request(testNow.Add(2*time.Minute), "1m"))
	if err != nil {
		t.Fatal(err)
	}
	kept, readErr := os.ReadFile(filepath.Join(dir, "tenant-a.nologin"))
	if readErr != nil || string(kept) != "maintenance window\n" {
		t.Fatalf("a tenant login block that could not be read was overwritten: %q err=%v", string(kept), readErr)
	}
	if joined := strings.Join(warnings, "\n"); !strings.Contains(joined, "cannot inspect") {
		t.Fatalf("no warning named the failed inspection: %v", warnings)
	}
}

// AC9: recovery from an unreadable state file stops and removes every notice
// unit the accepted maximum allows, not just the poweroff and block pair.
func TestCancelRunRecoversEveryNoticeUnitItWrote(t *testing.T) {
	env, dir := testEnv(t)
	installFakes(t, dir)
	s := testSchedule()
	s.Notices = nil
	for i := 0; i < MaxNotices; i++ {
		s.Notices = append(s.Notices, Notice{Lead: fmt.Sprintf("%dm", 30+i), At: s.At.Add(-time.Duration(30+i) * time.Minute)})
	}
	if err := save(env.Runner, s); err != nil {
		t.Fatal(err)
	}
	if _, err := installUnits(context.Background(), env.Runner, Units("/usr/local/bin/rootfiles", s)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(StatePath, []byte("{"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := CancelRun(context.Background(), env); err != nil {
		t.Fatalf("recovery failed with an unreadable record: %v", err)
	}
	for i := 0; i < MaxNotices; i++ {
		for _, suffix := range []string{".service", ".timer"} {
			if _, err := os.Stat(filepath.Join(UnitDir, noticeUnit(i)+suffix)); !os.IsNotExist(err) {
				t.Fatalf("notice unit %d%s survived a reported recovery", i, suffix)
			}
		}
	}
	log := readLog(t, filepath.Join(dir, "systemctl.log"))
	if !strings.Contains(log, "disable --now "+noticeUnit(MaxNotices-1)+".timer") {
		t.Fatalf("the last notice timer was never stopped:\n%s", log)
	}
}

// AC6/AC8: a schedule that fails before it writes anything leaves an existing
// record in force, and says so, so the controller does not cancel it as part of
// a rollback.
func TestScheduleRunMarksAFailureBeforeItWroteAsUntouched(t *testing.T) {
	env, dir := testEnv(t)
	installFakes(t, dir)
	if err := save(env.Runner, testSchedule()); err != nil {
		t.Fatal(err)
	}
	if _, err := installUnits(context.Background(), env.Runner, Units("/usr/local/bin/rootfiles", testSchedule())); err != nil {
		t.Fatal(err)
	}

	// An invalid notice list is rejected before the existing schedule is touched.
	_, _, err := ScheduleRun(context.Background(), env, request(testSchedule().At, "soon"))
	if !errors.Is(err, ErrUntouched) {
		t.Fatalf("a pre-write failure was not marked as untouched: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(UnitDir, UnitName+".timer")); statErr != nil {
		t.Fatalf("the existing schedule's poweroff timer was removed by a failed attempt: %v", statErr)
	}
	if s, loadErr := Load(); loadErr != nil || s == nil || s.ID != testSchedule().ID {
		t.Fatalf("the existing record was not left in force: %+v err=%v", s, loadErr)
	}
}

// Old versions may have written a marked host file; cancel still removes it.
func TestCancelRemovesLegacyMarkedHostNologin(t *testing.T) {
	env, dir := testEnv(t)
	installFakes(t, dir)
	s := testSchedule()
	if err := save(env.Runner, s); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(NologinPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(NologinPath, []byte(NologinText(s)), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CancelRun(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(NologinPath); !os.IsNotExist(err) {
		t.Fatalf("legacy marked host file remains: %v", err)
	}
}

// D2: logins are blocked inside tenant containers only. The block never writes
// the host's /run/nologin, because that would close host sshd and the
// controller needs it to stay reachable so `fleet shutdown cancel` can always
// disarm a run it just armed. A marked host file an earlier version left is not
// even rewritten by the block: only the cancel removes it.
func TestBlockLoginsNeverWritesTheHostNologin(t *testing.T) {
	env, dir := testEnv(t)
	installFakes(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "containers"), []byte("tenant-a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tenant-a.pam"), nil, 0644); err != nil {
		t.Fatal(err)
	}

	s, warnings, err := ScheduleRun(context.Background(), env, request(testNow.Add(2*time.Minute), "1m"))
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if _, err := os.Stat(NologinPath); !os.IsNotExist(err) {
		t.Fatalf("the block wrote the host's %s, which closes host sshd: %v", NologinPath, err)
	}
	if text := readLog(t, filepath.Join(dir, "tenant-a.nologin")); !WroteNologin(text, s.ID) {
		t.Fatalf("tenant-a was not blocked: %q", text)
	}

	// An earlier version's marked host file is left byte for byte as it is, and
	// the block does not warn about a file it no longer manages.
	legacy := NologinText(*s)
	if err := os.MkdirAll(filepath.Dir(NologinPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(NologinPath, []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}
	if _, warnings, err := BlockRun(context.Background(), env); err != nil || len(warnings) != 0 {
		t.Fatalf("block = %v, %v", warnings, err)
	}
	if data, err := os.ReadFile(NologinPath); err != nil || string(data) != legacy {
		t.Fatalf("the block rewrote the host's %s: %q err=%v", NologinPath, data, err)
	}
}

func TestInspectTenantLoginsReportsBlockedAndUnknown(t *testing.T) {
	env, dir := testEnv(t)
	installFakes(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "containers"), []byte("tenant-a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tenant-a.pam"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tenant-a.nologin"), []byte("maintenance\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got := InspectTenantLogins(context.Background(), env.Runner)
	if got.Checked != 1 || got.Blocked != 1 || got.Unknown != 0 {
		t.Fatalf("blocked: %+v", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "failcat"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	got = InspectTenantLogins(context.Background(), env.Runner)
	if got.Checked != 1 || got.Blocked != 0 || got.Unknown != 1 {
		t.Fatalf("unknown: %+v", got)
	}
}
