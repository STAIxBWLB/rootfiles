package module

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/exec"
)

type agreementPackageManager struct {
	installed map[string]bool
	installs  [][]string
	updates   int
	onInstall func([]string) error
}

func (p *agreementPackageManager) Update(context.Context) error {
	p.updates++
	return nil
}
func (p *agreementPackageManager) Install(_ context.Context, packages []string) error {
	p.installs = append(p.installs, append([]string(nil), packages...))
	if p.installed == nil {
		p.installed = map[string]bool{}
	}
	for _, pkg := range packages {
		p.installed[pkg] = true
	}
	if p.onInstall != nil {
		return p.onInstall(packages)
	}
	return nil
}
func (p *agreementPackageManager) IsInstalled(pkg string) bool { return p.installed[pkg] }
func (p *agreementPackageManager) Installed(packages []string) map[string]bool {
	out := map[string]bool{}
	for _, pkg := range packages {
		out[pkg] = p.installed[pkg]
	}
	return out
}
func (*agreementPackageManager) AddKeyring(context.Context, string, string) error    { return nil }
func (*agreementPackageManager) AddSourceList(context.Context, string, string) error { return nil }

func agreementCommand(t *testing.T, dir, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
}

func agreementQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func fakeDockerAgreementPaths(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	oldCompose, oldPlugins := dockerComposePath, composePluginPaths
	dockerComposePath = filepath.Join(dir, "docker-compose")
	composePluginPaths = []string{filepath.Join(dir, "compose-plugin"), filepath.Join(dir, "alternate-plugin")}
	t.Cleanup(func() { dockerComposePath, composePluginPaths = oldCompose, oldPlugins })
	withDaemonJSON(t, "")
	return dir
}

func TestAgreementInstallPlansMatchAppliedPackages(t *testing.T) {
	for _, tc := range []struct {
		name, distro, command string
		packages              []string
	}{
		{"docker", "ubuntu", "apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin", []string{"docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin", "docker-compose-plugin"}},
		{"nvidia", "ubuntu", "apt-get install -y nvidia-container-toolkit", []string{"nvidia-container-toolkit"}},
		{"security", "ubuntu", "apt-get install -y systemd-timesyncd", []string{"systemd-timesyncd"}},
		{"security", "rocky", "dnf install chrony", []string{"chrony"}},
	} {
		t.Run(tc.name+"/"+tc.distro, func(t *testing.T) {
			rc := newRealRC(t)
			dir := fakeDockerAgreementPaths(t)
			t.Setenv("PATH", dir)
			agreementCommand(t, dir, "systemctl", "case $1 in is-active) echo active;; is-enabled) echo enabled;; esac")
			agreementCommand(t, dir, "timedatectl", "echo yes")
			if err := os.WriteFile(dockerComposePath, []byte("compatibility fixture"), 0755); err != nil {
				t.Fatal(err)
			}
			rc.Config.System = &config.SystemInfo{OS: tc.distro, Version: "9.5"}
			pm := &agreementPackageManager{}
			rc.APT = pm
			var module Module
			switch tc.name {
			case "docker":
				module = NewDockerModule()
			case "nvidia":
				module = NewNvidiaModule()
			case "security":
				module = NewSecurityModule()
				rc.Config.Modules.Security.TimeSync = true
			}
			before, err := module.Check(context.Background(), rc)
			if err != nil || len(before.Changes) != 1 || before.Changes[0].Command != tc.command {
				t.Fatalf("install plan=%+v err=%v, want %q", before, err, tc.command)
			}
			applied, err := module.Apply(context.Background(), rc)
			if err != nil || !applied.Changed {
				t.Fatalf("install result=%+v err=%v", applied, err)
			}
			if !reflect.DeepEqual(pm.installs, [][]string{tc.packages}) {
				t.Fatalf("applied packages=%v, want %v", pm.installs, tc.packages)
			}
		})
	}
}

