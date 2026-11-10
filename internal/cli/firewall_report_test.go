package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/module"
)

func TestNonRootFirewallReportsRemainReadable(t *testing.T) {
	for _, subject := range []string{"network", "monitoring"} {
		for _, op := range []string{"check", "status"} {
			t.Run(subject+"/"+op, func(t *testing.T) {
				dir := t.TempDir()
				home := filepath.Join(dir, "home")
				if err := os.MkdirAll(home, 0755); err != nil {
					t.Fatal(err)
				}
				calls := filepath.Join(dir, "calls")
				quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
				bin := filepath.Join(dir, "bin")
				if err := os.MkdirAll(bin, 0755); err != nil {
					t.Fatal(err)
				}
				for name, body := range map[string]string{
					"ufw":       "echo 'fixture permission denied' >&2\nexit 1",
					"systemctl": "echo inactive", "getent": "exit 0", "ip": "exit 0",
				} {
					script := "#!/bin/sh\nprintf '%s\\n' '" + name + " '\"$*\" >> " + quote(calls) + "\n" + body + "\n"
					if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0755); err != nil {
						t.Fatal(err)
					}
				}
				t.Setenv("PATH", bin)
				t.Setenv("ROOTFILES_STATE_DIR", filepath.Join(dir, "state"))
				t.Setenv("ROOTFILES_LOCK_FILE", filepath.Join(dir, "lock"))
				t.Setenv("ROOTFILES_LOG_FILE", filepath.Join(dir, "audit"))
				oldDetect, oldEUID, oldCloud := detectSystem, geteuid, cloudflaredStatusBinary
				detectSystem = func() (*config.SystemInfo, error) { return &config.SystemInfo{OS: "ubuntu", Version: "24.04"}, nil }
				geteuid = func() int { return 1000 }
				cloudflaredStatusBinary = filepath.Join(dir, "missing-cloudflared")
				t.Cleanup(func() { detectSystem, geteuid, cloudflaredStatusBinary = oldDetect, oldEUID, oldCloud })
				site := filepath.Join(dir, "site.yaml")
				body := fmt.Sprintf(`extends: base
users:
  home_base: %q
modules:
  locale:
    enabled: false
  packages:
    enabled: false
  ssh:
    enabled: false
  cloudflared:
    private_network:
      interface: test-no-vlan
  network:
    enabled: %t
    ufw: %t
  monitoring:
    enabled: %t
    node_exporter: %t
    listen_address: 127.0.0.1
`, home, subject == "network", subject == "network", subject == "monitoring", subject == "monitoring")
				if err := os.WriteFile(site, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				root := NewRootCmd("test", "test")
				var output bytes.Buffer
				root.SetOut(&output)
				root.SetArgs([]string{op, "--config", site, "-o", "json"})
				err := root.Execute()
				if op == "check" {
					exit, ok := err.(*ExitError)
					if !ok || exit.Code != exitDrift {
						t.Fatalf("check must report blocked drift, not abort: %v\n%s", err, output.String())
					}
				} else if err != nil {
					t.Fatalf("status aborted: %v", err)
				}
				var report struct {
					ConfigError string `json:"config_error"`
					ModuleError string `json:"module_check_error"`
					Modules     []struct {
						Name    string          `json:"name"`
						Changes []module.Change `json:"changes"`
					} `json:"modules"`
				}
				if err := json.Unmarshal(output.Bytes(), &report); err != nil {
					t.Fatalf("report not readable JSON: %v\n%s", err, output.String())
				}
				if report.ConfigError != "" || report.ModuleError != "" || len(report.Modules) != 1 || report.Modules[0].Name != subject {
					t.Fatalf("report fell back or aborted: %+v", report)
				}
				blocked := false
				for _, change := range report.Modules[0].Changes {
					if change.State == module.ChangeBlocked && strings.Contains(change.Description, "UFW") {
						blocked = true
					}
				}
				if !blocked {
					t.Fatalf("missing blocked firewall change: %+v", report.Modules)
				}
				data, _ := os.ReadFile(calls)
				if strings.Contains(string(data), "ufw allow") || strings.Contains(string(data), "ufw --force") {
					t.Fatalf("read-only report changed firewall: %s", data)
				}
				for _, name := range []string{"lock", "audit"} {
					if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
						t.Fatalf("read-only report created %s: %v", name, err)
					}
				}
			})
		}
	}
}
