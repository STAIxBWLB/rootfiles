package module

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/exec"
)

func TestAddRockySSHDIncludeAtTopBeforeMatch(t *testing.T) {
	main := []byte("# Rocky sshd config\nPort 22\nMatch User deploy\nAllowTcpForwarding yes\n")
	wantPrefix := "Include /etc/ssh/sshd_config.d/*.conf\n"
	got, changed, err := addRockySSHDInclude(main)
	if err != nil || !changed {
		t.Fatalf("addRockySSHDInclude changed=%t err=%v", changed, err)
	}
	if !strings.HasPrefix(string(got), wantPrefix) || !strings.HasSuffix(string(got), string(main)) {
		t.Fatalf("include not inserted at top without changing existing content: %q", got)
	}
	got, changed, err = addRockySSHDInclude([]byte(wantPrefix + string(main)))
	if err != nil || changed || string(got) != wantPrefix+string(main) {
		t.Fatalf("existing global include changed=%t err=%v", changed, err)
	}
	if _, _, err := addRockySSHDInclude([]byte("Match User deploy\nInclude /etc/ssh/sshd_config.d/*.conf\n")); err == nil {
		t.Fatal("include scoped to Match must be rejected")
	}
}

func rockySSHTestPaths(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	oldMain, oldDir, oldDrop, oldRun := sshdConfigPath, sshdConfigDir, sshdDropInPath, sshdPrivilegeSeparationDir
	sshdConfigPath = filepath.Join(dir, "sshd_config")
	sshdConfigDir = filepath.Join(dir, "sshd_config.d")
	sshdDropInPath = filepath.Join(sshdConfigDir, "00-rootfiles.conf")
	sshdPrivilegeSeparationDir = filepath.Join(dir, "run", "sshd")
	t.Cleanup(func() {
		sshdConfigPath, sshdConfigDir, sshdDropInPath, sshdPrivilegeSeparationDir = oldMain, oldDir, oldDrop, oldRun
	})
	return dir
}

func TestRockySSHStock89CheckPendingThenApplyConverges(t *testing.T) {
	dir := rockySSHTestPaths(t)
	mainBefore := []byte("# Rocky stock sshd configuration\nPort 22\n")
	if err := os.WriteFile(sshdConfigPath, mainBefore, 0640); err != nil {
		t.Fatal(err)
	}
	fakeCommandOutput(t, map[string]string{
		"sshd":      "permitrootlogin no\n",
		"systemctl": "inactive\n",
	})
	rc := newRealRC(t)
	rc.Runner.Backup = exec.NewBackup(filepath.Join(dir, "backups"), "stock Rocky SSH convergence")
	rc.Config.System = &config.SystemInfo{OS: "rocky", Version: "8.9"}
	rc.Config.SSH = config.SSHConfig{DisableRootLogin: true}
	module := NewSSHModule()
	check, err := module.Check(context.Background(), rc)
	if err != nil {
		t.Fatalf("first Check: %v", err)
	}
	if check.Satisfied || len(check.Changes) < 2 || !strings.Contains(check.Changes[0].Description, "Enable Rocky sshd_config.d") {
		t.Fatalf("stock 8.9 Check did not report include and drop-in: %+v", check)
	}
	mainAfterCheck, err := os.ReadFile(sshdConfigPath)
	if err != nil || string(mainAfterCheck) != string(mainBefore) {
		t.Fatalf("Check mutated stock sshd config: %q, %v", mainAfterCheck, err)
	}
	result, err := module.Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Changed {
		t.Fatalf("Apply result = %+v, want changed", result)
	}
	mainAfter, err := os.ReadFile(sshdConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	wantMain := append([]byte("Include "+sshdConfigDir+"/*.conf\n"), mainBefore...)
	if string(mainAfter) != string(wantMain) {
		t.Fatalf("main sshd config = %q, want %q", mainAfter, wantMain)
	}
	info, err := os.Stat(sshdConfigPath)
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("main config mode = %v, %v; want 0640", info, err)
	}
	drop, err := os.ReadFile(sshdDropInPath)
	if err != nil || string(drop) != module.buildConfig(rc.Config.SSH) {
		t.Fatalf("managed sshd drop-in = %q, %v", drop, err)
	}
	check, err = module.Check(context.Background(), rc)
	if err != nil || !check.Satisfied {
		t.Fatalf("second Check = %+v, %v; want satisfied", check, err)
	}
	if !rc.Runner.Backup.Used() {
		t.Fatal("main config and new drop-in were not recorded in the runner backup")
	}
	manifest, err := exec.LoadBackup(rc.Runner.Backup.Root, rc.Runner.Backup.ID)
	if err != nil {
		t.Fatalf("load backup manifest: %v", err)
	}
	var mainSaved, dropRecorded bool
	for _, entry := range manifest.Entries {
		if entry.Path == sshdConfigPath && entry.Existed && entry.Mode.Perm() == 0640 {
			mainSaved = true
		}
		if entry.Path == sshdDropInPath && !entry.Existed {
			dropRecorded = true
		}
	}
	if !mainSaved || !dropRecorded {
		t.Fatalf("backup manifest missing prior main mode or newly created drop-in: %+v", manifest.Entries)
	}
}

