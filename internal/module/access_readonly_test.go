package module

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHReadOnlyQueriesLeaveRuntimeStateAbsent(t *testing.T) {
	rockySSHTestPaths(t)
	runtimeDir := filepath.Join(t.TempDir(), "missing-runtime")
	old := sshdPrivilegeSeparationDir
	sshdPrivilegeSeparationDir = runtimeDir
	t.Cleanup(func() { sshdPrivilegeSeparationDir = old })
	log := fakeBin(t, "sshd")
	if err := os.WriteFile(filepath.Join(filepath.Dir(log), "sshd"), []byte("#!/bin/sh\necho 'Missing privilege separation directory' >&2\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	rc := newDryRunRC(t)
	ports := sshPorts(context.Background(), rc)
	if len(ports) != 1 || ports[0] != 22 {
		t.Fatalf("fallback ports: %v", ports)
	}
	findings := doctorSSH(context.Background(), rc)
	if len(findings) != 1 || findings[0].Level != LevelFail || !strings.Contains(findings[0].Hint, "sudo sshd -T") {
		t.Fatalf("missing actionable inspection error: %+v", findings)
	}
	if _, err := os.Stat(runtimeDir); !os.IsNotExist(err) {
		t.Fatalf("query created runtime state: %v", err)
	}
}
