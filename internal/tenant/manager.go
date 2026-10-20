package tenant

import (
	"bufio"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/STAIxBWLB/rootfiles/internal/exec"
)

// GuardPath is the GPU device guard the host-prep module installs.
const GuardPath = "/usr/local/sbin/rootfiles-gpu-guard"

// Manager carries out tenant operations on one host.
type Manager struct {
	Runner  *exec.Runner
	Root    string // tenant data root, e.g. /raid/tenants
	SpecDir string
	UnitDir string // systemd unit directory
	Out     io.Writer
	// ReserveGPUs records gpus for tenant in the shared allocation ledger,
	// rejecting overlaps; an empty list releases the tenant's entry.
	ReserveGPUs func(ctx context.Context, tenant string, gpus []int) error
	// WaitTimeout bounds the readiness check of a new container.
	WaitTimeout time.Duration
	// Piper, when set, keeps sshpiper routes for every tenant user.
	Piper *Piper
}

// Paths are a tenant's host paths.
type Paths struct {
	Dir, Image, Mount, System string
}

// Paths returns the host paths of tenant t.
func (m *Manager) Paths(t string) Paths {
	dir := filepath.Join(m.Root, t)
	return Paths{
		Dir:    dir,
		Image:  filepath.Join(dir, "volume.img"),
		Mount:  filepath.Join(dir, "vol"),
		System: filepath.Join(dir, "system"),
	}
}

func (m *Manager) unitPath(t string) string {
	return filepath.Join(m.UnitDir, UnitName(m.Paths(t).Mount))
}

func (m *Manager) printf(format string, a ...any) { fmt.Fprintf(m.Out, format, a...) }

func (m *Manager) run(ctx context.Context, name string, args ...string) error {
	_, err := m.Runner.Run(ctx, name, args...)
	return err
}

// SaveSpec writes s to the spec directory.
func (m *Manager) SaveSpec(s *Spec) error {
	if err := s.Validate(); err != nil {
		return err
	}
	data, err := s.Marshal()
	if err != nil {
		return err
	}
	if err := m.Runner.MkdirAll(m.SpecDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(m.SpecDir, s.Name+".yaml")
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return fmt.Errorf("refusing to write %s: not a regular file", path)
	}
	return m.Runner.WriteFile(path, data, 0o600)
}

// Load reads tenant t's spec.
func (m *Manager) Load(t string) (*Spec, error) { return LoadSpec(m.SpecDir, t) }

// --- container state ---

type containerState struct {
	Exists, Running bool
	Digest          string
	Restart         string
	GPUs            string // the GPUs the container was started with
}

func (m *Manager) inspect(ctx context.Context, name string) containerState {
	res, err := m.Runner.Query(ctx, "docker", "inspect", "-f",
		`{{.State.Running}}|{{index .Config.Labels "rootfiles.tenant.spec"}}|{{.HostConfig.RestartPolicy.Name}}|{{index .Config.Labels "rootfiles.tenant.gpus"}}`, name)
	if err != nil {
		return containerState{}
	}
	f := strings.Split(strings.TrimSpace(res.Stdout), "|")
	st := containerState{Exists: true}
	if len(f) == 4 {
		st.Running, st.Digest, st.Restart, st.GPUs = f[0] == "true", f[1], f[2], f[3]
	}
	return st
}

