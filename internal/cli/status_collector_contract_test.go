package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/module"
)

// Baselines were captured before the collector refactor, at d1ad011 with only
// read-only fixture seams. Only fixture-root and hostname are normalized.
// Unsupported-OS text has no legacy golden: it now displays the capability
// refusal already present in JSON, instead of checking unsupported modules.
func TestStatusCollectorByteContract(t *testing.T) {
	cases := []struct {
		name string
	}{
		{"populated"},
		{"empty-databases"},
		{"corrupt-databases"},
		{"missing-binary"},
		{"failed-binary-query"},
		{"failed-host-queries"},
		{"failed-user-scan"},
		{"config-fallback"},
		{"unknown-home"},
		{"module-filter"},
		{"unsupported-os"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixtureRoot, site, filter := statusContractFixture(t, tc.name)
			for _, format := range []string{"json", "text"} {
				if tc.name == "unsupported-os" && format == "text" {
					continue
				}
				var stdout, stderr bytes.Buffer
				root := NewRootCmd("status-contract", "fixture")
				root.SetOut(&stdout)
				root.SetErr(&stderr)
				args := []string{"status", "--config", site, "--output", format}
				if filter != "" {
					args = append(args, "--module", filter)
				}
				root.SetArgs(args)
				if err := root.Execute(); err != nil {
					t.Fatalf("%s status must remain a report: %v, stderr=%s", format, err, stderr.String())
				}
				if stderr.Len() != 0 {
					t.Fatalf("%s status wrote unexpected stderr: %s", format, stderr.String())
				}
				if format == "json" {
					statusContractAssertJSON(t, tc.name, stdout.Bytes())
				} else if tc.name != "config-fallback" && tc.name != "unknown-home" {
					// The text and JSON paths must cover the same selected set.
					if !strings.Contains(stdout.String(), "packages") {
						t.Fatalf("text omitted selected packages module: %s", stdout.String())
					}
					if tc.name != "module-filter" && !strings.Contains(stdout.String(), "system") {
						t.Fatalf("text omitted selected system module: %s", stdout.String())
					}
				}
				hostname, err := os.Hostname()
				if err != nil || hostname == "" {
					t.Fatalf("cannot normalize fixture hostname: %v", err)
				}
				got := strings.ReplaceAll(stdout.String(), fixtureRoot, "<fixture>")
				got = strings.ReplaceAll(got, hostname, "<hostname>")
				golden := filepath.Join("testdata", "status-t3", tc.name+"."+format+".golden")
				want, err := os.ReadFile(golden)
				if err != nil {
					t.Fatalf("read baseline golden %s: %v; generate on d1ad011 before the collector changes", golden, err)
				}
				if got != string(want) {
					t.Errorf("%s output changed\n--- baseline\n%s\n--- current\n%s", format, want, got)
				}
			}
		})
	}
}

func statusContractAssertJSON(t *testing.T, name string, data []byte) {
	t.Helper()
	var report statusReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("status output is not JSON: %v\n%s", err, data)
	}
	got := make([]string, 0, len(report.Modules))
	for _, entry := range report.Modules {
		got = append(got, entry.Name)
	}
	want := []string{"system", "packages"}
	if name == "module-filter" {
		want = []string{"packages"}
	}
	if name == "config-fallback" || name == "unknown-home" || name == "unsupported-os" {
		want = []string{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("module set/order = %v, want %v", got, want)
	}
	if name == "populated" {
		if report.Tunnel.Binary != "cloudflared version 2026.11.10 <fixture>&\"quoted\"" || !strings.Contains(string(data), "<fixture>&") {
			t.Fatalf("raw tunnel version or non-HTML-escaped JSON changed: %s", data)
		}
		if report.GPU == nil || len(report.GPU.Allocations) != 2 || report.Users.Managed != 2 || report.Users.System != 2 {
			t.Fatalf("populated database collection changed: gpu=%+v users=%+v", report.GPU, report.Users)
		}
	}
	if name == "unknown-home" && (report.Users.HomeBase != "" || report.GPU != nil || report.ConfigErr == "") {
		t.Fatalf("unknown home became a default home: %+v", report)
	}
	if name == "unsupported-os" && !strings.Contains(report.ModuleCheckError, "unsupported operating system") {
		t.Fatalf("unsupported OS capability failure disappeared: %+v", report)
	}
}

