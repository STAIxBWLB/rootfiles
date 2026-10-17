package module

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNvidiaModule_Name(t *testing.T) {
	if n := NewNvidiaModule().Name(); n != "nvidia" {
		t.Errorf("Name() = %q, want nvidia", n)
	}
}

func TestNvidiaModule_Check(t *testing.T) {
	rc := newDryRunRC(t)
	result, err := NewNvidiaModule().Check(context.Background(), rc)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result == nil {
		t.Fatal("Check returned nil result")
	}
}

func TestNvidiaModule_ApplyDryRun(t *testing.T) {
	rc := newDryRunRC(t)
	result, err := NewNvidiaModule().Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result == nil {
		t.Fatal("Apply returned nil result")
	}
}

func TestNvidiaRuntimeRegistrationReloadsDocker(t *testing.T) {
	old := dockerDaemonJSONPath
	dockerDaemonJSONPath = filepath.Join(t.TempDir(), "daemon.json")
	t.Cleanup(func() { dockerDaemonJSONPath = old })
	log := fakeBin(t, "docker", "nvidia-ctk", "systemctl")
	fakeMonitoringCommand(t, "dpkg-query", "#!/bin/sh\nprintf 'nvidia-container-toolkit install ok installed\\n'\n")
	rc := newRealRC(t)
	if _, err := NewNvidiaModule().Apply(context.Background(), rc); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "systemctl reload docker") || strings.Contains(string(calls), "restart docker") {
		t.Fatalf("registering the runtime should reload docker, not restart it: %s", calls)
	}
}
