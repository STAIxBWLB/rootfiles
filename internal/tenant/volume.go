package tenant

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// EnsureVolume creates the tenant volume image, its mount unit and the
// data/home/tmp directories, and grows the image to the spec size.
func (m *Manager) EnsureVolume(ctx context.Context, s *Spec) error {
	p := m.Paths(s.Name)
	size, err := s.Volume.Bytes()
	if err != nil {
		return err
	}
	if err := m.run(ctx, "install", "-d", "-m", "0755", m.Root); err != nil {
		return err
	}
	// 0700: host users must not reach setuid files a tenant leaves in its volume.
	if err := m.run(ctx, "install", "-d", "-m", "0700", p.Dir); err != nil {
		return err
	}
	if err := m.run(ctx, "install", "-d", "-m", "0755", p.Mount); err != nil {
		return err
	}
	fresh := !m.Runner.FileExists(p.Image)
	if fresh {
		m.printf("Creating %s volume of %s for %s\n", s.Volume.Filesystem(), s.Volume.Size, s.Name)
		if err := m.run(ctx, "truncate", "-s", strconv.FormatUint(size, 10), p.Image); err != nil {
			return err
		}
		mkfs := []string{"mkfs.ext4", "-q", "-F", "-m", "0", p.Image}
		if s.Volume.Filesystem() == "xfs" {
			mkfs = []string{"mkfs.xfs", "-q", p.Image}
		}
		if err := m.run(ctx, mkfs[0], mkfs[1:]...); err != nil {
			return err
		}
	}
	if err := m.ensureMountUnit(ctx, s); err != nil {
		return err
	}
	if !m.Runner.DryRun && !isMountPoint(p.Mount) {
		return fmt.Errorf("%s is not mounted; check `systemctl status %s`", p.Mount, UnitName(p.Mount))
	}
	uid, gid := strconv.Itoa(s.Users[0].UID), strconv.Itoa(s.Users[0].GID)
	for _, d := range [][]string{
		{"-m", "0755", "-o", uid, "-g", gid, filepath.Join(p.Mount, "data")},
		{"-m", "0755", filepath.Join(p.Mount, "home")},
		{"-m", "1777", filepath.Join(p.Mount, "tmp")},
	} {
		if !fresh && m.Runner.FileExists(d[len(d)-1]) {
			continue // keep what the tenant changed
		}
		if err := m.run(ctx, "install", append([]string{"-d"}, d...)...); err != nil {
			return err
		}
	}
	if fresh {
		return nil
	}
	cur, err := fileSize(p.Image)
	if err != nil {
		return err
	}
	switch {
	case size > cur:
		return m.grow(ctx, s, size)
	case size < cur:
		m.printf("⚠ %s volume is %s, larger than the spec's %s; shrink it with `rootfiles tenant volume resize %s %s --offline`\n",
			s.Name, human(cur), s.Volume.Size, s.Name, s.Volume.Size)
	}
	return nil
}

func (m *Manager) ensureMountUnit(ctx context.Context, s *Spec) error {
	p := m.Paths(s.Name)
	unit := UnitName(p.Mount)
	changed, err := m.writeUnit(s)
	if err != nil {
		return err
	}
	if changed {
		if err := m.run(ctx, "systemctl", "daemon-reload"); err != nil {
			return err
		}
	}
	if res, err := m.Runner.Query(ctx, "systemctl", "is-enabled", unit); err != nil || strings.TrimSpace(res.Stdout) != "enabled" {
		if err := m.run(ctx, "systemctl", "enable", unit); err != nil {
			return err
		}
	}
	if m.Runner.DryRun || !isMountPoint(p.Mount) {
		return m.run(ctx, "systemctl", "start", unit)
	}
	return nil
}

// writeUnit writes the mount unit when it differs and reports a change.
func (m *Manager) writeUnit(s *Spec) (bool, error) {
	want := MountUnit(s.Name, m.Paths(s.Name), s.Volume.Filesystem())
	path := m.unitPath(s.Name)
	if cur, err := os.ReadFile(path); err == nil && string(cur) == want {
		return false, nil
	}
	return true, m.writeIfChanged(path, []byte(want), 0o644)
}

// UnitDrift reports whether tenant s's mount unit file is missing or stale.
func (m *Manager) UnitDrift(s *Spec) bool {
	cur, err := os.ReadFile(m.unitPath(s.Name))
	return err != nil || string(cur) != MountUnit(s.Name, m.Paths(s.Name), s.Volume.Filesystem())
}

