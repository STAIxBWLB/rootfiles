package tenant

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/exec"
)

func stubCpusetHost(t *testing.T) {
	t.Helper()
	oldCPU, oldMems := onlineCPUsPath, memoryNodesPath
	t.Cleanup(func() { onlineCPUsPath, memoryNodesPath = oldCPU, oldMems })
	dir := t.TempDir()
	onlineCPUsPath, memoryNodesPath = filepath.Join(dir, "online"), filepath.Join(dir, "has_memory")
	for file, content := range map[string]string{onlineCPUsPath: "0-7,16-23\n", memoryNodesPath: "0-1\n"} {
		if err := os.WriteFile(file, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// Only a private docker is executable. Any unexpected ambient tool fails closed.
func cpusetManager(t *testing.T) (*Manager, string, *bytes.Buffer) {
	t.Helper()
	stubCpusetHost(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	t.Setenv("CPUSET_CALLS", log)
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$CPUSET_CALLS"
case "$1" in
inspect)
    [ -n "$CPUSET_INSPECT" ] || { echo 'container missing' >&2; exit 1; }
    printf '%s\n' "$CPUSET_INSPECT" ;;
update)
    if [ "$CPUSET_FAIL" = 1 ]; then echo 'cpuset update rejected by fixture' >&2; exit 1; fi ;;
exec) : ;;
*) echo "unexpected docker mutation: $*" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("CPUSET_FAIL", "")
	t.Setenv("CPUSET_INSPECT", "")
	out := &bytes.Buffer{}
	m := &Manager{Runner: exec.NewRunner(false, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))), Root: t.TempDir(), SpecDir: t.TempDir(), UnitDir: t.TempDir(), Out: out}
	m.Runner.QueryGuard = func(file string) error {
		if filepath.Dir(file) != dir {
			t.Fatalf("ambient query %s", file)
		}
		return nil
	}
	return m, log, out
}

func calls(t *testing.T, log string) string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func TestCpusetValidation(t *testing.T) {
	for _, value := range []string{"", "0", "0-3,16-23", "4,0-2,2-4", "2147483647"} {
		s := validSpec()
		s.Cpuset = &Cpuset{CPUs: value, Mems: value}
		if err := s.Validate(); err != nil {
			t.Errorf("%q: %v", value, err)
		}
	}
	for _, value := range []string{"-1", "1-0", "0,", ",0", "0,,1", "0-1-2", " 0", "0, 1", "1\n", "+1", "1.0", "2147483648", "9999999999999999999999999999999"} {
		s := validSpec()
		s.Cpuset = &Cpuset{CPUs: value, Mems: value}
		s.Image = ""
		err := s.Validate()
		if err == nil {
			t.Errorf("accepted %q", value)
			continue
		}
		for _, field := range []string{"cpuset.cpus", "cpuset.mems", "N or N-M", "image"} {
			if !strings.Contains(err.Error(), field) {
				t.Errorf("missing %s: %v", field, err)
			}
		}
	}
}

func TestCpusetLegacyGolden(t *testing.T) {
	s := validSpec()
	// Captured from the pre-cpuset JSON serializer; upgrade must retain it.
	const digest = "47f70c9332843d06"
	if s.Digest() != digest {
		t.Fatalf("legacy digest changed: %s", s.Digest())
	}
	m := &Manager{Root: "/raid/tenants"}
	want := []string{"run", "-d", "--name", "tenant-est", "--hostname", "est", "--restart=always", "--init", "--ipc=host", "--label", "rootfiles.tenant=est", "--label", "rootfiles.tenant.spec=" + digest, "--label", "rootfiles.tenant.gpus=2,3", "--runtime=nvidia", "--gpus", `"device=2,3"`, "-p", "3889:22", "-p", "8888:8888", "-p", "9000:9000/udp", "--mount", "type=bind,src=/raid/tenants/est/vol/data,dst=/data", "--mount", "type=bind,src=/raid/tenants/est/vol/home,dst=/home", "--mount", "type=bind,src=/raid/tenants/est/vol/tmp,dst=/tmp", "--mount", "type=bind,src=/raid/tenants/est/system,dst=/etc/tenant-system,readonly", "--entrypoint", "/bin/sh", "registry.local/est:1", "/etc/tenant-system/init.sh"}
	if got := m.RunArgs(s); !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy RunArgs changed: %v", got)
	}
	s.Cpuset = &Cpuset{}
	if s.Digest() != digest || !reflect.DeepEqual(m.RunArgs(s), want) {
		t.Fatal("empty cpuset changed legacy behavior")
	}
	for _, pin := range []Cpuset{{CPUs: "0-3"}, {Mems: "1"}, {CPUs: "0,2", Mems: "0-1"}} {
		s.Cpuset = &pin
		if s.Digest() != digest {
			t.Fatal("cpuset changed recreate digest")
		}
		got := strings.Join(m.RunArgs(s), " ")
		for _, flag := range []struct{ key, value string }{{"--cpuset-cpus", pin.CPUs}, {"--cpuset-mems", pin.Mems}} {
			if flag.value != "" && !strings.Contains(got, flag.key+" "+flag.value) {
				t.Errorf("missing %s: %s", flag.key, got)
			}
			if flag.value == "" && strings.Contains(got, flag.key) {
				t.Errorf("unmanaged flag %s: %s", flag.key, got)
			}
		}
	}
}

