package module

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

func TestDockerModule_Name(t *testing.T) {
	if n := NewDockerModule().Name(); n != "docker" {
		t.Errorf("Name() = %q, want docker", n)
	}
}

func TestDockerModule_Check(t *testing.T) {
	rc := newDryRunRC(t)
	rc.Config.Modules.Docker = config.DockerConfig{Enabled: true}
	result, err := NewDockerModule().Check(context.Background(), rc)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result == nil {
		t.Fatal("Check returned nil result")
	}
}

func TestDockerModule_ApplyDryRun(t *testing.T) {
	rc := newDryRunRC(t)
	rc.Config.Modules.Docker = config.DockerConfig{Enabled: true, StorageDir: "/var/lib/docker-custom"}
	result, err := NewDockerModule().Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result == nil {
		t.Fatal("Apply returned nil result")
	}
}

func withDaemonJSON(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "daemon.json")
	if content != "" {
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	old := dockerDaemonJSONPath
	dockerDaemonJSONPath = p
	t.Cleanup(func() { dockerDaemonJSONPath = old })
	return p
}

func TestDockerModule_ApplyPreservesNvidiaRuntime(t *testing.T) {
	p := withDaemonJSON(t, `{
  "default-runtime": "nvidia",
  "runtimes": {"nvidia": {"path": "nvidia-container-runtime", "runtimeArgs": []}}
}`)
	rc := newRealRC(t)
	storage := filepath.Join(t.TempDir(), "docker")
	rc.Config.Modules.Docker = config.DockerConfig{Enabled: true, StorageDir: storage}

	m := NewDockerModule()
	daemon, err := readDaemonJSON(rc)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range m.daemonUpdates(daemon, storage) {
		daemon[k] = v
	}
	if err := writeDaemonJSON(rc, daemon); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(p)
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("invalid JSON written: %v\n%s", err, data)
	}
	if got["data-root"] != storage {
		t.Errorf("data-root = %v, want %s", got["data-root"], storage)
	}
	if !hasNvidiaRuntime(got) || got["default-runtime"] != "nvidia" {
		t.Errorf("nvidia runtime settings were lost: %s", data)
	}
	if got["log-driver"] != "json-file" {
		t.Errorf("log defaults not added: %s", data)
	}

	// Once merged, no further updates are wanted.
	if u := m.daemonUpdates(got, storage); len(u) != 0 {
		t.Errorf("daemonUpdates after merge = %v, want none", u)
	}
}

func TestDockerModule_DaemonUpdatesRespectsOperatorLogDriver(t *testing.T) {
	m := NewDockerModule()
	u := m.daemonUpdates(map[string]any{"log-driver": "journald", "data-root": "/raid/docker"}, "/raid/docker")
	if len(u) != 0 {
		t.Errorf("daemonUpdates = %v, want none", u)
	}
	if u := m.daemonUpdates(map[string]any{}, ""); len(u) != 0 {
		t.Errorf("no storage dir should mean no daemon.json management, got %v", u)
	}
}

func TestReadDaemonJSON_InvalidIsError(t *testing.T) {
	withDaemonJSON(t, "{not json")
	if _, err := readDaemonJSON(newDryRunRC(t)); err == nil {
		t.Error("invalid daemon.json should be reported, not overwritten")
	}
}

func TestReadDaemonJSON_Missing(t *testing.T) {
	withDaemonJSON(t, "")
	m, err := readDaemonJSON(newDryRunRC(t))
	if err != nil || len(m) != 0 {
		t.Errorf("missing daemon.json should be empty map, got %v, %v", m, err)
	}
}