// RunArgs is the docker run command line of s.
func (m *Manager) RunArgs(s *Spec) []string {
	p := m.Paths(s.Name)
	args := []string{"run", "-d",
		"--name", ContainerName(s.Name),
		"--hostname", s.Name,
		"--restart=always", "--init", "--ipc=host",
		"--label", "rootfiles.tenant=" + s.Name,
		"--label", "rootfiles.tenant.spec=" + s.Digest(),
		"--label", "rootfiles.tenant.gpus=" + intList(s.GPUs),
	}
	if len(s.GPUs) > 0 {
		ids := make([]string, len(s.GPUs))
		for i, g := range s.GPUs {
			ids[i] = strconv.Itoa(g)
		}
		// The nvidia runtime lists the devices in the OCI spec, so a
		// systemd daemon-reload cannot revoke them as it does under runc.
		args = append(args, "--runtime=nvidia", "--gpus", `"device=`+strings.Join(ids, ",")+`"`)
	}
	args = append(args, "-p", strconv.Itoa(s.SSHPort)+":22")
	for _, port := range s.Ports {
		args = append(args, "-p", port)
	}
	mount := func(src, dst string, ro bool) {
		v := "type=bind,src=" + src + ",dst=" + dst
		if ro {
			v += ",readonly"
		}
		args = append(args, "--mount", v)
	}
	// type=bind fails on a missing source, so an unmounted volume (no
	// data/home/tmp on the bare mount point) stops the container instead
	// of letting it write past the quota.
	mount(filepath.Join(p.Mount, "data"), "/data", false)
	mount(filepath.Join(p.Mount, "home"), "/home", false)
	mount(filepath.Join(p.Mount, "tmp"), "/tmp", false)
	mount(p.System, SystemMount, true)
	for _, b := range s.ExtraBinds {
		src, dst, ro, _ := ParseBind(b)
		mount(src, dst, ro)
	}
	return append(args, "--entrypoint", "/bin/sh", s.Image, SystemMount+"/"+initScript)
}

// --- apply ---

// Apply converges tenant s: GPU ledger, volume, system files and container.
// When it fails, the ledger goes back to the GPUs of whatever container is
// left running, so GPUs a rolled-back container still uses stay reserved.
func (m *Manager) Apply(ctx context.Context, s *Spec) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if m.ReserveGPUs != nil {
		if err := m.ReserveGPUs(ctx, s.Name, s.GPUs); err != nil {
			return err
		}
	}
	err := m.converge(ctx, s)
	if err == nil {
		err = m.WriteRoutes(ctx, s)
	}
	if err != nil && m.ReserveGPUs != nil && !m.Runner.DryRun {
		left := m.inspect(ctx, ContainerName(s.Name))
		if rerr := m.ReserveGPUs(ctx, s.Name, parseInts(left.GPUs)); rerr != nil {
			err = fmt.Errorf("%w; restoring the GPU ledger also failed: %v", err, rerr)
		}
	}
	return err
}

