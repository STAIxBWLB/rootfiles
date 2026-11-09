package module

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

func TestFirewallParserKnownStateAndRules(t *testing.T) {
	for _, output := range []string{"", "Status: broken\n", "something else\n", "Status: active\nStatus: inactive\n"} {
		if parseUFWStatus(output).Known {
			t.Fatalf("unknown output accepted: %q", output)
		}
	}
	out := `Status: active
Default: deny (incoming), allow (outgoing), disabled (routed)
22/tcp ALLOW IN Anywhere
22/tcp (v6) ALLOW IN Anywhere (v6)
443/tcp ALLOW OUT Anywhere
53/udp ALLOW IN Anywhere
9100/tcp ALLOW IN 192.0.2.10
Nginx Full ALLOW IN 192.0.2.20
80:90/tcp DENY IN Anywhere
`
	st := parseUFWStatus(out)
	if !st.Known || !st.Active || st.DefaultInbound != "deny" {
		t.Fatalf("state: %+v", st)
	}
	if !st.Allowed[22] || st.Allowed[443] || st.Allowed[53] {
		t.Fatalf("wrong TCP/IN allowances: %v", st.Allowed)
	}
	if !st.allowsUnrestrictedTCPPort(22) || st.allowsUnrestrictedTCPPort(9100) {
		t.Fatal("unrestricted SSH predicate lost family/source rules")
	}
	if !st.hasDenyOrRejectRule() {
		t.Fatal("deny rule lost")
	}
	if len(st.Rules) != 7 || st.Rules[5].Target != "Nginx Full" || st.Rules[4].Source != "192.0.2.10" {
		t.Fatalf("parsed rules: %+v", st.Rules)
	}
}

func fakeFirewallStatus(t *testing.T, output string, fail bool) string {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + calls + "'\nprintf '%s\\n' '" + output + "'\n"
	if fail {
		script += "exit 1\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "ufw"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return calls
}

func TestSSHUnknownFirewallDoesNotWrite(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		fail         bool
	}{{"failed query", "permission denied", true}, {"unknown status", "Status: unknown", false}} {
		t.Run(tc.name, func(t *testing.T) {
			fakeFirewallStatus(t, tc.output, tc.fail)
			path := filepath.Join(t.TempDir(), "00-rootfiles.conf")
			old := sshdDropInPath
			sshdDropInPath = path
			t.Cleanup(func() { sshdDropInPath = old })
			rc := newDryRunRC(t)
			rc.DryRun = false
			rc.Runner.DryRun = false
			rc.Config.System = &config.SystemInfo{OS: "ubuntu", Version: "24.04"}
			rc.Config.SSH = config.SSHConfig{Port: 2222}
			rc.Force = true
			for _, run := range []func() error{
				func() error { _, err := NewSSHModule().Check(context.Background(), rc); return err },
				func() error { _, err := NewSSHModule().Apply(context.Background(), rc); return err },
			} {
				if err := run(); err == nil || !strings.Contains(err.Error(), "UFW") || !strings.Contains(err.Error(), "ssh.port") {
					t.Fatalf("missing refusal/cause/fix: %v", err)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("unknown firewall wrote sshd config: %v", err)
				}
			}
		})
	}
}

