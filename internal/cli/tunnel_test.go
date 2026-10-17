package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/module"
	"github.com/STAIxBWLB/rootfiles/internal/state"
)

func TestNewTunnelCmd_Subcommands(t *testing.T) {
	cmd := newTunnelCmd()
	if cmd.Use != "tunnel" {
		t.Errorf("Use = %q, want tunnel", cmd.Use)
	}

	want := map[string]bool{
		"install":   false,
		"setup":     false,
		"status":    false,
		"restart":   false,
		"update":    false,
		"uninstall": false,
	}
	for _, sub := range cmd.Commands() {
		name := sub.Name()
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("tunnel missing subcommand %q", name)
		}
	}
}

// noHostCommands empties PATH so system detection in buildRunContext cannot
// run host binaries such as nvidia-smi.
func noHostCommands(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
}

func TestBuildRunContext_LoadsWithoutFlags(t *testing.T) {
	noHostCommands(t)
	// Build a root command so buildRunContext can resolve persistent flags.
	root := NewRootCmd("test", "abc")
	// Find the tunnel status subcommand (simple, no args)
	sub, _, err := root.Find([]string{"tunnel", "status"})
	if err != nil {
		t.Fatalf("find tunnel status: %v", err)
	}
	// Set required flag values to their defaults.
	rc, err := buildRunContext(sub)
	if err != nil {
		t.Fatal(err)
	}
	if rc == nil {
		t.Fatal("buildRunContext returned nil")
	}
	if rc.Config == nil {
		t.Fatal("rc.Config is nil")
	}
	if rc.Runner == nil {
		t.Error("rc.Runner is nil")
	}
	if rc.APT == nil {
		t.Error("rc.APT is nil")
	}
}

