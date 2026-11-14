package tenant

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// measureDockerKeep reads without writing even in dry run. Inspect every parent
// separately so a symlink in the source path cannot be hidden by Docker's
// container-root resolution of the final path.
func (m *Manager) measureDockerKeep(ctx context.Context, from, keep string) (uint64, error) {
	parts := strings.Split(strings.TrimPrefix(keep, "/"), "/")
	for i := 1; i < len(parts); i++ {
		prefix := "/" + strings.Join(parts[:i], "/")
		if _, err := m.dockerArchiveSize(ctx, from, prefix, true); err != nil {
			return 0, err
		}
	}
	return m.dockerArchiveSize(ctx, from, keep, false)
}

type archiveCounter struct {
	io.Reader
	count uint64
}

func (r *archiveCounter) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if ^uint64(0)-r.count < uint64(n) {
		return n, fmt.Errorf("archive size overflow")
	}
	r.count += uint64(n)
	return n, err
}

func (m *Manager) dockerArchiveSize(ctx context.Context, from, keep string, parent bool) (uint64, error) {
	ctx, cancel := context.WithCancel(ctx)
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, err := m.Runner.QueryOutput(ctx, writer, "docker", "cp", "--", from+":"+keep, "-")
		writer.CloseWithError(err)
		done <- err
	}()
	defer func() { cancel(); reader.Close(); <-done }()
	counter := &archiveCounter{Reader: reader}
	tr := tar.NewReader(counter)
	root := path.Base(keep)
	entries := map[string]byte{}
	links := map[string]string{}
	var allocation uint64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("--keep %s: reading Docker archive: %w", keep, err)
		}
		name := strings.TrimSuffix(h.Name, "/")
		if !safeArchiveName(name) || (name != root && !strings.HasPrefix(name, root+"/")) {
			return 0, fmt.Errorf("--keep %s: archive path %q escapes the selected path; choose an ordinary path inside the container", keep, h.Name)
		}
		if _, exists := entries[name]; exists {
			return 0, fmt.Errorf("--keep %s: duplicate archive path %q", keep, name)
		}
		if len(entries) == 0 && (name != root || h.Typeflag == tar.TypeSymlink || h.Typeflag == tar.TypeLink || (parent && h.Typeflag != tar.TypeDir)) {
			return 0, fmt.Errorf("--keep %s: not an ordinary path inside the container (symlink source); choose its real /home or /tmp path", keep)
		}
		switch h.Typeflag {
		case tar.TypeReg, tar.TypeRegA, tar.TypeDir:
		case tar.TypeSymlink:
			containerName := path.Join(path.Dir(keep), name)
			if escapesContainer(path.Dir(containerName), h.Linkname) {
				return 0, fmt.Errorf("--keep %s: symlink %s escapes the container; remove it before import", keep, name)
			}
		case tar.TypeLink:
			if !safeArchiveName(h.Linkname) || !strings.HasPrefix(h.Linkname, root+"/") {
				return 0, fmt.Errorf("--keep %s: hardlink %s escapes the selected path; remove it before import", keep, name)
			}
			links[name] = h.Linkname
		default:
			return 0, fmt.Errorf("--keep %s: unsupported archive entry %s; remove device or special files before import", keep, name)
		}
		entries[name] = h.Typeflag
		needed, err := archiveAllocation(h)
		if err != nil || ^uint64(0)-allocation < needed {
			return 0, fmt.Errorf("--keep %s: archive size overflow", keep)
		}
		allocation += needed
		if parent {
			return 0, nil
		} // Only the directory itself is relevant to a parent probe.
	}
	if len(entries) == 0 {
		return 0, fmt.Errorf("--keep %s: Docker returned an empty archive", keep)
	}
	for name := range entries {
		for ancestor := path.Dir(name); ancestor != "."; ancestor = path.Dir(ancestor) {
			if kind, ok := entries[ancestor]; ok && kind != tar.TypeDir {
				return 0, fmt.Errorf("--keep %s: archive path %s traverses a non-directory; stop writes and retry", keep, name)
			}
		}
	}
	for name, target := range links {
		if kind, ok := entries[target]; !ok || (kind != tar.TypeReg && kind != tar.TypeRegA) {
			return 0, fmt.Errorf("--keep %s: hardlink %s has no regular in-archive target; remove it before import", keep, name)
		}
	}
	// Drain padding and wait for the command error through the pipe, so a failed
	// docker cp cannot be mistaken for a successfully terminated tar stream.
	if _, err := io.Copy(io.Discard, counter); err != nil {
		return 0, fmt.Errorf("--keep %s: Docker archive failed: %w", keep, err)
	}
	return max(counter.count, allocation), nil
}