func TestRockySSHInvalidCandidateRestoresMainAndDropIn(t *testing.T) {
	rockySSHTestPaths(t)
	mainBefore := []byte("# Rocky stock sshd configuration\nPort 22\n")
	dropBefore := []byte("# existing managed file\n")
	if err := os.WriteFile(sshdConfigPath, mainBefore, 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sshdConfigDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sshdDropInPath, dropBefore, 0600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	badSSHD := "#!/bin/sh\nif [ \"$1\" = '-t' ]; then echo invalid config >&2; exit 1; fi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "sshd"), []byte(badSSHD), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	rc := newRealRC(t)
	rc.Config.System = &config.SystemInfo{OS: "rocky", Version: "8.9"}
	rc.Config.SSH = config.SSHConfig{DisableRootLogin: true}
	if _, err := NewSSHModule().Apply(context.Background(), rc); err == nil {
		t.Fatal("Apply accepted invalid sshd candidate")
	}
	mainAfter, err := os.ReadFile(sshdConfigPath)
	if err != nil || string(mainAfter) != string(mainBefore) {
		t.Fatalf("main config not restored: %q, %v", mainAfter, err)
	}
	mainInfo, err := os.Stat(sshdConfigPath)
	if err != nil || mainInfo.Mode().Perm() != 0640 {
		t.Fatalf("main config mode not restored: %v, %v", mainInfo, err)
	}
	dropAfter, err := os.ReadFile(sshdDropInPath)
	if err != nil || string(dropAfter) != string(dropBefore) {
		t.Fatalf("drop-in not restored: %q, %v", dropAfter, err)
	}
	dropInfo, err := os.Stat(sshdDropInPath)
	if err != nil || dropInfo.Mode().Perm() != 0600 {
		t.Fatalf("drop-in mode not restored: %v, %v", dropInfo, err)
	}
}

func writeRockyFirewallFake(t *testing.T, dir, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "firewall-cmd"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestRockyFirewallAllowsSSHFailClosedOnUnknownStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script string
	}{
		{name: "state command error", script: "#!/bin/sh\nexit 1\n"},
		{name: "unrecognized state", script: "#!/bin/sh\necho maybe\n"},
		{name: "inactive text with unexpected error", script: "#!/bin/sh\necho 'not running'\nexit 2\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeRockyFirewallFake(t, dir, tc.script)
			allowed, err := rockyFirewallAllowsSSH(context.Background(), newDryRunRC(t), 2222)
			if err == nil || allowed {
				t.Fatalf("unknown firewall state returned allowed=%t err=%v", allowed, err)
			}
		})
	}
}

