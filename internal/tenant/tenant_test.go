package tenant

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/exec"
)

func validSpec() *Spec {
	return &Spec{
		Name:    "est",
		Image:   "registry.local/est:1",
		GPUs:    []int{2, 3},
		SSHPort: 3889,
		Ports:   []string{"8888:8888", "9000:9000/udp"},
		Volume:  Volume{Size: "10G"},
		Users:   []User{{Name: "est", UID: 1000, GID: 1000, Sudo: true}},
	}
}

func TestValidate(t *testing.T) {
	if err := validSpec().Validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	cases := map[string]func(*Spec){
		"name":        func(s *Spec) { s.Name = "Bad_Name" },
		"prev suffix": func(s *Spec) { s.Name = "est-prev" },
		"image":       func(s *Spec) { s.Image = "--privileged" },
		"gpu dup":     func(s *Spec) { s.GPUs = []int{1, 1} },
		"ssh port":    func(s *Spec) { s.SSHPort = 0 },
		"port clash":  func(s *Spec) { s.Ports = []string{"3889:22"} },
		"port format": func(s *Spec) { s.Ports = []string{"80"} },
		"size":        func(s *Spec) { s.Volume.Size = "10 GB" },
		"tiny":        func(s *Spec) { s.Volume.Size = "1M" },
		"fs":          func(s *Spec) { s.Volume.FS = "btrfs" },
		"no users":    func(s *Spec) { s.Users = nil },
		"root user":   func(s *Spec) { s.Users[0].Name = "root" },
		"system uid":  func(s *Spec) { s.Users[0].UID = 0 },
		"dup uid":     func(s *Spec) { s.Users = append(s.Users, User{Name: "b", UID: 1000, GID: 1000}) },
		"operator":    func(s *Spec) { s.Operators = []string{"bad name"} },
		"bind target": func(s *Spec) { s.ExtraBinds = []string{"/raid/x:/home/x"} },
		"bind system": func(s *Spec) { s.ExtraBinds = []string{"/raid/x:/etc/tenant-system/k"} },
		"bind rel":    func(s *Spec) { s.ExtraBinds = []string{"raid:/legacy"} },
		"bind comma":  func(s *Spec) { s.ExtraBinds = []string{"/a,readonly:/legacy"} },
	}
	for name, mutate := range cases {
		s := validSpec()
		mutate(s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: invalid spec accepted", name)
		}
	}
	s := validSpec()
	s.ExtraBinds = []string{"/raid/data/est:/legacy:ro"}
	if err := s.Validate(); err != nil {
		t.Errorf("legacy bind rejected: %v", err)
	}
}

func TestDigestCoversContainerSettingsOnly(t *testing.T) {
	a, b := validSpec(), validSpec()
	b.Users = append(b.Users, User{Name: "b", UID: 1001, GID: 1001})
	b.Volume.Size = "20G"
	b.Operators = []string{"alice"}
	b.GPUs = []int{3, 2}
	if a.Digest() != b.Digest() {
		t.Error("users, volume size, operators or GPU order changed the container digest")
	}
	for _, mutate := range []func(*Spec){
		func(s *Spec) { s.Image = "registry.local/est:2" },
		func(s *Spec) { s.GPUs = []int{2} },
		func(s *Spec) { s.SSHPort = 2222 },
		func(s *Spec) { s.ExtraBinds = []string{"/a:/b"} },
	} {
		c := validSpec()
		mutate(c)
		if c.Digest() == a.Digest() {
			t.Errorf("container change kept the digest: %+v", c)
		}
	}
}

func TestUnitName(t *testing.T) {
	cases := map[string]string{
		"/raid/tenants/est/vol":    "raid-tenants-est-vol.mount",
		"/raid/tenants/est-2/vol":  `raid-tenants-est\x2d2-vol.mount`,
		"/srv/my.tenants/a_b/vol/": "srv-my.tenants-a_b-vol.mount",
	}
	for in, want := range cases {
		if got := UnitName(in); got != want {
			t.Errorf("UnitName(%q) = %q, want %q", in, got, want)
		}
	}
	if out, err := osexec.Command("systemd-escape", "--path", "--suffix=mount", "/raid/tenants/est-2/vol").Output(); err == nil {
		if got := strings.TrimSpace(string(out)); got != UnitName("/raid/tenants/est-2/vol") {
			t.Errorf("systemd-escape says %q", got)
		}
	}
}

func TestMountUnitOrdersBeforeDocker(t *testing.T) {
	m := &Manager{Root: "/raid/tenants"}
	u := MountUnit("est", m.Paths("est"), "ext4")
	for _, want := range []string{"What=/raid/tenants/est/volume.img", "Where=/raid/tenants/est/vol", "Before=docker.service",
		"WantedBy=multi-user.target docker.service", "Options=loop,discard,nodev,nosuid", "RequiresMountsFor=/raid/tenants/est"} {
		if !strings.Contains(u, want) {
			t.Errorf("mount unit lacks %q:\n%s", want, u)
		}
	}
}