// AC1: commands that read or write user or GPU state stop on a config load
// error instead of falling back to /home and an empty profile.
func TestBuildRunContext_LoadErrorStopsStatefulCommands(t *testing.T) {
	cases := []struct {
		name, homeBase string
		args           []string
		want           string
	}{
		{"user add, mistyped profile", "/home", []string{"user", "add", "alice", "--profile", "nope"}, `profile "nope" not found`},
		{"gpu assign, mistyped profile", "/home", []string{"gpu", "assign", "alice", "--gpus", "0", "--profile", "nope"}, `profile "nope" not found`},
		{"user add, relative ROOTFILES_HOME_BASE", "home", []string{"user", "add", "alice"}, "users.home_base"},
		{"gpu assign, relative ROOTFILES_HOME_BASE", "home", []string{"gpu", "assign", "alice", "--gpus", "0"}, "users.home_base"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			noHostCommands(t)
			t.Setenv("ROOTFILES_HOME_BASE", tc.homeBase)
			root := NewRootCmd("test", "abc")
			root.SetArgs(append(tc.args, "--dry-run"))
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			if err := root.Execute(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// AC2: config-free commands such as `tunnel status` still run.
func TestBuildConfigFreeRunContext_ToleratesLoadError(t *testing.T) {
	noHostCommands(t)
	t.Setenv("ROOTFILES_HOME_BASE", "home")
	root := NewRootCmd("test", "abc")
	sub, _, err := root.Find([]string{"tunnel", "status"})
	if err != nil {
		t.Fatalf("find tunnel status: %v", err)
	}
	if err := sub.ParseFlags([]string{"--profile", "nope"}); err != nil {
		t.Fatal(err)
	}
	if _, err := buildRunContext(sub); err == nil {
		t.Fatal("buildRunContext accepted a mistyped profile")
	}
	rc, err := buildConfigFreeRunContext(sub)
	if err != nil || rc == nil || rc.Config == nil {
		t.Fatalf("buildConfigFreeRunContext = %v, %v; want a run context", rc, err)
	}
}

// AC2 wiring: config-free commands run their action without a loadable
// profile. Dry runs keep them off the host.
func TestConfigFreeCommandsRunWithoutProfile(t *testing.T) {
	for _, args := range [][]string{{"tunnel", "restart"}, {"schedule", "disable"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			noHostCommands(t)
			root := NewRootCmd("test", "abc")
			root.SetArgs(append(args, "--profile", "nope", "--dry-run"))
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			if err := root.Execute(); err != nil {
				t.Errorf("err = %v, want the command to run without a profile", err)
			}
		})
	}
}

// runContextFor builds the run context of the subcommand at path with flags.
func runContextFor(t *testing.T, build func(*cobra.Command) (*module.RunContext, error), path []string, flags ...string) (*module.RunContext, error) {
	t.Helper()
	root := NewRootCmd("test", "abc")
	sub, _, err := root.Find(path)
	if err != nil {
		t.Fatalf("find %v: %v", path, err)
	}
	if err := sub.ParseFlags(flags); err != nil {
		t.Fatal(err)
	}
	return build(sub)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// #32: subcommands pick their config the way apply and check do.
func TestBuildRunContext_ConfigSelection(t *testing.T) {
	noHostCommands(t)
	t.Setenv("ROOTFILES_HOME_BASE", "")
	t.Setenv("ROOTFILES_STATE_DIR", t.TempDir())
	dir := t.TempDir()
	site := filepath.Join(dir, "site.yaml")
	writeFile(t, site, "extends: minimal\nusers:\n  home_base: /srv/home\n  default_shell: /bin/sh\n")
	broken := filepath.Join(dir, "broken.yaml")
	writeFile(t, broken, "users: [\n")
	userAdd := []string{"user", "add"}

	// AC1: --config is honored.
	rc, err := runContextFor(t, buildRunContext, userAdd, "--config", site)
	if err != nil || rc.Config.Users.HomeBase != "/srv/home" || rc.Config.Users.DefaultShell != "/bin/sh" {
		t.Fatalf("--config: got %+v, err=%v", rc, err)
	}
	// AC2: a broken --config file stops the command.
	if _, err := runContextFor(t, buildRunContext, userAdd, "--config", broken); err == nil {
		t.Error("broken --config: want a load error")
	}
	// AC3: without flags or env, the config last applied wins, through the
	// copy apply kept; the recorded file is not read again.
	fingerprint, err := rc.Config.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Record(state.Run{ConfigPath: site, ConfigSHA256: fingerprint, Success: true}); err != nil {
		t.Fatal(err)
	}
	if err := saveAppliedConfig(rc.Config); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "base.yaml"), "extends: minimal\nusers:\n  default_groups: [tampered]\n")
	writeFile(t, site, "extends: base.yaml\nusers:\n  home_base: /srv/changed\n")
	if rc, err := runContextFor(t, buildRunContext, userAdd); err != nil || rc.Config.Users.HomeBase != "/srv/home" ||
		rc.Config.Users.DefaultShell != "/bin/sh" || strings.Contains(strings.Join(rc.Config.Users.DefaultGroups, ","), "tampered") {
		t.Errorf("last applied config: got %+v, err=%v; want the kept copy", rc, err)
	}
	// The kept copy is reused only when it is the recorded run's config,
	// root alone controls it and this process can read it; otherwise the
	// subcommand falls back.
	t.Setenv("ROOTFILES_HOME_BASE", "/home") // keep the fallback profile off host detection
	if err := state.Record(state.Run{ConfigPath: site, ConfigSHA256: "another run", Success: true}); err != nil {
		t.Fatal(err)
	}
	if rc, err := runContextFor(t, buildRunContext, userAdd); err != nil || rc.Config.Users.DefaultShell == "/bin/sh" {
		t.Errorf("kept copy of another run: got %+v, err=%v; want the fallback profile", rc, err)
	}
	if err := state.Record(state.Run{ConfigPath: site, ConfigSHA256: fingerprint, Success: true}); err != nil {
		t.Fatal(err)
	}
	snap := state.AppliedConfigPath()
	for _, mode := range []os.FileMode{0o666, 0o000} {
		if mode == 0 && os.Geteuid() == 0 {
			continue // root reads a 0000 file
		}
		if err := os.Chmod(snap, mode); err != nil {
			t.Fatal(err)
		}
		if rc, err := runContextFor(t, buildRunContext, userAdd); err != nil || rc.Config.Users.DefaultShell == "/bin/sh" {
			t.Errorf("kept copy with mode %o: got %+v, err=%v; want the fallback profile", mode, rc, err)
		}
	}
	if err := state.SaveAppliedConfig(nil); err != nil {
		t.Fatal(err)
	}
	if rc, err := runContextFor(t, buildRunContext, userAdd); err != nil || rc.Config.Users.DefaultShell == "/bin/sh" {
		t.Errorf("no kept copy: got %+v, err=%v; want the fallback profile", rc, err)
	}
	// A profile apply is reused through its kept copy, with what the apply
	// added on top of the profile; the profile name alone is not re-resolved.
	dgx, err := config.LoadWithHomeBase("dgx", "", &config.SystemInfo{}, "/srv/override")
	if err != nil {
		t.Fatal(err)
	}
	dgxPrint, _ := dgx.Fingerprint()
	if err := state.Record(state.Run{Profile: "dgx", ConfigSHA256: dgxPrint, Success: true}); err != nil {
		t.Fatal(err)
	}
	if err := saveAppliedConfig(dgx); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROOTFILES_HOME_BASE", "")
	if rc, err := runContextFor(t, buildRunContext, userAdd); err != nil || rc.Config.Users.HomeBase != "/srv/override" {
		t.Errorf("profile apply with --home-base: got %+v, err=%v; want the kept override", rc, err)
	}
	t.Setenv("ROOTFILES_HOME_BASE", "/home")
	if err := state.SaveAppliedConfig(nil); err != nil {
		t.Fatal(err)
	}
	if rc, err := runContextFor(t, buildRunContext, userAdd); err != nil || rc.Config.Modules.Cloudflared.PrivateNetwork.Enabled {
		t.Errorf("profile apply without a kept copy: got %+v, err=%v; want minimal, not dgx's private network", rc, err)
	}
	// check and status, which read the recorded path, ignore a relative one.
	other := t.TempDir()
	writeFile(t, filepath.Join(other, "site.yaml"), "extends: minimal\n")
	chdir(t, other)
	if err := state.Record(state.Run{ConfigPath: "site.yaml", Success: true}); err != nil {
		t.Fatal(err)
	}
	root := NewRootCmd("test", "abc")
	sub, _, _ := root.Find([]string{"check"})
	if _, path, _ := resolveTarget(sub, &config.SystemInfo{}); path != "" {
		t.Errorf("resolveTarget used the relative recorded path %q", path)
	}
}

// apply keeps the resolved config, mode 0600, without the inline tunnel token
// or extends, and its bytes hash to the config's fingerprint.
func TestSaveAppliedConfig(t *testing.T) {
	t.Setenv("ROOTFILES_STATE_DIR", t.TempDir())
	cfg := &config.Config{Extends: "minimal"}
	cfg.Users.HomeBase = "/srv/home"
	cfg.Modules.Cloudflared.TunnelToken = "secret-token"
	if err := saveAppliedConfig(cfg); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(state.AppliedConfigPath())
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("kept copy: %v, err=%v; want mode 0600", fi, err)
	}
	data, _ := os.ReadFile(state.AppliedConfigPath())
	if strings.Contains(string(data), "secret-token") || strings.Contains(string(data), "extends") || !strings.Contains(string(data), "/srv/home") {
		t.Errorf("kept copy:\n%s", data)
	}
	if cfg.Modules.Cloudflared.TunnelToken != "secret-token" {
		t.Error("saveAppliedConfig changed the live config")
	}
	if print, _ := cfg.Fingerprint(); func() string {
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:])
	}() != print {
		t.Error("kept copy does not hash to the config's fingerprint")
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
}