func statusContractFixture(t *testing.T, name string) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	home := filepath.Join(dir, "home")
	for _, p := range []string{bin, filepath.Join(home, ".rootfiles")} {
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin) // No ambient systemctl, ip, package queries or NSS tools.
	t.Setenv("HOME", home)
	t.Setenv("ROOTFILES_STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv("ROOTFILES_LOCK_FILE", filepath.Join(dir, "lock"))
	t.Setenv("ROOTFILES_LOG_FILE", filepath.Join(dir, "audit"))
	for _, key := range []string{"ROOTFILES_PROFILE", "ROOTFILES_USER", "ROOTFILES_SSH_PUBKEY", "ROOTFILES_TUNNEL_TOKEN", "ROOTFILES_VLAN_ADDRESS", "ROOTFILES_VLAN_INTERFACE", "ROOTFILES_DOCKER_ROOT", "ROOTFILES_DATA_DIR", "ROOTFILES_TIMEZONE", "ROOTFILES_HOME_BASE"} {
		t.Setenv(key, "")
	}
	oldDetect, oldUID, oldBinary := detectSystem, geteuid, cloudflaredStatusBinary
	oldScan, oldWarn, oldVersion := scanStatusUsers, warnStatusMetadata, buildVersion
	t.Cleanup(func() {
		detectSystem, geteuid, cloudflaredStatusBinary = oldDetect, oldUID, oldBinary
		scanStatusUsers, warnStatusMetadata, buildVersion = oldScan, oldWarn, oldVersion
	})
	sys := &config.SystemInfo{OS: "ubuntu", Version: "24.04", Codename: "noble", Arch: "amd64", CPUCores: 8, MemoryGB: 32, StorageLayout: []config.MountPoint{}}
	if name == "unsupported-os" {
		sys.OS = "fixture-unsupported"
	}
	detectSystem = func() (*config.SystemInfo, error) { return sys, nil }
	geteuid = func() int { return 1000 }
	// The metadata itself is private and synthetic; do not walk the test
	// user's real ancestors to inspect their ownership as root would.
	warnStatusMetadata = func(*module.RunContext) {}
	scanStatusUsers = func(context.Context, *module.RunContext) ([]module.UserMeta, error) {
		if name == "failed-user-scan" {
			return nil, errors.New("fixture passwd unavailable")
		}
		return []module.UserMeta{{Name: "fixture-one", UID: 1001}, {Name: "fixture-two", UID: 1002}}, nil
	}
	commands := map[string]string{
		"systemctl":   "printf 'active\\n'",
		"ip":          "printf 'fixture0 UP 203.0.113.7/32\\n'",
		"dpkg-query":  "exit 0",
		"cloudflared": "printf '%s\\n' 'cloudflared version 2026.11.10 <fixture>&\"quoted\"'",
	}
	if name == "failed-binary-query" {
		commands["cloudflared"] = "printf 'fixture version query failed\\n' >&2; exit 1"
	}
	if name == "failed-host-queries" {
		commands["systemctl"] = "printf 'inactive\\n'; exit 3"
		commands["ip"] = "exit 1"
	}
	for command, body := range commands {
		if err := os.WriteFile(filepath.Join(bin, command), []byte("#!/bin/sh\n"+body+"\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	cloudflaredStatusBinary = filepath.Join(bin, "cloudflared")
	if name == "missing-binary" {
		cloudflaredStatusBinary = filepath.Join(bin, "absent-cloudflared")
	}
	if name != "empty-databases" {
		dbs := map[string]any{
			"users.json":           module.UsersDB{Version: 1, HomeBase: home, Users: []module.UserMeta{{Name: "alice", UID: 1001}, {Name: "bob", UID: 1002}}},
			"gpu-allocations.json": module.GPUAllocationsDB{Version: 1, TotalGPUs: 2, GPUModel: "fixture GPU", Allocations: []module.GPUAllocation{{Username: "bob", GPUs: []int{1}, Method: "env"}, {Username: "alice", GPUs: []int{0}, Method: "env"}}},
		}
		for filename, db := range dbs {
			data, err := json.Marshal(db)
			if err != nil {
				t.Fatal(err)
			}
			if name == "corrupt-databases" {
				data = []byte("not JSON")
			}
			if err := os.WriteFile(filepath.Join(home, ".rootfiles", filename), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	site := filepath.Join(dir, "site.yaml")
	content := fmt.Sprintf("users:\n  home_base: %q\nmodules:\n  system:\n    enabled: true\n  packages:\n    enabled: true\n  cloudflared:\n    private_network:\n      interface: fixture0\npackages: [fixture-package]\n", home)
	if name == "config-fallback" || name == "unknown-home" {
		content = "modules: [invalid fixture\n"
		if name == "config-fallback" {
			t.Setenv("ROOTFILES_HOME_BASE", home)
		}
	}
	if err := os.WriteFile(site, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	filter := ""
	if name == "module-filter" {
		filter = "packages"
	}
	return dir, site, filter
}
