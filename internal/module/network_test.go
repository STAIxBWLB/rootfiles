package module

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

func TestNetworkModule_Name(t *testing.T) {
	if n := NewNetworkModule().Name(); n != "network" {
		t.Errorf("Name() = %q, want network", n)
	}
}

func TestNetworkModule_CheckDisabledIsSatisfied(t *testing.T) {
	rc := newDryRunRC(t)
	rc.Config.Modules.Network = config.NetworkConfig{UFW: false}
	result, err := NewNetworkModule().Check(context.Background(), rc)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !result.Satisfied {
		t.Errorf("Check with UFW disabled should be satisfied, got %+v", result.Changes)
	}
}

func TestNetworkModule_ApplyDisabledNoChange(t *testing.T) {
	rc := newDryRunRC(t)
	rc.Config.Modules.Network = config.NetworkConfig{UFW: false}
	result, err := NewNetworkModule().Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Changed {
		t.Error("Apply with UFW disabled should not report changes")
	}
}

func TestParseUFWStatus(t *testing.T) {
	out := `Status: active

To                         Action      From
--                         ----        ----
22                         ALLOW       Anywhere
2222/tcp                   ALLOW       Anywhere
80,443/tcp                 ALLOW       Anywhere
53/udp                     ALLOW       Anywhere
8080                       DENY        Anywhere
22 (v6)                    ALLOW       Anywhere (v6)
`
	st := parseUFWStatus(out)
	if !st.Active {
		t.Error("expected active")
	}
	for _, p := range []int{22, 2222, 80, 443} {
		if !st.Allowed[p] {
			t.Errorf("port %d should be allowed", p)
		}
	}
	for _, p := range []int{53, 8080, 222} {
		if st.Allowed[p] {
			t.Errorf("port %d should not be allowed", p)
		}
	}
}

func TestParseUFWStatus_Inactive(t *testing.T) {
	if parseUFWStatus("Status: inactive\n").Active {
		t.Error(`"Status: inactive" must not be parsed as active`)
	}
}

func TestNetworkModule_RequiredPortsIncludeSSH(t *testing.T) {
	rc := newDryRunRC(t)
	rc.Config.SSH.Port = 2222
	rc.Config.Modules.Network = config.NetworkConfig{UFW: true, AllowedPorts: []int{443, 80}}
	got := NewNetworkModule().requiredPorts(context.Background(), rc)
	want := []int{80, 443, 2222}
	if len(got) != len(want) {
		t.Fatalf("requiredPorts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("requiredPorts = %v, want %v", got, want)
		}
	}
}

func TestParsePortDirectives(t *testing.T) {
	got := parsePortDirectives("# Port 99\nPort 2222\nport 22\nPort 2222\nListenAddress 0.0.0.0\n")
	if len(got) != 2 || got[0] != 2222 || got[1] != 22 {
		t.Errorf("parsePortDirectives = %v", got)
	}
}

func TestSSHPorts_FallsBackToConfigFiles(t *testing.T) {
	if _, err := exec.LookPath("sshd"); err == nil {
		t.Skip("sshd installed; sshd -T path is used instead")
	}
	dir := t.TempDir()
	saved := []string{sshdConfigPath, sshdConfigDir}
	sshdConfigPath = filepath.Join(dir, "sshd_config")
	sshdConfigDir = filepath.Join(dir, "sshd_config.d")
	t.Cleanup(func() { sshdConfigPath, sshdConfigDir = saved[0], saved[1] })
	os.MkdirAll(sshdConfigDir, 0755)
	os.WriteFile(sshdConfigPath, []byte("Include /etc/ssh/sshd_config.d/*.conf\n#Port 22\n"), 0644)
	os.WriteFile(filepath.Join(sshdConfigDir, "50-custom.conf"), []byte("Port 2200\n"), 0644)

	if got := sshPorts(context.Background(), newDryRunRC(t)); len(got) != 1 || got[0] != 2200 {
		t.Errorf("sshPorts = %v, want [2200] from sshd_config.d", got)
	}
}