func TestRunArgs(t *testing.T) {
	m := &Manager{Root: "/raid/tenants"}
	s := validSpec()
	s.ExtraBinds = []string{"/raid/data/est:/legacy:ro"}
	args := strings.Join(m.RunArgs(s), " ")
	for _, want := range []string{
		"--name tenant-est", "--restart=always", "--init", "--ipc=host",
		"--runtime=nvidia --gpus \"device=2,3\"",
		"-p 3889:22", "-p 8888:8888", "-p 9000:9000/udp",
		"--mount type=bind,src=/raid/tenants/est/vol/data,dst=/data",
		"--mount type=bind,src=/raid/tenants/est/vol/home,dst=/home",
		"--mount type=bind,src=/raid/tenants/est/vol/tmp,dst=/tmp",
		"--mount type=bind,src=/raid/tenants/est/system,dst=/etc/tenant-system,readonly",
		"--mount type=bind,src=/raid/data/est,dst=/legacy,readonly",
		"--label rootfiles.tenant.spec=" + s.Digest(),
		"--entrypoint /bin/sh registry.local/est:1 /etc/tenant-system/init.sh",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("docker run lacks %q:\n%s", want, args)
		}
	}
	s.GPUs = nil
	if args := strings.Join(m.RunArgs(s), " "); strings.Contains(args, "--gpus") || strings.Contains(args, "--runtime") {
		t.Errorf("GPU flags without GPUs: %s", args)
	}
}

func TestShadowFileKeepsUsableHashesOfSpecUsers(t *testing.T) {
	s := validSpec()
	s.Users = append(s.Users, User{Name: "new", UID: 1001, GID: 1001})
	shadow := "root:*:19000:0:99999:7:::\nest:$y$abc:20000:0:99999:7:::\nnew:!:20000:0:99999:7:::\nother:$6$x:1:::::\n"
	if got := string(shadowFile(s, shadow)); got != "est:$y$abc:20000\n" {
		t.Errorf("shadowFile = %q", got)
	}
}

func TestInitScriptSyntax(t *testing.T) {
	if _, err := osexec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	cmd := osexec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(InitScript)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init.sh: %v\n%s", err, out)
	}
}

func TestAuthorizeOps(t *testing.T) {
	s := validSpec()
	s.Operators = []string{"alice"}
	if err := AuthorizeOps(s, "alice", true, "restart"); err != nil {
		t.Errorf("listed operator refused: %v", err)
	}
	if err := AuthorizeOps(s, "root", false, "status"); err != nil {
		t.Errorf("root refused: %v", err)
	}
	if AuthorizeOps(s, "bob", true, "status") == nil {
		t.Error("unlisted account allowed")
	}
	for _, action := range []string{"image", "gpu", "create", "volume", ""} {
		if AuthorizeOps(s, "alice", true, action) == nil {
			t.Errorf("operator allowed %q", action)
		}
	}
}

func TestLoadSpecRejectsUnknownFieldsAndRename(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := validSpec().Marshal()
	write("est", string(data))
	if s, err := LoadSpec(dir, "est"); err != nil || s.Image != "registry.local/est:1" {
		t.Fatalf("round trip: %v %+v", err, s)
	}
	write("other", string(data))
	if _, err := LoadSpec(dir, "other"); err == nil {
		t.Error("file name and tenant name mismatch accepted")
	}
	write("typo", strings.Replace(string(data), "name: est", "name: typo\nimgae: x", 1))
	if _, err := LoadSpec(dir, "typo"); err == nil {
		t.Error("unknown field accepted")
	}
	if names, _ := ListSpecs(dir); strings.Join(names, ",") != "est,other,typo" {
		t.Errorf("ListSpecs = %v", names)
	}
}

func TestParseLegacy(t *testing.T) {
	users := parseLegacyUsers("root:x:0:0::/root:/bin/bash\nsshd:x:110:65534::/run/sshd:/usr/sbin/nologin\nest:x:1000:1000::/home/est:/bin/bash\nsvc:x:1001:1001::/:/bin/false\n", map[string]bool{"est": true})
	if len(users) != 1 || users[0] != (User{Name: "est", UID: 1000, GID: 1000, Sudo: true}) {
		t.Errorf("parseLegacyUsers = %+v", users)
	}
	var c legacyContainer
	c.HostConfig.DeviceRequests = append(c.HostConfig.DeviceRequests, struct {
		Driver    string
		Count     int
		DeviceIDs []string
	}{DeviceIDs: []string{"3", "1"}})
	if g, err := legacyGPUs(c); err != nil || intList(g) != "1,3" {
		t.Errorf("legacyGPUs = %v %v", g, err)
	}
	c.HostConfig.DeviceRequests[0].DeviceIDs, c.HostConfig.DeviceRequests[0].Count = nil, -1
	if _, err := legacyGPUs(c); err == nil {
		t.Error("--gpus all accepted without IDs")
	}
}