func TestAgreementTimeSyncPartialFailures(t *testing.T) {
	for _, tc := range []struct {
		name, query        string
		queryFail          bool
		unitFail, ntpFail  bool
		changed            bool
		warnings, messages int
	}{
		{"current", "yes", false, false, false, false, 0, 0},
		{"query failed", "", true, false, false, false, 1, 0},
		{"both failed", "no", false, true, true, false, 2, 0},
		{"unit succeeded", "no", false, false, true, true, 1, 1},
		{"ntp succeeded", "no", false, true, false, true, 1, 1},
		{"both succeeded", "no", false, false, false, true, 0, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rc := newRealRC(t)
			dir := t.TempDir()
			calls := filepath.Join(dir, "calls")
			t.Setenv("PATH", dir)
			unitStatus, ntpStatus, queryStatus := "0", "0", "0"
			if tc.unitFail {
				unitStatus = "1"
			}
			if tc.ntpFail {
				ntpStatus = "1"
			}
			if tc.queryFail {
				queryStatus = "1"
			}
			agreementCommand(t, dir, "systemctl", "printf 'systemctl %s\\n' \"$*\" >> "+agreementQuote(calls)+"\nexit "+unitStatus)
			agreementCommand(t, dir, "timedatectl", "if [ \"$1\" = show ]; then printf '%s' "+agreementQuote(tc.query)+"; exit "+queryStatus+"; fi\nprintf 'timedatectl %s\\n' \"$*\" >> "+agreementQuote(calls)+"\nexit "+ntpStatus)
			rc.APT = &agreementPackageManager{installed: map[string]bool{"chrony": true}}
			rc.Config.Modules.Security.TimeSync = true
			module := NewSecurityModule()
			planned, err := module.Check(context.Background(), rc)
			if err != nil {
				t.Fatal(err)
			}
			if tc.queryFail {
				if !planned.Satisfied || len(planned.Changes) != 0 {
					t.Fatalf("warning-only query failure blocks preview: %+v", planned)
				}
			} else if tc.query == "yes" {
				if !planned.Satisfied {
					t.Fatalf("current NTP reported drift: %+v", planned)
				}
			} else if len(planned.Changes) != 2 || planned.Changes[0].Command != "systemctl enable --now chrony" || planned.Changes[1].Command != "timedatectl set-ntp true" {
				t.Fatalf("time sync plan omits an applied command: %+v", planned)
			}
			applied, err := module.Apply(context.Background(), rc)
			if err != nil || applied.Changed != tc.changed || len(applied.Warnings) != tc.warnings || len(applied.Messages) != tc.messages {
				t.Fatalf("result=%+v err=%v, want changed=%t warnings=%d messages=%d", applied, err, tc.changed, tc.warnings, tc.messages)
			}
			got, _ := os.ReadFile(calls)
			want := ""
			if !tc.queryFail && tc.query != "yes" {
				want = "systemctl enable --now chrony\ntimedatectl set-ntp true\n"
			}
			if string(got) != want {
				t.Fatalf("applied commands=%q, want %q", got, want)
			}
		})
	}
}

