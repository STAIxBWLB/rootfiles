package cli

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/rootfiles/internal/config"
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

func TestScheduleLegacyHeadersSkipReloadAndRetainPersistentEnable(t *testing.T) {
	for _, enabledState := range []string{"enabled", "enabled-runtime"} {
		t.Run(enabledState, func(t *testing.T) {

			oldDir, oldOS, oldUID, oldDetect, oldLock, oldAudit := scheduleUnitDir, nativeHostOS, geteuid, detectSystem, heldLock, auditLogger
			scheduleUnitDir = t.TempDir()
			nativeHostOS = "linux"
			geteuid = func() int { return 0 }
			detectSystem = func() (*config.SystemInfo, error) { return &config.SystemInfo{OS: "ubuntu", Version: "24.04"}, nil }
			heldLock, auditLogger = nil, nil
			t.Cleanup(func() {
				if heldLock != oldLock {
					heldLock.Release()
				}
				scheduleUnitDir, nativeHostOS, geteuid, detectSystem, heldLock, auditLogger = oldDir, oldOS, oldUID, oldDetect, oldLock, oldAudit
			})
			stateDir := t.TempDir()
			t.Setenv("ROOTFILES_STATE_DIR", stateDir)
			t.Setenv("ROOTFILES_HOME_BASE", t.TempDir())
			t.Setenv("ROOTFILES_LOCK_FILE", filepath.Join(stateDir, "lock"))
			t.Setenv("ROOTFILES_LOG_FILE", filepath.Join(stateDir, "audit"))
			if err := os.WriteFile(filepath.Join(stateDir, "check.json"), []byte("{}"), 0644); err != nil {
				t.Fatal(err)
			}
			commands := t.TempDir()
			mutations := filepath.Join(commands, "mutations")
			script := "#!/bin/sh\ncase $1 in is-enabled) echo " + enabledState + ";; is-active) echo active;; *) printf '%s\\n' \"$*\" >> '" + mutations + "';; esac\n"
			if err := os.WriteFile(filepath.Join(commands, "systemctl"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", commands)
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			if resolved, err := filepath.EvalSymlinks(binary); err == nil {
				binary = resolved
			}
			before := map[string]os.FileInfo{}
			contents := map[string]string{}
			for ext, generated := range map[string]string{".service": scheduleService(binary), ".timer": scheduleTimer("daily")} {
				file := filepath.Join(scheduleUnitDir, scheduleName+ext)
				legacy := strings.Replace(generated, "# Managed by rootfiles", "# Managed by rootfiles-v2", 1)
				if err := os.WriteFile(file, []byte(legacy), 0644); err != nil {
					t.Fatal(err)
				}
				stamp := time.Unix(1234567890, 0)
				if err := os.Chtimes(file, stamp, stamp); err != nil {
					t.Fatal(err)
				}
				contents[file] = legacy
				before[file], _ = os.Stat(file)
			}
			root := NewRootCmd("test", "test")
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetArgs([]string{"schedule", "enable"})
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(mutations)
			if enabledState == "enabled" && !os.IsNotExist(err) {
				t.Fatalf("converged schedule ran mutations: %q, %v", data, err)
			}
			if enabledState == "enabled-runtime" && (err != nil || string(data) != "enable --now --no-reload rootfiles-check.timer\n") {
				t.Fatalf("runtime timer not persistently enabled without reload: %q, %v", data, err)
			}
			for file, want := range contents {
				data, err := os.ReadFile(file)
				after, statErr := os.Stat(file)
				if err != nil || statErr != nil || string(data) != want || !os.SameFile(before[file], after) || !before[file].ModTime().Equal(after.ModTime()) {
					t.Fatalf("schedule command rewrote %s", file)
				}
			}

		})
	}
}