// fakeDocker stands in for docker: inspect reports a running container,
// exec cat /etc/shadow prints a hash, and every argv and stdin is logged.
func fakeDocker(t *testing.T) (argsLog, stdinLog string) {
	t.Helper()
	dir := t.TempDir()
	argsLog, stdinLog = filepath.Join(dir, "args"), filepath.Join(dir, "stdin")
	script := `#!/bin/sh
echo "$*" >> ` + argsLog + `
case "$1 $2" in
  "inspect -f") [ -n "$FAKE_INSPECT" ] || exit 1; echo "$FAKE_INSPECT" ;;
  "exec -i") cat >> ` + stdinLog + ` ;;
  "exec tenant-est") [ "$3" = cat ] && echo 'est:$y$new:0:0:99999:7:::' ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return argsLog, stdinLog
}

func TestPasswdNeverLogsPassword(t *testing.T) {
	argsLog, stdinLog := fakeDocker(t)
	t.Setenv("FAKE_INSPECT", "true|x|always|")
	var logs bytes.Buffer
	runner := exec.NewRunner(false, slog.New(slog.NewTextHandler(&logs, nil)))
	root := t.TempDir()
	m := &Manager{Runner: runner, Root: root, Out: &bytes.Buffer{}}
	s := validSpec()
	if err := os.MkdirAll(m.Paths("est").System, 0o700); err != nil {
		t.Fatal(err)
	}
	pw, err := m.Passwd(context.Background(), s, "est")
	if err != nil || len(pw) != 16 {
		t.Fatalf("Passwd = %q, %v", pw, err)
	}
	args, _ := os.ReadFile(argsLog)
	stdin, _ := os.ReadFile(stdinLog)
	if strings.Contains(string(args), pw) || strings.Contains(logs.String(), pw) {
		t.Error("temporary password leaked into argv or the log")
	}
	if string(stdin) != "est:"+pw+"\n" {
		t.Errorf("chpasswd stdin = %q", stdin)
	}
	if !strings.Contains(string(args), "exec tenant-est chage -d 0 est") {
		t.Errorf("password not expired:\n%s", args)
	}
	shadow, _ := os.ReadFile(filepath.Join(m.Paths("est").System, "shadow"))
	if string(shadow) != "est:$y$new:0\n" {
		t.Errorf("stored hash = %q", shadow)
	}
	if _, err := m.Passwd(context.Background(), s, "nobody"); err == nil {
		t.Error("unknown user accepted")
	}
}

func TestDryRunApplyChangesNothing(t *testing.T) {
	argsLog, _ := fakeDocker(t)
	t.Setenv("FAKE_INSPECT", "")
	runner := exec.NewRunner(true, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	root := filepath.Join(t.TempDir(), "tenants")
	specDir := filepath.Join(t.TempDir(), "specs")
	m := &Manager{Runner: runner, Root: root, SpecDir: specDir, UnitDir: t.TempDir(), Out: &bytes.Buffer{}}
	s := validSpec()
	if err := m.SaveSpec(s); err != nil {
		t.Fatal(err)
	}
	if err := m.Apply(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if err := m.Resize(context.Background(), s, "20G", false); err == nil {
		t.Error("resize without a volume image succeeded")
	}
	for _, p := range []string{root, specDir} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("dry run created %s", p)
		}
	}
	if units, _ := os.ReadDir(m.UnitDir); len(units) != 0 {
		t.Errorf("dry run wrote units: %v", units)
	}
	args, _ := os.ReadFile(argsLog)
	for _, line := range strings.Split(strings.TrimSpace(string(args)), "\n") {
		if !strings.HasPrefix(line, "inspect ") {
			t.Errorf("dry run ran docker %s", line)
		}
	}
}

func TestFailedApplyRestoresGPULedger(t *testing.T) {
	fakeDocker(t)
	t.Setenv("FAKE_INSPECT", "") // no container is left after the failure
	dir := t.TempDir()
	for _, n := range []string{"install", "truncate", "mkfs.ext4", "systemctl", "ssh-keygen"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	var calls [][]int
	m := &Manager{
		Runner: exec.NewRunner(false, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))),
		Root:   t.TempDir(), SpecDir: t.TempDir(), UnitDir: t.TempDir(), Out: &bytes.Buffer{},
		ReserveGPUs: func(_ context.Context, _ string, gpus []int) error {
			calls = append(calls, gpus)
			return nil
		},
	}
	// The fake mount never happens, so Apply fails after reserving.
	if err := m.Apply(context.Background(), validSpec()); err == nil {
		t.Fatal("apply on an unmounted volume succeeded")
	}
	if len(calls) != 2 || intList(calls[0]) != "2,3" || len(calls[1]) != 0 {
		t.Errorf("ledger calls = %v, want [[2 3] []]", calls)
	}
}
