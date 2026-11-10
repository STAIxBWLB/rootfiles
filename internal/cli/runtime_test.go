package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/exec"
	"github.com/STAIxBWLB/rootfiles/internal/state"
)

func TestMarkMutating(t *testing.T) {
	root := NewRootCmd("test", "abc")
	for _, path := range [][]string{{"apply"}, {"user", "add"}, {"gpu", "assign"}, {"tunnel", "setup"}, {"tenant", "cpuset"}} {
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

func TestRedactArgs(t *testing.T) {
	tests := []struct {
		name       string
		args, want []string
	}{
		{"apply", []string{"apply", "--tunnel-token", "secret"}, []string{"apply", "--tunnel-token", "***"}},
		{"inline token", []string{"apply", "--tunnel-token=secret"}, []string{"apply", "--tunnel-token=***"}},
		{"password", []string{"user", "passwd", "alice", "--password", "secret"}, []string{"user", "passwd", "alice", "--password", "***"}},
		{"inline password", []string{"user", "passwd", "alice", "--password=secret"}, []string{"user", "passwd", "alice", "--password=***"}},
		{"positional", []string{"tunnel", "setup", "secret"}, []string{"tunnel", "setup", "***"}},
		{"install alias positional", []string{"tunnel", "install", "secret"}, []string{"tunnel", "install", "***"}},
		{"install alias interspersed", []string{"tunnel", "install", "--config", "site.yaml", "secret"}, []string{"tunnel", "install", "--config", "site.yaml", "***"}},
		{"install alias delimiter", []string{"tunnel", "install", "--", "--token-like-value"}, []string{"tunnel", "install", "--", "***"}},
		{"interspersed flags", []string{"--profile", "tunnel", "tunnel", "--yes", "setup", "--config", "site.yaml", "secret", "--vlan-address=host"}, []string{"--profile", "tunnel", "tunnel", "--yes", "setup", "--config", "site.yaml", "***", "--vlan-address=host"}},
		{"delimiter", []string{"tunnel", "setup", "--", "--token-like-value"}, []string{"tunnel", "setup", "--", "***"}},
		{"both", []string{"tunnel", "setup", "secret", "--tunnel-token", "other"}, []string{"tunnel", "setup", "***", "--tunnel-token", "***"}},
		{"same value elsewhere", []string{"--profile", "secret", "apply", "--password=secret"}, []string{"--profile", "secret", "apply", "--password=***"}},
		{"non-setup", []string{"tunnel", "update", "--version", "2026.1.0"}, []string{"tunnel", "update", "--version", "2026.1.0"}},
		{"no positional", []string{"tunnel", "setup", "--profile=minimal"}, []string{"tunnel", "setup", "--profile=minimal"}},
		{"missing value", []string{"apply", "--tunnel-token"}, []string{"apply", "--tunnel-token"}},
		{"empty", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := append([]string(nil), tt.args...)
			if got := redactArgs(tt.args); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			if !reflect.DeepEqual(tt.args, original) {
				t.Fatal("modified input arguments")
			}
		})
	}
}

func TestRedactAuditAndBackupNotes(t *testing.T) {
	for _, args := range [][]string{
		{"apply", "--tunnel-token", "sentinel-secret"},
		{"apply", "--tunnel-token=sentinel-secret"},
		{"tunnel", "setup", "sentinel-secret"},
		{"tunnel", "install", "sentinel-secret"},
		{"user", "passwd", "alice", "--password", "sentinel-secret"},
		{"user", "passwd", "alice", "--password=sentinel-secret"},
	} {
		t.Run(strings.Join(args[:2], " "), func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("ROOTFILES_STATE_DIR", dir)
			t.Setenv("ROOTFILES_LOCK_FILE", filepath.Join(dir, "lock"))
			var audit bytes.Buffer
			oldArgs, oldEUID, oldOS, oldAudit, oldLock := os.Args, geteuid, nativeHostOS, auditLogger, heldLock
			os.Args = append([]string{"rootfiles"}, args...)
			geteuid = func() int { return 0 }
			nativeHostOS = "linux"
			auditLogger = slog.New(slog.NewJSONHandler(&audit, nil))
			heldLock = nil
			t.Cleanup(func() {
				heldLock.Release()
				os.Args, geteuid, nativeHostOS, auditLogger, heldLock = oldArgs, oldEUID, oldOS, oldAudit, oldLock
			})
			root := NewRootCmd("test", "abc")
			cmd, _, err := root.Find(args)
			if err != nil {
				t.Fatal(err)
			}
			if err := preflight(cmd, nil); err != nil {
				t.Fatal(err)
			}
			var record map[string]any
			if err := json.Unmarshal(audit.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			if text, ok := record["args"].(string); !ok || strings.Contains(text, "sentinel-secret") || !strings.Contains(text, "***") {
				t.Fatalf("audit args: %v", record)
			}
			runner := newRunner(cmd, false)
			if runner.Backup == nil {
				t.Fatal("missing backup session")
			}
			if err := runner.Backup.Preserve(filepath.Join(dir, "managed-file")); err != nil {
				t.Fatal(err)
			}
			manifest, err := exec.LoadBackup(state.BackupsDir(), runner.Backup.ID)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(manifest.Note, "sentinel-secret") || !strings.Contains(manifest.Note, "***") {
				t.Fatalf("backup note: %q", manifest.Note)
			}
		})
	}
}

func TestPreflightReadOnlyFlags(t *testing.T) {
	oldEUID, oldOS, oldLock, oldAudit := geteuid, nativeHostOS, heldLock, auditLogger
	t.Cleanup(func() { geteuid, nativeHostOS, heldLock, auditLogger = oldEUID, oldOS, oldLock, oldAudit })
	for _, path := range [][]string{{"update"}, {"upgrade"}, {"tunnel", "update"}} {
		for _, uid := range []int{1000, 0} {
			root := NewRootCmd("test", "abc")
			cmd, _, _ := root.Find(path)
			if err := cmd.Flags().Set("check", "true"); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			t.Setenv("ROOTFILES_LOCK_FILE", filepath.Join(dir, "lock"))
			t.Setenv("ROOTFILES_LOG_FILE", filepath.Join(dir, "audit"))
			geteuid = func() int { return uid }
			nativeHostOS = "darwin"
			heldLock, auditLogger = nil, nil
			if err := preflight(cmd, nil); err != nil {
				t.Fatalf("%v uid=%d: %v", path, uid, err)
			}
			if runner := newRunner(cmd, false); runner.Backup != nil {
				t.Fatal("read-only check created a backup")
			}
			if heldLock != nil || auditLogger != nil {
				t.Fatal("read-only check took lock or opened audit")
			}
			for _, file := range []string{"lock", "audit"} {
				if _, err := os.Stat(filepath.Join(dir, file)); !os.IsNotExist(err) {
					t.Fatalf("read-only check created %s: %v", file, err)
				}
			}
			cmd.Flags().Set("check", "false")
			geteuid = func() int { return 1000 }
			if err := preflight(cmd, nil); err == nil || !strings.Contains(err.Error(), "root") {
				t.Fatalf("%v --check=false bypassed root: %v", path, err)
			}
		}
	}
}

func TestReadOnlyCommandsRejectVLANAddress(t *testing.T) {
	oldDetect := detectSystem
	detectSystem = func() (*config.SystemInfo, error) {
		t.Fatal("non-reader reached host detection instead of refusing --vlan-address")
		return nil, nil
	}
	t.Cleanup(func() { detectSystem = oldDetect })
	noHostCommands(t)
	for _, command := range []string{"doctor", "status"} {
		for _, flags := range [][]string{{"--vlan-address", "203.0.113.10/32"}, {"--vlan-address=203.0.113.10/32"}} {
			t.Run(command+"/"+flags[0], func(t *testing.T) {
				stateDir := t.TempDir()
				t.Setenv("ROOTFILES_STATE_DIR", stateDir)
				root := NewRootCmd("test", "test")
				root.SetOut(io.Discard)
				root.SetErr(io.Discard)
				root.SetArgs(append([]string{command}, flags...))
				err := root.Execute()
				if err == nil || !strings.Contains(err.Error(), "rootfiles "+command+" does not use --vlan-address") || !strings.Contains(err.Error(), "remove these flags") {
					t.Fatalf("non-reader refusal lost: %v", err)
				}
				entries, err := os.ReadDir(stateDir)
				if err != nil || len(entries) != 0 {
					t.Fatalf("refusal touched state: %v, %v", entries, err)
				}
			})
		}
	}
}
