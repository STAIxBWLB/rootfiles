package module

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/exec"
)

// This is a real profile/module-phase contract, not a full CLI acceptance
// test: it loads each embedded profile, runs its actual Check methods through
// RunAll, and asserts dry-run outcomes and zero managed filesystem mutations.
// Only filesystem destinations are redirected; no module toggle is changed.
// The synthetic host has missing packages, a current locale, and an active
// firewalld, so UFW-enabled profiles also exercise the blocked-change path.
//
// Root captures before with original production module sources in a Go overlay,
// then after with the routed implementation. Neither capture executes Apply.
// Before captures use 769b013 plus only the output-field seam; after captures use the routed code.
func TestProfileModuleDryRunOutputContract(t *testing.T) {
	t.Setenv("LC_ALL", "C.UTF-8")
	t.Setenv("NO_COLOR", "1")
	t.Setenv("COLUMNS", "")
	for _, profile := range config.AvailableProfiles() {
		t.Run(profile, func(t *testing.T) {
			rc, fixtureRoot := outputProfileFixture(t, profile)
			modules := NewRegistry().Resolve(rc.Config, nil)
			var wantNames []string
			for _, name := range config.ModuleNames() {
				if rc.Config.IsModuleEnabled(name) {
					wantNames = append(wantNames, name)
				}
			}
			if err := config.ValidateCapabilities(rc.Config, rc.Config.System, wantNames); err != nil {
				t.Fatal(err)
			}
			before := outputProfileSnapshot(t, fixtureRoot)
			capture, err := os.Create(filepath.Join(fixtureRoot, "captured-output"))
			if err != nil {
				t.Fatal(err)
			}
			oldStdout := os.Stdout
			os.Stdout, rc.Out = capture, capture
			// Restore even if a future module panics or the test aborts.
			t.Cleanup(func() { os.Stdout = oldStdout; _ = capture.Close() })
			outcomes, runErr := RunAll(context.Background(), modules, rc)
			os.Stdout = oldStdout
			if err := capture.Close(); err != nil {
				t.Fatal(err)
			}
			var gotNames []string
			pending, satisfied, failed := 0, 0, 0
			for _, outcome := range outcomes {
				gotNames = append(gotNames, outcome.Name)
				switch outcome.Status {
				case "pending":
					pending++
				case "satisfied":
					satisfied++
				case "failed":
					failed++
					if outcome.Name != "network" || !strings.Contains(outcome.Error, "firewalld") {
						t.Fatalf("unexpected preview failure: %+v", outcome)
					}
				default:
					t.Fatalf("dry-run executed or skipped a module: %+v", outcome)
				}
			}
			if !reflect.DeepEqual(gotNames, wantNames) || pending == 0 || satisfied == 0 {
				t.Fatalf("profile coverage names=%v want=%v pending=%d satisfied=%d", gotNames, wantNames, pending, satisfied)
			}
			wantBlocked := rc.Config.Modules.Network.Enabled && rc.Config.Modules.Network.UFW
			if (runErr != nil) != wantBlocked || failed != map[bool]int{false: 0, true: 1}[wantBlocked] {
				t.Fatalf("blocked preview result: failed=%d err=%v wantBlocked=%t", failed, runErr, wantBlocked)
			}
			if after := outputProfileSnapshot(t, fixtureRoot); !reflect.DeepEqual(before, after) || rc.Runner.Backup.Used() {
				t.Fatalf("dry-run changed managed paths: before=%v after=%v backup=%t", before, after, rc.Runner.Backup.Used())
			}
			data, err := os.ReadFile(capture.Name())
			if err != nil {
				t.Fatal(err)
			}
			got := strings.ReplaceAll(string(data), fixtureRoot, "<fixture>")
			stage := "after"

			golden := filepath.Join("testdata", "output-t4", profile+"."+stage+".golden")
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read reviewed output golden: %v; restore the reviewed after capture from version control", err)
			}
			if got != string(want) {
				t.Errorf("dry-run output changed\n--- reviewed\n%s\n--- current\n%s", want, got)
			}
		})
	}
}