func TestRockyFirewallAllowsSSHKnownInactiveAndExistingAllowance(t *testing.T) {
	t.Run("known inactive", func(t *testing.T) {
		dir := t.TempDir()
		writeRockyFirewallFake(t, dir, "#!/bin/sh\necho 'not running'\nexit 252\n")
		allowed, err := rockyFirewallAllowsSSH(context.Background(), newDryRunRC(t), 2222)
		if err != nil || !allowed {
			t.Fatalf("known inactive firewalld returned allowed=%t err=%v", allowed, err)
		}
	})
	t.Run("known inactive on stderr", func(t *testing.T) {
		dir := t.TempDir()
		writeRockyFirewallFake(t, dir, "#!/bin/sh\necho 'not running' >&2\nexit 252\n")
		allowed, err := rockyFirewallAllowsSSH(context.Background(), newDryRunRC(t), 2222)
		if err != nil || !allowed {
			t.Fatalf("known inactive stderr returned allowed=%t err=%v", allowed, err)
		}
	})
	t.Run("contradictory inactive output", func(t *testing.T) {
		dir := t.TempDir()
		writeRockyFirewallFake(t, dir, "#!/bin/sh\necho running\necho 'not running' >&2\nexit 252\n")
		allowed, err := rockyFirewallAllowsSSH(context.Background(), newDryRunRC(t), 2222)
		if err == nil || allowed {
			t.Fatalf("contradictory firewall state accepted: allowed=%t err=%v", allowed, err)
		}
	})
	t.Run("active zone allows requested port", func(t *testing.T) {
		dir := t.TempDir()
		script := "#!/bin/sh\ncase \"$1:$2:$3\" in\n--state::) echo running;;\n--get-active-zones::) echo 'public (default, active)';;\n--zone:public:--query-port) echo yes;;\nesac\n"
		writeRockyFirewallFake(t, dir, script)
		allowed, err := rockyFirewallAllowsSSH(context.Background(), newDryRunRC(t), 2222)
		if err != nil || !allowed {
			t.Fatalf("existing firewalld allowance returned allowed=%t err=%v", allowed, err)
		}
	})
	t.Run("every active zone allows requested port", func(t *testing.T) {
		dir := t.TempDir()
		script := "#!/bin/sh\ncase \"$1:$2:$3\" in\n--state::) echo running;;\n--get-active-zones::) printf 'public (active)\\nmgmt (active)\\n';;\n--zone:public:--query-port|--zone:mgmt:--query-port) echo yes;;\nesac\n"
		writeRockyFirewallFake(t, dir, script)
		allowed, err := rockyFirewallAllowsSSH(context.Background(), newDryRunRC(t), 2222)
		if err != nil || !allowed {
			t.Fatalf("all active zones allowed returned allowed=%t err=%v", allowed, err)
		}
	})
	t.Run("one active zone denies requested port", func(t *testing.T) {
		dir := t.TempDir()
		script := "#!/bin/sh\ncase \"$1:$2:$3\" in\n--state::) echo running;;\n--get-active-zones::) printf 'public (active)\\nmgmt (active)\\n';;\n--zone:public:--query-port) echo yes;;\n--zone:mgmt:--query-port) echo no; exit 1;;\nesac\n"
		writeRockyFirewallFake(t, dir, script)
		allowed, err := rockyFirewallAllowsSSH(context.Background(), newDryRunRC(t), 2222)
		if err != nil || allowed {
			t.Fatalf("one denied active zone returned allowed=%t err=%v", allowed, err)
		}
	})
	t.Run("port 22 must be allowed in every active zone", func(t *testing.T) {
		dir := t.TempDir()
		script := "#!/bin/sh\ncase \"$1:$2:$3\" in\n--state::) echo running;;\n--get-active-zones::) printf 'public (active)\\nmgmt (active)\\n';;\n--zone:public:--query-port) echo yes;;\n--zone:mgmt:--query-port) echo no; exit 1;;\n--zone:mgmt:--query-service) echo no; exit 1;;\nesac\n"
		writeRockyFirewallFake(t, dir, script)
		allowed, err := rockyFirewallAllowsSSH(context.Background(), newDryRunRC(t), 22)
		if err != nil || allowed {
			t.Fatalf("port 22 allowed in only one zone returned allowed=%t err=%v", allowed, err)
		}
	})
	t.Run("port 22 SSH service in every active zone", func(t *testing.T) {
		dir := t.TempDir()
		script := "#!/bin/sh\ncase \"$1:$2:$3\" in\n--state::) echo running;;\n--get-active-zones::) printf 'public (active)\\nmgmt (active)\\n';;\n--zone:public:--query-port|--zone:mgmt:--query-port) echo no; exit 1;;\n--zone:public:--query-service|--zone:mgmt:--query-service) echo yes;;\nesac\n"
		writeRockyFirewallFake(t, dir, script)
		allowed, err := rockyFirewallAllowsSSH(context.Background(), newDryRunRC(t), 22)
		if err != nil || !allowed {
			t.Fatalf("SSH service in every zone returned allowed=%t err=%v", allowed, err)
		}
	})
}