// WriteUnit writes tenant s's mount unit file; the caller reloads systemd.
func (m *Manager) WriteUnit(s *Spec) error {
	_, err := m.writeUnit(s)
	return err
}

// UnitFor returns the mount unit name of tenant t.
func (m *Manager) UnitFor(t string) string { return UnitName(m.Paths(t).Mount) }

func fileSize(path string) (uint64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return uint64(fi.Size()), nil
}

// loopDevice returns the loop device backing the tenant image.
func (m *Manager) loopDevice(ctx context.Context, p Paths) (string, error) {
	res, err := m.Runner.Query(ctx, "losetup", "-j", p.Image)
	if err != nil {
		return "", err
	}
	dev, _, ok := strings.Cut(strings.TrimSpace(res.Stdout), ":")
	if !ok || !strings.HasPrefix(dev, "/dev/loop") {
		return "", fmt.Errorf("no loop device backs %s; is %s mounted?", p.Image, p.Mount)
	}
	return dev, nil
}

// grow enlarges a mounted volume without stopping the container.
func (m *Manager) grow(ctx context.Context, s *Spec, size uint64) error {
	p := m.Paths(s.Name)
	dev := "/dev/loopN"
	if !m.Runner.DryRun {
		d, err := m.loopDevice(ctx, p)
		if err != nil {
			return err
		}
		dev = d
	}
	m.printf("Growing %s volume to %s online\n", s.Name, human(size))
	if err := m.run(ctx, "truncate", "-s", strconv.FormatUint(size, 10), p.Image); err != nil {
		return err
	}
	if err := m.run(ctx, "losetup", "-c", dev); err != nil {
		return err
	}
	if s.Volume.Filesystem() == "xfs" {
		return m.run(ctx, "xfs_growfs", p.Mount)
	}
	return m.run(ctx, "resize2fs", dev)
}

// Resize sets the volume size. Growing works while the container runs;
// shrinking needs offline (ext4 only), which stops the container.
func (m *Manager) Resize(ctx context.Context, s *Spec, newSize string, offline bool) error {
	s.Volume.Size = newSize
	if err := s.Validate(); err != nil {
		return err
	}
	size, _ := s.Volume.Bytes()
	p := m.Paths(s.Name)
	cur, err := fileSize(p.Image)
	if err != nil {
		return fmt.Errorf("tenant %s has no volume image: %w", s.Name, err)
	}
	switch {
	case size == cur:
		m.printf("%s volume is already %s\n", s.Name, human(size))
	case size > cur:
		if err := m.grow(ctx, s, size); err != nil {
			return err
		}
	case !offline:
		return fmt.Errorf("shrinking a mounted volume is not supported; rerun with --offline to stop %s and shrink it", ContainerName(s.Name))
	case s.Volume.Filesystem() != "ext4":
		return fmt.Errorf("%s volumes cannot shrink", s.Volume.Filesystem())
	default:
		if err := m.shrink(ctx, s, size); err != nil {
			return err
		}
	}
	return m.SaveSpec(s)
}

func (m *Manager) shrink(ctx context.Context, s *Spec, size uint64) error {
	p := m.Paths(s.Name)
	name := ContainerName(s.Name)
	unit := UnitName(p.Mount)
	running := m.inspect(ctx, name).Running
	m.printf("Shrinking %s volume to %s offline\n", s.Name, human(size))
	if running {
		if err := m.run(ctx, "docker", "stop", name); err != nil {
			return err
		}
	}
	if err := m.run(ctx, "systemctl", "stop", unit); err != nil {
		return err
	}
	// e2fsck exits 1 after correcting errors; the image is then consistent.
	if res, err := m.Runner.Run(ctx, "e2fsck", "-f", "-p", p.Image); err != nil && (res == nil || res.ExitCode != 1) {
		return fmt.Errorf("checking %s before shrink (volume left unmounted): %w", p.Image, err)
	}
	if err := m.run(ctx, "resize2fs", p.Image, strconv.FormatUint(size/1024, 10)+"K"); err != nil {
		if m.run(ctx, "systemctl", "start", unit) == nil && running {
			_ = m.run(ctx, "docker", "start", name)
		}
		return fmt.Errorf("shrinking filesystem (data too large for %s?): %w", s.Volume.Size, err)
	}
	if err := m.run(ctx, "truncate", "-s", strconv.FormatUint(size, 10), p.Image); err != nil {
		return err
	}
	if err := m.run(ctx, "systemctl", "start", unit); err != nil {
		return err
	}
	if running {
		return m.run(ctx, "docker", "start", name)
	}
	return nil
}
