package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

func TestConfigTemplateIsValidForEveryProfile(t *testing.T) {
	for _, p := range config.AvailableProfiles() {
		f := filepath.Join(t.TempDir(), "site.yaml")
		os.WriteFile(f, []byte(configTemplate(p)), 0600)
		if _, err := config.Load("", f, nil); err != nil {
			t.Errorf("template extending %s: %v", p, err)
		}
	}
}

func TestConfigShowMasksToken(t *testing.T) {
	t.Setenv("ROOTFILES_STATE_DIR", t.TempDir())
	t.Setenv("ROOTFILES_TUNNEL_TOKEN", "eyJhIjoic2VjcmV0LXRva2VuLXZhbHVlIn0")
	root := NewRootCmd("test", "abc")
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetArgs([]string{"config", "show", "--profile", "minimal"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "c2VjcmV0") {
		t.Errorf("token leaked:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "(masked)") {
		t.Errorf("token not shown as masked:\n%s", buf.String())
	}
}

func TestConfigValidateFailsOnBadConfig(t *testing.T) {
	f := filepath.Join(t.TempDir(), "bad.yaml")
	os.WriteFile(f, []byte("extends: minimal\nssh:\n  port: 70000\n"), 0600)
	root := NewRootCmd("test", "abc")
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"config", "validate", "--config", f})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "ssh.port") {
		t.Errorf("expected ssh.port validation error, got %v", err)
	}
}

func TestConfigValidateAcceptsVLANAddressFlag(t *testing.T) {
	f := filepath.Join(t.TempDir(), "site.yaml")
	os.WriteFile(f, []byte("extends: minimal\nusers:\n  home_base: /home\nmodules:\n  cloudflared:\n    private_network:\n      enabled: true\n"), 0600)
	root := NewRootCmd("test", "abc")
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"config", "validate", "--config", f, "--vlan-address", "203.0.113.10/32"})
	if err := root.Execute(); err != nil {
		t.Errorf("--vlan-address should satisfy the address check, got %v", err)
	}
}

func TestConfigValidateRejectsPrivateNetworkWithoutAddress(t *testing.T) {
	f := filepath.Join(t.TempDir(), "site.yaml")
	os.WriteFile(f, []byte("extends: minimal\nmodules:\n  cloudflared:\n    private_network:\n      enabled: true\n"), 0600)
	root := NewRootCmd("test", "abc")
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"config", "validate", "--config", f})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "modules.cloudflared.private_network.address") {
		t.Errorf("expected private_network.address validation error, got %v", err)
	}
}

func TestConfigInitOverwriteTightensMode(t *testing.T) {
	file := filepath.Join(t.TempDir(), "site.yaml")
	if err := os.WriteFile(file, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	root := NewRootCmd("test", "test")
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"config", "init", "--extends", "minimal", "--file", file, "--overwrite"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0600 {
		t.Fatalf("mode after overwrite: %v %v", fi.Mode(), err)
	}
	if strings.Contains(out.String(), "extends: minimal") {
		t.Fatalf("config content printed with --file: %q", out.String())
	}
}

func TestWritePrivateConfigNewFileIsPrivate(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "capture.yaml")
	token := "synthetic-test-token-not-real"
	if err := writePrivateConfig(file, []byte("tunnel_token: "+token+"\n"), false); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(file)
	if err != nil || fi.Mode().Perm() != 0600 {
		t.Fatalf("new capture mode: %v %v", fi.Mode(), err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
	if err := writePrivateConfig(file, []byte("x"), false); err == nil || !strings.Contains(err.Error(), "--overwrite") {
		t.Fatalf("existing file without overwrite: %v", err)
	}
}

func TestWritePrivateConfigRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "site.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	err := writePrivateConfig(link, []byte("secret"), true)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("symlink destination: %v", err)
	}
	if data, _ := os.ReadFile(target); string(data) != "keep" {
		t.Fatalf("symlink target changed: %q", data)
	}
}

func TestWritePrivateConfigFailureKeepsPreviousFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permission")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "site.yaml")
	if err := os.WriteFile(file, []byte("previous"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	err := writePrivateConfig(file, []byte("new"), true)
	if err == nil || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("read-only directory: %v", err)
	}
	if data, _ := os.ReadFile(file); string(data) != "previous" {
		t.Fatalf("previous file changed: %q", data)
	}
}
