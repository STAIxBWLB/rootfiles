package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/exec"
	"github.com/STAIxBWLB/rootfiles/internal/state"
)

// Shared runtime plumbing for mutating commands: root/lock preflight,
// audit logging and per-run file backups.

var nativeHostOS = runtime.GOOS

const annotMutates = "rootfiles/mutates"

// mutatingCommands change the system; they need root and the global lock.
var mutatingCommands = [][]string{
	{"apply"},
	{"update"},
	{"user", "add"}, {"user", "restore"}, {"user", "rehome"},
	{"user", "group-add"}, {"user", "group-del"}, {"user", "passwd"},
	{"user", "del"}, {"user", "lock"}, {"user", "unlock"}, {"user", "expire"},
	{"user", "key", "add"}, {"user", "key", "rm"},
	{"user", "quota", "set"}, {"user", "quota", "rm"},
	{"schedule", "enable"}, {"schedule", "disable"},
	{"gpu", "assign"}, {"gpu", "revoke"},
	{"tunnel", "install"}, {"tunnel", "setup"}, {"tunnel", "restart"},
	{"tunnel", "update"}, {"tunnel", "uninstall"},
}

func markMutating(root *cobra.Command) {
	for _, path := range mutatingCommands {
		c, _, err := root.Find(path)
		if err != nil || c == root {
			continue
		}
		if c.Annotations == nil {
			c.Annotations = map[string]string{}
		}
		c.Annotations[annotMutates] = "true"
	}
}

// heldLock is released when the process exits; kept for explicit release.
var heldLock *state.Lock

// geteuid is overridable in tests.
var geteuid = os.Geteuid

// preflight enforces root and the global lock for mutating commands.
// Dry runs are exempt: they only read the system.
func preflight(cmd *cobra.Command, _ []string) error {
	if cmd.Annotations[annotMutates] != "true" {
		return nil
	}
	if f := cmd.Flag("dry-run"); f != nil && f.Value.String() == "true" {
		return nil
	}
	return requireRootAndLock(cmd)
}

// requireRootAndLock checks for root and takes the global lock.
func requireRootAndLock(cmd *cobra.Command) error {
	if geteuid() != 0 {
		return fmt.Errorf("'%s' changes the system and must run as root (try: sudo %s ...)",
			cmd.CommandPath(), cmd.CommandPath())
	}
	if nativeHostOS != "linux" {
		return fmt.Errorf("native host mutations require Linux; use the fleet controller on %s", nativeHostOS)
	}
	lock, err := state.Acquire()
	if err != nil {
		return err
	}
	heldLock = lock
	auditf(cmd, "start", "args", strings.Join(os.Args[1:], " "))
	return nil
}

// auditLogger appends JSON records to the audit log; nil when unavailable
// (e.g. not root). Opened lazily once per process.
var auditLogger *slog.Logger

func openAudit() *slog.Logger {
	if auditLogger != nil {
		return auditLogger
	}
	f, err := os.OpenFile(state.LogPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		return nil
	}
	auditLogger = slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})).
		With("pid", os.Getpid(), "version", buildVersion)
	return auditLogger
}

func auditf(cmd *cobra.Command, msg string, args ...any) {
	if l := openAudit(); l != nil {
		l.Info(msg, append([]any{"cmd", cmd.CommandPath()}, args...)...)
	}
}

// newLogger returns the operator-facing logger (stderr text) teed into the
// audit log for real (non-dry-run) mutating runs.
func newLogger(cmd *cobra.Command, dryRun bool, level slog.Level) *slog.Logger {
	console := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	if dryRun || cmd.Annotations[annotMutates] != "true" {
		return slog.New(console)
	}
	audit := openAudit()
	if audit == nil {
		return slog.New(console)
	}
	return slog.New(teeHandler{console, audit.Handler().WithAttrs([]slog.Attr{slog.String("cmd", cmd.CommandPath())})})
}

// newRunner builds a Runner for cmd; real mutating runs get a backup
// session so overwritten files can be rolled back.
func newRunner(cmd *cobra.Command, dryRun bool) *exec.Runner {
	r := exec.NewRunner(dryRun, newLogger(cmd, dryRun, slog.LevelInfo))
	if !dryRun && cmd.Annotations[annotMutates] == "true" && geteuid() == 0 {
		r.Backup = exec.NewBackup(state.BackupsDir(), strings.Join(os.Args, " "))
	}
	return r
}

// teeHandler fans records out to several handlers.
type teeHandler []slog.Handler

func (t teeHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range t {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (t teeHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range t {
		if h.Enabled(ctx, r.Level) {
			_ = h.Handle(ctx, r.Clone())
		}
	}
	return nil
}

func (t teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(teeHandler, len(t))
	for i, h := range t {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (t teeHandler) WithGroup(name string) slog.Handler {
	out := make(teeHandler, len(t))
	for i, h := range t {
		out[i] = h.WithGroup(name)
	}
	return out
}