func TestRockyFirewallAllowsSSHRejectsUntrustedBooleanOutput(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$1:$2:$3\" in\n--state::) echo running;;\n--get-active-zones::) echo 'public (default, active)';;\n--zone:public:--query-port) echo no; exit 2;;\nesac\n"
	writeRockyFirewallFake(t, dir, script)
	allowed, err := rockyFirewallAllowsSSH(context.Background(), newDryRunRC(t), 2222)
	if err == nil || allowed {
		t.Fatalf("no output with unexpected exit status returned allowed=%t err=%v", allowed, err)
	}
}

func TestRockyFirewallRejectsFirewalldRichRules(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$1:$2:$3\" in\n--state::) echo running;;\n--get-active-zones::) echo 'public (default, active)';;\n--zone:public:--list-rich-rules) echo 'rule family=ipv4 source address=10.0.0.0/8 port port=2222 protocol=tcp drop';;\n--zone:public:--query-port) echo yes;;\nesac\n"
	writeRockyFirewallFake(t, dir, script)
	allowed, err := rockyFirewallAllowsSSH(context.Background(), newDryRunRC(t), 2222)
	if err != nil || allowed {
		t.Fatalf("firewalld rich-rule override returned allowed=%t err=%v", allowed, err)
	}
}

func TestRockyFirewallAllowsSSHFailClosedOnUnknownUFWStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script string
	}{
		{name: "status command error", script: "#!/bin/sh\nexit 1\n"},
		{name: "unrecognized status", script: "#!/bin/sh\necho 'Status: maybe'\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "ufw"), []byte(tc.script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			allowed, err := rockyFirewallAllowsSSH(context.Background(), newDryRunRC(t), 2222)
			if err == nil || allowed {
				t.Fatalf("unknown UFW status returned allowed=%t err=%v", allowed, err)
			}
		})
	}
}

