package module

import (
	"context"
	"fmt"
	"os"

	"github.com/STAIxBWLB/rootfiles/internal/exec"
)

// fileEquals retains the existing management-marker comparison for Check.
func fileEquals(rc *RunContext, path, want string) bool {
	data, err := rc.Runner.ReadFile(path)
	return err == nil && exec.ManagedContentEqual(string(data), want)
}

// ensureFile writes only changed content. Callers own parent-directory modes,
// metadata safety, atomic replacement and any special whitespace policy.
// The mode is used for a new file; matching bytes do not claim a mode repair.
func ensureFile(rc *RunContext, path, content string, mode os.FileMode) (bool, error) {
	data, err := rc.Runner.ReadFile(path)
	if err == nil && exec.ManagedContentEqual(string(data), content) {
		return false, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("cannot read managed file %s: %w; repair that path or its permissions before retrying", path, err)
	}
	if err := rc.Runner.WriteFile(path, []byte(content), mode); err != nil {
		return false, fmt.Errorf("cannot write managed file %s: %w; check its parent directory and backup permissions before retrying", path, err)
	}
	return true, nil
}

// systemdReload warns after a real unit write. A converged later Check may skip
// Apply, so the warning gives an explicit repair rather than promising a retry.
func systemdReload(ctx context.Context, rc *RunContext) string {
	if _, err := rc.Runner.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return "systemctl daemon-reload failed: " + firstLine(err.Error()) + "; run sudo systemctl daemon-reload after restoring systemd access"
	}
	return ""
}

// enableUnit preserves the caller's exact enable/start command. It adds no
// probes or explicit reload; systemctl enable retains its implicit reload.
// Specialized no-reload sequences and Changed accounting stay at callers.
func enableUnit(ctx context.Context, rc *RunContext, unit string, now bool) error {
	args := []string{"enable"}
	if now {
		args = append(args, "--now")
	}
	args = append(args, unit)
	if _, err := rc.Runner.Run(ctx, "systemctl", args...); err != nil {
		return fmt.Errorf("cannot enable systemd unit %s; inspect systemctl status %s before retrying: %w", unit, unit, err)
	}
	return nil
}
