package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/exec"
	"github.com/STAIxBWLB/rootfiles/internal/module"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

func unexpectedProcessStdout(t *testing.T, run func()) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "process-stdout")
	file, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = file
	defer func() { os.Stdout = previous; _ = file.Close() }()
	run()
	os.Stdout = previous
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestApplySummaryWritesOnlyToExplicitWriter(t *testing.T) {
	var output bytes.Buffer
	cfg := &config.Config{Timezone: "UTC"}
	leaked := unexpectedProcessStdout(t, func() {
		printApplySummary(&output, cfg)
		if err := configureInteractive(&output, cfg, true, false); err != nil {
			t.Fatal(err)
		}
	})
	if got, want := output.String(), "\n=== Summary ===\n  Timezone: UTC\n"; got != want || leaked != "" {
		t.Fatalf("summary=%q want=%q process stdout=%q", got, want, leaked)
	}
}

func TestApplyRoutesCommandAndModuleOutput(t *testing.T) {
	aliasCLIFixture(t)
	site := filepath.Join(t.TempDir(), "site.yaml")
	if err := os.WriteFile(site, []byte("timezone: UTC\nmodules:\n  packages:\n    enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output, diagnostic bytes.Buffer
	root := NewRootCmd("test", "test")
	root.SetOut(&output)
	root.SetErr(&diagnostic)
	root.SetArgs([]string{"apply", "--config", site, "--yes", "--dry-run"})
	leaked := unexpectedProcessStdout(t, func() {
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"Profile: minimal\n", "Mode: dry-run (no changes will be made)\n", "Modules: packages\n", "packages: already satisfied"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("command writer missing %q: %s", want, output.String())
		}
	}
	if leaked != "" {
		t.Fatalf("apply leaked process stdout: %q", leaked)
	}
}

func TestUpdateCheckKeepsTextAndExitCodeOnCommandWriter(t *testing.T) {
	for _, tc := range []struct {
		version, want string
		code          int
	}{
		{"v1.0.0", "Already at version v1.0.0, nothing to do.\n", 0},
		{"v2.0.0", "Current: v1.0.0 " + ui.MarkNext + " Target: v2.0.0\nUpdate available. Run `sudo rootfiles update` to install.\n", exitDrift},
	} {
		var output bytes.Buffer
		root := NewRootCmd("v1.0.0", "test")
		root.SetOut(&output)
		root.SetErr(io.Discard)
		root.SetArgs([]string{"update", "--check", "--version", tc.version})
		var runErr error
		leaked := unexpectedProcessStdout(t, func() { runErr = root.Execute() })
		code := 0
		if runErr != nil {
			var exit *ExitError
			if !errors.As(runErr, &exit) {
				t.Fatal(runErr)
			}
			code = exit.Code
		}
		if output.String() != tc.want || code != tc.code || leaked != "" {
			t.Fatalf("update text=%q code=%d process stdout=%q", output.String(), code, leaked)
		}
	}
}

func TestTunnelUpdateCheckUsesRunContextWriter(t *testing.T) {
	previousPath, previousTransport := cloudflaredPath, http.DefaultTransport
	t.Cleanup(func() { cloudflaredPath, http.DefaultTransport = previousPath, previousTransport })
	cloudflaredPath = filepath.Join(t.TempDir(), "cloudflared")
	if err := os.WriteFile(cloudflaredPath, []byte("#!/bin/sh\nprintf 'cloudflared version 2026.1.0 (built fixture)\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	http.DefaultTransport = updateCheckTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"tag_name":"v2026.1.0"}`))}, nil
	})
	var output bytes.Buffer
	rc := &module.RunContext{Out: &output, Runner: exec.NewRunner(true, slog.New(slog.NewTextHandler(io.Discard, nil)))}
	leaked := unexpectedProcessStdout(t, func() {
		if err := tunnelUpdateCheck(context.Background(), rc); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"Cloudflared update check", "Current:", "2026.1.0", "Latest:", "v2026.1.0", "up to date."} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("tunnel writer missing %q: %s", want, output.String())
		}
	}
	if leaked != "" {
		t.Fatalf("tunnel check leaked process stdout: %q", leaked)
	}
}

func TestUserPasswordTargetsAndContextUseCommandWriter(t *testing.T) {
	aliasCLIFixture(t)
	var output bytes.Buffer
	previous := setCLIUserPasswords
	setCLIUserPasswords = func(_ context.Context, rc *module.RunContext, entries []module.PasswordEntry, _ string, _ bool) error {
		if rc.Output() == nil || len(entries) != 2 {
			t.Fatalf("password context writer=%v entries=%v", rc.Out, entries)
		}
		ui.WriteLine(rc.Output(), "password helper output")
		return nil
	}
	t.Cleanup(func() { setCLIUserPasswords = previous })
	root := NewRootCmd("test", "test")
	root.SetOut(&output)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"user", "passwd", "alice", "bob", "--yes", "--dry-run"})
	leaked := unexpectedProcessStdout(t, func() {
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
	})
	if got, want := output.String(), "Target users (2):\n  - alice\n  - bob\npassword helper output\n"; got != want || leaked != "" {
		t.Fatalf("password output=%q want=%q process stdout=%q", got, want, leaked)
	}
}

func TestDoctorContextWriterKeepsJSONSeparate(t *testing.T) {
	aliasCLIFixture(t)
	var output bytes.Buffer
	previous := doctorChecks
	doctorChecks = func(_ context.Context, rc *module.RunContext) []module.Finding {
		if rc.Output() != &output {
			t.Fatal("doctor did not pass its injected writer to the report context")
		}
		return []module.Finding{{Check: "fixture", Level: module.LevelOK, Detail: "healthy"}}
	}
	t.Cleanup(func() { doctorChecks = previous })
	root := NewRootCmd("test", "test")
	root.SetOut(&output)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"doctor", "-o", "json"})
	leaked := unexpectedProcessStdout(t, func() {
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
	})
	var report struct {
		Findings []module.Finding `json:"findings"`
	}
	if err := json.Unmarshal(output.Bytes(), &report); err != nil || len(report.Findings) != 1 || leaked != "" {
		t.Fatalf("doctor JSON=%q error=%v process stdout=%q", output.String(), err, leaked)
	}
}