func TestAgreementLocalePlansActualFileWrite(t *testing.T) {
	for _, distro := range []string{"ubuntu", "rocky"} {
		t.Run(distro, func(t *testing.T) {
			rc := newRealRC(t)
			dir := fakeLocaleFS(t)
			t.Setenv("PATH", dir)
			agreementCommand(t, dir, "locale-gen", "printf '%s' \"$*\" > "+agreementQuote(filepath.Join(dir, "generated")))
			agreementCommand(t, dir, "localectl", "printf 'en_US.utf8\\nC\\n'")
			rc.Config.System = &config.SystemInfo{OS: distro, Version: "9.5"}
			rc.Config.Locale = "en_US.UTF-8"
			rc.APT = &agreementPackageManager{}
			path := localeConfigPath(rc)
			if err := os.WriteFile(path, []byte("LANG=C\nLC_ALL=C\nKEEP=1\n"), 0644); err != nil {
				t.Fatal(err)
			}
			module := NewLocaleModule()
			planned, err := module.Check(context.Background(), rc)
			if err != nil {
				t.Fatal(err)
			}
			writes := false
			for _, change := range planned.Changes {
				if strings.Contains(change.Command, "update-locale") {
					t.Fatalf("fictitious command: %+v", planned)
				}
				writes = writes || change.Command == "write "+path
			}
			if !writes {
				t.Fatalf("locale write not planned: %+v", planned)
			}
			applied, err := module.Apply(context.Background(), rc)
			if err != nil || !applied.Changed {
				t.Fatalf("Apply=%+v err=%v", applied, err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "LANG=en_US.UTF-8\nKEEP=1\n" {
				t.Fatalf("locale file=%q err=%v", data, err)
			}
			if after, err := module.Check(context.Background(), rc); err != nil || !after.Satisfied {
				t.Fatalf("Check after Apply=%+v err=%v", after, err)
			}
			if again, err := module.Apply(context.Background(), rc); err != nil || again.Changed {
				t.Fatalf("idempotent Apply=%+v err=%v", again, err)
			}
		})
	}
}

func TestAgreementDockerServiceOnlyActsOnDrift(t *testing.T) {
	for _, tc := range []struct {
		name         string
		active, fail bool
		changed      bool
		warnings     int
	}{
		{"current", true, false, false, 0}, {"start", false, false, true, 0}, {"failed start", false, true, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rc := newRealRC(t)
			dir := fakeDockerAgreementPaths(t)
			t.Setenv("PATH", dir)
			agreementCommand(t, dir, "docker", "exit 0")
			calls := filepath.Join(dir, "service-calls")
			activeScript, fail := "exit 1", "0"
			if tc.active {
				activeScript = "echo active; exit 0"
			}
			if tc.fail {
				fail = "1"
			}
			agreementCommand(t, dir, "systemctl", "case $1 in is-active) "+activeScript+";; is-enabled) echo enabled; exit 0;; esac\nprintf '%s\\n' \"$*\" >> "+agreementQuote(calls)+"\nexit "+fail)
			if err := os.WriteFile(dockerComposePath, []byte("compat fixture"), 0755); err != nil {
				t.Fatal(err)
			}
			module := NewDockerModule()
			planned, err := module.Check(context.Background(), rc)
			if err != nil {
				t.Fatal(err)
			}
			if planned.Satisfied != tc.active || !tc.active && (len(planned.Changes) != 1 || planned.Changes[0].Command != "systemctl enable --now docker") {
				t.Fatalf("service plan=%+v", planned)
			}
			applied, err := module.Apply(context.Background(), rc)
			if err != nil || applied.Changed != tc.changed || len(applied.Warnings) != tc.warnings {
				t.Fatalf("Apply=%+v err=%v", applied, err)
			}
			data, _ := os.ReadFile(calls)
			want := ""
			if !tc.active {
				want = "enable --now docker\n"
			}
			if string(data) != want {
				t.Fatalf("service calls=%q, want %q", data, want)
			}
		})
	}
}

func TestAgreementNetworkInstallAndRulesMatchApply(t *testing.T) {
	rc := newRealRC(t)
	dir := t.TempDir()
	calls := filepath.Join(dir, "ufw-calls")
	t.Setenv("PATH", dir)
	rc.Config.SSH.Port = 22
	rc.Config.Modules.Network.UFW = true
	pm := &agreementPackageManager{onInstall: func(_ []string) error {
		agreementCommand(t, dir, "ufw", "if [ \"$1\" = status ]; then echo 'Status: inactive'; exit 0; fi\nprintf '%s\\n' \"$*\" >> "+agreementQuote(calls))
		return nil
	}}
	rc.APT = pm
	module := NewNetworkModule()
	planned, err := module.Check(context.Background(), rc)
	if err != nil {
		t.Fatal(err)
	}
	var commands []string
	for _, change := range planned.Changes {
		commands = append(commands, change.Command)
	}
	want := []string{"apt-get install -y ufw", "ufw allow 22/tcp", "ufw --force enable"}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("network plan=%v, want %v", commands, want)
	}
	applied, err := module.Apply(context.Background(), rc)
	if err != nil || !applied.Changed {
		t.Fatalf("Apply=%+v err=%v", applied, err)
	}
	if !reflect.DeepEqual(pm.installs, [][]string{{"ufw"}}) {
		t.Fatalf("installed packages=%v", pm.installs)
	}
	data, _ := os.ReadFile(calls)
	if string(data) != "allow 22/tcp\n--force enable\n" {
		t.Fatalf("UFW commands=%q", data)
	}
}

func TestAgreementExporterInstallUsesBackendCommand(t *testing.T) {
	rc := newDryRunRC(t)
	t.Setenv("PATH", t.TempDir())
	rc.APT = &agreementPackageManager{}
	rc.Config.System = &config.SystemInfo{OS: "ubuntu"}
	rc.Config.Modules.Monitoring.NodeExporter = true
	planned, err := checkMonitoringExporters(context.Background(), rc)
	if err != nil || len(planned) != 1 || planned[0].Command != "apt-get install -y prometheus-node-exporter" {
		t.Fatalf("exporter plan=%+v err=%v", planned, err)
	}
}

func TestAgreementRockyDoesNotEnableVendorModules(t *testing.T) {
	for _, module := range []string{"docker", "nvidia", "network", "monitoring"} {
		cfg := &config.Config{}
		err := config.ValidateCapabilities(cfg, &config.SystemInfo{OS: "rocky", Version: "9.5"}, []string{module})
		if err == nil || !strings.Contains(err.Error(), module) {
			t.Fatalf("Rocky admitted unsupported module %s: %v", module, err)
		}
	}
}

