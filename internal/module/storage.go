package module

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

type StorageModule struct{}

func NewStorageModule() *StorageModule { return &StorageModule{} }
func (m *StorageModule) Name() string  { return "storage" }

func (m *StorageModule) Check(_ context.Context, rc *RunContext) (*CheckResult, error) {
	if err := requireTrustedMetadata(rc); err != nil {
		return nil, err
	}
	var changes []Change
	cfg := rc.Config.Modules.Storage

	// Check data directory
	if cfg.DataDir != "" && !rc.Runner.FileExists(cfg.DataDir) {
		changes = append(changes, Change{
			Description: fmt.Sprintf("Create data directory %s", cfg.DataDir),
			Command:     fmt.Sprintf("mkdir -p %s", cfg.DataDir),
		})
	}

	// Check home base
	homeBase := rc.Config.Users.HomeBase
	if homeBase != "" && homeBase != "/home" && !rc.Runner.FileExists(homeBase) {
		changes = append(changes, Change{
			Description: fmt.Sprintf("Create home base directory %s", homeBase),
			Command:     fmt.Sprintf("mkdir -p %s", homeBase),
		})
	}

	// Check metadata directory
	if homeBase != "" && homeBase != "/home" {
		metaDir := filepath.Join(homeBase, ".rootfiles")
		if !rc.Runner.FileExists(metaDir) {
			changes = append(changes, Change{
				Description: "Create rootfiles metadata directory",
				Command:     fmt.Sprintf("mkdir -p %s", metaDir),
			})
		}
	}

	// Check symlinks
	for link, target := range cfg.Symlinks {
		if isSymlinkTo(link, target) {
			continue
		}
		desc := fmt.Sprintf("Create symlink %s "+ui.MarkNext+" %s", link, target)
		if err := symlinkBlocker(link); err != nil {
			desc += fmt.Sprintf(" (blocked: %v)", err)
		}
		changes = append(changes, Change{
			Description: desc,
			Command:     fmt.Sprintf("ln -sfn %s %s", target, link),
		})
	}

	// Check Docker storage directory
	dockerDir := rc.Config.Modules.Docker.StorageDir
	if dockerDir != "" && !rc.Runner.FileExists(dockerDir) {
		changes = append(changes, Change{
			Description: fmt.Sprintf("Create Docker storage directory %s", dockerDir),
			Command:     fmt.Sprintf("mkdir -p %s", dockerDir),
		})
	}

	return &CheckResult{
		Satisfied: len(changes) == 0,
		Changes:   changes,
	}, nil
}

func (m *StorageModule) Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	if err := requireTrustedMetadata(rc); err != nil {
		return nil, err
	}
	cfg := rc.Config.Modules.Storage
	var messages []string
	changed := false

	// Create data directory
	if cfg.DataDir != "" && !rc.Runner.FileExists(cfg.DataDir) {
		if err := rc.Runner.MkdirAll(cfg.DataDir, 0755); err != nil {
			return nil, fmt.Errorf("creating data dir: %w", err)
		}
		messages = append(messages, fmt.Sprintf("data directory %s created", cfg.DataDir))
		changed = true
	}

	// Create home base and metadata directory
	homeBase := rc.Config.Users.HomeBase
	if homeBase != "" && homeBase != "/home" {
		metaDir := filepath.Join(homeBase, ".rootfiles")
		if !rc.Runner.FileExists(homeBase) || !isDir(metaDir) {
			if err := rc.Runner.MkdirAll(homeBase, 0755); err != nil {
				return nil, fmt.Errorf("creating home base: %w", err)
			}
			if err := ensureMetaDir(rc.Runner, homeBase); err != nil {
				return nil, fmt.Errorf("creating metadata dir: %w", err)
			}
			messages = append(messages, fmt.Sprintf("home base %s ready", homeBase))
			changed = true
		}
	}

	// Create symlinks. An existing path is only replaced when doing so
	// cannot lose data: a stale symlink or an empty directory. Anything
	// else is reported as an error so the operator can move it manually.
	for link, target := range cfg.Symlinks {
		if isSymlinkTo(link, target) {
			continue
		}
		if err := symlinkBlocker(link); err != nil {
			return nil, fmt.Errorf("cannot create symlink %s "+ui.MarkNext+" %s: %w", link, target, err)
		}
		if err := rc.Runner.MkdirAll(target, 0755); err != nil {
			return nil, fmt.Errorf("creating symlink target %s: %w", target, err)
		}
		if _, err := os.Lstat(link); err == nil {
			if err := rc.Runner.Remove(link); err != nil {
				return nil, fmt.Errorf("removing %s: %w", link, err)
			}
		}
		if err := rc.Runner.Symlink(target, link); err != nil {
			return nil, fmt.Errorf("creating symlink %s "+ui.MarkNext+" %s: %w", link, target, err)
		}
		messages = append(messages, fmt.Sprintf("symlink %s "+ui.MarkNext+" %s", link, target))
		changed = true
	}

	// Docker storage directory
	dockerDir := rc.Config.Modules.Docker.StorageDir
	if dockerDir != "" && !rc.Runner.FileExists(dockerDir) {
		if err := rc.Runner.MkdirAll(dockerDir, 0710); err != nil {
			return nil, fmt.Errorf("creating docker storage dir: %w", err)
		}
		messages = append(messages, fmt.Sprintf("docker storage directory %s created", dockerDir))
		changed = true
	}

	return &ApplyResult{Changed: changed, Messages: messages}, nil
}

// symlinkBlocker reports why link cannot be safely replaced by a symlink.
// A missing path, an existing symlink, or an empty directory are safe to
// replace; a regular file or a populated directory are not.
func symlinkBlocker(link string) error {
	fi, err := os.Lstat(link)
	if err != nil {
		return nil
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s exists and is not a directory or symlink; move it aside first", link)
	}
	entries, err := os.ReadDir(link)
	if err != nil {
		return fmt.Errorf("reading %s: %w", link, err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("%s is a non-empty directory; move its contents to the target first", link)
	}
	return nil
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func isSymlinkTo(link, target string) bool {
	fi, err := os.Lstat(link)
	if err != nil {
		return false
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return false
	}
	actual, err := os.Readlink(link)
	if err != nil {
		return false
	}
	return actual == target
}
