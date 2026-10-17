package module

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

func fakeSystemPaths(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	saved := []string{hostnamePath, hostsPath, procSwapsPath, swapfilePath, fstabPath, sysctlPath, journaldConfPath}
	savedSources := aptSourcesPaths
	hostnamePath = filepath.Join(dir, "hostname")
	hostsPath = filepath.Join(dir, "hosts")
	procSwapsPath = filepath.Join(dir, "swaps")
	swapfilePath = filepath.Join(dir, "swapfile")
	fstabPath = filepath.Join(dir, "fstab")
	sysctlPath = filepath.Join(dir, "sysctl.d", "90-rootfiles.conf")
	journaldConfPath = filepath.Join(dir, "journald.conf.d", "90-rootfiles.conf")
	aptSourcesPaths = []string{filepath.Join(dir, "ubuntu.sources"), filepath.Join(dir, "sources.list")}
	os.MkdirAll(filepath.Dir(sysctlPath), 0755)
	t.Cleanup(func() {
		hostnamePath, hostsPath, procSwapsPath, swapfilePath, fstabPath, sysctlPath, journaldConfPath =
			saved[0], saved[1], saved[2], saved[3], saved[4], saved[5], saved[6]
		aptSourcesPaths = savedSources
	})
	return dir
}

func TestSystemModule_Converges(t *testing.T) {
	fakeSystemPaths(t)
	fakeBin(t, "hostnamectl", "sysctl", "systemctl", "fallocate", "chmod", "mkswap", "swapon")
	os.WriteFile(hostnamePath, []byte("old-name\n"), 0644)
	os.WriteFile(hostsPath, []byte("127.0.0.1\tlocalhost\n127.0.1.1\told-name\n"), 0644)
	os.WriteFile(procSwapsPath, []byte("Filename\tType\tSize\tUsed\tPriority\n"), 0644)
	os.WriteFile(aptSourcesPaths[0], []byte("Types: deb\nURIs: http://kr.archive.ubuntu.com/ubuntu/\nSuites: noble\n\nTypes: deb\nURIs: http://security.ubuntu.com/ubuntu/\n"), 0644)

	rc := newRealRC(t)
	rc.Config.Modules.System = config.SystemConfig{
		Enabled:        true,
		Hostname:       "gpu01",
		SwapSize:       "8G",
		JournaldMaxUse: "2G",
		Sysctl:         map[string]string{"vm.swappiness": "10", "fs.inotify.max_user_watches": "524288"},
		AptMirror:      "http://mirror.kakao.com/ubuntu",
	}
	m := NewSystemModule()
	check, _ := m.Check(context.Background(), rc)
	if len(check.Changes) != 5 {
		t.Fatalf("expected 5 changes, got %+v", check.Changes)
	}
	res, err := m.Apply(context.Background(), rc)
	if err != nil || !res.Changed {
		t.Fatalf("Apply = %+v, %v", res, err)
	}

	hosts, _ := os.ReadFile(hostsPath)
	if !strings.Contains(string(hosts), "127.0.1.1\tgpu01") || strings.Contains(string(hosts), "old-name") {
		t.Errorf("hosts not updated:\n%s", hosts)
	}
	src, _ := os.ReadFile(aptSourcesPaths[0])
	if !strings.Contains(string(src), "http://mirror.kakao.com/ubuntu/") || !strings.Contains(string(src), "security.ubuntu.com") {
		t.Errorf("mirror rewrite wrong (security must stay):\n%s", src)
	}
	fstab, _ := os.ReadFile(fstabPath)
	if !strings.Contains(string(fstab), swapfilePath+" none swap") {
		t.Errorf("fstab missing swap entry: %s", fstab)
	}
	sysctl, _ := os.ReadFile(sysctlPath)
	if !strings.Contains(string(sysctl), "fs.inotify.max_user_watches = 524288\nvm.swappiness = 10") {
		t.Errorf("sysctl not sorted/written:\n%s", sysctl)
	}

	// Simulate the effects the stubbed commands would have had.
	os.WriteFile(hostnamePath, []byte("gpu01\n"), 0644)
	os.WriteFile(procSwapsPath, []byte("Filename\tType\n"+swapfilePath+"\tfile\n"), 0644)
	if check, _ := m.Check(context.Background(), rc); !check.Satisfied {
		t.Errorf("should converge, got %+v", check.Changes)
	}
}

func TestSystemModule_ExistingSwapUntouched(t *testing.T) {
	fakeSystemPaths(t)
	os.WriteFile(procSwapsPath, []byte("Filename\tType\n/dev/sda2\tpartition\n"), 0644)
	rc := newDryRunRC(t)
	rc.Config.Modules.System = config.SystemConfig{Enabled: true, SwapSize: "8G"}
	if check, _ := NewSystemModule().Check(context.Background(), rc); !check.Satisfied {
		t.Errorf("existing swap must satisfy swap_size, got %+v", check.Changes)
	}
}