func safeArchiveName(name string) bool {
	if name == "" || path.IsAbs(name) || path.Clean(name) != name {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

func escapesContainer(base, target string) bool {
	depth := 0
	if !path.IsAbs(target) {
		depth = len(strings.Split(strings.Trim(base, "/"), "/"))
	}
	for _, part := range strings.Split(target, "/") {
		switch part {
		case "", ".":
		case "..":
			depth--
			if depth < 0 {
				return true
			}
		default:
			depth++
		}
	}
	return false
}

// copyDockerKeep uses Docker's confined extraction into a fresh private host
// directory. No untrusted archive is extracted over an existing volume tree.
// -a preserves numeric UID/GID; rsync never follows the staged symbolic links.
func (m *Manager) copyDockerKeep(ctx context.Context, from, keep, mount string) error {
	stage := filepath.Join(m.Root, ".rootfiles-import-DRYRUN")
	if !m.Runner.DryRun {
		result, err := m.Runner.Run(ctx, "mktemp", "-d", filepath.Join(m.Root, ".rootfiles-import-XXXXXXXX"))
		if err != nil {
			return fmt.Errorf("creating private import staging: %w", err)
		}
		stage = strings.TrimSpace(result.Stdout)
		if filepath.Dir(stage) != filepath.Clean(m.Root) || !strings.HasPrefix(filepath.Base(stage), ".rootfiles-import-") {
			return fmt.Errorf("mktemp returned an unexpected staging path; inspect %s before retrying", m.Root)
		}
		defer func() {
			if err := m.run(ctx, "rm", "-r", "--", stage); err != nil {
				m.printf("Import temporary directory %s could not be removed: %v\n", stage, err)
			}
		}()
	}
	rel := strings.TrimPrefix(keep, "/")
	parent := filepath.Join(stage, filepath.Dir(rel))
	if err := m.Runner.MkdirAll(parent, 0755); err != nil {
		return err
	}
	if err := m.run(ctx, "docker", "cp", "-a", "--", from+":"+keep, parent+"/"); err != nil {
		return fmt.Errorf("copying %s:%s to private staging: %w", from, keep, err)
	}
	destination := filepath.Join(mount, filepath.Dir(rel))
	if !m.Runner.DryRun {
		if err := safeImportDestination(mount, destination); err != nil {
			return err
		}
	}
	if err := m.Runner.MkdirAll(destination, 0755); err != nil {
		return err
	}
	if err := m.run(ctx, "rsync", "-aHAX", filepath.Join(stage, rel), destination+"/"); err != nil {
		return fmt.Errorf("copying kept path %s into tenant volume: %w", keep, err)
	}
	return nil
}

// The volume may remain from an earlier failed import. Refuse symlinked
// destination parents rather than allowing rsync to traverse them on the host.
func safeImportDestination(root, destination string) error {
	rel, err := filepath.Rel(root, destination)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("import destination leaves tenant volume")
	}
	current := root
	for _, part := range append([]string{""}, strings.Split(rel, string(filepath.Separator))...) {
		current = filepath.Join(current, part)
		fi, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return fmt.Errorf("import destination %s is not an ordinary directory; remove the symlink or choose a clean tenant volume", current)
		}
	}
	return nil
}

// Both supported mkfs defaults use 4KiB blocks. Allow an extra block per entry
// for inode/directory metadata rather than treating a tiny file as one byte.
// The existing 5% volume margin remains additional to this estimate.
func archiveAllocation(h *tar.Header) (uint64, error) {
	const block uint64 = 4096
	if h.Size < 0 {
		return 0, fmt.Errorf("negative archive size")
	}
	n := uint64(h.Size)
	if n > ^uint64(0)-2*block {
		return 0, fmt.Errorf("archive allocation overflow")
	}
	if h.Typeflag == tar.TypeReg || h.Typeflag == tar.TypeRegA {
		return ((n+block-1)/block)*block + block, nil
	}
	return 2 * block, nil
}
