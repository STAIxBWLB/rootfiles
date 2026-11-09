package module

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

func TestModuleWriterPreservesNamesOnlyPayloads(t *testing.T) {
	rc := newDryRunRC(t)
	var out bytes.Buffer
	rc.Out = &out
	path := usersDBPath(rc)
	data, err := json.Marshal(UsersDB{Version: 1, Users: []UserMeta{{Name: "alice"}, {Name: "bob"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ListUserNames(rc); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "alice\nbob\n" {
		t.Fatalf("managed names changed: %q", got)
	}
	out.Reset()
	fixtureSystemUsers(t)
	if err := ListSystemUserNames(context.Background(), rc); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "fixture-user\n" {
		t.Fatalf("system names changed: %q", got)
	}
}

func TestModuleWriterPreservesRawIDAndQuotaPayloads(t *testing.T) {
	fakePasswd(t, "alice")
	const idPayload = "uid=1000(alice) groups=1000 100% %s"
	const quotaPayload = "blocks 100% %s\n\n  indented row"
	fakeCommandOutput(t, map[string]string{"id": idPayload, "repquota": quotaPayload})
	rc := newDryRunRC(t)
	var out bytes.Buffer
	rc.Out = &out
	for _, report := range []func(context.Context, *RunContext, string) error{ShowUserID, ListUserGroups} {
		out.Reset()
		if err := report(context.Background(), rc, "alice"); err != nil {
			t.Fatal(err)
		}
		if got := out.String(); got != idPayload {
			t.Fatalf("id payload formatting/newline changed: %q", got)
		}
	}
	mounts := filepath.Join(t.TempDir(), "mounts")
	if err := os.WriteFile(mounts, []byte("/dev/fixture "+rc.Config.Users.HomeBase+" ext4 rw,usrquota 0 0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	old := procMountsPath
	procMountsPath = mounts
	t.Cleanup(func() { procMountsPath = old })
	out.Reset()
	if err := ShowQuotas(context.Background(), rc); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != quotaPayload {
		t.Fatalf("quota payload formatting/newline changed: %q", got)
	}
}

func TestModuleWriterCapturesGPUAndKeyTables(t *testing.T) {
	fakePasswd(t, "alice")
	rc := newDryRunRC(t)
	var out bytes.Buffer
	rc.Out = &out
	setupDBWithAllocations(t, rc.Config.Users.HomeBase, &GPUAllocationsDB{Version: 1, TotalGPUs: 3, Allocations: []GPUAllocation{{Username: "alice", GPUs: []int{0, 2}, Method: "env", UpdatedAt: "fixture-time"}}})
	if err := ListGPUAllocations(rc); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"GPU Allocations", "USER", "GPUs", "alice", "0,2", "fixture-time"} {
		if !strings.Contains(out.String(), field) {
			t.Fatalf("GPU table lost %q from injected writer: %q", field, out.String())
		}
	}
	out.Reset()
	if err := ListKeys(rc, "alice"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ssh-ed25519 AAAA") || !strings.Contains(out.String(), "test") {
		t.Fatalf("key table bypassed injected writer: %q", out.String())
	}
}

func TestModuleWriterCapturesCloudflaredReportsWithoutMutation(t *testing.T) {
	fakeCloudflaredPaths(t)
	const version = "cloudflared version 2026.11.10 writer 100% %s"
	if err := os.WriteFile(cloudflaredBinary, []byte("#!/bin/sh\nprintf '%s\\n' '"+version+"'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	fakeCommandOutput(t, map[string]string{"systemctl": "active\n", "ip": "  inet 203.0.113.2/32 scope global vlan0\n"})
	rc := newDryRunRC(t)
	var out bytes.Buffer
	rc.Out = &out
	if err := TunnelStatus(context.Background(), rc); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"Tunnel", version, "active", "203.0.113.2/32"} {
		if !strings.Contains(out.String(), field) {
			t.Fatalf("tunnel report lost %q from injected writer: %q", field, out.String())
		}
	}
	out.Reset()
	if err := TunnelUpdate(context.Background(), rc, "2026.11.10"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Cloudflared update") || !strings.Contains(out.String(), "already on target version") {
		t.Fatalf("no-op update report bypassed writer: %q", out.String())
	}
}

func TestModuleWriterKeepsMetadataWarningsOnSeparateStream(t *testing.T) {
	rc := newDryRunRC(t)
	var out, warnings bytes.Buffer
	rc.Out = &out
	old := warnOut
	warnOut = &warnings
	t.Cleanup(func() { warnOut = old })
	if err := os.Chmod(filepath.Join(rc.Config.Users.HomeBase, ".rootfiles"), 0777); err != nil {
		t.Fatal(err)
	}
	WarnUntrustedMetadata(rc)
	if out.Len() != 0 || !strings.Contains(warnings.String(), "untrusted rootfiles metadata") || !strings.HasPrefix(warnings.String(), ui.MarkWarn+" ") {
		t.Fatalf("warning stream changed: stdout=%q warnings=%q", out.String(), warnings.String())
	}
}

func TestModuleWriterCapturesForcedSSHWarning(t *testing.T) {
	fakePasswd(t)
	withPasswords(t, "alice")
	fakeBin(t, "sshd", "systemctl")
	rc := newDryRunRC(t)
	rc.Force = true
	rc.Config.SSH.DisablePasswordAuth = true
	var out bytes.Buffer
	rc.Out = &out
	old := sshdDropInPath
	sshdDropInPath = filepath.Join(t.TempDir(), "drop-in.conf")
	t.Cleanup(func() { sshdDropInPath = old })
	if _, err := NewSSHModule().Apply(context.Background(), rc); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), ui.MarkWarn+" ssh:") || !strings.Contains(out.String(), "continuing because of --force") {
		t.Fatalf("force warning bypassed injected writer: %q", out.String())
	}
}

func TestModuleMachineChangeMarkersRemainUnstyledBytes(t *testing.T) {
	rc := newDryRunRC(t)
	link, target := filepath.Join(t.TempDir(), "link"), filepath.Join(t.TempDir(), "target")
	rc.Config.Modules.Storage.Symlinks = map[string]string{link: target}
	storage, err := NewStorageModule().Check(context.Background(), rc)
	if err != nil {
		t.Fatal(err)
	}
	want := "Create symlink " + link + " " + ui.MarkPending + " " + target
	found := false
	for _, change := range storage.Changes {
		found = found || change.Description == want
	}
	if !found {
		t.Fatalf("machine storage description changed: %+v", storage.Changes)
	}
	old := cloudflaredBinary
	cloudflaredBinary = filepath.Join(t.TempDir(), "missing-cloudflared")
	t.Cleanup(func() { cloudflaredBinary = old })
	cloud, err := NewCloudflaredModule().Check(context.Background(), rc)
	if err != nil {
		t.Fatal(err)
	}
	if len(cloud.Changes) != 1 || cloud.Changes[0].Command != "download cloudflared "+ui.MarkPending+" "+cloudflaredBinary {
		t.Fatalf("machine cloudflared command changed: %+v", cloud.Changes)
	}
}

func TestNewTenantManagerHonorsContextAndExplicitWriters(t *testing.T) {
	rc := newDryRunRC(t)
	var contextOut, explicitOut bytes.Buffer
	rc.Out = &contextOut
	if manager := NewTenantManager(rc, nil); manager.Out != &contextOut {
		t.Fatal("nil manager writer did not inherit context output")
	}
	if manager := NewTenantManager(rc, &explicitOut); manager.Out != &explicitOut {
		t.Fatal("explicit manager writer was replaced")
	}
	if manager := NewTenantManager(rc, io.Discard); manager.Out != io.Discard {
		t.Fatal("explicit discard writer was replaced")
	}
}