// #36 AC1, AC3: the config-free fallback keeps env overrides and --home-base.
func TestBuildConfigFreeRunContext_FallbackKeepsOverrides(t *testing.T) {
	noHostCommands(t)
	t.Setenv("ROOTFILES_VLAN_INTERFACE", "vlan7")
	rc, err := runContextFor(t, buildConfigFreeRunContext, []string{"tunnel", "status"}, "--profile", "nope", "--home-base", "/srv/h")
	if err != nil {
		t.Fatal(err)
	}
	if got := rc.Config.Modules.Cloudflared.PrivateNetwork.Interface; got != "vlan7" {
		t.Errorf("VLAN interface = %q, want vlan7", got)
	}
	if got := rc.Config.Users.HomeBase; got != "/srv/h" {
		t.Errorf("home base = %q, want /srv/h", got)
	}
}

// #36 AC2, AC3: status after a config load error reads the user and GPU
// databases only under a home base it was given.
func TestStatus_ConfigLoadErrorHomeBase(t *testing.T) {
	noHostCommands(t)
	t.Setenv("ROOTFILES_HOME_BASE", "")
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, ".rootfiles"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(base, ".rootfiles", "users.json"), `{"users":[{"name":"alice"}]}`)
	status := func(t *testing.T, args ...string) statusReport {
		t.Helper()
		var out bytes.Buffer
		root := NewRootCmd("test", "abc")
		root.SetOut(&out)
		root.SetErr(io.Discard)
		root.SetArgs(append([]string{"status", "-o", "json", "--profile", "nope"}, args...))
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		var r statusReport
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := status(t); r.ConfigErr == "" || r.Users.HomeBase != "" || r.Users.Managed != 0 || r.GPU != nil {
		t.Errorf("no home base: got config_error=%q users=%+v gpu=%v", r.ConfigErr, r.Users, r.GPU)
	}
	if r := status(t, "--home-base", base); r.Users.HomeBase != base || r.Users.Managed != 1 {
		t.Errorf("--home-base: got users=%+v", r.Users)
	}
	// A relative home base is what failed validation; it is not read either.
	chdir(t, filepath.Dir(base))
	if r := status(t, "--home-base", filepath.Base(base)); r.Users.HomeBase != "" || r.Users.Managed != 0 {
		t.Errorf("relative --home-base: got users=%+v", r.Users)
	}
}

