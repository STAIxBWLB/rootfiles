package exec

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	osexec "os/exec"
	"strings"
)

// Runner wraps shell command execution and file I/O with dry-run support.
type Runner struct {
	DryRun bool
	Logger *slog.Logger
	// Backup, when set, preserves each file before it is overwritten,
	// renamed over or removed, enabling `rootfiles rollback`.
	Backup *Backup
	// QueryGuard, when set, checks the resolved executable before Query runs it.
	// Tests use it to refuse accidental calls to host binaries; nil permits all.
	QueryGuard func(resolvedPath string) error
}

// preserve snapshots path into the backup session (no-op without one).
func (r *Runner) preserve(path string) error {
	if r.Backup == nil {
		return nil
	}
	if err := r.Backup.Preserve(path); err != nil {
		return fmt.Errorf("backing up %s before change: %w", path, err)
	}
	return nil
}

// Result holds the output of a command execution.
type Result struct {
	Command  string
	Stdout   string
	Stderr   string
	ExitCode int
}

// NewRunner creates a new Runner.
func NewRunner(dryRun bool, logger *slog.Logger) *Runner {
	return &Runner{DryRun: dryRun, Logger: logger}
}

// Query executes a read-only command that always runs, even in dry-run mode.
// Use this for status checks (dpkg -s, ufw status, etc.) that don't modify the system.
func (r *Runner) Query(ctx context.Context, name string, args ...string) (*Result, error) {
	var stdout strings.Builder
	result, err := r.QueryOutput(ctx, &stdout, name, args...)
	result.Stdout = stdout.String()
	return result, err
}

// QueryOutput streams read-only stdout to dst, including in dry run, with the
// same executable guard as Query. Result.Stdout stays empty; bytes are not logged.
func (r *Runner) QueryOutput(ctx context.Context, dst io.Writer, name string, args ...string) (*Result, error) {
	cmdStr := name + " " + strings.Join(args, " ")
	r.Logger.Info("query", "cmd", cmdStr)
	cmd := osexec.CommandContext(ctx, name, args...)
	if r.QueryGuard != nil && cmd.Err == nil {
		if err := r.QueryGuard(cmd.Path); err != nil {
			return &Result{Command: cmdStr, ExitCode: -1}, fmt.Errorf("query %q refused before execution: %w", cmdStr, err)
		}
	}
	var stderr strings.Builder
	cmd.Stdout = dst
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := &Result{
		Command: cmdStr,
		Stderr:  stderr.String(),
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		return result, fmt.Errorf("command %q failed: %w\nstderr: %s", cmdStr, err, result.Stderr)
	}
	return result, nil
}

// Run executes a command. In dry-run mode, logs but does not execute.
func (r *Runner) Run(ctx context.Context, name string, args ...string) (*Result, error) {
	return r.RunEnv(ctx, nil, name, args...)
}

// RunEnv is Run with extra environment variables ("KEY=value") appended to
// the inherited environment.
func (r *Runner) RunEnv(ctx context.Context, env []string, name string, args ...string) (*Result, error) {
	cmdStr := name + " " + strings.Join(args, " ")

	if r.DryRun {
		r.Logger.Info("dry-run", "cmd", cmdStr)
		return &Result{Command: cmdStr}, nil
	}

	r.Logger.Info("exec", "cmd", cmdStr)
	cmd := osexec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := &Result{
		Command: cmdStr,
		Stdout:  stdout.String(),
		Stderr:  stderr.String(),
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		return result, fmt.Errorf("command %q failed: %w\nstderr: %s", cmdStr, err, result.Stderr)
	}
	return result, nil
}

// RunInput is Run with stdin fed from input. The input is never logged,
// so it is the safe way to pass secrets (e.g. to chpasswd).
func (r *Runner) RunInput(ctx context.Context, input string, name string, args ...string) (*Result, error) {
	cmdStr := name + " " + strings.Join(args, " ")

	if r.DryRun {
		r.Logger.Info("dry-run", "cmd", cmdStr, "stdin", "<redacted>")
		return &Result{Command: cmdStr}, nil
	}

	r.Logger.Info("exec", "cmd", cmdStr, "stdin", "<redacted>")
	cmd := osexec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := &Result{
		Command: cmdStr,
		Stdout:  stdout.String(),
		Stderr:  stderr.String(),
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		return result, fmt.Errorf("command %q failed: %w\nstderr: %s", cmdStr, err, result.Stderr)
	}
	return result, nil
}

// RunShell executes a command via "sh -c" for pipes and redirects.
func (r *Runner) RunShell(ctx context.Context, script string) (*Result, error) {
	return r.Run(ctx, "sh", "-c", script)
}

// CommandExists checks if a command is available in PATH (never dry-run gated).
func (r *Runner) CommandExists(name string) bool {
	_, err := osexec.LookPath(name)
	return err == nil
}

// FileExists checks if a path exists (never dry-run gated).
func (r *Runner) FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ReadFile reads a file (never dry-run gated — reads are always real).
func (r *Runner) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// WriteFile writes content to a path. Respects dry-run.
func (r *Runner) WriteFile(path string, content []byte, perm os.FileMode) error {
	if r.DryRun {
		r.Logger.Info("dry-run: write file", "path", path, "size", len(content))
		return nil
	}
	if err := r.preserve(path); err != nil {
		return err
	}
	r.Logger.Info("write file", "path", path, "size", len(content))
	return os.WriteFile(path, content, perm)
}

// MkdirAll creates directories. Respects dry-run.
func (r *Runner) MkdirAll(path string, perm os.FileMode) error {
	if r.DryRun {
		r.Logger.Info("dry-run: mkdir", "path", path)
		return nil
	}
	return os.MkdirAll(path, perm)
}

// Symlink creates a symbolic link. Respects dry-run.
func (r *Runner) Symlink(target, link string) error {
	if r.DryRun {
		r.Logger.Info("dry-run: symlink", "target", target, "link", link)
		return nil
	}
	if err := r.preserve(link); err != nil {
		return err
	}
	r.Logger.Info("symlink", "target", target, "link", link)
	return os.Symlink(target, link)
}

// Remove deletes a single file, symlink, or empty directory. It never
// recurses, so a populated directory yields an error instead of data loss.
// Respects dry-run.
func (r *Runner) Remove(path string) error {
	if r.DryRun {
		r.Logger.Info("dry-run: remove", "path", path)
		return nil
	}
	if err := r.preserve(path); err != nil {
		return err
	}
	r.Logger.Info("remove", "path", path)
	return os.Remove(path)
}

// Rename moves a path. Respects dry-run.
func (r *Runner) Rename(oldPath, newPath string) error {
	if r.DryRun {
		r.Logger.Info("dry-run: rename", "from", oldPath, "to", newPath)
		return nil
	}
	if err := r.preserve(newPath); err != nil {
		return err
	}
	r.Logger.Info("rename", "from", oldPath, "to", newPath)
	return os.Rename(oldPath, newPath)
}