func outputProfileFixture(t *testing.T, profile string) (*RunContext, string) {
	t.Helper()
	root := t.TempDir()
	// Keep every read destination independent of other tests and the host.
	paths := []*string{&passwdPath, &shadowPath, &sshdConfigPath, &sshdConfigDir, &sshdDropInPath, &sshdPrivilegeSeparationDir, &useraddDefaultsFile, &sudoersDir,
		&defaultLocalePath, &rockyLocalePath, &timezonePath, &localtimePath, &zoneinfoDir, &hostnamePath, &hostsPath, &procSwapsPath, &swapfilePath, &fstabPath, &sysctlPath, &journaldConfPath,
		&autoUpgradesPath, &unattendedPolicyPath, &fail2banJailPath, &rockyAutomaticConfigPath, &rockyPlatformPython, &dockerDaemonJSONPath, &dockerComposePath, &dockerWrapperPath,
		&gpuDeviceDir, &gpuProfileDir, &gpuSystemdDir, &cloudflaredBinary, &cloudflaredUnitPath, &cloudflaredEnvPath, &vlanNetdevPath, &vlanNetworkPath}
	for i, destination := range paths {
		old := *destination
		*destination = filepath.Join(root, "host", fmt.Sprintf("%02d-%s", i, filepath.Base(old)))
		t.Cleanup(func() { *destination = old })
	}
	oldPlugins, oldLegacy := composePluginPaths, legacyUnattendedPaths
	composePluginPaths = []string{filepath.Join(root, "host", "compose-plugin")}
	legacyUnattendedPaths = []string{filepath.Join(root, "host", "legacy-unattended")}
	t.Cleanup(func() { composePluginPaths, legacyUnattendedPaths = oldPlugins, oldLegacy })
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(filepath.Join(home, ".rootfiles"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ROOTFILES_HOME_BASE", "ROOTFILES_TIMEZONE", "ROOTFILES_TUNNEL_TOKEN", "ROOTFILES_VLAN_ADDRESS", "ROOTFILES_VLAN_INTERFACE", "ROOTFILES_DOCKER_ROOT", "ROOTFILES_DATA_DIR"} {
		t.Setenv(key, "")
	}
	t.Setenv("HOME", home)
	sys := &config.SystemInfo{OS: "ubuntu", Version: "24.04", Codename: "noble", Arch: "amd64"}
	if profile == "rocky" {
		sys.OS, sys.Version = "rocky", "9.5"
	}
	cfg, err := config.LoadWithHomeBase(profile, "", sys, home)
	if err != nil {
		t.Fatal(err)
	}
	// These profile values are filesystem targets, not behavior switches.
	if cfg.Modules.Docker.StorageDir != "" {
		cfg.Modules.Docker.StorageDir = filepath.Join(root, "site", "docker")
	}
	if cfg.Modules.Storage.DataDir != "" {
		cfg.Modules.Storage.DataDir = filepath.Join(root, "site", "data")
	}
	if len(cfg.Modules.Storage.Symlinks) != 0 {
		cfg.Modules.Storage.Symlinks = map[string]string{filepath.Join(root, "site", "data-link"): filepath.Join(root, "site", "data")}
	}
	write := func(file, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(defaultLocalePath, "LANG="+cfg.Locale+"\n")
	write(rockyLocalePath, "LANG="+cfg.Locale+"\n")
	write(timezonePath, cfg.Timezone+"\n")
	// Keep pre-existing Docker map iteration out of the output contract: only data-root drifts.
	write(dockerDaemonJSONPath, `{"log-driver":"json-file"}`)
	write(sshdConfigPath, "Include "+sshdConfigDir+"/*.conf\nPort 22\n")
	write(useraddDefaultsFile, "HOME="+home+"\n")
	write(passwdPath, "root:x:0:0::"+home+":/bin/bash\n")
	write(shadowPath, "root:!:20000:0:99999:7:::\n")
	bin := filepath.Join(root, "commands")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"dpkg-query": "exit 0", "rpm": "exit 0", "locale-gen": "exit 0", "docker": "exit 0", "getent": "exit 0",
		"localectl": "printf 'en_US.UTF-8\\n'", "timedatectl": "printf 'yes\\n'",
		"python3": "printf 'rootfiles-updateinfo-ok:appstream,baseos\\n'",
		"sshd":    "printf 'port 22\\npubkeyauthentication yes\\npasswordauthentication yes\\n'",
		"ufw":     "printf 'Status: inactive\\n'", "firewall-cmd": "printf 'running\\n'",
		"systemctl": "case $1 in is-active) echo inactive; exit 3;; is-enabled) echo disabled; exit 1;; list-unit-files) printf '%s enabled\\n' \"$2\";; esac",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	runner := guardedTestRunner(t, true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	runner.Backup = exec.NewBackup(filepath.Join(root, "backups"), "profile dry-run fixture")
	pm, err := exec.NewPackageManager(runner, sys)
	if err != nil {
		t.Fatal(err)
	}
	return &RunContext{Config: cfg, Runner: runner, APT: pm, DryRun: true, Yes: true}, root
}

func outputProfileSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	for _, managed := range []string{"host", "home", "site", "backups"} {
		dir := filepath.Join(root, managed)
		if _, err := os.Lstat(dir); os.IsNotExist(err) {
			continue
		}
		if err := filepath.WalkDir(dir, func(file string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, file)
			if err != nil {
				return err
			}
			value := info.Mode().String()
			if !entry.IsDir() {
				data, err := os.ReadFile(file)
				if err != nil {
					return err
				}
				value += fmt.Sprintf(" %x", sha256.Sum256(data))
			}
			snapshot[relative] = value
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	return snapshot
}
