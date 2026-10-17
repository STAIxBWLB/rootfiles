package module

import (
	"log/slog"
	"os"
	"syscall"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/exec"
)

// TestMain lets the test user stand in for root as the owner of custom home
// bases, which tests create under t.TempDir(): TMPDIR moves to a private
// directory where the ownership walk starts, and umask 022 keeps the
// directories tests create from being group-writable.
func TestMain(m *testing.M) {
	syscall.Umask(0o022)
	dir, err := os.MkdirTemp("", "rootfiles-module-test")
	if err != nil {
		panic(err)
	}
	os.Setenv("TMPDIR", dir)
	homeBaseRoot, homeBaseOwner = dir, uint32(os.Getuid())
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// newDryRunRC returns a RunContext with dry-run Runner and APT. Caller may
// mutate rc.Config to set module-specific toggles. Used by per-module tests
// that exercise Check/Apply without touching the real filesystem or apt-get.
func newDryRunRC(t *testing.T) *RunContext {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	runner := exec.NewRunner(true, logger)
	return &RunContext{
		Config: &config.Config{},
		Runner: runner,
		APT:    exec.NewAPT(runner),
		DryRun: true,
	}
}
