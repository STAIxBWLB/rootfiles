package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestResolveProfile_Base(t *testing.T) {
	cfg, err := resolveProfile("base", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Locale != "en_US.UTF-8" {
		t.Errorf("locale = %q, want en_US.UTF-8", cfg.Locale)
	}
	if cfg.Timezone != "Asia/Seoul" {
		t.Errorf("timezone = %q, want Asia/Seoul", cfg.Timezone)
	}
	if !cfg.Modules.Locale.Enabled {
		t.Error("locale module should be enabled")
	}
	if !cfg.Modules.Packages.Enabled {
		t.Error("packages module should be enabled")
	}
	if !cfg.Modules.SSH.Enabled {
		t.Error("ssh module should be enabled")
	}
	if cfg.Modules.Docker.Enabled {
		t.Error("docker module should not be enabled in base")
	}
	if len(cfg.Packages) == 0 {
		t.Error("base should have packages")
	}
}

func TestResolveProfile_Minimal(t *testing.T) {
	cfg, err := resolveProfile("minimal", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should inherit base packages
	if len(cfg.Packages) == 0 {
		t.Error("minimal should inherit base packages")
	}
	// Should have extra packages
	if len(cfg.PackagesExtra) == 0 {
		t.Error("minimal should have packages_extra")
	}
	// Users should be enabled
	if !cfg.Modules.Users.Enabled {
		t.Error("users module should be enabled in minimal")
	}
	// Cloudflared should be enabled
	if !cfg.Modules.Cloudflared.Enabled {
		t.Error("cloudflared module should be enabled in minimal")
	}
	// Users config: home_base is left unset so Load can detect a data drive
	if cfg.Users.HomeBase != "" {
		t.Errorf("home_base = %q, want unset", cfg.Users.HomeBase)
	}
	if cfg.Users.DefaultShell != "/usr/bin/zsh" {
		t.Errorf("default_shell = %q, want /usr/bin/zsh", cfg.Users.DefaultShell)
	}
}

func TestResolveProfile_DGX(t *testing.T) {
	cfg, err := resolveProfile("dgx", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should inherit minimal → base
	if len(cfg.Packages) == 0 {
		t.Error("dgx should inherit base packages")
	}
	// DGX-specific
	if cfg.Users.HomeBase != "/raid/home" {
		t.Errorf("home_base = %q, want /raid/home", cfg.Users.HomeBase)
	}
	if !cfg.Modules.Docker.Enabled {
		t.Error("docker should be enabled in dgx")
	}
	if cfg.Modules.Docker.StorageDir != "/raid/docker" {
		t.Errorf("docker storage_dir = %q, want /raid/docker", cfg.Modules.Docker.StorageDir)
	}
	if !cfg.Modules.Nvidia.Enabled {
		t.Error("nvidia should be enabled in dgx")
	}
	if !cfg.Modules.Cloudflared.PrivateNetwork.Enabled {
		t.Error("cloudflared private_network should be enabled in dgx")
	}
	if cfg.Modules.Cloudflared.PrivateNetwork.Address != "172.16.229.32/32" {
		t.Errorf("vlan address = %q, want 172.16.229.32/32", cfg.Modules.Cloudflared.PrivateNetwork.Address)
	}
	if !cfg.Modules.Storage.Enabled {
		t.Error("storage should be enabled in dgx")
	}
	if !cfg.Modules.Network.Enabled {
		t.Error("network should be enabled in dgx")
	}
	if !cfg.SSH.DisablePasswordAuth {
		t.Error("ssh password auth should be disabled in dgx")
	}
	if !cfg.Modules.Nvidia.GPUAllocation.Enabled {
		t.Error("gpu_allocation should be enabled in dgx")
	}
	if cfg.Modules.Nvidia.GPUAllocation.Method != "both" {
		t.Errorf("gpu_allocation method = %q, want both", cfg.Modules.Nvidia.GPUAllocation.Method)
	}
}

func TestResolveProfile_GPUServer(t *testing.T) {
	cfg, err := resolveProfile("gpu-server", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Users.HomeBase != "" {
		t.Errorf("home_base = %q, want unset", cfg.Users.HomeBase)
	}
	if !cfg.Modules.Docker.Enabled {
		t.Error("docker should be enabled")
	}
	if !cfg.Modules.Nvidia.Enabled {
		t.Error("nvidia should be enabled")
	}
	if !cfg.Modules.Nvidia.GPUAllocation.Enabled {
		t.Error("gpu_allocation should be enabled in gpu-server")
	}
	if cfg.Modules.Nvidia.GPUAllocation.Method != "env" {
		t.Errorf("gpu_allocation method = %q, want env", cfg.Modules.Nvidia.GPUAllocation.Method)
	}
}

func TestResolveProfile_Full(t *testing.T) {
	cfg, err := resolveProfile("full", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Modules.Docker.Enabled {
		t.Error("docker should be enabled in full")
	}
	if !cfg.Modules.Storage.Enabled {
		t.Error("storage should be enabled in full")
	}
	if !cfg.Modules.Network.Enabled {
		t.Error("network should be enabled in full")
	}
}

func TestResolveProfile_NotFound(t *testing.T) {
	_, err := resolveProfile("nonexistent", 0)
	if err == nil {
		t.Error("expected error for nonexistent profile")
	}
}

func TestMergeTrees(t *testing.T) {
	base := map[string]any{
		"locale":         "en_US.UTF-8",
		"packages_extra": []any{"a"},
		"users":          map[string]any{"home_base": "/home", "sudo_nopasswd": true},
		"modules":        map[string]any{"docker": map[string]any{"enabled": true, "storage_dir": "/x"}},
	}
	overlay := map[string]any{
		"packages_extra": []any{"b"},
		"users":          map[string]any{"home_base": "/raid/home", "sudo_nopasswd": false},
		"modules":        map[string]any{"docker": map[string]any{"enabled": false}},
	}
	m := mergeTrees(base, overlay)
	users := m["users"].(map[string]any)
	if users["home_base"] != "/raid/home" || users["sudo_nopasswd"] != false {
		t.Errorf("users = %v", users)
	}
	docker := m["modules"].(map[string]any)["docker"].(map[string]any)
	if docker["enabled"] != false || docker["storage_dir"] != "/x" {
		t.Errorf("docker = %v (explicit false must win, other keys kept)", docker)
	}
	if extra := m["packages_extra"].([]any); len(extra) != 2 {
		t.Errorf("packages_extra = %v, want accumulated", extra)
	}
	if m["locale"] != "en_US.UTF-8" {
		t.Errorf("locale lost: %v", m["locale"])
	}
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad_CustomFileExtendsProfileAndDisables(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "site.yaml", `extends: dgx
modules:
  cloudflared:
    enabled: false
ssh:
  disable_root_login: false
users:
  sudo_nopasswd: false
`)
	cfg, err := Load("", p, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Users.HomeBase != "/raid/home" || !cfg.Modules.Docker.Enabled {
		t.Error("dgx settings should be inherited")
	}
	if cfg.Modules.Cloudflared.Enabled {
		t.Error("child must be able to disable a module")
	}
	if cfg.SSH.DisableRootLogin {
		t.Error("child must be able to set a bool back to false")
	}
	if !cfg.SSH.DisablePasswordAuth {
		t.Error("unrelated ssh settings should be kept")
	}
	if cfg.Users.SudoNopasswd {
		t.Error("sudo_nopasswd override to false ignored")
	}
	if len(cfg.PackagesExtra) == 0 || len(cfg.Packages) == 0 {
		t.Error("packages should be inherited through the chain")
	}
}

func TestLoad_CustomFileExtendsRelativeFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "common.yaml", "extends: minimal\ntimezone: UTC\n")
	os.MkdirAll(filepath.Join(dir, "hosts"), 0755)
	p := writeFile(t, filepath.Join(dir, "hosts"), "gpu01.yaml", "extends: ../common.yaml\nlocale: ko_KR.UTF-8\n")
	cfg, err := Load("", p, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Timezone != "UTC" || cfg.Locale != "ko_KR.UTF-8" || !cfg.Modules.Users.Enabled {
		t.Errorf("unexpected merge: tz=%q locale=%q users=%v", cfg.Timezone, cfg.Locale, cfg.Modules.Users.Enabled)
	}
}

func TestLoad_ExtendsCycle(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.yaml", "extends: b.yaml\n")
	p := writeFile(t, dir, "b.yaml", "extends: a.yaml\n")
	if _, err := Load("", p, nil); err == nil {
		t.Error("expected cycle error")
	}
}

func TestLoad_UnknownKeyRejected(t *testing.T) {
	p := writeFile(t, t.TempDir(), "typo.yaml", "extends: minimal\nssh:\n  disable_pasword_auth: true\n")
	_, err := Load("", p, nil)
	if err == nil || !strings.Contains(err.Error(), "disable_pasword_auth") {
		t.Errorf("expected unknown field error, got %v", err)
	}
}

func TestValidate(t *testing.T) {
	cfg := &Config{
		SSH:   SSHConfig{Port: 70000},
		Users: UsersConfig{HomeBase: "raid/home"},
		Modules: ModulesConfig{
			Nvidia:      NvidiaConfig{GPUAllocation: GPUAllocationConfig{Method: "magic"}},
			Cloudflared: CloudflaredConfig{PrivateNetwork: PrivateNetworkConfig{Address: "172.16.0.1"}},
			Storage:     StorageConfig{Symlinks: map[string]string{"/": "/raid"}},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	for _, want := range []string{"ssh.port", "home_base", "method", "CIDR", "refusing"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q:\n%v", want, err)
		}
	}
}

func TestAllProfilesValid(t *testing.T) {
	for _, name := range AvailableProfiles() {
		if _, err := Load(name, "", nil); err != nil {
			t.Errorf("profile %s: %v", name, err)
		}
	}
}

func TestApplyEnvOverrides(t *testing.T) {
	os.Setenv("ROOTFILES_HOME_BASE", "/custom/home")
	os.Setenv("ROOTFILES_TIMEZONE", "UTC")
	os.Setenv("ROOTFILES_TUNNEL_TOKEN", "test-token")
	os.Setenv("ROOTFILES_VLAN_ADDRESS", "10.0.0.1/32")
	defer func() {
		os.Unsetenv("ROOTFILES_HOME_BASE")
		os.Unsetenv("ROOTFILES_TIMEZONE")
		os.Unsetenv("ROOTFILES_TUNNEL_TOKEN")
		os.Unsetenv("ROOTFILES_VLAN_ADDRESS")
	}()

	cfg := &Config{}
	applyEnvOverrides(cfg)

	if cfg.Users.HomeBase != "/custom/home" {
		t.Errorf("home_base = %q, want /custom/home", cfg.Users.HomeBase)
	}
	if cfg.Timezone != "UTC" {
		t.Errorf("timezone = %q, want UTC", cfg.Timezone)
	}
	if cfg.Modules.Cloudflared.TunnelToken != "test-token" {
		t.Errorf("tunnel_token = %q, want test-token", cfg.Modules.Cloudflared.TunnelToken)
	}
	if cfg.Modules.Cloudflared.PrivateNetwork.Address != "10.0.0.1/32" {
		t.Errorf("vlan_address = %q, want 10.0.0.1/32", cfg.Modules.Cloudflared.PrivateNetwork.Address)
	}
}

func TestAllPackages(t *testing.T) {
	cfg := &Config{
		Packages:      []string{"git", "curl"},
		PackagesExtra: []string{"docker-ce", "curl"}, // duplicate curl
	}
	all := cfg.AllPackages()
	if len(all) != 3 { // git, curl, docker-ce (deduped)
		t.Errorf("AllPackages() returned %d, want 3", len(all))
	}
}

func TestIsModuleEnabled(t *testing.T) {
	cfg := &Config{
		Modules: ModulesConfig{
			Locale: ModuleToggle{Enabled: true},
			Docker: DockerConfig{Enabled: false},
			Nvidia: NvidiaConfig{
				Enabled:       true,
				GPUAllocation: GPUAllocationConfig{Enabled: true},
			},
		},
	}
	if !cfg.IsModuleEnabled("locale") {
		t.Error("locale should be enabled")
	}
	if cfg.IsModuleEnabled("docker") {
		t.Error("docker should not be enabled")
	}
	if cfg.IsModuleEnabled("unknown") {
		t.Error("unknown module should not be enabled")
	}
	if !cfg.IsModuleEnabled("nvidia") {
		t.Error("nvidia should be enabled")
	}
	if !cfg.IsModuleEnabled("gpu") {
		t.Error("gpu should be enabled when gpu_allocation is enabled")
	}

	// GPU disabled when allocation not enabled
	cfg2 := &Config{
		Modules: ModulesConfig{
			Nvidia: NvidiaConfig{Enabled: true},
		},
	}
	if cfg2.IsModuleEnabled("gpu") {
		t.Error("gpu should not be enabled when gpu_allocation is not enabled")
	}
}

func TestLoad_TunnelTokenFile(t *testing.T) {
	dir := t.TempDir()
	both := writeFile(t, dir, "both.yaml", "extends: minimal\nmodules:\n  cloudflared:\n    enabled: true\n    tunnel_token: inline\n    tunnel_token_file: /etc/rootfiles/tunnel-token\n")
	if _, err := Load("", both, nil); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("tunnel_token with tunnel_token_file: want 'not both' error, got %v", err)
	}

	rel := writeFile(t, dir, "rel.yaml", "extends: minimal\nmodules:\n  cloudflared:\n    enabled: true\n    tunnel_token_file: tunnel-token\n")
	if _, err := Load("", rel, nil); err == nil || !strings.Contains(err.Error(), "tunnel_token_file") {
		t.Errorf("relative tunnel_token_file: want path error, got %v", err)
	}

	// The file is read at check/apply time, so a path that only exists on
	// the server still validates on an operator laptop.
	ok := writeFile(t, dir, "ok.yaml", "extends: minimal\nmodules:\n  cloudflared:\n    enabled: true\n    tunnel_token_file: /nonexistent/rootfiles/tunnel-token\n")
	cfg, err := Load("", ok, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Modules.Cloudflared.TunnelTokenFile != "/nonexistent/rootfiles/tunnel-token" || cfg.Modules.Cloudflared.TunnelToken != "" {
		t.Errorf("unexpected cloudflared config %+v", cfg.Modules.Cloudflared)
	}

	// ROOTFILES_TUNNEL_TOKEN may still override a file-based config.
	t.Setenv("ROOTFILES_TUNNEL_TOKEN", "from-env")
	cfg, err = Load("", ok, nil)
	if err != nil || cfg.Modules.Cloudflared.TunnelToken != "from-env" {
		t.Errorf("env override with tunnel_token_file: token=%q err=%v", cfg.Modules.Cloudflared.TunnelToken, err)
	}
}

func TestValidate_PasswordAuthUsers(t *testing.T) {
	cases := map[string][]string{
		"root":      {"root"},
		"bad name":  {"Bob Smith"},
		"duplicate": {"bob", "bob"},
	}
	for name, users := range cases {
		cfg := &Config{SSH: SSHConfig{DisablePasswordAuth: true, PasswordAuthUsers: users}}
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "password_auth_users") {
			t.Errorf("%s: want password_auth_users error, got %v", name, err)
		}
	}
	ok := &Config{SSH: SSHConfig{DisablePasswordAuth: true, PasswordAuthUsers: []string{"bob", "carol.k"}}}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid exception list rejected: %v", err)
	}
}

// TestMain keeps home-base detection off the host for every Load here.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "rootfiles-config-test")
	if err != nil {
		panic(err)
	}
	useraddDefaultsPath = filepath.Join(dir, "useradd")
	hostRoot = filepath.Join(dir, "root")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// isolateHomeDetection points home-base detection at a temp host root owned
// by the test user, who stands in for root. /home, /raid, /data and /nvme
// exist with mode 0755, and every mount is on a different device than /.
// managed, when set, is a home base that already holds .rootfiles. It
// returns the host root.
func isolateHomeDetection(t *testing.T, useradd, managed string) string {
	t.Helper()
	dir := t.TempDir()
	oldUseradd, oldRoot, oldUID, oldDev := useraddDefaultsPath, hostRoot, rootUID, deviceOf
	t.Cleanup(func() { useraddDefaultsPath, hostRoot, rootUID, deviceOf = oldUseradd, oldRoot, oldUID, oldDev })
	useraddDefaultsPath = filepath.Join(dir, "useradd")
	hostRoot = filepath.Join(dir, "root")
	rootUID = uint32(os.Getuid())
	// Every path but / stands on a separate device.
	deviceOf = func(path string) (uint64, bool) {
		if path == "/" {
			return 1, true
		}
		return 2, true
	}
	mkdir := func(rel string) {
		t.Helper()
		p := filepath.Join(hostRoot, rel)
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{"/", "/home", "/raid", "/data", "/nvme"} {
		mkdir(d)
	}
	if useradd != "" {
		if err := os.WriteFile(useraddDefaultsPath, []byte(useradd), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if managed != "" {
		mkdir(managed)
		mkdir(filepath.Join(managed, ".rootfiles"))
	}
	return hostRoot
}

func TestDefaultHomeBase(t *testing.T) {
	mounts := func(m ...MountPoint) *SystemInfo { return &SystemInfo{OS: "ubuntu", StorageLayout: m} }
	data := MountPoint{Device: "/dev/sdb1", MountPath: "/data", FSType: "ext4"}
	cases := []struct {
		name    string
		sys     *SystemInfo
		useradd string
		managed string
		want    string
	}{
		{"no system info", nil, "", "", "/home"},
		{"no data drive", mounts(), "", "", "/home"},
		{"data drive", mounts(data), "", "", "/data/home"},
		{"raid preferred over data", mounts(data, MountPoint{Device: "/dev/md0", MountPath: "/raid", FSType: "xfs"}), "", "", "/raid/home"},
		{"network filesystem skipped", mounts(MountPoint{Device: "nas:/x", MountPath: "/data", FSType: "nfs4"}), "", "", "/home"},
		{"ephemeral /mnt skipped", mounts(MountPoint{Device: "/dev/sdb1", MountPath: "/mnt", FSType: "ext4"}), "", "", "/home"},
		{"nested mount skipped", mounts(MountPoint{Device: "/dev/sdb1", MountPath: "/data/ssd", FSType: "ext4"}), "", "", "/home"},
		{"custom useradd HOME kept", mounts(data), "SHELL=/bin/sh\nHOME=/srv/home\n", "", "/srv/home"},
		{"useradd HOME=/home kept", mounts(data), "HOME=/home\n", "", "/home"},
		{"stale custom HOME rewritten to /home kept", mounts(data), "HOME=/data/home\nHOME=/home\n", "", "/home"},
		{"commented useradd HOME ignored", mounts(data), "# HOME=/srv/home\n", "", "/data/home"},
		{"users already managed under /home", mounts(data), "", "/home", "/home"},
		{"users managed under old gpu-server pin", mounts(), "", "/data/home", "/data/home"},
		{"dgx os data drive", &SystemInfo{OS: "dgx-os", StorageLayout: []MountPoint{data}}, "", "", "/data/home"},
		{"rocky keeps /home", &SystemInfo{OS: "rocky", Version: "9.4", StorageLayout: []MountPoint{data}}, "", "", "/home"},
		{"useradd HOME cleaned", mounts(data), "HOME=/srv/home/\n", "", "/srv/home"},
		{"relative useradd HOME ignored", mounts(data), "HOME=home\n", "", "/data/home"},
		{"indented HOME ignored like useradd", mounts(data), "  HOME=/srv/home\n", "", "/data/home"},
		{"unsupported distro keeps /home", &SystemInfo{OS: "debian", StorageLayout: []MountPoint{data}}, "", "", "/home"},
		{"read-write data drive", mounts(MountPoint{Device: "/dev/sdb1", MountPath: "/data", FSType: "ext4", Options: "rw,relatime"}), "", "", "/data/home"},
		{"read-only data drive skipped", mounts(MountPoint{Device: "/dev/sdb1", MountPath: "/data", FSType: "ext4", Options: "ro,relatime"}), "", "", "/home"},
		{"read-only remount wins", mounts(data, MountPoint{Device: "/dev/sdb1", MountPath: "/data", FSType: "ext4", Options: "ro"}), "", "", "/home"},
		{"read-only nested home skipped", mounts(data, MountPoint{Device: "/dev/sdc1", MountPath: "/data/home", FSType: "ext4", Options: "ro"}), "", "", "/home"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateHomeDetection(t, tc.useradd, tc.managed)
			if got := defaultHomeBase(tc.sys); got != tc.want {
				t.Errorf("defaultHomeBase = %q, want %q", got, tc.want)
			}
		})
	}
}

// AC2b: a base an unprivileged user could control is never chosen.
func TestDefaultHomeBase_UntrustedBases(t *testing.T) {
	sys := &SystemInfo{OS: "ubuntu", StorageLayout: []MountPoint{{Device: "/dev/sdb1", MountPath: "/data", FSType: "ext4"}}}
	cases := []struct {
		name    string
		managed string
		setup   func(t *testing.T, root string)
	}{
		{"world-writable mount root", "", func(t *testing.T, root string) {
			chmod(t, filepath.Join(root, "data"), 0o1777)
		}},
		{"pre-created group-writable base", "", func(t *testing.T, root string) {
			mkdirMode(t, filepath.Join(root, "data", "home"), 0o775)
		}},
		{"base is a symlink", "", func(t *testing.T, root string) {
			target := t.TempDir()
			if err := os.Symlink(target, filepath.Join(root, "data", "home")); err != nil {
				t.Fatal(err)
			}
		}},
		{"not owned by root", "", func(t *testing.T, root string) {
			rootUID = uint32(os.Getuid()) + 1
		}},
		{"managed base in world-writable mount", "/data/home", func(t *testing.T, root string) {
			chmod(t, filepath.Join(root, "data"), 0o1777)
		}},
		{"world-writable host root", "", func(t *testing.T, root string) {
			chmod(t, root, 0o777)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := isolateHomeDetection(t, "", tc.managed)
			tc.setup(t, root)
			if got := defaultHomeBase(sys); got != "/home" {
				t.Errorf("defaultHomeBase = %q, want /home", got)
			}
		})
	}
}

// AC4b: metadata under another base stops detection instead of guessing.
func TestLoad_AmbiguousHomeBase(t *testing.T) {
	sys := &SystemInfo{OS: "dgx-os"}
	writeMeta := func(t *testing.T, root, base, name string) {
		t.Helper()
		dir := filepath.Join(root, base, ".rootfiles")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		chmod(t, filepath.Join(root, base), 0755)
		chmod(t, dir, 0755)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"users.json", "gpu-allocations.json"} {
		t.Run(name, func(t *testing.T) {
			root := isolateHomeDetection(t, "HOME=/raid/home\n", "")
			t.Setenv("ROOTFILES_HOME_BASE", "")
			writeMeta(t, root, "/home", name)
			_, err := Load("minimal", "", sys)
			if !errors.Is(err, ErrAmbiguousHomeBase) || !strings.Contains(err.Error(), "/home/.rootfiles/"+name) {
				t.Fatalf("Load error = %v, want ambiguous home base naming %s", err, name)
			}
			cfg, err := LoadWithHomeBase("minimal", "", sys, "/raid/home")
			if err != nil || cfg.Users.HomeBase != "/raid/home" {
				t.Errorf("--home-base: got %v, err=%v", cfg, err)
			}
			t.Setenv("ROOTFILES_HOME_BASE", "/home")
			if cfg, err = Load("minimal", "", sys); err != nil || cfg.Users.HomeBase != "/home" {
				t.Errorf("env override: got %v, err=%v", cfg, err)
			}
		})
	}
	t.Run("metadata only under the detected base", func(t *testing.T) {
		root := isolateHomeDetection(t, "HOME=/raid/home\n", "")
		t.Setenv("ROOTFILES_HOME_BASE", "")
		mkdirMode(t, filepath.Join(root, "raid", "home"), 0755)
		writeMeta(t, root, "/raid/home", "users.json")
		if cfg, err := Load("minimal", "", sys); err != nil || cfg.Users.HomeBase != "/raid/home" {
			t.Errorf("got %v, err=%v", cfg, err)
		}
	})
	t.Run("legacy flat .rootfiles file", func(t *testing.T) {
		root := isolateHomeDetection(t, "HOME=/raid/home\n", "")
		t.Setenv("ROOTFILES_HOME_BASE", "")
		if err := os.WriteFile(filepath.Join(root, "home", ".rootfiles"), []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load("minimal", "", sys); !errors.Is(err, ErrAmbiguousHomeBase) {
			t.Errorf("Load error = %v, want ambiguous home base", err)
		}
	})
	t.Run("untrusted base ignored", func(t *testing.T) {
		root := isolateHomeDetection(t, "", "")
		t.Setenv("ROOTFILES_HOME_BASE", "")
		writeMeta(t, root, "/data/home", "users.json")
		chmod(t, filepath.Join(root, "data"), 0o1777)
		if cfg, err := Load("minimal", "", sys); err != nil || cfg.Users.HomeBase != "/home" {
			t.Errorf("got %v, err=%v", cfg, err)
		}
	})
}

// AC1: a bind mount of the root filesystem is not a data drive. The test
// host root and its /data share a device, as such a bind mount would.
func TestDefaultHomeBase_SameDeviceAsRoot(t *testing.T) {
	isolateHomeDetection(t, "", "")
	sys := &SystemInfo{OS: "ubuntu", StorageLayout: []MountPoint{{Device: "/dev/sda1", MountPath: "/data", FSType: "ext4"}}}
	deviceOf = statDevice
	if got := defaultHomeBase(sys); got != "/home" {
		t.Errorf("same device as /: got %q, want /home", got)
	}
	deviceOf = func(string) (uint64, bool) { return 0, false }
	if got := defaultHomeBase(sys); got != "/home" {
		t.Errorf("unreadable device: got %q, want /home", got)
	}

	// A mount at <mount>/home holds the homes, so its device decides.
	nested := &SystemInfo{OS: "ubuntu", StorageLayout: []MountPoint{
		{Device: "/dev/sdb1", MountPath: "/data", FSType: "ext4"},
		{Device: "/dev/sda1", MountPath: "/data/home", FSType: "ext4"},
	}}
	for _, tc := range []struct {
		name, rootDevPath, want string
	}{
		{"root bind mounted at /data/home", "/data/home", "/home"},
		{"separate /data/home on a root bind at /data", "/data", "/data/home"},
	} {
		deviceOf = func(path string) (uint64, bool) {
			if path == "/" || path == tc.rootDevPath {
				return 1, true
			}
			return 2, true
		}
		if got := defaultHomeBase(nested); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// #37: detection keeps the strict rule for a symlinked /home, so /home and
// its target are never two managed bases with the same metadata.
func TestDetectHomeBase_SymlinkedHomeIsNotManaged(t *testing.T) {
	root := isolateHomeDetection(t, "", "/raid/home")
	t.Setenv("ROOTFILES_HOME_BASE", "")
	if err := os.Remove(filepath.Join(root, "home")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("raid/home", filepath.Join(root, "home")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "raid", "home", ".rootfiles", "users.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load("minimal", "", &SystemInfo{OS: "ubuntu"})
	if err != nil || cfg.Users.HomeBase != "/raid/home" {
		t.Fatalf("got %v, err=%v; want /raid/home", cfg, err)
	}
}

// #38: a btrfs subvolume of the root filesystem or a ZFS dataset in the root
// pool has its own st_dev but is not a data drive.
func TestDefaultHomeBase_RootFilesystemSubvolumes(t *testing.T) {
	mp := func(dev, path, fs string) MountPoint { return MountPoint{Device: dev, MountPath: path, FSType: fs} }
	cases := []struct {
		name   string
		root   MountPoint
		mounts []MountPoint
		want   string
	}{
		{"btrfs subvolume of root", mp("/dev/sda2", "/", "btrfs"), []MountPoint{mp("/dev/sda2", "/data", "btrfs")}, "/home"},
		{"separate btrfs", mp("/dev/sda2", "/", "btrfs"), []MountPoint{mp("/dev/sdb1", "/data", "btrfs")}, "/data/home"},
		{"zfs dataset in the root pool", mp("rpool/ROOT/ubuntu", "/", "zfs"), []MountPoint{mp("rpool/data", "/data", "zfs")}, "/home"},
		{"zfs dataset in another pool", mp("rpool/ROOT/ubuntu", "/", "zfs"), []MountPoint{mp("tank/data", "/data", "zfs")}, "/data/home"},
		{"btrfs data drive on an ext4 root", mp("/dev/sda2", "/", "ext4"), []MountPoint{mp("/dev/sdb1", "/data", "btrfs")}, "/data/home"},
		{"nested home subvolume of root", mp("/dev/sda2", "/", "btrfs"), []MountPoint{mp("/dev/sdb1", "/data", "btrfs"), mp("/dev/sda2", "/data/home", "btrfs")}, "/home"},
	}
	// Two names of one device, as /dev/disk/by-uuid/... and /dev/sda2 are.
	dev := filepath.Join(t.TempDir(), "sda2")
	if err := os.WriteFile(dev, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	byUUID := filepath.Join(t.TempDir(), "by-uuid")
	if err := os.Symlink(dev, byUUID); err != nil {
		t.Fatal(err)
	}
	cases = append(cases, struct {
		name   string
		root   MountPoint
		mounts []MountPoint
		want   string
	}{"btrfs subvolume of root under another device name", mp(byUUID, "/", "btrfs"), []MountPoint{mp(dev, "/data", "btrfs")}, "/home"})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateHomeDetection(t, "", "")
			root := tc.root
			sys := &SystemInfo{OS: "ubuntu", RootMount: &root, StorageLayout: tc.mounts}
			if got := defaultHomeBase(sys); got != tc.want {
				t.Errorf("defaultHomeBase = %q, want %q", got, tc.want)
			}
		})
	}
}

// AC2 relies on detectStorage keeping the mount options.
func TestDetectStorage_KeepsMountOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mounts")
	mounts := "/dev/sda2 / btrfs rw 0 0\n/dev/sdb1 /data ext4 rw,relatime 0 0\n/dev/sdc1 /raid xfs ro,noatime 0 0\nproc /proc proc rw 0 0\n"
	if err := os.WriteFile(path, []byte(mounts), 0o644); err != nil {
		t.Fatal(err)
	}
	old := procMountsPath
	procMountsPath = path
	t.Cleanup(func() { procMountsPath = old })
	var info SystemInfo
	detectStorage(&info)
	want := []MountPoint{
		{Device: "/dev/sdb1", MountPath: "/data", FSType: "ext4", Options: "rw,relatime"},
		{Device: "/dev/sdc1", MountPath: "/raid", FSType: "xfs", Options: "ro,noatime"},
	}
	if !reflect.DeepEqual(info.StorageLayout, want) {
		t.Errorf("StorageLayout = %+v, want %+v", info.StorageLayout, want)
	}
	if r := info.RootMount; r == nil || r.Device != "/dev/sda2" || r.FSType != "btrfs" {
		t.Errorf("RootMount = %+v, want /dev/sda2 btrfs", r)
	}
}

func TestDefaultHomeBase_LastMountEntryWins(t *testing.T) {
	isolateHomeDetection(t, "", "")
	local := MountPoint{Device: "/dev/sdb1", MountPath: "/data", FSType: "ext4"}
	nfs := MountPoint{Device: "nas:/x", MountPath: "/data", FSType: "nfs4"}
	if got := defaultHomeBase(&SystemInfo{OS: "ubuntu", StorageLayout: []MountPoint{local, nfs}}); got != "/home" {
		t.Errorf("local disk under NFS: got %q, want /home", got)
	}
	if got := defaultHomeBase(&SystemInfo{OS: "ubuntu", StorageLayout: []MountPoint{nfs, local}}); got != "/data/home" {
		t.Errorf("NFS under local disk: got %q, want /data/home", got)
	}
	nestedNFS := MountPoint{Device: "nas:/home", MountPath: "/data/home", FSType: "nfs4"}
	if got := defaultHomeBase(&SystemInfo{OS: "ubuntu", StorageLayout: []MountPoint{local, nestedNFS}}); got != "/home" {
		t.Errorf("NFS mounted at /data/home: got %q, want /home", got)
	}
}

func chmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func mkdirMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Mkdir(path, mode); err != nil {
		t.Fatal(err)
	}
	chmod(t, path, mode)
}

func TestLoad_HomeBaseDetectionOnlyWhenUnset(t *testing.T) {
	isolateHomeDetection(t, "", "")
	t.Setenv("ROOTFILES_HOME_BASE", "")
	sys := &SystemInfo{OS: "ubuntu", StorageLayout: []MountPoint{{Device: "/dev/sdb1", MountPath: "/data", FSType: "xfs"}}}

	cfg, err := Load("minimal", "", sys)
	if err != nil || cfg.Users.HomeBase != "/data/home" {
		t.Fatalf("unset home_base: got %q, err=%v; want /data/home", cfg.Users.HomeBase, err)
	}

	explicit := filepath.Join(t.TempDir(), "site.yaml")
	if err := os.WriteFile(explicit, []byte("extends: minimal\nusers:\n  home_base: /home\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if cfg, err = Load("", explicit, sys); err != nil || cfg.Users.HomeBase != "/home" {
		t.Errorf("explicit home_base: got %q, err=%v; want /home", cfg.Users.HomeBase, err)
	}

	t.Setenv("ROOTFILES_HOME_BASE", "/env/home")
	if cfg, err = Load("minimal", "", sys); err != nil || cfg.Users.HomeBase != "/env/home" {
		t.Errorf("env override: got %q, err=%v; want /env/home", cfg.Users.HomeBase, err)
	}
}

func TestLoadSite_RequiresExplicitHomeBase(t *testing.T) {
	site := filepath.Join(t.TempDir(), "site.yaml")
	if err := os.WriteFile(site, []byte("extends: minimal\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSite(site); err == nil || !strings.Contains(err.Error(), "users.home_base") {
		t.Errorf("site config without home_base: want users.home_base error, got %v", err)
	}
}

func TestLoad_DoctorDiskThresholds(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "site.yaml", "extends: minimal\ndoctor:\n  disk:\n    fail_percent: 95\n    min_free: 100GiB\n")
	cfg, err := Load("", p, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	warn, fail, minFree := cfg.Doctor.Disk.Thresholds()
	if warn != 80 || fail != 95 || minFree != 100<<30 {
		t.Fatalf("thresholds = %v %v %v", warn, fail, minFree)
	}
	for _, bad := range []string{"min_free: lots", "min_free: 100GB", "fail_percent: 120", "warn_percent: 95\n    fail_percent: 90", "warn_percent: -1"} {
		p := writeFile(t, dir, "bad.yaml", "extends: minimal\ndoctor:\n  disk:\n    "+bad+"\n")
		cfg, err := Load("", p, nil)
		if err == nil {
			err = cfg.Validate()
		}
		if err == nil || !strings.Contains(err.Error(), "doctor.disk") {
			t.Errorf("%q should be rejected, got %v", bad, err)
		}
	}
	// Unset thresholds must not change existing fingerprints.
	plain, err := Load("minimal", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := plain.Effective(); strings.Contains(string(data), "doctor") {
		t.Fatalf("an unset doctor block leaked into the effective config:\n%s", data)
	}
}
