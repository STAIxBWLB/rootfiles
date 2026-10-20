package tenant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ImportOptions describe moving a hand-made container under rootfiles.
type ImportOptions struct {
	Tenant, From string
	Size, FS     string
	// Data is a host directory copied into the new /data.
	Data string
	// Keep lists paths under /home or /tmp in the old container that are
	// copied into the volume.
	Keep      []string
	Operators []string
}

type legacyContainer struct {
	Config struct {
		Image string
		Env   []string
	}
	State struct {
		Running bool
	}
	HostConfig struct {
		PortBindings   map[string][]struct{ HostPort string }
		DeviceRequests []struct {
			Driver    string
			Count     int
			DeviceIDs []string
		}
		RestartPolicy struct{ Name string }
	}
	Mounts []struct {
		Type, Source, Destination string
		RW                        bool
	}
	GraphDriver struct {
		Data map[string]string
	}
}

// Import writes a spec from a running legacy container, copies its host
// keys, password hashes and kept data into a new volume, stops the old
// container (kept for rollback) and starts the tenant container.
func (m *Manager) Import(ctx context.Context, o ImportOptions) (*Spec, error) {
	if err := CheckName(o.Tenant); err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(m.SpecDir, o.Tenant+".yaml")); err == nil {
		return nil, fmt.Errorf("tenant %s already exists", o.Tenant)
	}
	if m.inspect(ctx, ContainerName(o.Tenant)).Exists {
		return nil, fmt.Errorf("container %s already exists", ContainerName(o.Tenant))
	}
	if !m.Runner.CommandExists("rsync") {
		return nil, fmt.Errorf("rsync is required for import")
	}
	res, err := m.Runner.Query(ctx, "docker", "inspect", o.From)
	if err != nil {
		return nil, fmt.Errorf("inspecting %s: %w", o.From, err)
	}
	var list []legacyContainer
	if err := json.Unmarshal([]byte(res.Stdout), &list); err != nil || len(list) != 1 {
		return nil, fmt.Errorf("parsing docker inspect %s: %v", o.From, err)
	}
	old := list[0]
	if !old.State.Running {
		return nil, fmt.Errorf("%s must be running so its accounts and files can be read; start it and ask the tenant to stop writing", o.From)
	}

	s := &Spec{Name: o.Tenant, Image: old.Config.Image, Volume: Volume{Size: o.Size, FS: o.FS}, Operators: o.Operators}
	for port, binds := range old.HostConfig.PortBindings {
		if len(binds) == 0 || binds[0].HostPort == "" {
			continue
		}
		if port == "22/tcp" {
			s.SSHPort, _ = strconv.Atoi(binds[0].HostPort)
			continue
		}
		cport, proto, _ := strings.Cut(port, "/")
		p := binds[0].HostPort + ":" + cport
		if proto != "" && proto != "tcp" {
			p += "/" + proto
		}
		s.Ports = append(s.Ports, p)
	}
	sort.Strings(s.Ports)
	s.GPUs, err = legacyGPUs(old)
	if err != nil {
		return nil, err
	}
	var notes []string
	for _, mt := range old.Mounts {
		if mt.Type != "bind" || filepath.Clean(mt.Source) == filepath.Clean(o.Data) {
			continue
		}
		mode := "ro"
		if mt.RW {
			mode = "rw"
		}
		b := mt.Source + ":" + mt.Destination + ":" + mode
		if _, _, _, err := ParseBind(b); err != nil {
			notes = append(notes, fmt.Sprintf("bind %s not carried over: %v", b, err))
			continue
		}
		s.ExtraBinds = append(s.ExtraBinds, b)
	}
	if s.Users, err = m.legacyUsers(ctx, o.From); err != nil {
		return nil, err
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}

	// Plan and size check before any change.
	merged := old.GraphDriver.Data["MergedDir"]
	type copyItem struct{ src, dst string }
	var items []copyItem
	p := m.Paths(s.Name)
	if o.Data != "" {
		items = append(items, copyItem{strings.TrimSuffix(o.Data, "/") + "/", filepath.Join(p.Mount, "data") + "/"})
	}
	for _, k := range o.Keep {
		k = filepath.Clean(k)
		if !strings.HasPrefix(k, "/home/") && !strings.HasPrefix(k, "/tmp/") {
			return nil, fmt.Errorf("--keep %s: only paths under /home or /tmp can be kept", k)
		}
		if merged == "" {
			return nil, fmt.Errorf("cannot read the writable layer of %s (no overlay MergedDir)", o.From)
		}
		// A symlink inside the container (say /home -> /) would resolve
		// against the host's root here, so the path must stay in the layer.
		base, _ := filepath.EvalSymlinks(merged) // docker's data root may itself be a symlink
		if real, err := filepath.EvalSymlinks(filepath.Join(merged, k)); err != nil || !strings.HasPrefix(real, base+"/") {
			return nil, fmt.Errorf("--keep %s: not a path inside %s's filesystem (%v)", k, o.From, err)
		}
		items = append(items, copyItem{merged + "/." + k, p.Mount + "/"})
	}
	var total uint64
	for _, it := range items {
		src := strings.Replace(it.src, "/./", "/", 1)
		n, err := m.du(ctx, src)
		if err != nil {
			return nil, err
		}
		total += n
	}
	size, _ := s.Volume.Bytes()
	free := freeSpace(m.Root)
	m.printf("Import plan for tenant %s from %s:\n", s.Name, o.From)
	m.printf("  image %s, ssh port %d, gpus %v, users %d, volume %s %s\n", s.Image, s.SSHPort, s.GPUs, len(s.Users), s.Volume.Filesystem(), s.Volume.Size)
	for _, it := range items {
		m.printf("  copy %s -> %s\n", it.src, it.dst)
	}
	for _, n := range notes {
		m.printf("  ⚠ %s\n", n)
	}
	m.printf("  data to copy %s, volume %s, free on %s %s\n", human(total), human(size), m.Root, human(free))
	if total > size*95/100 {
		return nil, fmt.Errorf("data to copy (%s) does not fit the %s volume with 5%% headroom; clean up first or pass a larger --size", human(total), s.Volume.Size)
	}
	if total > free {
		return nil, fmt.Errorf("data to copy (%s) exceeds the free space on %s (%s)", human(total), m.Root, human(free))
	}

	if m.ReserveGPUs != nil {
		if err := m.ReserveGPUs(ctx, s.Name, s.GPUs); err != nil {
			return nil, err
		}
	}
	if err := m.SaveSpec(s); err != nil {
		return nil, err
	}
	if err := m.EnsureVolume(ctx, s); err != nil {
		return nil, err
	}
	if err := m.run(ctx, "install", "-d", "-m", "0700", p.System, filepath.Join(p.System, "ssh")); err != nil {
		return nil, err
	}
	for _, kt := range []string{"ed25519", "ecdsa", "rsa"} {
		for _, suffix := range []string{"", ".pub"} {
			f := "ssh_host_" + kt + "_key" + suffix
			// A missing key type is generated by writeSystem. -L copies the
			// content, never a symlink that would point into the host.
			_ = m.run(ctx, "docker", "cp", "-L", o.From+":/etc/ssh/"+f, filepath.Join(p.System, "ssh", f))
		}
	}
	if sh, err := m.readInContainer(ctx, o.From, "/etc/shadow"); err == nil {
		if err := m.writeIfChanged(filepath.Join(p.System, "shadow"), shadowFile(s, sh.Stdout), 0o600); err != nil {
			return nil, err
		}
	}
	if err := m.writeSystem(ctx, s); err != nil {
		return nil, err
	}
	for _, it := range items {
		args := []string{"-aHAX", it.src, it.dst}
		if strings.Contains(it.src, "/./") {
			args = []string{"-aHAX", "--relative", it.src, it.dst}
		}
		if err := m.run(ctx, "rsync", args...); err != nil {
			return nil, fmt.Errorf("copying %s: %w", it.src, err)
		}
	}
	for _, a := range [][]string{{"update", "--restart=no", o.From}, {"stop", o.From}} {
		if err := m.run(ctx, "docker", a...); err != nil {
			return nil, err
		}
	}
	if err := m.start(ctx, s); err != nil {
		_ = m.run(ctx, "docker", "update", "--restart="+orDefault(old.HostConfig.RestartPolicy.Name, "always"), o.From)
		_ = m.run(ctx, "docker", "start", o.From)
		return nil, fmt.Errorf("%w; %s was restarted, the copied volume and spec are kept for another try", err, o.From)
	}
	m.printf("✓ tenant %s imported; %s is stopped and kept for rollback until you remove it\n", s.Name, o.From)
	return s, nil
}