func parseInts(s string) []int {
	var out []int
	for _, f := range strings.Split(s, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(f)); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func (m *Manager) converge(ctx context.Context, s *Spec) error {
	if err := m.EnsureVolume(ctx, s); err != nil {
		return err
	}
	if err := m.writeSystem(ctx, s); err != nil {
		return err
	}
	name := ContainerName(s.Name)
	st := m.inspect(ctx, name)
	switch {
	case !st.Exists:
		return m.start(ctx, s)
	case st.Digest != s.Digest():
		return m.recreate(ctx, s, st)
	case !st.Running:
		m.printf("Starting %s\n", name)
		return m.run(ctx, "docker", "start", name)
	default:
		m.printf("%s is up to date; syncing accounts\n", name)
		return m.run(ctx, "docker", "exec", name, "/bin/sh", SystemMount+"/"+initScript, "users")
	}
}

func (m *Manager) start(ctx context.Context, s *Spec) error {
	name := ContainerName(s.Name)
	m.printf("Creating %s (image %s)\n", name, s.Image)
	if err := m.run(ctx, "docker", m.RunArgs(s)...); err != nil {
		return err
	}
	if err := m.Verify(ctx, s); err != nil {
		_ = m.run(ctx, "docker", "rm", "-f", name)
		return fmt.Errorf("new container failed verification and was removed: %w", err)
	}
	return nil
}

func (m *Manager) recreate(ctx context.Context, s *Spec, st containerState) error {
	name := ContainerName(s.Name)
	prev := name + "-prev"
	if m.inspect(ctx, prev).Exists {
		return fmt.Errorf("%s still holds the previous container; check it and remove it (docker rm %s) before recreating", prev, prev)
	}
	if st.Running {
		if err := m.SyncShadow(ctx, s); err != nil {
			return fmt.Errorf("saving passwords before recreate: %w", err)
		}
	}
	if size := m.writableSize(ctx, name); size != "" {
		m.printf("⚠ %s writable layer holds %s; files outside /data, /home and /tmp stay in %s, not in the new container\n", name, size, prev)
	}
	m.printf("Recreating %s (image %s)\n", name, s.Image)
	steps := [][]string{
		{"stop", name},
		{"rename", name, prev},
		{"update", "--restart=no", prev},
	}
	for i, a := range steps {
		if err := m.run(ctx, "docker", a...); err != nil {
			// Undo what ran: the rename, then the stop.
			if i == 2 {
				_ = m.run(ctx, "docker", "rename", prev, name)
			}
			if i > 0 {
				_ = m.run(ctx, "docker", "start", name)
			}
			return fmt.Errorf("preparing recreate (old container restarted): %w", err)
		}
	}
	err := m.run(ctx, "docker", m.RunArgs(s)...)
	if err == nil {
		err = m.Verify(ctx, s)
	}
	if err != nil {
		m.printf("✗ new container failed (%v); rolling back\n", firstLine(err.Error()))
		_ = m.run(ctx, "docker", "rm", "-f", name)
		for _, a := range [][]string{{"rename", prev, name}, {"update", "--restart=" + orDefault(st.Restart, "always"), name}, {"start", name}} {
			if rerr := m.run(ctx, "docker", a...); rerr != nil {
				return fmt.Errorf("recreate failed (%v) and rollback failed: %w", err, rerr)
			}
		}
		return fmt.Errorf("recreate failed and was rolled back: %w", err)
	}
	m.printf("Previous container kept as %s; remove it with `docker rm %s` once the tenant confirms\n", prev, prev)
	return nil
}

func (m *Manager) writableSize(ctx context.Context, name string) string {
	res, err := m.Runner.Query(ctx, "docker", "ps", "-a", "-s", "--filter", "name=^/"+name+"$", "--format", "{{.Size}}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(res.Stdout)
}

// Verify waits for sshd with the stored host key and, with GPUs, for
// nvidia-smi to list them.
func (m *Manager) Verify(ctx context.Context, s *Spec) error {
	if m.Runner.DryRun {
		return nil
	}
	timeout := m.WaitTimeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(s.SSHPort))
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if lastErr = sshBanner(addr); lastErr == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sshd on %s: %w", addr, lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if err := m.checkHostKey(ctx, s); err != nil {
		return err
	}
	if len(s.GPUs) > 0 {
		res, err := m.Runner.Query(ctx, "docker", "exec", ContainerName(s.Name), "nvidia-smi", "-L")
		if err != nil {
			return fmt.Errorf("GPU check: %w", err)
		}
		if n := strings.Count(res.Stdout, "GPU "); n != len(s.GPUs) {
			return fmt.Errorf("GPU check: nvidia-smi lists %d GPUs, want %d", n, len(s.GPUs))
		}
	}
	what := "sshd and host key"
	if len(s.GPUs) > 0 {
		what += " and GPUs " + intList(s.GPUs)
	}
	m.printf("✓ %s: %s verified on port %d\n", ContainerName(s.Name), what, s.SSHPort)
	return nil
}

func sshBanner(addr string) error {
	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return err
	}
	if !strings.HasPrefix(line, "SSH-") {
		return fmt.Errorf("unexpected banner %q", strings.TrimSpace(line))
	}
	return nil
}

