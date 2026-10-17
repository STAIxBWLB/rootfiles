package cli

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/exec"
)

func TestMarkMutating(t *testing.T) {
	root := NewRootCmd("test", "abc")
	for _, path := range [][]string{{"apply"}, {"user", "add"}, {"gpu", "assign"}, {"tunnel", "setup"}} {
		c, _, err := root.Find(path)
		if err != nil || c.Annotations[annotMutates] != "true" {
			t.Errorf("%v should be marked mutating", path)
		}
	}
	for _, path := range [][]string{{"check"}, {"status"}, {"user", "list"}, {"gpu", "list"}, {"tunnel", "status"}} {
		c, _, _ := root.Find(path)
		if c.Annotations[annotMutates] == "true" {
			t.Errorf("%v should not be marked mutating", path)
		}
	}
}

func TestPreflight(t *testing.T) {
	t.Setenv("ROOTFILES_LOCK_FILE", filepath.Join(t.TempDir(), "lock"))
	t.Setenv("ROOTFILES_LOG_FILE", filepath.Join(t.TempDir(), "audit.log"))
	oldOS := nativeHostOS
	nativeHostOS = "linux"
	t.Cleanup(func() { nativeHostOS = oldOS })
	old := geteuid
	t.Cleanup(func() { geteuid = old; heldLock.Release(); heldLock = nil })

	root := NewRootCmd("test", "abc")
	apply, _, _ := root.Find([]string{"apply"})
	status, _, _ := root.Find([]string{"status"})

	geteuid = func() int { return 1000 }
	if err := preflight(apply, nil); err == nil || !strings.Contains(err.Error(), "root") {
		t.Errorf("non-root apply should be refused, got %v", err)
	}
	if err := preflight(status, nil); err != nil {
		t.Errorf("read-only command must not need root: %v", err)
	}
	root.PersistentFlags().Set("dry-run", "true")
	if err := preflight(apply, nil); err != nil {
		t.Errorf("dry-run must not need root: %v", err)
	}
	root.PersistentFlags().Set("dry-run", "false")

	geteuid = func() int { return 0 }
	if err := preflight(apply, nil); err != nil {
		t.Fatalf("root apply: %v", err)
	}
	if err := preflight(apply, nil); err == nil {
		t.Error("second concurrent mutating command should fail on the lock")
	}
}

func TestHumanBytes(t *testing.T) {
	for b, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 3 << 20: "3.0 MiB", 5 << 40: "5.0 TiB"} {
		if got := humanBytes(b); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", b, got, want)
		}
	}
}

func TestScheduleUnits(t *testing.T) {
	svc := scheduleService("/usr/local/bin/rootfiles")
	for _, want := range []string{"SuccessExitStatus=2", "rootfiles check -o json", "rootfiles doctor -o json", "Type=oneshot"} {
		if !strings.Contains(svc, want) {
			t.Errorf("service missing %q:\n%s", want, svc)
		}
	}
	if tmr := scheduleTimer("hourly"); !strings.Contains(tmr, "OnCalendar=hourly") || !strings.Contains(tmr, "Persistent=true") {
		t.Errorf("unexpected timer:\n%s", tmr)
	}
}

func TestNativeMutationsRefuseNonLinuxBeforeLock(t *testing.T) {
	oldOS, oldEUID := nativeHostOS, geteuid
	nativeHostOS = "darwin"
	geteuid = func() int { return 0 }
	t.Cleanup(func() { nativeHostOS = oldOS; geteuid = oldEUID })
	root := NewRootCmd("test", "abc")
	cmd, _, _ := root.Find([]string{"update"})
	if err := preflight(cmd, nil); err == nil || !strings.Contains(err.Error(), "require Linux") {
		t.Fatalf("native update: %v", err)
	}
	fleetCmd, _, _ := root.Find([]string{"fleet", "update"})
	if err := preflight(fleetCmd, nil); err != nil {
		t.Fatalf("operator controller refused: %v", err)
	}
}

func TestScheduleEnableStartsFirstReportOnlyWithoutOne(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("ROOTFILES_STATE_DIR", stateDir)
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte("#!/bin/sh\necho \"$*\" >> '"+calls+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	runner := exec.NewRunner(false, slog.New(slog.NewTextHandler(io.Discard, nil)))

	var out strings.Builder
	startFirstReport(context.Background(), runner, &out, io.Discard)
	if got, _ := os.ReadFile(calls); string(got) != "start --no-block rootfiles-check.service\n" || !strings.Contains(out.String(), "first report started") {
		t.Fatalf("no report yet: calls=%q out=%q", got, out.String())
	}

	if err := os.WriteFile(filepath.Join(stateDir, "check.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	startFirstReport(context.Background(), runner, io.Discard, io.Discard)
	if got, _ := os.ReadFile(calls); strings.Count(string(got), "start") != 1 {
		t.Fatalf("an existing report must not trigger another run: %q", got)
	}
}