func TestCpusetConvergeContainer(t *testing.T) {
	for _, tc := range []struct {
		name, running string
		pin           *Cpuset
		current, want string
	}{
		{"live", "true", &Cpuset{CPUs: "2-3", Mems: "1"}, "0-1|0", "update --cpuset-cpus 2-3 --cpuset-mems 1 tenant-est\nexec tenant-est /bin/sh /etc/tenant-system/init.sh users\n"},
		{"matching", "true", &Cpuset{CPUs: "0-1", Mems: "0"}, "0,1|0", "exec tenant-est /bin/sh /etc/tenant-system/init.sh users\n"},
		{"unmanaged", "true", nil, "16-23|1", "exec tenant-est /bin/sh /etc/tenant-system/init.sh users\n"},
		{"empty", "true", &Cpuset{}, "16-23|1", "exec tenant-est /bin/sh /etc/tenant-system/init.sh users\n"},
		{"one-key", "true", &Cpuset{Mems: "1"}, "16-23|0", "update --cpuset-mems 1 tenant-est\nexec tenant-est /bin/sh /etc/tenant-system/init.sh users\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, log, _ := cpusetManager(t)
			s := validSpec()
			s.Cpuset = tc.pin
			t.Setenv("CPUSET_INSPECT", tc.running+"|"+s.Digest()+"|always|2,3|"+tc.current)
			if err := m.convergeContainer(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			lines := strings.SplitAfter(calls(t, log), "\n")
			if got := strings.Join(lines[1:], ""); got != tc.want {
				t.Fatalf("steps = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCpusetHostRefusesBeforeAnyApplyChange(t *testing.T) {
	for _, pin := range []Cpuset{{CPUs: "8"}, {CPUs: "0-16"}, {Mems: "2"}, {CPUs: "0", Mems: "99"}} {
		m, log, _ := cpusetManager(t)
		s := validSpec()
		s.Cpuset = &pin
		reserved := false
		m.ReserveGPUs = func(context.Context, string, []int) error { reserved = true; return nil }
		err := m.Apply(context.Background(), s)
		if err == nil || !strings.Contains(err.Error(), "available list") || !strings.Contains(err.Error(), "choose") {
			t.Fatalf("host validation: %v", err)
		}
		if reserved || calls(t, log) != "" {
			t.Fatal("mutated before host validation")
		}
		if files, _ := os.ReadDir(m.Root); len(files) > 0 {
			t.Fatal("wrote tenant data")
		}
	}
	m, log, _ := cpusetManager(t)
	s := validSpec()
	s.Cpuset = &Cpuset{CPUs: "0"}
	if err := os.Remove(onlineCPUsPath); err != nil {
		t.Fatal(err)
	}
	if err := m.Apply(context.Background(), s); err == nil || !strings.Contains(err.Error(), "sysfs") {
		t.Fatalf("missing sysfs accepted: %v", err)
	}
	if calls(t, log) != "" {
		t.Fatal("docker ran with missing sysfs")
	}
	// Unmanaged keys do not even read missing sysfs.
	if err := validateHostCpuset(nil); err != nil {
		t.Fatal(err)
	}
	if err := validateHostCpuset(&Cpuset{}); err != nil {
		t.Fatal(err)
	}
}

func TestChangeCpusetLiveOnlyRollbackAndClear(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		fail, clear, dry, missing bool
		digest, running           string
	}{
		{name: "pending", digest: "old", running: "true"},
		{name: "unlabelled", digest: "<no value>", running: "true"},
		{name: "stopped", digest: "old", running: "false"},
		{name: "failed", fail: true, digest: "old", running: "true"},
		{name: "missing", missing: true},
		{name: "clear", clear: true, digest: "old", running: "true"},
		{name: "dry", dry: true, digest: "old", running: "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, log, out := cpusetManager(t)
			s := validSpec()
			before, _ := s.Marshal()
			before = append([]byte("# retain this operator comment\n"), before...)
			file := filepath.Join(m.SpecDir, "est.yaml")
			if err := os.WriteFile(file, before, 0600); err != nil {
				t.Fatal(err)
			}
			s.Cpuset = &Cpuset{CPUs: "2-3", Mems: "1"}
			if !tc.missing {
				t.Setenv("CPUSET_INSPECT", tc.running+"|"+tc.digest+"|always|2,3|0-1|0")
			}
			if tc.fail {
				t.Setenv("CPUSET_FAIL", "1")
			}
			m.Runner.DryRun = tc.dry
			err := m.ChangeCpuset(context.Background(), s, tc.clear)
			after, _ := os.ReadFile(file)
			if tc.fail || tc.missing {
				if err == nil || !strings.Contains(err.Error(), "previous spec restored") {
					t.Fatalf("failure = %v", err)
				}
				if !bytes.Equal(before, after) {
					t.Fatal("spec was not restored byte for byte")
				}
				if tc.fail && !strings.Contains(err.Error(), "cpuset update rejected by fixture") {
					t.Fatalf("missing docker cause: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(out.String(), "rootfiles tenant apply est") {
					t.Fatalf("missing pending recreate message: %s", out)
				}
				if tc.dry {
					if !bytes.Equal(before, after) {
						t.Fatal("dry run changed spec")
					}
				} else {
					loaded, err := m.Load("est")
					if err != nil {
						t.Fatal(err)
					}
					if tc.clear && loaded.Cpuset != nil {
						t.Fatal("clear retained field")
					}
					if !tc.clear && (loaded.Cpuset == nil || *loaded.Cpuset != *s.Cpuset) {
						t.Fatal("pin not saved")
					}
				}
			}
			text := calls(t, log)
			for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
				if !strings.HasPrefix(line, "inspect ") && !strings.HasPrefix(line, "update --cpuset-") {
					t.Fatalf("non-live step: %s", line)
				}
			}
			if tc.clear && !strings.Contains(text, "update --cpuset-cpus 0-7,16-23 --cpuset-mems 0-1 tenant-est") {
				t.Fatalf("clear did not reset: %s", text)
			}
			if tc.dry && strings.Contains(text, "update ") {
				t.Fatal("dry run updated container")
			}
		})
	}
}

func TestCpusetStatusShowsLiveDriftAndUnmanagedPins(t *testing.T) {
	m, _, out := cpusetManager(t)
	s := validSpec()
	s.Cpuset = &Cpuset{CPUs: "2-3"}
	if err := m.SaveSpec(s); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPUSET_INSPECT", "true|"+s.Digest()+"|always|2,3|0-1|1")
	if err := m.Status(context.Background(), []string{"est"}, true); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"update", "cpuset spec: cpus=2-3 mems=unmanaged", `cpuset container: cpus="0-1" mems="1"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %s: %s", want, out)
		}
	}
	out.Reset()
	s.Cpuset = nil
	if err := m.SaveSpec(s); err != nil {
		t.Fatal(err)
	}
	if err := m.Status(context.Background(), []string{"est"}, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "current") || !strings.Contains(out.String(), `cpus="0-1"`) {
		t.Fatalf("unmanaged pin not visible/current: %s", out)
	}
}

func TestImportCarriesCpuset(t *testing.T) {
	stubCpusetHost(t)
	dir := t.TempDir()
	script := `#!/bin/sh
case "$1 $2" in
"inspect -f") exit 1 ;;
"inspect legacy") printf '%s\n' '[{"Config":{"Image":"ubuntu:test"},"State":{"Running":true},"HostConfig":{"CpusetCpus":"2-3","CpusetMems":"1","PortBindings":{"22/tcp":[{"HostPort":"2201"}]}}}]' ;;
"exec legacy") case "$*" in
    */etc/passwd) printf '%s\n' 'alice:x:1000:1000::/home/alice:/bin/bash' ;;
    */etc/group) printf '%s\n' 'sudo:x:27:alice' ;;
    */etc/shadow) printf '%s\n' 'alice:!:0:0:99999:7:::' ;;
    esac ;;
*) exit 1 ;;
esac
`
	for name, body := range map[string]string{"docker": script, "rsync": "#!/bin/sh\nexit 0\n", "systemctl": "#!/bin/sh\nprintf 'enabled\\n'\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	var log bytes.Buffer
	m := &Manager{Runner: exec.NewRunner(true, slog.New(slog.NewTextHandler(&log, nil))), Root: t.TempDir(), SpecDir: t.TempDir(), UnitDir: t.TempDir(), Out: &bytes.Buffer{}}
	s, err := m.Import(context.Background(), ImportOptions{Tenant: "imported", From: "legacy", Size: "128M"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Cpuset == nil || *s.Cpuset != (Cpuset{CPUs: "2-3", Mems: "1"}) {
		t.Fatalf("legacy pin lost: %+v", s)
	}
	if !strings.Contains(log.String(), "--cpuset-cpus 2-3 --cpuset-mems 1") {
		t.Fatalf("import run lost pin: %s", &log)
	}
}
