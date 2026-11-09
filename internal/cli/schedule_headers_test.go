package cli

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/rootfiles/internal/exec"
)

func TestScheduleLegacyHeadersDoNotRewriteUnits(t *testing.T) {
	old := scheduleUnitDir
	scheduleUnitDir = t.TempDir()
	t.Cleanup(func() { scheduleUnitDir = old })
	t.Setenv("ROOTFILES_STATE_DIR", t.TempDir())
	runner := exec.NewRunner(false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	runner.Backup = exec.NewBackup(t.TempDir(), "legacy schedule fixture")
	originals := map[string]string{}
	info := map[string]os.FileInfo{}
	for ext, current := range map[string]string{".service": scheduleService("/usr/local/bin/rootfiles"), ".timer": scheduleTimer("daily")} {
		file := filepath.Join(scheduleUnitDir, scheduleName+ext)
		data := strings.Replace(current, "# Managed by rootfiles", "# Managed by rootfiles-v2", 1)
		if err := os.WriteFile(file, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
		stamp := time.Unix(1234567890, 0)
		if err := os.Chtimes(file, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		originals[file] = data
		info[file], _ = os.Stat(file)
	}
	changed, err := writeScheduleUnits(runner, "/usr/local/bin/rootfiles", "daily")
	if err != nil || changed || runner.Backup.Used() {
		t.Fatalf("header-only change: changed=%t backup=%t err=%v", changed, runner.Backup.Used(), err)
	}
	for file, expected := range originals {
		data, err := os.ReadFile(file)
		after, statErr := os.Stat(file)
		if err != nil || statErr != nil || string(data) != expected || !os.SameFile(info[file], after) || !info[file].ModTime().Equal(after.ModTime()) {
			t.Fatalf("rewrote legacy schedule %s", file)
		}
	}
	changed, err = writeScheduleUnits(runner, "/usr/local/bin/rootfiles", "hourly")
	if err != nil || !changed || !runner.Backup.Used() {
		t.Fatalf("real schedule change missing: changed=%t err=%v", changed, err)
	}
	timer, _ := os.ReadFile(filepath.Join(scheduleUnitDir, scheduleName+".timer"))
	if !strings.HasPrefix(string(timer), "# Managed by rootfiles\n") || !strings.Contains(string(timer), "OnCalendar=hourly") {
		t.Fatalf("new timer header/payload %q", timer)
	}
	service, _ := os.ReadFile(filepath.Join(scheduleUnitDir, scheduleName+".service"))
	if !strings.HasPrefix(string(service), "# Managed by rootfiles-v2\n") {
		t.Fatal("unchanged service was rewritten alongside timer")
	}
}