func TestAgreementDockerConfigRestartAndComposeSource(t *testing.T) {
	rc := newRealRC(t)
	dir := fakeDockerAgreementPaths(t)
	t.Setenv("PATH", dir)
	agreementCommand(t, dir, "docker", "exit 0")
	calls := filepath.Join(dir, "service-calls")
	agreementCommand(t, dir, "systemctl", "case $1 in is-active) echo active; exit 0;; is-enabled) echo enabled; exit 0;; esac\nprintf '%s\\n' \"$*\" >> "+agreementQuote(calls))
	if err := os.WriteFile(composePluginPaths[1], []byte("plugin fixture"), 0755); err != nil {
		t.Fatal(err)
	}
	oldStorage := filepath.Join(dir, "old-docker")
	if err := os.Mkdir(oldStorage, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dockerDaemonJSONPath, []byte(`{"data-root":`+strconv.Quote(oldStorage)+`,"log-driver":"journald"}`), 0644); err != nil {
		t.Fatal(err)
	}
	rc.Config.Modules.Docker.StorageDir = filepath.Join(dir, "new-docker")
	module := NewDockerModule()
	planned, err := module.Check(context.Background(), rc)
	if err != nil {
		t.Fatal(err)
	}
	commands := map[string]bool{}
	for _, change := range planned.Changes {
		commands[change.Command] = true
	}
	for _, want := range []string{"ln -sf " + composePluginPaths[1] + " " + dockerComposePath, "merge into " + dockerDaemonJSONPath, "systemctl restart docker"} {
		if !commands[want] {
			t.Fatalf("Check omitted %q: %+v", want, planned)
		}
	}
	if commands["systemctl enable --now docker"] {
		t.Fatal("Check would enable an already current service")
	}
	applied, err := module.Apply(context.Background(), rc)
	if err != nil || !applied.Changed {
		t.Fatalf("Apply=%+v err=%v", applied, err)
	}
	if link, err := os.Readlink(dockerComposePath); err != nil || link != composePluginPaths[1] {
		t.Fatalf("compose source=%q err=%v", link, err)
	}
	if data, _ := os.ReadFile(calls); string(data) != "restart docker\n" {
		t.Fatalf("service commands=%q", data)
	}
	if after, err := module.Check(context.Background(), rc); err != nil || !after.Satisfied {
		t.Fatalf("Check after Apply=%+v err=%v", after, err)
	}
}