func (m *Manager) checkHostKey(ctx context.Context, s *Spec) error {
	pub, err := os.ReadFile(filepath.Join(m.Paths(s.Name).System, "ssh", "ssh_host_ed25519_key.pub"))
	if err != nil || !m.Runner.CommandExists("ssh-keyscan") {
		m.printf("⚠ host key not compared (no stored ed25519 key or no ssh-keyscan)\n")
		return nil
	}
	want := strings.Fields(string(pub))
	res, err := m.Runner.Query(ctx, "ssh-keyscan", "-T", "5", "-t", "ed25519", "-p", strconv.Itoa(s.SSHPort), "127.0.0.1")
	if err != nil {
		return fmt.Errorf("host key check: %w", err)
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && len(want) >= 2 && f[2] == want[1] {
			return nil
		}
	}
	return fmt.Errorf("host key check: sshd does not present the stored ed25519 host key")
}

// --- system files ---

func (m *Manager) writeSystem(ctx context.Context, s *Spec) error {
	p := m.Paths(s.Name)
	if err := m.run(ctx, "install", "-d", "-m", "0700", p.System, filepath.Join(p.System, "ssh")); err != nil {
		return err
	}
	if err := m.writeIfChanged(filepath.Join(p.System, initScript), []byte(InitScript), 0o755); err != nil {
		return err
	}
	if err := m.writeIfChanged(filepath.Join(p.System, "users"), usersFile(s), 0o644); err != nil {
		return err
	}
	if err := m.writePiperKey(s); err != nil {
		return err
	}
	for _, kt := range []string{"ed25519", "ecdsa", "rsa"} {
		key := filepath.Join(p.System, "ssh", "ssh_host_"+kt+"_key")
		if m.Runner.FileExists(key) {
			continue
		}
		if err := m.run(ctx, "ssh-keygen", "-q", "-t", kt, "-N", "", "-C", "tenant-"+s.Name, "-f", key); err != nil {
			return fmt.Errorf("generating %s host key: %w", kt, err)
		}
	}
	return nil
}

func (m *Manager) writeIfChanged(path string, data []byte, perm os.FileMode) error {
	if cur, err := os.ReadFile(path); err == nil && string(cur) == string(data) {
		return nil
	}
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return fmt.Errorf("refusing to write %s: not a regular file", path)
	}
	// Through a rename: sshpiperd reads route and key files on every
	// connection, and the lock-free sync may rewrite them during an apply.
	tmp := path + ".rootfiles-tmp-" + strconv.Itoa(os.Getpid())
	if err := m.Runner.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := m.Runner.Rename(tmp, path); err != nil {
		_ = m.Runner.Remove(tmp)
		return err
	}
	return nil
}

// --- passwords ---

const passwordAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func generatePassword(n int) (string, error) {
	b := make([]byte, n)
	max := big.NewInt(int64(len(passwordAlphabet)))
	for i := range b {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b[i] = passwordAlphabet[idx.Int64()]
	}
	return string(b), nil
}

// Passwd issues a temporary password for user, expired so it must be
// changed at first login, and stores the hash. The password goes to the
// container on stdin only; it is returned once and never logged.
func (m *Manager) Passwd(ctx context.Context, s *Spec, user string) (string, error) {
	if _, ok := s.User(user); !ok {
		return "", fmt.Errorf("tenant %s has no user %s", s.Name, user)
	}
	name := ContainerName(s.Name)
	if !m.Runner.DryRun && !m.inspect(ctx, name).Running {
		return "", fmt.Errorf("%s is not running", name)
	}
	pw, err := generatePassword(16)
	if err != nil {
		return "", err
	}
	if _, err := m.Runner.RunInput(ctx, user+":"+pw+"\n", "docker", "exec", "-i", name, "chpasswd"); err != nil {
		return "", err
	}
	if err := m.run(ctx, "docker", "exec", name, "chage", "-d", "0", user); err != nil {
		return "", err
	}
	if err := m.SyncShadow(ctx, s); err != nil {
		return "", err
	}
	if m.Runner.DryRun {
		return "", nil
	}
	return pw, nil
}

