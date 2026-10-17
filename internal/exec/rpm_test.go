package exec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

func writeRPMFake(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestNewPackageManagerSelectsDNFOnRocky(t *testing.T) {
	pm, err := NewPackageManager(quietRunner(true), &config.SystemInfo{OS: "rocky", Version: "8.10"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pm.(*RPM); !ok {
		t.Fatalf("package manager type = %T, want *RPM", pm)
	}
}

func TestRPMInstalledQueriesRPMAndParsesPresentNames(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	writeRPMFake(t, bin, "rpm", "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+log+"'\nprintf 'curl\\nwget\\n'")
	r := NewRPM(quietRunner(false))
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+oldPath)
	got := r.Installed([]string{"curl", "wget", "missing"})
	if !got["curl"] || !got["wget"] || got["missing"] {
		t.Fatalf("Installed() = %#v", got)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "-q --qf %{NAME}\\n curl wget missing curl-minimal") {
		t.Errorf("rpm query = %q", calls)
	}
}

func TestRPMInstalledTreatsCurlMinimalAsCurlOnly(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	writeRPMFake(t, bin, "rpm", "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+log+"'\nprintf 'curl-minimal\\n'")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	r := NewRPM(quietRunner(false))
	got := r.Installed([]string{"curl", "curl-minimal"})
	if !got["curl"] || !got["curl-minimal"] {
		t.Fatalf("Installed() = %#v, want logical curl and explicit curl-minimal", got)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "curl curl-minimal") {
		t.Errorf("query omitted native curl variant: %q", calls)
	}
}

func TestRPMInstallUsesDNFAndRejectsAPTRepositoryOperations(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	writeRPMFake(t, bin, "dnf", "#!/bin/sh\nprintf 'dnf %s\\n' \"$*\" >> '"+log+"'\n")
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+oldPath)
	r := NewRPM(quietRunner(false))
	if err := r.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.Install(context.Background(), []string{"curl", "vim-enhanced"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Install(context.Background(), []string{"--allowerasing"}); err == nil {
		t.Fatal("Install accepted an option as a package name")
	}
	if err := r.AddSourceList(context.Background(), "docker", "deb ..."); err == nil {
		t.Fatal("AddSourceList accepted APT syntax")
	}
	calls, _ := os.ReadFile(log)
	if strings.Contains(string(calls), "allowerasing") || !strings.Contains(string(calls), "dnf -q makecache") || !strings.Contains(string(calls), "dnf -y --setopt=install_weak_deps=False install curl vim-enhanced") {
		t.Errorf("unexpected calls: %s", calls)
	}
}
