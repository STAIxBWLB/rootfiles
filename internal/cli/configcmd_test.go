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
