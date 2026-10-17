package module

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

// fakeBin puts stub commands (exit 0, append argv to a log) first on PATH
// so tests never touch the host's systemd or network.
func fakeBin(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	for _, n := range names {
		script := "#!/bin/sh\necho \"" + n + " $*\" >> " + log + "\n"
		if err := os.WriteFile(filepath.Join(dir, n), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return log
}

func fakeCloudflaredPaths(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	saved := []string{cloudflaredBinary, cloudflaredUnitPath, cloudflaredEnvPath, vlanNetdevPath, vlanNetworkPath}
	cloudflaredBinary = filepath.Join(dir, "bin", "cloudflared")
	cloudflaredUnitPath = filepath.Join(dir, "systemd", "cloudflared.service")
	cloudflaredEnvPath = filepath.Join(dir, "cloudflared", "tunnel.env")
	vlanNetdevPath = filepath.Join(dir, "network", "10.netdev")
	vlanNetworkPath = filepath.Join(dir, "network", "10.network")
	os.MkdirAll(filepath.Dir(cloudflaredBinary), 0755)
	os.MkdirAll(filepath.Dir(cloudflaredUnitPath), 0755)
	os.WriteFile(cloudflaredBinary, []byte("#!/bin/sh\n"), 0755)
	t.Cleanup(func() {
		cloudflaredBinary, cloudflaredUnitPath, cloudflaredEnvPath, vlanNetdevPath, vlanNetworkPath =
			saved[0], saved[1], saved[2], saved[3], saved[4]
	})
	return dir
}

func TestCloudflared_TokenServiceConvergence(t *testing.T) {
	fakeCloudflaredPaths(t)
	calls := fakeBin(t, "systemctl", "ip")

	rc := newRealRC(t)
	rc.Config.Modules.Cloudflared = config.CloudflaredConfig{Enabled: true, TunnelToken: "tok-1"}
	m := NewCloudflaredModule()

	check, _ := m.Check(context.Background(), rc)
	if check.Satisfied {
		t.Fatal("missing tunnel service should be reported")
	}
	res, err := m.Apply(context.Background(), rc)
	if err != nil || !res.Changed {
		t.Fatalf("Apply = %+v, %v", res, err)
	}

	unit, _ := os.ReadFile(cloudflaredUnitPath)
	if strings.Contains(string(unit), "tok-1") {
		t.Error("token must not appear in the world-readable unit file")
	}
	fi, err := os.Stat(cloudflaredEnvPath)
	if err != nil || fi.Mode().Perm() != 0600 {
		t.Errorf("token file mode = %v, %v; want 0600", fi, err)
	}
	if log, _ := os.ReadFile(calls); !strings.Contains(string(log), "systemctl restart cloudflared") {
		t.Errorf("service not restarted, calls:\n%s", log)
	}

	if check, _ := m.Check(context.Background(), rc); !check.Satisfied {
		t.Errorf("after Apply Check should be satisfied, got %+v", check.Changes)
	}

	// Token rotation is detected.
	rc.Config.Modules.Cloudflared.TunnelToken = "tok-2"
	if check, _ := m.Check(context.Background(), rc); check.Satisfied {
		t.Error("token change should be reported")
	}
}

func TestCloudflared_VLANAddressDrift(t *testing.T) {
	fakeCloudflaredPaths(t)
	fakeBin(t, "systemctl", "ip")

	rc := newRealRC(t)
	rc.Config.Modules.Cloudflared = config.CloudflaredConfig{Enabled: true,
		PrivateNetwork: config.PrivateNetworkConfig{Enabled: true, Address: "172.16.0.1/32"}}
	m := NewCloudflaredModule()
	if _, err := m.Apply(context.Background(), rc); err != nil {
		t.Fatal(err)
	}
	if check, _ := m.Check(context.Background(), rc); !check.Satisfied {
		t.Errorf("Check after Apply = %+v", check.Changes)
	}
	rc.Config.Modules.Cloudflared.PrivateNetwork.Address = "172.16.0.2/32"
	if check, _ := m.Check(context.Background(), rc); check.Satisfied {
		t.Error("VLAN address change should be reported (was only checking file existence)")
	}
}

func TestInstallTunnelService_RejectsBadToken(t *testing.T) {
	fakeCloudflaredPaths(t)
	if _, err := installTunnelService(context.Background(), newRealRC(t), "abc\nExecStart=/bin/evil"); err == nil {
		t.Error("token with newline must be rejected")
	}
}

// tokenFile writes a token file owned by the test user and points the
// required owner at that user.
func tokenFile(t *testing.T, content string, perm os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tunnel-token")
	if err := os.WriteFile(p, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, perm); err != nil {
		t.Fatal(err)
	}
	saved := tunnelTokenFileUID
	tunnelTokenFileUID = uint32(os.Getuid())
	t.Cleanup(func() { tunnelTokenFileUID = saved })
	return p
}

func TestCloudflared_TokenFileInstallsService(t *testing.T) {
	fakeCloudflaredPaths(t)
	fakeBin(t, "systemctl", "ip")
	path := tokenFile(t, "tok-file\n", 0600)

	rc := newRealRC(t)
	rc.Config.Modules.Cloudflared = config.CloudflaredConfig{Enabled: true, TunnelTokenFile: path}
	m := NewCloudflaredModule()

	check, err := m.Check(context.Background(), rc)
	if err != nil || check.Satisfied {
		t.Fatalf("Check = %+v, %v; want pending tunnel service", check, err)
	}
	if _, err := m.Apply(context.Background(), rc); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	env, _ := os.ReadFile(cloudflaredEnvPath)
	if string(env) != "TUNNEL_TOKEN=tok-file\n" {
		t.Errorf("env file = %q, want trimmed token from file", env)
	}
	if check, err := m.Check(context.Background(), rc); err != nil || !check.Satisfied {
		t.Errorf("after Apply Check = %+v, %v; want satisfied", check, err)
	}
}

func TestCloudflared_TokenFileRejected(t *testing.T) {
	fakeCloudflaredPaths(t)
	cases := map[string]func(t *testing.T) string{
		"group readable": func(t *testing.T) string { return tokenFile(t, "tok", 0640) },
		"empty":          func(t *testing.T) string { return tokenFile(t, " \n", 0600) },
		"missing":        func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent") },
		"inner space":    func(t *testing.T) string { return tokenFile(t, "tok en", 0600) },
		"symlink": func(t *testing.T) string {
			target := tokenFile(t, "tok", 0600)
			link := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			return link
		},
		"wrong owner": func(t *testing.T) string {
			p := tokenFile(t, "tok", 0600)
			tunnelTokenFileUID = uint32(os.Getuid()) + 1
			return p
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			rc := newRealRC(t)
			rc.Config.Modules.Cloudflared = config.CloudflaredConfig{Enabled: true, TunnelTokenFile: mk(t)}
			m := NewCloudflaredModule()
			if _, err := m.Check(context.Background(), rc); err == nil {
				t.Error("Check accepted a bad tunnel_token_file")
			}
			if _, err := m.Apply(context.Background(), rc); err == nil {
				t.Error("Apply accepted a bad tunnel_token_file")
			}
			if _, err := os.Stat(cloudflaredEnvPath); err == nil {
				t.Error("env file written from a rejected token file")
			}
		})
	}
}

func TestCloudflared_ExplicitTokenOverridesFile(t *testing.T) {
	fakeCloudflaredPaths(t)
	fakeBin(t, "systemctl", "ip")
	// A world-readable file is never opened when an explicit token is set.
	path := tokenFile(t, "tok-file", 0644)

	rc := newRealRC(t)
	rc.Config.Modules.Cloudflared = config.CloudflaredConfig{Enabled: true, TunnelToken: "tok-flag", TunnelTokenFile: path}
	if _, err := NewCloudflaredModule().Apply(context.Background(), rc); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if env, _ := os.ReadFile(cloudflaredEnvPath); string(env) != "TUNNEL_TOKEN=tok-flag\n" {
		t.Errorf("env file = %q, want the explicit token", env)
	}
}