// Without a usable record, or a usable kept copy after a config apply,
// subcommands fall back to minimal plus detection, as before #32, never to a
// suggested profile such as dgx that pins another home base and network.
func TestResolveRunTarget_FallbackAfterConfigApply(t *testing.T) {
	t.Setenv("ROOTFILES_STATE_DIR", t.TempDir())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sub, _, err := NewRootCmd("test", "abc").Find([]string{"user", "add"})
	if err != nil {
		t.Fatal(err)
	}
	if p, c := resolveRunTarget(sub, logger); p != "minimal" || c != "" {
		t.Errorf("never applied: got %q %q, want minimal", p, c)
	}
	if err := state.Record(state.Run{ConfigPath: "/etc/rootfiles/site.yaml", Success: true}); err != nil {
		t.Fatal(err)
	}
	if p, c := resolveRunTarget(sub, logger); p != "minimal" || c != "" {
		t.Errorf("config apply without a kept copy: got %q %q, want minimal", p, c)
	}
}

// Any rollback drops the kept copy.
func TestForgetRolledBackRun(t *testing.T) {
	t.Setenv("ROOTFILES_STATE_DIR", t.TempDir())
	if err := state.SaveAppliedConfig([]byte("users: {}\n")); err != nil {
		t.Fatal(err)
	}
	if err := forgetRolledBackRun(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(state.AppliedConfigPath()); !os.IsNotExist(err) {
		t.Errorf("rollback kept the copy: %v", err)
	}
}

// A non-root user cannot read the root-only copy: read-only commands fall
// back quietly, but a mutating one (a dry run, since it is not root) warns
// that its preview is not the applied config.
func TestResolveRunTarget_WarnsMutatingPreview(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 file")
	}
	t.Setenv("ROOTFILES_STATE_DIR", t.TempDir())
	cfg := &config.Config{}
	print, _ := cfg.Fingerprint()
	if err := state.Record(state.Run{ConfigPath: "/etc/rootfiles/site.yaml", ConfigSHA256: print, Success: true}); err != nil {
		t.Fatal(err)
	}
	if err := saveAppliedConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state.AppliedConfigPath(), 0o000); err != nil {
		t.Fatal(err)
	}
	root := NewRootCmd("test", "abc")
	for _, tc := range []struct {
		path []string
		warn bool
	}{{[]string{"user", "add"}, true}, {[]string{"user", "list"}, false}} {
		sub, _, err := root.Find(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		var log bytes.Buffer
		resolveRunTarget(sub, slog.New(slog.NewTextHandler(&log, nil)))
		if got := strings.Contains(log.String(), "not reusing"); got != tc.warn {
			t.Errorf("%v: warned = %v, want %v (%q)", tc.path, got, tc.warn, log.String())
		}
	}
}