func TestRockyFirewallAllowsSSHExistingUFWAllowance(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf 'Status: active\\nTo Action From\\n2222/tcp ALLOW Anywhere\\n2222/tcp (v6) ALLOW Anywhere (v6)\\n'\n"
	if err := os.WriteFile(filepath.Join(dir, "ufw"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	allowed, err := rockyFirewallAllowsSSH(context.Background(), newDryRunRC(t), 2222)
	if err != nil || !allowed {
		t.Fatalf("existing UFW allowance returned allowed=%t err=%v", allowed, err)
	}
}

func TestRockyFirewallRequiresUnrestrictedUFWAllowance(t *testing.T) {
	t.Run("source limited is not enough", func(t *testing.T) {
		dir := t.TempDir()
		script := "#!/bin/sh\nprintf 'Status: active\\nTo Action From\\n2222/tcp ALLOW 10.0.0.0/8\\n'\n"
		if err := os.WriteFile(filepath.Join(dir, "ufw"), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir)
		allowed, err := rockyFirewallAllowsSSH(context.Background(), newDryRunRC(t), 2222)
		if err != nil || allowed {
			t.Fatalf("source-limited UFW allowance returned allowed=%t err=%v", allowed, err)
		}
	})
	t.Run("deny overrides unrestricted allow", func(t *testing.T) {
		dir := t.TempDir()
		script := "#!/bin/sh\nprintf 'Status: active\\nTo Action From\\n2222/tcp DENY 10.0.0.0/8\\n2222/tcp ALLOW Anywhere\\n2222/tcp (v6) ALLOW Anywhere (v6)\\n'\n"
		if err := os.WriteFile(filepath.Join(dir, "ufw"), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir)
		allowed, err := rockyFirewallAllowsSSH(context.Background(), newDryRunRC(t), 2222)
		if err != nil || allowed {
			t.Fatalf("deny plus unrestricted allow returned allowed=%t err=%v", allowed, err)
		}
	})
	t.Run("unrestricted TCP allowance", func(t *testing.T) {
		dir := t.TempDir()
		script := "#!/bin/sh\nprintf 'Status: active\\nTo Action From\\n2222/tcp ALLOW Anywhere\\n2222/tcp (v6) ALLOW Anywhere (v6)\\n'\n"
		if err := os.WriteFile(filepath.Join(dir, "ufw"), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir)
		allowed, err := rockyFirewallAllowsSSH(context.Background(), newDryRunRC(t), 2222)
		if err != nil || !allowed {
			t.Fatalf("unrestricted UFW allowance returned allowed=%t err=%v", allowed, err)
		}
	})
	for _, tc := range []struct {
		name string
		rows string
	}{
		{name: "IPv4 only", rows: "2222/tcp ALLOW Anywhere\\n"},
		{name: "IPv6 only", rows: "2222/tcp (v6) ALLOW Anywhere (v6)\\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			script := "#!/bin/sh\nprintf 'Status: active\\nTo Action From\\n" + tc.rows + "'\n"
			if err := os.WriteFile(filepath.Join(dir, "ufw"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			allowed, err := rockyFirewallAllowsSSH(context.Background(), newDryRunRC(t), 2222)
			if err != nil || allowed {
				t.Fatalf("family-limited UFW allowance returned allowed=%t err=%v", allowed, err)
			}
		})
	}
}

func TestRockyFirewallRequiresUnrestrictedUFWAllowanceForPort22(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf 'Status: active\\nTo Action From\\n22/tcp ALLOW 10.0.0.0/8\\n22/tcp (v6) ALLOW Anywhere (v6)\\n'\n"
	if err := os.WriteFile(filepath.Join(dir, "ufw"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	allowed, err := rockyFirewallAllowsSSH(context.Background(), newDryRunRC(t), 22)
	if err != nil || allowed {
		t.Fatalf("source-limited port 22 UFW rule returned allowed=%t err=%v", allowed, err)
	}
}

func TestRockySSHUnknownFirewallBlocksCheckAndApplyBeforeWrites(t *testing.T) {
	dir := rockySSHTestPaths(t)
	main := []byte("Include " + sshdConfigDir + "/*.conf\nPort 22\n")
	if err := os.WriteFile(sshdConfigPath, main, 0644); err != nil {
		t.Fatal(err)
	}
	writeRockyFirewallFake(t, dir, "#!/bin/sh\necho unavailable\nexit 0\n")
	rc := newRealRC(t)
	rc.Config.System = &config.SystemInfo{OS: "rocky", Version: "9.8"}
	rc.Config.SSH = config.SSHConfig{Port: 2222}
	module := NewSSHModule()
	if _, err := module.Check(context.Background(), rc); err == nil {
		t.Fatal("Check accepted unrecognized active firewall state")
	}
	if _, err := module.Apply(context.Background(), rc); err == nil {
		t.Fatal("Apply accepted unrecognized active firewall state")
	}
	mainAfter, err := os.ReadFile(sshdConfigPath)
	if err != nil || string(mainAfter) != string(main) {
		t.Fatalf("main sshd config changed before firewall proof: %q, %v", mainAfter, err)
	}
	if _, err := os.Stat(sshdDropInPath); !os.IsNotExist(err) {
		t.Fatalf("managed drop-in was written before firewall proof: %v", err)
	}
}

func TestParseFirewalldZones(t *testing.T) {
	got := parseFirewalldZones("public (default, active)\n  interfaces: eth0\ntrusted\n  sources: 10.0.0.0/8\n")
	if strings.Join(got, ",") != "public,trusted" {
		t.Fatalf("zones = %v", got)
	}
}

func TestPortInRangeList(t *testing.T) {
	if !portInRangeList([]string{"22,2220-2230,8022"}, 2222) || portInRangeList([]string{"22,2220-2230,8022"}, 222) {
		t.Fatal("port range matching failed")
	}
}

func TestEnsureRockySSHSELinuxPort(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "semanage.log")
	script := "#!/bin/sh\nif [ \"$1:$2\" = 'port:-l' ]; then printf 'ssh_port_t tcp 22, 2222\\nhttp_port_t tcp 80, 443\\n'; else printf '%s\\n' \"$*\" >> '" + log + "'; fi\n"
	if err := os.WriteFile(filepath.Join(dir, "semanage"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	rc := newRealRC(t)
	added, err := ensureRockySSHSELinuxPort(context.Background(), rc, 2222)
	if err != nil || added {
		t.Fatalf("existing SSH label = %t, %v", added, err)
	}
	added, err = ensureRockySSHSELinuxPort(context.Background(), rc, 8080)
	if err != nil || !added {
		t.Fatalf("new SSH label = %t, %v", added, err)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "port -a -t ssh_port_t -p tcp 8080") {
		t.Errorf("SELinux label command not recorded: %s", calls)
	}
	if _, err := ensureRockySSHSELinuxPort(context.Background(), rc, 80); err == nil || !strings.Contains(err.Error(), "http_port_t") {
		t.Errorf("conflicting label error = %v", err)
	}
}

func TestVerifyRockySSHEffectiveValues(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "sshd")
	script := "#!/bin/sh\ncat <<'EOF'\npermitrootlogin no\npasswordauthentication no\nkbdinteractiveauthentication no\nport 2222\nmaxauthtries 4\nEOF\n"
	if err := os.WriteFile(bin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	rc := newDryRunRC(t)
	rc.Config.System = &config.SystemInfo{OS: "rocky", Version: "9.5"}
	rc.Config.SSH = config.SSHConfig{DisableRootLogin: true, DisablePasswordAuth: true, Port: 2222, MaxAuthTries: 4}
	if err := verifyRockySSHEffective(context.Background(), rc, bin); err != nil {
		t.Fatalf("effective SSH config rejected: %v", err)
	}
	rc.Config.SSH.Port = 2200
	if err := verifyRockySSHEffective(context.Background(), rc, bin); err == nil {
		t.Fatal("mismatched effective SSH port should fail")
	}
}

func TestRockySecurityConfigurationIsSecurityOnlyAndNoReboot(t *testing.T) {
	for _, want := range []string{"upgrade_type = security", "apply_updates = yes", "reboot = never", "nvidia*", "cuda*", "libnvidia*"} {
		if !strings.Contains(rockyAutomaticConfig, want) {
			t.Errorf("Rocky automatic policy missing %q", want)
		}
	}
	if strings.Contains(strings.ToLower(rockyAutomaticConfig), "reboot = yes") {
		t.Fatal("Rocky automatic update policy enables reboot")
	}
}

func TestVerifyRockyUpdateinfoUsesCacheAndRequestsMissingMetadataRefresh(t *testing.T) {
	oldPython := rockyPlatformPython
	rockyPlatformPython = "platform-python"
	t.Cleanup(func() { rockyPlatformPython = oldPython })
	for _, output := range []string{"", "rootfiles-updateinfo-error:required repository missing", "rootfiles-updateinfo-error:invalid updateinfo root for enabled repository baseos"} {
		fakeCommandOutput(t, map[string]string{"platform-python": output})
		if _, _, err := verifyRockyUpdateinfo(context.Background(), newDryRunRC(t), false); err == nil {
			t.Errorf("updateinfo output %q should be rejected as unverifiable", output)
		}
	}
	fakeCommandOutput(t, map[string]string{"platform-python": "rootfiles-updateinfo-ok:appstream,baseos\n"})
	if ready, gaps, err := verifyRockyUpdateinfo(context.Background(), newDryRunRC(t), false); err != nil || !ready || len(gaps) != 0 {
		t.Errorf("valid metadata proof rejected: %v", err)
	}
	fakeCommandOutput(t, map[string]string{"platform-python": "rootfiles-updateinfo-refresh-needed:baseos\n"})
	if ready, _, err := verifyRockyUpdateinfo(context.Background(), newDryRunRC(t), false); err != nil || ready {
		t.Errorf("stale metadata should request refresh, got ready=%t err=%v", ready, err)
	}
	fakeCommandOutput(t, map[string]string{"platform-python": "rootfiles-updateinfo-ok:appstream,baseos\nrootfiles-updateinfo-coverage-gap:extras,epel\n"})
	if ready, gaps, err := verifyRockyUpdateinfo(context.Background(), newDryRunRC(t), false); err != nil || !ready || strings.Join(gaps, ",") != "extras,epel" {
		t.Errorf("optional repo gaps should not block required coverage: ready=%t gaps=%v err=%v", ready, gaps, err)
	}
	if !strings.Contains(rockyUpdateinfoCheck, "base.conf.cacheonly = True") || !strings.Contains(rockyUpdateinfoCheck, "base.conf.substitutions.update_from_etc(base.conf.installroot)") || !strings.Contains(rockyUpdateinfoCheck, `required = set(("baseos", "appstream"))`) || !strings.Contains(rockyUpdateinfoCheck, `if strict and repo._repo.isExpired():`) || !strings.Contains(rockyUpdateinfoCheck, `repo.get_metadata_content("updateinfo")`) || !strings.Contains(rockyUpdateinfoCheck, `ET.fromstring(content)`) || !strings.Contains(rockyUpdateinfoCheck, `path.endswith(".solvx")`) {
		t.Fatal("DNF proof must bind and parse the enabled repository's updateinfo metadata")
	}
}

type rockyRecordingPackageManager struct{ logPath string }

func (p rockyRecordingPackageManager) Update(context.Context) error {
	return appendRockyCall(p.logPath, "update")
}
func (rockyRecordingPackageManager) Install(context.Context, []string) error { return nil }
func (rockyRecordingPackageManager) IsInstalled(string) bool                 { return true }
func (rockyRecordingPackageManager) Installed(names []string) map[string]bool {
	installed := make(map[string]bool, len(names))
	for _, name := range names {
		installed[name] = true
	}
	return installed
}
func (rockyRecordingPackageManager) AddKeyring(context.Context, string, string) error {
	return nil
}
func (rockyRecordingPackageManager) AddSourceList(context.Context, string, string) error {
	return nil
}

func appendRockyCall(path, call string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(call + "\n")
	return err
}

func TestRockySecurityApplyRefreshesAndProvesBeforeWritingPolicy(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls")
	pythonPath := filepath.Join(dir, "platform-python")
	python := "#!/bin/sh\nprintf 'proof\\n' >> '" + logPath + "'\nprintf 'rootfiles-updateinfo-ok:appstream,baseos\\nrootfiles-updateinfo-coverage-gap:epel\\n'\n"
	if err := os.WriteFile(pythonPath, []byte(python), 0755); err != nil {
		t.Fatal(err)
	}
	systemctl := "#!/bin/sh\nif [ \"$1\" = is-active ]; then echo inactive; else exit 0; fi\n"
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(systemctl), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	oldPython, oldConfigPath := rockyPlatformPython, rockyAutomaticConfigPath
	rockyPlatformPython = "platform-python"
	rockyAutomaticConfigPath = filepath.Join(dir, "automatic.conf")
	t.Cleanup(func() {
		rockyPlatformPython, rockyAutomaticConfigPath = oldPython, oldConfigPath
	})
	if err := os.WriteFile(rockyAutomaticConfigPath, []byte("previous policy\n"), 0644); err != nil {
		t.Fatal(err)
	}
	rc := newRealRC(t)
	rc.Config.Modules.Security = config.SecurityConfig{UnattendedUpgrades: true}
	rc.APT = rockyRecordingPackageManager{logPath: logPath}
	result, err := rockySecurityApply(context.Background(), rc)
	if err != nil {
		t.Fatalf("rockySecurityApply: %v", err)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "epel") {
		t.Fatalf("missing optional-repository coverage warning: %+v", result.Warnings)
	}
	calls, _ := os.ReadFile(logPath)
	if strings.Index(string(calls), "update") < 0 || strings.Index(string(calls), "proof") < 0 || strings.Index(string(calls), "update") > strings.Index(string(calls), "proof") {
		t.Fatalf("expected DNF metadata refresh before proof, calls=%q", calls)
	}
	policy, err := os.ReadFile(rockyAutomaticConfigPath)
	if err != nil || string(policy) != rockyAutomaticConfig {
		t.Fatalf("policy after verified refresh = %q, %v", policy, err)
	}
}

func TestRockySecurityApplyLeavesPolicyUntouchedWithoutFreshMetadata(t *testing.T) {
	dir := t.TempDir()
	pythonPath := filepath.Join(dir, "platform-python")
	python := "#!/bin/sh\nprintf 'rootfiles-updateinfo-refresh-needed:baseos\\n'\n"
	if err := os.WriteFile(pythonPath, []byte(python), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	oldPython, oldConfigPath := rockyPlatformPython, rockyAutomaticConfigPath
	rockyPlatformPython = "platform-python"
	rockyAutomaticConfigPath = filepath.Join(dir, "automatic.conf")
	t.Cleanup(func() {
		rockyPlatformPython, rockyAutomaticConfigPath = oldPython, oldConfigPath
	})
	const previous = "existing policy\n"
	if err := os.WriteFile(rockyAutomaticConfigPath, []byte(previous), 0644); err != nil {
		t.Fatal(err)
	}
	rc := newRealRC(t)
	rc.Config.Modules.Security = config.SecurityConfig{UnattendedUpgrades: true}
	rc.APT = rockyRecordingPackageManager{logPath: filepath.Join(dir, "calls")}
	if _, err := rockySecurityApply(context.Background(), rc); err == nil {
		t.Fatal("Apply accepted stale advisory metadata after refresh")
	}
	policy, err := os.ReadFile(rockyAutomaticConfigPath)
	if err != nil || string(policy) != previous {
		t.Fatalf("policy changed without fresh metadata: %q, %v", policy, err)
	}
}

func TestRockySecurityCheckDoesNotLoopOnOptionalCoverageGap(t *testing.T) {
	dir := t.TempDir()
	fakeCommandOutput(t, map[string]string{
		"platform-python": "rootfiles-updateinfo-ok:appstream,baseos\nrootfiles-updateinfo-coverage-gap:epel\n",
		"systemctl":       "active\n",
	})
	oldPython, oldConfigPath := rockyPlatformPython, rockyAutomaticConfigPath
	rockyPlatformPython = "platform-python"
	rockyAutomaticConfigPath = filepath.Join(dir, "automatic.conf")
	t.Cleanup(func() {
		rockyPlatformPython, rockyAutomaticConfigPath = oldPython, oldConfigPath
	})
	if err := os.WriteFile(rockyAutomaticConfigPath, []byte(rockyAutomaticConfig), 0644); err != nil {
		t.Fatal(err)
	}
	rc := newDryRunRC(t)
	rc.Config.Modules.Security = config.SecurityConfig{UnattendedUpgrades: true}
	rc.APT = rockyRecordingPackageManager{logPath: filepath.Join(dir, "unused")}
	result, err := rockySecurityCheck(context.Background(), rc)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Satisfied || len(result.Changes) != 0 {
		t.Fatalf("optional repo gap should not cause an apply loop: %+v", result)
	}
}

func TestRockyProfileAvoidsUbuntuAndUnsupportedModules(t *testing.T) {
	data, err := os.ReadFile("../config/profiles/rocky.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, denied := range []string{"apt-get", "ufw", "unattended-upgrades", "docker:", "nvidia:", "cloudflared:", "network:", "monitoring:"} {
		if strings.Contains(text, denied) {
			t.Errorf("Rocky profile contains unsupported Ubuntu/module reference %q", denied)
		}
	}
	for _, required := range []string{"gnupg2", "vim-enhanced", "gcc-c++", "glibc-langpack-en", "wheel", "dnf-automatic"} {
		if !strings.Contains(text, required) {
			t.Errorf("Rocky profile is missing %q", required)
		}
	}
}