func TestSSHCheckPlansFirewallAllowance(t *testing.T) {
	oldDrop, oldDir, oldPrivilege := sshdDropInPath, sshdConfigDir, sshdPrivilegeSeparationDir
	dir := t.TempDir()
	sshdConfigDir = filepath.Join(dir, "sshd_config.d")
	sshdDropInPath = filepath.Join(sshdConfigDir, "managed.conf")
	sshdPrivilegeSeparationDir = filepath.Join(dir, "run-sshd")
	t.Cleanup(func() { sshdDropInPath, sshdConfigDir, sshdPrivilegeSeparationDir = oldDrop, oldDir, oldPrivilege })
	fakeFirewallStatus(t, "Status: active\n22/tcp ALLOW Anywhere\n22/tcp (v6) ALLOW Anywhere (v6)", false)
	rc := newDryRunRC(t)
	rc.Config.System = &config.SystemInfo{OS: "ubuntu", Version: "24.04"}
	rc.Config.SSH = config.SSHConfig{Port: 2222}
	check, err := NewSSHModule().Check(context.Background(), rc)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, change := range check.Changes {
		if change.Command == "ufw allow 2222/tcp" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing UFW allow plan: %+v", check.Changes)
	}
	result, err := NewSSHModule().Apply(context.Background(), rc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(result.Messages, " "), "ufw: allowed ssh port 2222") {
		t.Fatalf("Apply did not act on plan: %+v", result)
	}
}

func TestNetworkUnknownFirewallDoesNotEnable(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		fail         bool
	}{{"failed", "permission denied", true}, {"malformed", "Status: broken", false}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := fakeFirewallStatus(t, tc.output, tc.fail)
			rc := newDryRunRC(t)
			rc.DryRun = false
			rc.Runner.DryRun = false
			rc.Config.Modules.Network = config.NetworkConfig{UFW: true}
			if _, err := NewNetworkModule().Check(context.Background(), rc); err == nil {
				t.Fatal("Check accepted unknown firewall")
			}
			if _, err := NewNetworkModule().Apply(context.Background(), rc); err == nil {
				t.Fatal("Apply accepted unknown firewall")
			}
			data, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "allow") || strings.Contains(string(data), "enable") {
				t.Fatalf("unknown state mutated UFW: %s", data)
			}
		})
	}
}

func TestSSHFirewallDoesNotWidenExistingRestrictedRule(t *testing.T) {
	calls := fakeFirewallStatus(t, "Status: active\n2222/tcp ALLOW IN 192.0.2.0/24\n2222/tcp (v6) ALLOW IN Anywhere (v6)", false)
	rc := newDryRunRC(t)
	rc.Config.System = &config.SystemInfo{OS: "ubuntu", Version: "24.04"}
	rc.Config.SSH = config.SSHConfig{Port: 2222}
	if _, err := NewSSHModule().Apply(context.Background(), rc); err == nil || !strings.Contains(err.Error(), "adjust the rule explicitly") {
		t.Fatalf("restricted rule silently widened: %v", err)
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "allow") {
		t.Fatalf("restricted policy mutated: %s", data)
	}
}

func TestNetworkKnownUnrelatedDenyPolicyRemainsIdempotent(t *testing.T) {
	fakeFirewallStatus(t, "Status: active\n22/tcp ALLOW IN Anywhere\n22/tcp (v6) ALLOW IN Anywhere (v6)\n8080/tcp DENY IN Anywhere", false)
	rc := newDryRunRC(t)
	rc.Config.SSH.Port = 22
	rc.Config.Modules.Network = config.NetworkConfig{UFW: true}
	check, err := NewNetworkModule().Check(context.Background(), rc)
	if err != nil || !check.Satisfied {
		t.Fatalf("known unrelated policy should remain unchanged: %+v, %v", check, err)
	}
	result, err := NewNetworkModule().Apply(context.Background(), rc)
	if err != nil || result.Changed {
		t.Fatalf("known unrelated policy should be idempotent: %+v, %v", result, err)
	}
}

func TestRockyFirewallRetainsOutboundDenyGuard(t *testing.T) {
	fakeFirewallStatus(t, "Status: active\n2222/tcp ALLOW IN Anywhere\n2222/tcp (v6) ALLOW IN Anywhere (v6)\n53/udp DENY OUT Anywhere", false)
	allowed, err := rockyFirewallAllowsSSH(context.Background(), newDryRunRC(t), 2222)
	if err != nil || allowed {
		t.Fatalf("Rocky's existing deny guard was relaxed: allowed=%v err=%v", allowed, err)
	}
}