// SyncShadow copies the spec users' password hashes from the running
// container into the system directory.
func (m *Manager) SyncShadow(ctx context.Context, s *Spec) error {
	name := ContainerName(s.Name)
	if !m.Runner.DryRun && !m.inspect(ctx, name).Running {
		return nil // nothing to read; the stored hashes stay
	}
	res, err := m.readInContainer(ctx, name, "/etc/shadow")
	if err != nil {
		if m.Runner.DryRun {
			return nil
		}
		return fmt.Errorf("reading passwords of %s: %w", name, err)
	}
	return m.writeIfChanged(filepath.Join(m.Paths(s.Name).System, "shadow"), shadowFile(s, res.Stdout), 0o600)
}

// readInContainer reads a file inside a container, where its symlinks
// resolve against the container's root, bounded in size and time: the
// tenant controls the file.
func (m *Manager) readInContainer(ctx context.Context, container, path string) (*exec.Result, error) {
	return m.Runner.Query(ctx, "docker", "exec", container, "timeout", "10", "head", "-c", "4194304", path)
}

// shadowFile keeps name:hash:lastchg of the spec users with usable hashes.
func shadowFile(s *Spec, shadow string) []byte {
	got := map[string]string{}
	for _, line := range strings.Split(shadow, "\n") {
		f := strings.Split(line, ":")
		if len(f) < 3 {
			continue
		}
		if h := f[1]; h == "" || strings.HasPrefix(h, "!") || strings.HasPrefix(h, "*") {
			continue
		}
		got[f[0]] = f[0] + ":" + f[1] + ":" + f[2] + "\n"
	}
	var b strings.Builder
	for _, u := range s.Users {
		b.WriteString(got[u.Name])
	}
	return []byte(b.String())
}

// --- simple operations ---

// Restart restarts the tenant container.
func (m *Manager) Restart(ctx context.Context, s *Spec) error {
	return m.run(ctx, "docker", "restart", ContainerName(s.Name))
}

// Logs prints the container log tail.
func (m *Manager) Logs(ctx context.Context, s *Spec, tail int) error {
	res, err := m.Runner.Query(ctx, "docker", "logs", "--tail", strconv.Itoa(tail), ContainerName(s.Name))
	if res != nil {
		fmt.Fprint(m.Out, res.Stdout, res.Stderr)
	}
	return err
}

// HealGPU runs the device guard for the tenant container.
func (m *Manager) HealGPU(ctx context.Context, s *Spec) error {
	if !m.Runner.FileExists(GuardPath) {
		return fmt.Errorf("%s is missing; enable the tenant module and run `rootfiles apply --module tenant`", GuardPath)
	}
	res, err := m.Runner.Run(ctx, GuardPath, ContainerName(s.Name))
	if res != nil {
		fmt.Fprint(m.Out, res.Stdout)
	}
	return err
}

// --- status ---

// Usage is a mounted volume's use.
type Usage struct {
	Used, Size uint64
	Mounted    bool
}

func (m *Manager) usage(t string) Usage {
	p := m.Paths(t)
	if !isMountPoint(p.Mount) {
		return Usage{}
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(p.Mount, &st); err != nil {
		return Usage{}
	}
	bs := uint64(st.Bsize)
	return Usage{Used: (st.Blocks - st.Bfree) * bs, Size: st.Blocks * bs, Mounted: true}
}

// isMountPoint reports whether dir is on a different device than its parent.
func isMountPoint(dir string) bool {
	a, err1 := os.Stat(dir)
	b, err2 := os.Stat(filepath.Dir(dir))
	if err1 != nil || err2 != nil {
		return false
	}
	sa, ok1 := a.Sys().(*syscall.Stat_t)
	sb, ok2 := b.Sys().(*syscall.Stat_t)
	return ok1 && ok2 && sa.Dev != sb.Dev
}

// Commitment compares the volume sizes not yet backed by disk blocks with
// the free space of the tenant root.
type Commitment struct {
	Committed, Allocated, Free uint64
}

// Overcommitted reports whether filling every volume would run out of space.
func (c Commitment) Overcommitted() bool { return c.Committed-c.Allocated > c.Free }

func (m *Manager) commitment(specs []*Spec) Commitment {
	var c Commitment
	for _, s := range specs {
		n, _ := s.Volume.Bytes()
		c.Committed += n
		if fi, err := os.Stat(m.Paths(s.Name).Image); err == nil {
			if st, ok := fi.Sys().(*syscall.Stat_t); ok {
				c.Allocated += uint64(st.Blocks) * 512
			}
		}
	}
	if c.Allocated > c.Committed {
		c.Allocated = c.Committed
	}
	c.Free = freeSpace(m.Root)
	return c
}

// freeSpace is the space available to root on the filesystem holding path,
// or its nearest existing parent.
func freeSpace(path string) uint64 {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		var st syscall.Statfs_t
		if syscall.Statfs(p, &st) == nil {
			return uint64(st.Bavail) * uint64(st.Bsize)
		}
		if p == filepath.Dir(p) {
			return 0
		}
	}
}

