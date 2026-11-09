package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/exec"
	"github.com/STAIxBWLB/rootfiles/internal/module"
	"github.com/STAIxBWLB/rootfiles/internal/state"
)

func TestMutatingCapabilitiesRefuseRocky(t *testing.T) {
	old := detectSystem
	detectSystem = func() (*config.SystemInfo, error) { return &config.SystemInfo{OS: "rocky", Version: "9.4"}, nil }
	t.Cleanup(func() { detectSystem = old })
	for _, tc := range []struct {
		args   []string
		module string
	}{
		{[]string{"gpu", "assign", "alice", "--gpus", "0"}, "gpu"},
		{[]string{"gpu", "revoke", "alice"}, "gpu"},
		{[]string{"tunnel", "setup", "secret"}, "cloudflared"},
		{[]string{"tunnel", "install"}, "cloudflared"},
		{[]string{"tunnel", "restart"}, "cloudflared"},
		{[]string{"tunnel", "update"}, "cloudflared"},
		{[]string{"tunnel", "uninstall"}, "cloudflared"},
		{[]string{"tenant", "restart", "demo"}, "tenant"},
	} {
		t.Run(strings.Join(tc.args[:2], " "), func(t *testing.T) {
			noHostCommands(t)
			dir := t.TempDir()
			t.Setenv("ROOTFILES_STATE_DIR", dir)
			t.Setenv("ROOTFILES_LOG_FILE", filepath.Join(dir, "audit"))
			root := NewRootCmd("test", "test")
			root.SetArgs(append(tc.args, "--dry-run", "--profile", "rocky"))
			err := root.Execute()
			if err == nil || !strings.Contains(err.Error(), "unsupported") || !strings.Contains(err.Error(), tc.module) {
				t.Fatalf("capability refusal: %v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("refused mutation wrote state: %v, %v", entries, err)
			}
		})
	}
}

func TestRunContextSelectsRockyPackageManager(t *testing.T) {
	old := detectSystem
	detectSystem = func() (*config.SystemInfo, error) { return &config.SystemInfo{OS: "rocky", Version: "9.4"}, nil }
	t.Cleanup(func() { detectSystem = old })
	noHostCommands(t)
	root := NewRootCmd("test", "test")
	cmd, _, _ := root.Find([]string{"user", "add"})
	if err := cmd.ParseFlags([]string{"--profile", "rocky", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	rc, err := buildRunContext(cmd, "users")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rc.APT.(*exec.RPM); !ok {
		t.Fatalf("Rocky backend: %T", rc.APT)
	}
}

func TestBackupPreflightLocksAndAudits(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ROOTFILES_LOCK_FILE", filepath.Join(dir, "lock"))
	oldEUID, oldOS, oldLock, oldAudit := geteuid, nativeHostOS, heldLock, auditLogger
	geteuid = func() int { return 0 }
	nativeHostOS = "linux"
	heldLock = nil
	var audit bytes.Buffer
	auditLogger = slog.New(slog.NewJSONHandler(&audit, nil))
	t.Cleanup(func() {
		heldLock.Release()
		geteuid, nativeHostOS, heldLock, auditLogger = oldEUID, oldOS, oldLock, oldAudit
	})
	root := NewRootCmd("test", "test")
	cmd, _, _ := root.Find([]string{"backup"})
	if err := preflight(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if heldLock == nil {
		t.Fatal("backup has no lock")
	}
	if _, err := state.Acquire(); err == nil {
		t.Fatal("backup did not exclude concurrent mutation")
	}
	var record map[string]any
	if err := json.Unmarshal(audit.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["cmd"] != "rootfiles backup" || record["msg"] != "start" {
		t.Fatalf("backup audit: %v", record)
	}
}

func TestNativeYesFlag(t *testing.T) {
	t.Setenv("ROOTFILES_YES", "true")
	for _, path := range [][]string{{"apply"}, {"tunnel", "setup"}, {"rollback"}} {
		root := NewRootCmd("test", "test")
		cmd, _, _ := root.Find(path)
		if err := cmd.ParseFlags(nil); err != nil {
			t.Fatal(err)
		}
		if !yesFlag(cmd) {
			t.Fatalf("%v ignored ROOTFILES_YES", path)
		}
	}
}

func TestRollbackHonorsYesEnvironment(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ROOTFILES_STATE_DIR", dir)
	t.Setenv("ROOTFILES_LOCK_FILE", filepath.Join(dir, "lock"))
	t.Setenv("ROOTFILES_LOG_FILE", filepath.Join(dir, "audit"))
	t.Setenv("ROOTFILES_YES", "true")
	oldEUID, oldOS, oldLock, oldAudit := geteuid, nativeHostOS, heldLock, auditLogger
	geteuid = func() int { return 0 }
	nativeHostOS = "linux"
	heldLock = nil
	auditLogger = slog.New(slog.NewTextHandler(io.Discard, nil))
	t.Cleanup(func() {
		heldLock.Release()
		geteuid, nativeHostOS, heldLock, auditLogger = oldEUID, oldOS, oldLock, oldAudit
	})
	file := filepath.Join(dir, "managed")
	if err := os.WriteFile(file, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	backup := exec.NewBackup(state.BackupsDir(), "fixture")
	if err := backup.Preserve(file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("after"), 0600); err != nil {
		t.Fatal(err)
	}
	root := NewRootCmd("test", "test")
	root.SetOut(io.Discard)
	root.SetArgs([]string{"rollback", backup.ID})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "before" {
		t.Fatalf("restore=%q: %v", got, err)
	}
}

func TestNativeFlagScopes(t *testing.T) {
	for _, tc := range []struct {
		args []string
		flag string
	}{
		{[]string{"doctor", "--module", "ssh"}, "module"},
		{[]string{"status", "--force"}, "force"},
		{[]string{"update", "--profile", "base"}, "profile"},
		{[]string{"gpu", "assign", "alice", "--tunnel-token", "secret"}, "tunnel-token"},
	} {
		root := NewRootCmd("test", "test")
		root.SetArgs(tc.args)
		if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "--"+tc.flag) {
			t.Fatalf("ignored flag %v: %v", tc.args, err)
		}
	}
}

func TestConfigInitOverwriteAndAlias(t *testing.T) {
	for _, flag := range []string{"--overwrite", "--force"} {
		t.Run(flag, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "site.yaml")
			if err := os.WriteFile(file, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			root := NewRootCmd("test", "test")
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetArgs([]string{"config", "init", "--extends", "minimal", "--file", file, flag})
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(file); err != nil || !strings.Contains(string(data), "extends: minimal") {
				t.Fatalf("config=%q: %v", data, err)
			}
		})
	}
}

func TestStatusModuleFilter(t *testing.T) {
	noHostCommands(t)
	t.Setenv("ROOTFILES_STATE_DIR", t.TempDir())
	cfg := &config.Config{}
	cfg.Modules.Locale.Enabled = true
	cfg.Modules.Packages.Enabled = true
	rc := &module.RunContext{Config: cfg, Runner: exec.NewRunner(true, slog.New(slog.NewTextHandler(io.Discard, nil))), DryRun: true}
	rc.APT = exec.NewAPT(rc.Runner)
	report := collectStatus(context.Background(), rc, &config.SystemInfo{OS: "ubuntu"}, "minimal", "", nil, nil, "locale")
	if len(report.Modules) != 1 || report.Modules[0].Name != "locale" {
		t.Fatalf("filtered modules: %+v (%s)", report.Modules, report.ModuleCheckError)
	}
	var text bytes.Buffer
	renderModulesSection(context.Background(), &text, rc, "locale")
	if strings.Contains(text.String(), "packages") {
		t.Fatalf("text filter ignored: %s", text.String())
	}
}

// Exercise the real mutation path as well as --dry-run: only lock/audit
// preflight may run before the unsupported capability is refused.
func TestRealMutationCapabilityGate(t *testing.T) {
	oldSystem, oldEUID, oldOS, oldLock, oldAudit := detectSystem, geteuid, nativeHostOS, heldLock, auditLogger
	detectSystem = func() (*config.SystemInfo, error) { return &config.SystemInfo{OS: "rocky", Version: "9.4"}, nil }
	geteuid = func() int { return 0 }
	nativeHostOS = "linux"
	t.Cleanup(func() {
		detectSystem, geteuid, nativeHostOS, heldLock, auditLogger = oldSystem, oldEUID, oldOS, oldLock, oldAudit
	})
	for _, args := range [][]string{{"gpu", "assign", "alice", "--gpus", "0"}, {"tunnel", "setup", "secret"}} {
		t.Run(args[0], func(t *testing.T) {
			noHostCommands(t)
			stateDir := t.TempDir()
			t.Setenv("ROOTFILES_STATE_DIR", stateDir)
			t.Setenv("ROOTFILES_LOCK_FILE", filepath.Join(t.TempDir(), "lock"))
			heldLock = nil
			var audit bytes.Buffer
			auditLogger = slog.New(slog.NewJSONHandler(&audit, nil))
			t.Cleanup(func() { heldLock.Release(); heldLock = nil })
			root := NewRootCmd("test", "test")
			root.SetArgs(append(args, "--profile", "rocky"))
			err := root.Execute()
			if err == nil || !strings.Contains(err.Error(), "unsupported") {
				t.Fatalf("capability refusal: %v", err)
			}
			if heldLock == nil || audit.Len() == 0 {
				t.Fatal("real mutation skipped native preflight")
			}
			if entries, err := os.ReadDir(stateDir); err != nil || len(entries) != 0 {
				t.Fatalf("refused mutation changed state: %v, %v", entries, err)
			}
		})
	}
}

func TestConsumedRootFlagContracts(t *testing.T) {
	inputs := []string{"--user", "flag-contract-user", "--ssh-pubkey", "ssh-ed25519 fixture-key", "--tunnel-token", "fixture-token", "--vlan-address", "192.0.2.1/32"}
	for _, path := range [][]string{{"apply"}, {"check"}, {"config", "show"}, {"config", "validate"}, {"user", "add"}, {"tunnel", "setup"}, {"tunnel", "install"}} {
		t.Run(strings.Join(path, " "), func(t *testing.T) {
			root := NewRootCmd("test", "test")
			cmd, _, err := root.Find(path)
			if err != nil {
				t.Fatal(err)
			}
			flags := inputs
			if path[0] == "user" {
				flags = inputs[:4]
			}
			if path[0] == "tunnel" {
				flags = inputs[4:]
			}
			flags = append(append([]string{}, flags...), "--profile", "minimal", "--dry-run")
			if err := cmd.ParseFlags(flags); err != nil {
				t.Fatal(err)
			}
			if err := preflight(cmd, nil); err != nil {
				t.Fatalf("reader refused consumed flags: %v", err)
			}
			// The preflight must preserve the parsed inputs for the existing readers.
			if path[0] != "tunnel" {
				name, _ := cmd.Flags().GetString("user")
				key, _ := cmd.Flags().GetString("ssh-pubkey")
				if name != "flag-contract-user" || key != "ssh-ed25519 fixture-key" {
					t.Fatalf("account inputs lost: %q, %q", name, key)
				}
			}
			if path[0] != "user" {
				token, _ := cmd.Flags().GetString("tunnel-token")
				address, _ := cmd.Flags().GetString("vlan-address")
				if token != "fixture-token" || address != "192.0.2.1/32" {
					t.Fatalf("tunnel inputs lost: %q, %q", token, address)
				}
			}
		})
	}
}

func TestCheckAcceptsApplyAccountAndTunnelOverrides(t *testing.T) {
	old := detectSystem
	detectSystem = func() (*config.SystemInfo, error) { return &config.SystemInfo{OS: "ubuntu", Version: "24.04"}, nil }
	t.Cleanup(func() { detectSystem = old })
	noHostCommands(t)
	t.Setenv("ROOTFILES_STATE_DIR", t.TempDir())
	root := NewRootCmd("test", "test")
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetArgs([]string{"check", "--profile", "minimal", "--user", "flag-contract-user", "--ssh-pubkey", "ssh-ed25519 fixture-key", "--tunnel-token", "fixture-token", "--vlan-address", "192.0.2.1/32", "-o", "json"})
	err := root.Execute()
	if err != nil {
		exit, ok := err.(*ExitError)
		if !ok || exit.Code != exitDrift {
			t.Fatalf("check overrides: %v", err)
		}
	}
	var report checkReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("check did not produce a report: %v", err)
	}
}
