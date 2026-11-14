// Package state records what rootfiles did on this host: the last applied
// profile and per-module outcome, an append-only history, an audit log, and
// the process-wide lock that serialises mutating commands.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Locations, overridable via environment for tests and non-standard hosts.
func Dir() string      { return envOr("ROOTFILES_STATE_DIR", "/var/lib/rootfiles") }
func LogPath() string  { return envOr("ROOTFILES_LOG_FILE", "/var/log/rootfiles.log") }
func LockPath() string { return envOr("ROOTFILES_LOCK_FILE", "/run/rootfiles.lock") }

// BackupsDir holds per-run copies of files rootfiles overwrote.
func BackupsDir() string { return filepath.Join(Dir(), "backups") }

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ModuleOutcome is the result of one module in an apply run.
type ModuleOutcome struct {
	Name     string   `json:"name"`
	Status   string   `json:"status"` // satisfied | changed | failed | skipped
	Error    string   `json:"error,omitempty"`
	Messages []string `json:"messages,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// Run describes one `rootfiles apply`.
type Run struct {
	ConfigSHA256 string          `json:"config_sha256,omitempty"`
	Version      string          `json:"version"`
	Profile      string          `json:"profile,omitempty"`
	ConfigPath   string          `json:"config_path,omitempty"`
	StartedAt    time.Time       `json:"started_at"`
	FinishedAt   time.Time       `json:"finished_at"`
	Success      bool            `json:"success"`
	BackupID     string          `json:"backup_id,omitempty"`
	Modules      []ModuleOutcome `json:"modules"`
}

func statePath() string   { return filepath.Join(Dir(), "state.json") }
func historyPath() string { return filepath.Join(Dir(), "history.jsonl") }

// Record saves run as the current state and appends it to the history.
func Record(run Run) error {
	if err := os.MkdirAll(Dir(), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	tmp := statePath() + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, statePath()); err != nil {
		return err
	}
	line, _ := json.Marshal(run)
	f, err := os.OpenFile(historyPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// AppliedConfigPath holds the resolved config of the last recorded apply,
// from a profile or a config file, which subcommands reuse.
func AppliedConfigPath() string { return filepath.Join(Dir(), "applied-config.yaml") }

// SaveAppliedConfig stores data, mode 0600, at AppliedConfigPath, or removes
// that file when data is nil.
func SaveAppliedConfig(data []byte) error {
	if data == nil {
		if err := os.Remove(AppliedConfigPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(Dir(), 0755); err != nil {
		return err
	}
	tmp := AppliedConfigPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, AppliedConfigPath())
}

// Last returns the most recently recorded run, or nil when none exists.
func Last() (*Run, error) {
	data, err := os.ReadFile(statePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Run
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", statePath(), err)
	}
	return &r, nil
}

// Lock is a held process lock.
type Lock struct{ f *os.File }

// LockHeldError identifies an occupied lock without making callers parse text.
// PID is zero when the holder file does not contain a positive numeric PID.
type LockHeldError struct {
	PID  int
	Path string
	Err  error
}

func (e *LockHeldError) Error() string {
	pid := "unknown"
	if e.PID > 0 {
		pid = strconv.Itoa(e.PID)
	}
	return fmt.Sprintf("another rootfiles command is running (pid %s); lock: %s", pid, e.Path)
}

func (e *LockHeldError) Unwrap() error { return e.Err }

// Acquire takes the global rootfiles lock without blocking. It fails with a
// message naming the holder's PID when another mutating command runs.
func Acquire() (*Lock, error) {
	path := LockPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder, _ := os.ReadFile(path)
		f.Close()
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("locking %s: %w", path, err)
		}
		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(holder)))
		if parseErr != nil || pid <= 0 {
			pid = 0
		}
		return nil, &LockHeldError{PID: pid, Path: path, Err: err}
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	return &Lock{f: f}, nil
}

// Release drops the lock. Safe on nil.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	l.f.Close()
	l.f = nil
}
