package cli

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/exec"
	"github.com/STAIxBWLB/rootfiles/internal/module"
)

func TestValidateModulesPreservesSelectionAndCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name, osName, version, want string
		modules                     []module.Module
	}{
		{"Ubuntu Docker", "ubuntu", "24.04", "", []module.Module{module.NewDockerModule()}},
		{"Rocky selected users", "rocky", "9.5", "", []module.Module{module.NewUsersModule()}},
		{"Rocky GPU refused", "rocky", "9.5", "gpu", []module.Module{module.NewGPUModule()}},
		{"Rocky mirror refused", "rocky", "9.5", "apt_mirror", []module.Module{module.NewSystemModule()}},
		{"Rocky fail2ban refused", "rocky", "9.5", "fail2ban", []module.Module{module.NewSecurityModule()}},
		{"unsupported distro", "debian", "12", "unsupported", []module.Module{module.NewUsersModule()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			// Unselected settings must not broaden the requested capability set.
			cfg.Modules.Docker.Enabled = true
			cfg.Modules.System.AptMirror = "https://example.invalid/ubuntu"
			cfg.Modules.Security.Fail2ban = true
			err := validateModules(cfg, &config.SystemInfo{OS: tc.osName, Version: tc.version}, tc.modules)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("selected modules rejected: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("capability error=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestReadOnlyRunContextKeepsLoggerBackendAndUnknownHome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		system *config.SystemInfo
		rpm    bool
	}{
		{"Ubuntu", &config.SystemInfo{OS: "ubuntu", Version: "24.04"}, false},
		{"Rocky", &config.SystemInfo{OS: "rocky", Version: "9.5"}, true},
		{"unsupported Rocky report", &config.SystemInfo{OS: "rocky", Version: "7"}, true},
		{"unsupported report", &config.SystemInfo{OS: "debian", Version: "12"}, false},
		{"unknown report", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			cfg := &config.Config{}
			oldAudit := auditLogger
			rc := readOnlyRunContext(cfg, tc.system, logger)
			if rc.Config != cfg || !rc.DryRun || !rc.Runner.DryRun || !rc.Yes || rc.Force || rc.Runner.Backup != nil || rc.Runner.Logger != logger {
				t.Fatalf("report context changed caller policy: %+v", rc)
			}
			if cfg.Users.HomeBase != "" {
				t.Fatalf("unknown home was defaulted: %q", cfg.Users.HomeBase)
			}
			if logs.Len() != 0 || auditLogger != oldAudit {
				t.Fatal("context construction logged or opened audit")
			}
			if tc.rpm {
				if _, ok := rc.APT.(*exec.RPM); !ok {
					t.Fatalf("backend=%T, want RPM", rc.APT)
				}
			} else if _, ok := rc.APT.(*exec.APT); !ok {
				t.Fatalf("backend=%T, want APT", rc.APT)
			}
		})
	}
}

func TestCheckKeepsStrictUnsupportedBackendPolicy(t *testing.T) {
	noHostCommands(t)
	t.Setenv("ROOTFILES_STATE_DIR", t.TempDir())
	old := detectSystem
	detectSystem = func() (*config.SystemInfo, error) { return &config.SystemInfo{OS: "debian", Version: "12"}, nil }
	t.Cleanup(func() { detectSystem = old })
	root := NewRootCmd("test", "test")
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"check", "--profile", "minimal", "--home-base", t.TempDir(), "--module", "locale", "-o", "json"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "cannot select package manager") {
		t.Fatalf("check inherited overview fallback policy: %v", err)
	}
}

func TestMaskSecretKeepsConfigPolicy(t *testing.T) {
	for _, tc := range []struct{ value, want string }{
		{"", ""}, {"a", "****"}, {"12345678", "****"}, {"123456789", "1234…(masked)"}, {"eyJhIjoic2VjcmV0", "eyJh…(masked)"},
	} {
		if got := maskSecret(tc.value); got != tc.want {
			t.Fatalf("maskSecret(%q)=%q, want %q", tc.value, got, tc.want)
		}
	}
}

func captureApplySummary(t *testing.T, cfg *config.Config) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "summary")
	out, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = out
	defer func() { os.Stdout = old; _ = out.Close() }()
	printApplySummary(cfg)
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = old
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestApplySummaryUsesSharedSaferMask(t *testing.T) {
	for _, secret := range []string{"tiny", "eightchr", "123456789abcdef"} {
		t.Run(secret, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Modules.Cloudflared = config.CloudflaredConfig{Enabled: true, TunnelToken: secret}
			printed := captureApplySummary(t, cfg)
			if !strings.Contains(printed, "token="+maskSecret(secret)+", vlan=false") {
				t.Fatalf("summary did not use common mask: %q", printed)
			}
			if strings.Contains(printed, secret) || len(secret) > 8 && strings.Contains(printed, secret[:8]) {
				t.Fatalf("summary disclosed too much of the token: %q", printed)
			}
			if cfg.Modules.Cloudflared.TunnelToken != secret {
				t.Fatal("summary mutated the configured credential")
			}
		})
	}
	cfg := &config.Config{}
	cfg.Modules.Cloudflared = config.CloudflaredConfig{Enabled: true, TunnelTokenFile: "/etc/rootfiles/token"}
	if printed := captureApplySummary(t, cfg); !strings.Contains(printed, "token=file:/etc/rootfiles/token, vlan=false") {
		t.Fatalf("token-file summary changed: %q", printed)
	}
}