// Seed the state checked by v0.18.1 directly, without running the new Apply:
// Docker and its compatibility link exist, daemon keys match, NTP is enabled,
// and LANG already matches even when locale generation tools are absent.
func TestUpgradeFromV0181ConvergedModulesStayClean(t *testing.T) {
	rc := newRealRC(t)
	dir := fakeDockerAgreementPaths(t)
	fakeLocaleFS(t)
	t.Setenv("PATH", dir)
	actions := filepath.Join(dir, "unexpected-mutations")
	agreementCommand(t, dir, "docker", "exit 0")
	agreementCommand(t, dir, "systemctl", "case \"$1 $2\" in 'is-active docker') echo active;; 'is-enabled docker') echo enabled;; *) printf '%s\\n' \"$*\" >> "+agreementQuote(actions)+"; exit 1;; esac")
	agreementCommand(t, dir, "timedatectl", "case $1 in show) echo yes;; *) printf '%s\\n' \"$*\" >> "+agreementQuote(actions)+"; exit 1;; esac")
	rc.Config.System = &config.SystemInfo{OS: "ubuntu", Version: "22.04"}
	rc.Config.Locale = "en_US.UTF-8"
	rc.Config.Modules.Docker.StorageDir = filepath.Join(dir, "docker-data")
	rc.Config.Modules.Security.TimeSync = true
	pm := &agreementPackageManager{installed: map[string]bool{"chrony": true}}
	rc.APT = pm
	if err := os.WriteFile(composePluginPaths[0], []byte("installed compose plugin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(composePluginPaths[0], dockerComposePath); err != nil {
		t.Fatal(err)
	}
	daemon := []byte("{\n  \"data-root\": " + strconv.Quote(rc.Config.Modules.Docker.StorageDir) + ",\n  \"log-driver\": \"json-file\",\n  \"log-opts\": {\"max-size\":\"10m\",\"max-file\":\"3\"}\n}\n")
	locale := []byte("# existing operator configuration\nLANG=en_US.UTF-8\nKEEP=1\n")
	files := map[string][]byte{dockerDaemonJSONPath: daemon, defaultLocalePath: locale}
	before := map[string]os.FileInfo{}
	for name, data := range files {
		if err := os.WriteFile(name, data, 0644); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		before[name] = info
	}
	linkBefore, err := os.Lstat(dockerComposePath)
	if err != nil {
		t.Fatal(err)
	}
	if rc.Runner.CommandExists("locale-gen") {
		t.Fatal("fixture must omit locale generation tools")
	}
	for _, module := range []Module{NewDockerModule(), NewSecurityModule(), NewLocaleModule()} {
		check, err := module.Check(context.Background(), rc)
		if err != nil || !check.Satisfied || len(check.Changes) != 0 {
			t.Fatalf("%s adds upgrade-only drift: %+v, %v", module.Name(), check, err)
		}
		result, err := module.Apply(context.Background(), rc)
		if err != nil || result.Changed || len(result.Warnings) != 0 {
			t.Fatalf("%s mutates converged host: %+v, %v", module.Name(), result, err)
		}
	}
	for name, want := range files {
		data, err := os.ReadFile(name)
		if err != nil || string(data) != string(want) {
			t.Fatalf("rewrote %s: %q, %v", name, data, err)
		}
		info, err := os.Stat(name)
		if err != nil || !os.SameFile(before[name], info) || !before[name].ModTime().Equal(info.ModTime()) {
			t.Fatalf("replaced/touched %s", name)
		}
	}
	linkAfter, err := os.Lstat(dockerComposePath)
	if err != nil || !os.SameFile(linkBefore, linkAfter) {
		t.Fatalf("replaced existing compose link: %v", err)
	}
	if len(pm.installs) != 0 {
		t.Fatalf("installed packages on converged host: %v", pm.installs)
	}
	if _, err := os.Stat(actions); !os.IsNotExist(err) {
		t.Fatalf("ran upgrade-only mutations: %v", err)
	}
}

func TestAPTOnlyModulesRefuseBackendBeforeMutation(t *testing.T) {
	for _, name := range []string{"docker", "nvidia"} {
		for _, backend := range []string{"rpm", "packages-only"} {
			t.Run(name+"/"+backend, func(t *testing.T) {
				rc := newRealRC(t)
				dir := fakeDockerAgreementPaths(t)
				t.Setenv("PATH", dir)
				calls := filepath.Join(dir, "calls")
				for _, command := range []string{"docker", "rpm", "dnf", "apt-get", "curl", "systemctl", "nvidia-ctk"} {
					agreementCommand(t, dir, command, "printf called >> "+agreementQuote(calls))
				}
				before := []byte(`{"unmanaged":"preserve"}`)
				if err := os.WriteFile(dockerDaemonJSONPath, before, 0o644); err != nil {
					t.Fatal(err)
				}
				rc.Config.Modules.Docker.StorageDir = filepath.Join(dir, "new-docker-root")
				packages := &agreementPackageManager{}
				if backend == "rpm" {
					rc.APT = exec.NewRPM(rc.Runner)
					rc.Config.System = &config.SystemInfo{OS: "rocky", Version: "9.5"}
				} else {
					// Narrow the wrapper's method set to package operations, even
					// though the wrapped agreement fake can also configure APT.
					rc.APT = struct{ exec.PackageManager }{packages}
					rc.Config.System = &config.SystemInfo{OS: "ubuntu", Version: "24.04"}
				}
				queries := 0
				guard := rc.Runner.QueryGuard
				rc.Runner.QueryGuard = func(executable string) error {
					queries++
					return guard(executable)
				}
				var module Module = NewDockerModule()
				if name == "nvidia" {
					module = NewNvidiaModule()
				}
				result, err := module.Apply(context.Background(), rc)
				if err == nil || !strings.Contains(err.Error(), "APT repository support") || !strings.Contains(err.Error(), "disable modules."+name+".enabled") {
					t.Fatalf("Apply result=%+v error=%v, want cause and next step", result, err)
				}
				if packages.updates != 0 || len(packages.installs) != 0 {
					t.Errorf("refusal changed packages: updates=%d installs=%v", packages.updates, packages.installs)
				}
				if queries != 0 {
					t.Errorf("refusal queried %d commands before checking backend capability", queries)
				}
				if _, err := os.Stat(calls); !os.IsNotExist(err) {
					t.Fatalf("refused backend executed commands: %v", err)
				}
				after, err := os.ReadFile(dockerDaemonJSONPath)
				if err != nil || string(after) != string(before) {
					t.Fatalf("refusal changed daemon.json: %q, %v", after, err)
				}
				for _, file := range []string{dockerComposePath, rc.Config.Modules.Docker.StorageDir} {
					if _, err := os.Lstat(file); !os.IsNotExist(err) {
						t.Errorf("refusal created %s: %v", file, err)
					}
				}
			})
		}
	}
}
