package module

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/exec"
)

func TestDryRunContextUsesPrivateHomeAndQueryFixtures(t *testing.T) {
	calls := fakeBin(t, "rootfiles-fixture-query")
	rc := newDryRunRC(t)
	if rc.Runner.QueryGuard == nil {
		t.Fatal("dry-run context has no query guard")
	}
	if rc.Config.Users.HomeBase != os.Getenv("HOME") {
		t.Fatalf("home base %q differs from private HOME %q", rc.Config.Users.HomeBase, os.Getenv("HOME"))
	}
	relative, err := filepath.Rel(moduleTestRoot, rc.Config.Users.HomeBase)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatalf("home base %q is outside private test root: %v", rc.Config.Users.HomeBase, err)
	}
	if _, err := rc.Runner.Query(context.Background(), "rootfiles-fixture-query", "fixture-argument"); err != nil {
		t.Fatalf("private shell fixture rejected: %v", err)
	}
	data, err := os.ReadFile(calls)
	if err != nil || !strings.Contains(string(data), "rootfiles-fixture-query fixture-argument") {
		t.Fatalf("fixture did not execute: %q, %v", data, err)
	}
}

func TestFixtureQueryGuardRejectsAmbientBinaryAndSymlink(t *testing.T) {
	// A sibling of TestMain's private root stands in for a host PATH entry.
	ambient, err := os.MkdirTemp(filepath.Dir(moduleTestRoot), "rootfiles-ambient-query-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(ambient) })
	marker := filepath.Join(ambient, "executed")
	binary := filepath.Join(ambient, "rootfiles-ambient-query")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf called > \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "query-link")
	if err := os.Symlink(binary, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", ambient)
	// Use the same validator without newDryRunRC's t.Error wrapper: these
	// refusals are expected, rather than accidental host calls by a module.
	runner := exec.NewRunner(true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	runner.QueryGuard = fixtureQueryGuard
	for _, command := range []string{"rootfiles-ambient-query", binary, link} {
		if _, err := runner.Query(context.Background(), command, marker); err == nil || !strings.Contains(err.Error(), "stub it with fakeBin") {
			t.Errorf("Query(%q) error = %v, want fixture refusal", command, err)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("ambient query executed through %q: %v", command, err)
		}
	}
}

func TestModuleDefaultReadPathsArePrivate(t *testing.T) {
	for _, candidate := range []string{
		passwdPath, groupPath, shadowPath, sshdConfigPath, sshdConfigDir, useraddDefaultsFile,
		defaultLocalePath, rockyLocalePath, timezonePath, localtimePath, zoneinfoDir,
		hostnamePath, hostsPath, procSwapsPath, fstabPath, sysctlPath, journaldConfPath,
		autoUpgradesPath, unattendedPolicyPath, fail2banJailPath, dockerDaemonJSONPath,
		rockyAutomaticConfigPath, rebootRequiredPath, rebootRequiredPkgsPath,
		nodeExporterDefaults, dcgmUnitPath, dcgmCollectorsPath, dockerWantsDir, gpuDeviceDir, gpuProfileDir, gpuSystemdDir,
	} {
		relative, err := filepath.Rel(moduleTestRoot, candidate)
		if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Errorf("default read path %q is outside private test root: %v", candidate, err)
		}
	}
}

func TestFixtureAccountLookupsUsePrivateRecords(t *testing.T) {
	for name, uid := range map[string]string{"rootfiles-test-user": "1000", "root": "0"} {
		account, err := lookupAccount(name)
		if err != nil || account.Uid != uid {
			t.Fatalf("lookupAccount(%q) = %+v, %v", name, account, err)
		}
	}
	var unknownUser user.UnknownUserError
	if _, err := lookupAccount("rootfiles-no-such-account"); !errors.As(err, &unknownUser) {
		t.Fatalf("unknown account error = %v", err)
	}
	fakePasswd(t)
	account, err := lookupAccount("alice")
	if err != nil || account.Uid != "1000" || !strings.HasPrefix(account.HomeDir, moduleTestRoot+string(filepath.Separator)) {
		t.Fatalf("private passwd account = %+v, %v", account, err)
	}
	fleetAccount, err := lookupFleetSudoUser("alice")
	if err != nil || *fleetAccount != *account {
		t.Fatalf("fleet lookup did not use private passwd record: %+v, %v", fleetAccount, err)
	}

	oldGroup := groupPath
	groupPath = filepath.Join(t.TempDir(), "group")
	t.Cleanup(func() { groupPath = oldGroup })
	if err := os.WriteFile(groupPath, []byte("fixture-group:x:2345:alice\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	group, err := lookupAccountGroup("fixture-group")
	if err != nil || group.Gid != "2345" {
		t.Fatalf("private group = %+v, %v", group, err)
	}
	var unknownGroup user.UnknownGroupError
	if _, err := lookupAccountGroup("rootfiles-no-such-group"); !errors.As(err, &unknownGroup) {
		t.Fatalf("unknown group error = %v", err)
	}
}
