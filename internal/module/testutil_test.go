package module

import (
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/exec"
)

var moduleTestRoot string

// TestMain lets the test user stand in for root as the owner of custom home
// bases, which tests create under t.TempDir(): TMPDIR moves to a private
// directory where the ownership walk starts, and umask 022 keeps the
// directories tests create from being group-writable.
func TestMain(m *testing.M) {
	sshdSystemBinary = "" // tests provide sshd on their fixture PATH
	syscall.Umask(0o022)
	dir, err := os.MkdirTemp("", "rootfiles-module-test")
	if err != nil {
		panic(err)
	}
	os.Setenv("TMPDIR", dir)
	moduleTestRoot = dir
	// Defaults are absent fixture files, not live host paths. Set them once
	// here so tests that override a path before newDryRunRC retain their fixture.
	setModuleFixturePaths(dir)
	lookupAccount, lookupFleetSudoUser = fixtureLookupAccount, fixtureLookupAccount
	lookupAccountGroup = fixtureLookupGroup
	homeBaseRoot, homeBaseOwner = dir, uint32(os.Getuid())
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// newDryRunRC returns a RunContext with dry-run Runner and APT. Caller may
// mutate rc.Config to set module-specific toggles. Used by per-module tests
// that exercise Check/Apply without touching the real filesystem or apt-get.
func newDryRunRC(t *testing.T) *RunContext {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	runner := guardedTestRunner(t, true, logger)
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(filepath.Join(home, ".rootfiles"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := &config.Config{}
	cfg.Users.HomeBase = home
	return &RunContext{
		Config: cfg,
		Runner: runner,
		APT:    exec.NewAPT(runner),
		DryRun: true,
	}
}

// guardedTestRunner fails the test on accidental host queries, including
// queries whose errors the module treats as an absent package or service.
func guardedTestRunner(t *testing.T, dryRun bool, logger *slog.Logger) *exec.Runner {
	t.Helper()
	runner := exec.NewRunner(dryRun, logger)
	runner.QueryGuard = func(resolvedPath string) error {
		err := fixtureQueryGuard(resolvedPath)
		if err != nil {
			t.Error(err)
		}
		return err
	}
	return runner
}

// fixtureQueryGuard accepts private test executables, including script fixtures
// whose interpreter is /bin/sh. Following symlinks prevents a fixture symlink
// from silently granting permission to execute the host's systemctl or sshd.
func fixtureQueryGuard(resolvedPath string) error {
	root, err := filepath.EvalSymlinks(moduleTestRoot)
	if err != nil {
		return fmt.Errorf("resolve private test root: %w", err)
	}
	executable, err := filepath.EvalSymlinks(resolvedPath)
	if err != nil {
		return fmt.Errorf("resolve query fixture %q: %w", resolvedPath, err)
	}
	relative, err := filepath.Rel(root, executable)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("query executable %q is outside the private test root; stub it with fakeBin", resolvedPath)
	}
	return nil
}

func setModuleFixturePaths(root string) {
	for _, target := range []*string{
		&passwdPath, &groupPath, &shadowPath, &sshdConfigPath, &sshdConfigDir,
		&sshdDropInPath, &sshdPrivilegeSeparationDir, &useraddDefaultsFile,
		&sudoersDir, &fleetSudoBinaryPath, &defaultLocalePath, &rockyLocalePath,
		&timezonePath, &localtimePath, &zoneinfoDir, &hostnamePath, &hostsPath,
		&procSwapsPath, &swapfilePath, &fstabPath, &sysctlPath, &journaldConfPath,
		&autoUpgradesPath, &unattendedPolicyPath, &fail2banJailPath,
		&rockyAutomaticConfigPath, &rockyPlatformPython, &dockerDaemonJSONPath,
		&dockerComposePath, &dockerWrapperPath, &gpuDeviceDir, &gpuProfileDir, &gpuSystemdDir, &procMountsPath, &cloudflaredBinary, &cloudflaredUnitPath,
		&cloudflaredEnvPath, &vlanNetdevPath, &vlanNetworkPath,
		&rebootRequiredPath, &rebootRequiredPkgsPath, &nodeExporterTextfileDir,
		&exporterGuardHelperPath, &exporterGuardConfPath,
		&nodeExporterDefaults, &dcgmUnitPath, &dcgmCollectorsPath, &dockerWantsDir,
		&monitoringHubConfigDir, &monitoringHubCompose, &monitoringHubUnitPath,
		&monitoringHubPassword, &monitoringHubAlertRules, &monitoringHubPromConfig,
		&monitoringHubAlertConfig, &monitoringHubDatasource,
		&tenantUnitDir, &tenantGuardPath, &piperLibDir, &piperConfDir,
	} {
		*target = filepath.Join(root, "host", strings.TrimPrefix(*target, "/"))
	}
	for _, targets := range [][]string{aptSourcesPaths, legacyUnattendedPaths, composePluginPaths, exporterGuardSetupFiles} {
		for i := range targets {
			targets[i] = filepath.Join(root, "host", strings.TrimPrefix(targets[i], "/"))
		}
	}
}

// fixtureAccountRecords reads only private passwd/group fixtures, including
// records installed by fakePasswd. It never falls back to NSS or /etc.
func fixtureAccountRecords(file string) [][]string {
	root, err := filepath.EvalSymlinks(moduleTestRoot)
	if err != nil {
		return nil
	}
	resolved, err := filepath.EvalSymlinks(file)
	if err != nil {
		return nil
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil
	}
	var records [][]string
	for _, line := range strings.Split(string(data), "\n") {
		records = append(records, strings.Split(line, ":"))
	}
	return records
}

func fixtureLookupAccount(name string) (*user.User, error) {
	for _, fields := range fixtureAccountRecords(passwdPath) {
		if len(fields) == 7 && fields[0] == name {
			return &user.User{Username: name, Uid: fields[2], Gid: fields[3], Name: fields[4], HomeDir: fields[5]}, nil
		}
	}
	switch name {
	case "rootfiles-test-user":
		return &user.User{Username: name, Uid: "1000", Gid: "1000", HomeDir: filepath.Join(moduleTestRoot, "accounts", name)}, nil
	case "root":
		return &user.User{Username: name, Uid: "0", Gid: "0", HomeDir: filepath.Join(moduleTestRoot, "accounts", name)}, nil
	default:
		return nil, user.UnknownUserError(name)
	}
}

func fixtureLookupGroup(name string) (*user.Group, error) {
	for _, fields := range fixtureAccountRecords(groupPath) {
		if len(fields) == 4 && fields[0] == name {
			return &user.Group{Name: name, Gid: fields[2]}, nil
		}
	}
	// These are the primary and documented administrator/container groups
	// used by the fixed test accounts. Other names need an explicit fixture.
	groups := map[string]string{"root": "0", "rootfiles-test-user": "1000", "sudo": "27", "wheel": "10", "docker": "999"}
	if gid, ok := groups[name]; ok {
		return &user.Group{Name: name, Gid: gid}, nil
	}
	return nil, user.UnknownGroupError(name)
}
