package module

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/rootfiles/internal/exec"
)

func TestEnsureFilePreservesLegacyHeaderAndExistingMode(t *testing.T) {
	rc := newRealRC(t)
	rc.Runner.Backup = exec.NewBackup(t.TempDir(), "unchanged managed file")
	var log bytes.Buffer
	rc.Runner.Logger = slog.New(slog.NewTextHandler(&log, nil))
	file := filepath.Join(t.TempDir(), "managed.conf")
	const legacy = "# Managed by rootfiles-v2\nsetting=kept\n"
	const canonical = "# Managed by rootfiles\nsetting=kept\n"
	if err := os.WriteFile(file, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1234567890, 0)
	if err := os.Chtimes(file, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if !fileEquals(rc, file, canonical) {
		t.Fatal("Check comparison did not recognize the legacy marker")
	}
	for _, content := range []string{canonical, legacy} {
		changed, err := ensureFile(rc, file, content, 0644)
		if err != nil || changed {
			t.Fatalf("equal content: changed=%t err=%v", changed, err)
		}
	}
	after, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != legacy {
		t.Fatalf("legacy bytes changed: %q, %v", data, err)
	}
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) || after.Mode().Perm() != 0600 {
		t.Fatalf("unchanged content altered inode, mtime or mode: before=%v after=%v", before, after)
	}
	if rc.Runner.Backup.Used() || log.Len() != 0 {
		t.Fatalf("equal content performed a write: backup=%t log=%s", rc.Runner.Backup.Used(), log.String())
	}
}