func legacyGPUs(c legacyContainer) ([]int, error) {
	var ids []string
	for _, r := range c.HostConfig.DeviceRequests {
		if len(r.DeviceIDs) > 0 {
			ids = append(ids, r.DeviceIDs...)
		} else if r.Count != 0 {
			return nil, fmt.Errorf("the container requests %d GPUs without IDs; set them with `tenant gpu` after import", r.Count)
		}
	}
	if len(ids) == 0 {
		for _, e := range c.Config.Env {
			if v, ok := strings.CutPrefix(e, "NVIDIA_VISIBLE_DEVICES="); ok && v != "" && v != "all" && v != "none" && v != "void" {
				ids = strings.Split(v, ",")
			}
		}
	}
	var gpus []int
	for _, id := range ids {
		for _, part := range strings.Split(id, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil {
				return nil, fmt.Errorf("GPU id %q is not an index; set GPUs with `tenant gpu` after import", part)
			}
			gpus = append(gpus, n)
		}
	}
	sort.Ints(gpus)
	return gpus, nil
}

// legacyUsers reads the regular accounts (uid 1000-65533 with a login
// shell) and their sudo/wheel membership from a running container.
func (m *Manager) legacyUsers(ctx context.Context, from string) ([]User, error) {
	pw, err := m.readInContainer(ctx, from, "/etc/passwd")
	if err != nil {
		return nil, err
	}
	gr, err := m.readInContainer(ctx, from, "/etc/group")
	if err != nil {
		return nil, err
	}
	sudoers := map[string]bool{}
	for _, line := range strings.Split(gr.Stdout, "\n") {
		f := strings.Split(line, ":")
		if len(f) == 4 && (f[0] == "sudo" || f[0] == "wheel") {
			for _, u := range strings.Split(f[3], ",") {
				sudoers[u] = true
			}
		}
	}
	return parseLegacyUsers(pw.Stdout, sudoers), nil
}

func parseLegacyUsers(passwd string, sudoers map[string]bool) []User {
	var users []User
	for _, line := range strings.Split(passwd, "\n") {
		f := strings.Split(line, ":")
		if len(f) != 7 || strings.HasSuffix(f[6], "nologin") || strings.HasSuffix(f[6], "false") {
			continue
		}
		uid, err1 := strconv.Atoi(f[2])
		gid, err2 := strconv.Atoi(f[3])
		if err1 != nil || err2 != nil || uid < 1000 || uid > 65533 {
			continue
		}
		users = append(users, User{Name: f[0], UID: uid, GID: gid, Sudo: sudoers[f[0]]})
	}
	return users
}

func (m *Manager) du(ctx context.Context, path string) (uint64, error) {
	res, err := m.Runner.Query(ctx, "du", "-sb", path)
	if err != nil {
		return 0, fmt.Errorf("measuring %s: %w", path, err)
	}
	f := strings.Fields(res.Stdout)
	if len(f) == 0 {
		return 0, fmt.Errorf("measuring %s: no output", path)
	}
	return strconv.ParseUint(f[0], 10, 64)
}
