package cli

import (
	"os"
	"syscall"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

// TestMain pins the home base so config loading in these tests never runs
// home-base detection against the host, and clears the other config
// overrides so a developer's shell cannot fail a load.
func TestMain(m *testing.M) {
	// Reused configs must be root-owned and below a root-only walk; let the
	// test user stand in for root under a private TMPDIR.
	syscall.Umask(0o022)
	tmp, err := os.MkdirTemp("", "rootfiles-cli-test")
	if err != nil {
		panic(err)
	}
	os.Setenv("TMPDIR", tmp)
	configTrustRoot, configOwnerUID = tmp, uint32(os.Getuid())
	// Keep system detection (os-release, /proc/mounts, nvidia-smi) off the host.
	detectSystem = func() (*config.SystemInfo, error) { return &config.SystemInfo{OS: "ubuntu", Version: "22.04"}, nil }
	os.Setenv("ROOTFILES_HOME_BASE", "/home")
	// Config selection falls back to the last applied run; keep it off the host.
	stateDir, err := os.MkdirTemp("", "rootfiles-cli-state")
	if err != nil {
		panic(err)
	}
	os.Setenv("ROOTFILES_STATE_DIR", stateDir)
	os.Setenv("ROOTFILES_LOG_FILE", stateDir+"/audit.log")
	for _, k := range []string{"ROOTFILES_PROFILE", "ROOTFILES_TIMEZONE", "ROOTFILES_TUNNEL_TOKEN",
		"ROOTFILES_VLAN_ADDRESS", "ROOTFILES_VLAN_INTERFACE", "ROOTFILES_DOCKER_ROOT", "ROOTFILES_DATA_DIR"} {
		os.Unsetenv(k)
	}
	code := m.Run()
	os.RemoveAll(stateDir)
	os.RemoveAll(tmp)
	os.Exit(code)
}