func TestEnsureFilePayloadChangeBacksUpAndConverges(t *testing.T) {
	rc := newRealRC(t)
	rc.Runner.Backup = exec.NewBackup(t.TempDir(), "managed payload change")
	var log bytes.Buffer
	rc.Runner.Logger = slog.New(slog.NewTextHandler(&log, nil))
	file := filepath.Join(t.TempDir(), "managed.conf")
	const old = "# Managed by rootfiles-v2\nsetting=old\n"
	const want = "# Managed by rootfiles\nsetting=new\n"
	if err := os.WriteFile(file, []byte(old), 0644); err != nil {
		t.Fatal(err)
	}
	if fileEquals(rc, file, want) {
		t.Fatal("legacy marker hid payload drift")
	}
	changed, err := ensureFile(rc, file, want, 0644)
	if err != nil || !changed || !rc.Runner.Backup.Used() {
		t.Fatalf("payload update: changed=%t backup=%t err=%v", changed, rc.Runner.Backup.Used(), err)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != want || !fileEquals(rc, file, want) {
		t.Fatalf("new content = %q, %v", data, err)
	}
	log.Reset()
	if changed, err := ensureFile(rc, file, want, 0644); err != nil || changed || log.Len() != 0 {
		t.Fatalf("second call wrote again: changed=%t err=%v log=%s", changed, err, log.String())
	}
	manifest, err := exec.LoadBackup(rc.Runner.Backup.Root, rc.Runner.Backup.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Entries) != 1 || manifest.Entries[0].Path != file || !manifest.Entries[0].Existed {
		t.Fatalf("unexpected backup entries: %+v", manifest.Entries)
	}
	if _, err := exec.RestoreBackup(rc.Runner.Backup.Root, rc.Runner.Backup.ID, false); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(file)
	if err != nil || string(data) != old {
		t.Fatalf("backup did not restore exact original: %q, %v", data, err)
	}
}

func TestEnsureFileCreatesOnlyTheRequestedFile(t *testing.T) {
	rc := newRealRC(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "new.conf")
	changed, err := ensureFile(rc, file, "new content\n", 0600)
	if err != nil || !changed {
		t.Fatalf("create: changed=%t err=%v", changed, err)
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("new file mode: %v, %v", info, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "new.conf" {
		t.Fatalf("unexpected paths created: %v, %v", entries, err)
	}
}

func TestEnsureFileRefusesReadAndParentErrors(t *testing.T) {
	t.Run("directory is not an absent file", func(t *testing.T) {
		rc := newRealRC(t)
		rc.Runner.Backup = exec.NewBackup(t.TempDir(), "read refusal")
		var log bytes.Buffer
		rc.Runner.Logger = slog.New(slog.NewTextHandler(&log, nil))
		dir := t.TempDir()
		keep := filepath.Join(dir, "keep")
		if err := os.WriteFile(keep, []byte("preserve"), 0600); err != nil {
			t.Fatal(err)
		}
		changed, err := ensureFile(rc, dir, "replacement", 0644)
		if err == nil || changed || !strings.Contains(err.Error(), "cannot read managed file "+dir) || !strings.Contains(err.Error(), "repair") {
			t.Fatalf("directory read error: changed=%t err=%v", changed, err)
		}
		if data, err := os.ReadFile(keep); err != nil || string(data) != "preserve" {
			t.Fatalf("read refusal changed child: %q, %v", data, err)
		}
		if log.Len() != 0 || rc.Runner.Backup.Used() {
			t.Fatalf("read refusal reached a write: backup=%t log=%s", rc.Runner.Backup.Used(), log.String())
		}
	})
	t.Run("caller must create parent", func(t *testing.T) {
		rc := newRealRC(t)
		root := t.TempDir()
		file := filepath.Join(root, "absent", "managed.conf")
		changed, err := ensureFile(rc, file, "new content", 0644)
		if changed || !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "parent directory") {
			t.Fatalf("missing parent: changed=%t err=%v", changed, err)
		}
		entries, readErr := os.ReadDir(root)
		if readErr != nil || len(entries) != 0 {
			t.Fatalf("helper created a parent or temporary file: %v, %v", entries, readErr)
		}
	})
}

func TestEnsureFileBackupFailureLeavesPayloadIntact(t *testing.T) {
	rc := newRealRC(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "managed.conf")
	backupRoot := filepath.Join(dir, "backup-root-is-a-file")
	for name, content := range map[string]string{file: "old payload", backupRoot: "keep"} {
		if err := os.WriteFile(name, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	rc.Runner.Backup = exec.NewBackup(backupRoot, "refuse update without backup")
	changed, err := ensureFile(rc, file, "new payload", 0600)
	if changed || err == nil || !strings.Contains(err.Error(), "backup permissions") {
		t.Fatalf("backup failure: changed=%t err=%v", changed, err)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "old payload" {
		t.Fatalf("failed backup changed the destination: %q, %v", data, err)
	}
}

func TestEnsureFileDryRunLeavesPathsAndBackupAbsent(t *testing.T) {
	rc := newDryRunRC(t)
	root := t.TempDir()
	rc.Runner.Backup = exec.NewBackup(filepath.Join(root, "backups"), "dry-run")
	file := filepath.Join(root, "absent", "managed.conf")
	if changed, err := ensureFile(rc, file, "planned payload", 0600); err != nil || !changed {
		t.Fatalf("dry-run plan: changed=%t err=%v", changed, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 || rc.Runner.Backup.Used() {
		t.Fatalf("dry-run created paths or backup: entries=%v backup=%t err=%v", entries, rc.Runner.Backup.Used(), err)
	}
	file = filepath.Join(root, "existing.conf")
	if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if changed, err := ensureFile(rc, file, "planned replacement", 0644); err != nil || !changed {
		t.Fatalf("dry-run replacement: changed=%t err=%v", changed, err)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "original" {
		t.Fatalf("dry-run changed existing content: %q, %v", data, err)
	}
}

func TestSystemdHelpersRunOnlyTheirRequestedCommand(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		now        bool
	}{
		{"enable only", "systemctl enable fixture.service\n", false},
		{"enable and start", "systemctl enable --now fixture.service\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := fakeBin(t, "systemctl")
			rc := newRealRC(t)
			queries := 0
			rc.Runner.QueryGuard = func(string) error { queries++; return errors.New("unexpected status query") }
			if err := enableUnit(context.Background(), rc, "fixture.service", tc.now); err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(calls); err != nil || string(data) != tc.want || queries != 0 {
				t.Fatalf("enable added probes or commands: %q, queries=%d err=%v", data, queries, err)
			}
		})
	}
	t.Run("reload success", func(t *testing.T) {
		calls := fakeBin(t, "systemctl")
		rc := newRealRC(t)
		queries := 0
		rc.Runner.QueryGuard = func(string) error { queries++; return errors.New("unexpected status query") }
		if warning := systemdReload(context.Background(), rc); warning != "" {
			t.Fatalf("successful reload warning: %s", warning)
		}
		if data, err := os.ReadFile(calls); err != nil || string(data) != "systemctl daemon-reload\n" || queries != 0 {
			t.Fatalf("reload added probes or commands: %q queries=%d err=%v", data, queries, err)
		}
	})
}

func TestSystemdHelperFailuresNameManualRepair(t *testing.T) {
	calls := fakeBin(t, "systemctl")
	script := "#!/bin/sh\nprintf 'systemctl %s\\n' \"$*\" >> '" + calls + "'\nprintf 'systemd unavailable\\n' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(calls), "systemctl"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	rc := newRealRC(t)
	warning := systemdReload(context.Background(), rc)
	if !strings.Contains(warning, "systemctl daemon-reload failed") || !strings.Contains(warning, "run sudo systemctl daemon-reload") {
		t.Fatalf("reload failure lacks cause/manual repair: %s", warning)
	}
	if err := enableUnit(context.Background(), rc, "fixture.service", false); err == nil || !strings.Contains(err.Error(), "cannot enable systemd unit fixture.service") || !strings.Contains(err.Error(), "systemctl status fixture.service") {
		t.Fatalf("enable failure lacks cause/next step: %v", err)
	}
	if data, err := os.ReadFile(calls); err != nil || string(data) != "systemctl daemon-reload\nsystemctl enable fixture.service\n" {
		t.Fatalf("failure helpers attempted hidden repairs: %q, %v", data, err)
	}
}
