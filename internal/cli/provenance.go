package cli

import (
	"path/filepath"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/exec"
	"github.com/STAIxBWLB/rootfiles/internal/state"
)

// appliedFingerprint only claims provenance for a successful apply of the
// same target. Callers pass the reported target (reportedTarget), which is
// the recorded profile and path when the verified applied copy was loaded,
// so the copy's proven run claims provenance too. Legacy state without a
// stored digest remains unverified.
func appliedFingerprint(cfg *config.Config, profile, path string) (string, error) {
	last, err := state.Last()
	if err != nil {
		return "", err
	}
	if last == nil || !last.Success || last.ConfigSHA256 == "" {
		return "", nil
	}
	if last.Profile != profile || last.ConfigPath != recordedConfigPath(path) {
		return "", nil
	}
	current, err := cfg.Fingerprint()
	if err != nil {
		return "", err
	}
	if current != last.ConfigSHA256 {
		return "", nil
	}
	return current, nil
}

// recordedConfigPath is the form apply records for --config: absolute, so a
// later run from another directory cannot pick up a different file. Stdin
// ("-") and an empty path are kept.
func recordedConfigPath(path string) string {
	if path == "" || path == "-" {
		return path
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// Reporting still works on hosts with unsupported configuration: capability
// errors are returned as report findings, never as package mutations.
func statusPackageManager(runner *exec.Runner, sys *config.SystemInfo) exec.PackageManager {
	pm, err := exec.NewPackageManager(runner, sys)
	if err != nil {
		if sys != nil && sys.OS == "rocky" {
			return exec.NewRPM(runner)
		}
		return exec.NewAPT(runner)
	}
	return pm
}
