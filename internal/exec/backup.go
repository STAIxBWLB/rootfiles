package exec

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"
)

// Backup preserves the prior version of every file a run overwrites or
// removes, so the run can be rolled back. The session directory is created
// lazily on the first preserved path; runs that change nothing leave no
// trace.
type Backup struct {
	Root string // e.g. /var/lib/rootfiles/backups
	ID   string // session id, e.g. 20260926-101500
	Note string // free-form (command line)

	mu      sync.Mutex
	entries []BackupEntry
	seen    map[string]bool
}

// BackupEntry records one path's state before the run first touched it.
type BackupEntry struct {
	Path    string      `json:"path"`
	Existed bool        `json:"existed"`
	Kind    string      `json:"kind,omitempty"` // file | symlink
	Mode    os.FileMode `json:"mode,omitempty"`
	UID     int         `json:"uid,omitempty"`
	GID     int         `json:"gid,omitempty"`
	Target  string      `json:"target,omitempty"` // symlink target
}

// BackupManifest is stored as manifest.json in each session directory.
type BackupManifest struct {
	ID        string        `json:"id"`
	Note      string        `json:"note,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
	Entries   []BackupEntry `json:"entries"`
}

// NewBackup starts a backup session under root.
func NewBackup(root, note string) *Backup {
	return &Backup{Root: root, ID: time.Now().Format("20060102-150405"), Note: note, seen: map[string]bool{}}
}

func (b *Backup) dir() string { return filepath.Join(b.Root, b.ID) }

// Used reports whether anything was preserved.
func (b *Backup) Used() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.entries) > 0
}

// Preserve snapshots path the first time it is seen in this session.
// Directories are not preserved (rootfiles never overwrites one).
func (b *Backup) Preserve(path string) error {
	if b == nil {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.seen[abs] {
		return nil
	}
	b.seen[abs] = true

	e := BackupEntry{Path: abs}
	fi, err := os.Lstat(abs)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// Recorded so rollback can remove what the run created.
	case err != nil:
		return err
	case fi.IsDir():
		return nil
	case fi.Mode()&os.ModeSymlink != 0:
		e.Existed, e.Kind = true, "symlink"
		if e.Target, err = os.Readlink(abs); err != nil {
			return err
		}
	case fi.Mode().IsRegular():
		e.Existed, e.Kind, e.Mode = true, "file", fi.Mode().Perm()
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			e.UID, e.GID = int(st.Uid), int(st.Gid)
		}
		if err := copyFile(abs, filepath.Join(b.dir(), "files", abs), 0600); err != nil {
			return fmt.Errorf("backing up %s: %w", abs, err)
		}
	default:
		return nil
	}
	b.entries = append(b.entries, e)
	return b.writeManifest()
}

func (b *Backup) writeManifest() error {
	if err := os.MkdirAll(b.dir(), 0700); err != nil {
		return err
	}
	m := BackupManifest{ID: b.ID, Note: b.Note, CreatedAt: time.Now().UTC(), Entries: b.entries}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(b.dir(), "manifest.json"), data, 0600)
}

func copyFile(src, dst string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// ListBackups returns manifests under root, newest first.
func ListBackups(root string) ([]BackupManifest, error) {
	dirs, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []BackupManifest
	for _, d := range dirs {
		m, err := LoadBackup(root, d.Name())
		if err == nil {
			out = append(out, *m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// LoadBackup reads one session's manifest.
func LoadBackup(root, id string) (*BackupManifest, error) {
	if id == "" || filepath.Base(id) != id {
		return nil, fmt.Errorf("invalid backup id %q", id)
	}
	data, err := os.ReadFile(filepath.Join(root, id, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m BackupManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// RestoreBackup puts every recorded path back to its pre-run state: files
// and symlinks are restored, paths the run created are removed. Returns a
// description of each action. With dryRun nothing is changed.
func RestoreBackup(root, id string, dryRun bool) ([]string, error) {
	m, err := LoadBackup(root, id)
	if err != nil {
		return nil, err
	}
	var actions []string
	for i := len(m.Entries) - 1; i >= 0; i-- {
		e := m.Entries[i]
		switch {
		case !e.Existed:
			// Directories the run created (e.g. a rehome backup) are left
			// alone: they may hold data and are never removed recursively.
			if fi, err := os.Lstat(e.Path); err == nil && fi.IsDir() {
				actions = append(actions, "keep directory "+e.Path)
				continue
			}
			actions = append(actions, "remove "+e.Path)
			if !dryRun {
				if err := os.Remove(e.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
					return actions, err
				}
			}
		case e.Kind == "symlink":
			actions = append(actions, fmt.Sprintf("symlink %s -> %s", e.Path, e.Target))
			if !dryRun {
				_ = os.Remove(e.Path)
				if err := os.Symlink(e.Target, e.Path); err != nil {
					return actions, err
				}
			}
		case e.Kind == "file":
			actions = append(actions, "restore "+e.Path)
			if !dryRun {
				if err := os.MkdirAll(filepath.Dir(e.Path), 0755); err != nil {
					return actions, err
				}
				_ = os.Remove(e.Path) // may currently be a symlink
				if err := copyFile(filepath.Join(root, id, "files", e.Path), e.Path, e.Mode); err != nil {
					return actions, err
				}
				if err := os.Chmod(e.Path, e.Mode); err != nil {
					return actions, err
				}
				_ = os.Lchown(e.Path, e.UID, e.GID)
			}
		}
	}
	return actions, nil
}