// Status prints one line per tenant and the volume commitment.
func (m *Manager) Status(ctx context.Context, names []string, detail bool) error {
	var specs []*Spec
	fmt.Fprintf(m.Out, "%-14s %-9s %-7s %-6s %-8s %-19s %s\n", "TENANT", "STATE", "SSH", "GPUS", "SPEC", "VOLUME", "IMAGE")
	for _, n := range names {
		s, err := m.Load(n)
		if err != nil {
			fmt.Fprintf(m.Out, "%-14s error: %v\n", n, err)
			continue
		}
		specs = append(specs, s)
		st := m.inspect(ctx, ContainerName(n))
		state := "missing"
		if st.Exists {
			state = "stopped"
			if st.Running {
				state = "running"
			}
		}
		drift := "current"
		if st.Exists && st.Digest != s.Digest() {
			drift = "drift"
		}
		vol := "unmounted/" + s.Volume.Size
		if u := m.usage(n); u.Mounted {
			vol = fmt.Sprintf("%s/%s %d%%", human(u.Used), human(u.Size), pct(u.Used, u.Size))
		}
		gpus := "-"
		if len(s.GPUs) > 0 {
			gpus = intList(s.GPUs)
		}
		fmt.Fprintf(m.Out, "%-14s %-9s %-7d %-6s %-8s %-19s %s\n", n, state, s.SSHPort, gpus, drift, vol, s.Image)
		for _, r := range m.Routes(s) {
			fmt.Fprintf(m.Out, "  %s\n", r)
		}
		if detail && st.Exists {
			if size := m.writableSize(ctx, ContainerName(n)); size != "" {
				fmt.Fprintf(m.Out, "  writable layer: %s (no quota; keep data in /data, /home or /tmp)\n", size)
			}
			if m.inspect(ctx, ContainerName(n)+"-prev").Exists {
				fmt.Fprintf(m.Out, "  previous container %s-prev is kept for rollback\n", ContainerName(n))
			}
		}
	}
	if len(specs) > 0 {
		c := m.commitment(specs)
		mark := "✓"
		if c.Overcommitted() {
			mark = "⚠ overcommitted:"
		}
		fmt.Fprintf(m.Out, "%s volumes commit %s, %s allocated, %s free on %s\n", mark, human(c.Committed), human(c.Allocated), human(c.Free), m.Root)
	}
	return nil
}

func pct(a, b uint64) uint64 {
	if b == 0 {
		return 0
	}
	return a * 100 / b
}

func human(n uint64) string {
	units := []string{"B", "K", "M", "G", "T", "P"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%dB", n)
	}
	return strconv.FormatFloat(f, 'f', 1, 64) + units[i]
}

func intList(v []int) string {
	s := append([]int(nil), v...)
	sort.Ints(s)
	parts := make([]string, len(s))
	for i, g := range s {
		parts[i] = strconv.Itoa(g)
	}
	return strings.Join(parts, ",")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
