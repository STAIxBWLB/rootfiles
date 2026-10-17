package module

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/exec"
	"github.com/STAIxBWLB/rootfiles/internal/state"
)

// Module is the interface that all rootfiles modules must implement.
type Module interface {
	Name() string
	Check(ctx context.Context, rc *RunContext) (*CheckResult, error)
	Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error)
}

// RunContext carries config and runtime options to all modules.
type RunContext struct {
	Config *config.Config
	Runner *exec.Runner
	APT    exec.PackageManager
	DryRun bool
	Yes    bool // unattended mode
	// Force bypasses safety guards that would otherwise refuse a change
	// (e.g. disabling SSH password auth with no key-based login available).
	Force bool
	// run maps the modules of the current RunAll or CheckAll to whether
	// they have failed so far; nil outside one.
	run map[string]bool
}

// moduleSucceeding reports whether module name is part of the current run
// and has not failed so far, so a later module can rely on its work.
// Outside RunAll and CheckAll it reports whether the config enables it.
func (rc *RunContext) moduleSucceeding(name string) bool {
	if rc.run == nil {
		return rc.Config.IsModuleEnabled(name)
	}
	failed, ok := rc.run[name]
	return ok && !failed
}

// startRun records the modules of a RunAll or CheckAll.
func (rc *RunContext) startRun(modules []Module) {
	rc.run = make(map[string]bool, len(modules))
	for _, m := range modules {
		rc.run[m.Name()] = false
	}
}

// CheckResult holds the result of a module's Check operation.
type CheckResult struct {
	Satisfied bool
	Changes   []Change
}

// Change describes a single pending change.
type Change struct {
	Description string `json:"description"`
	Command     string `json:"command,omitempty"` // shell command for dry-run display
}

// ApplyResult holds the result of a module's Apply operation.
type ApplyResult struct {
	Changed  bool
	Messages []string
	// Warnings are non-fatal problems the operator should act on
	// (e.g. a service reload that could not be performed).
	Warnings []string
}

// defaultOrder defines the static module execution order.
// Dependencies are implicit in the ordering.
var defaultOrder = []string{
	"locale",
	"system", // before packages: an APT mirror applies to the first install
	"packages",
	"users", // before ssh: declared accounts (and their keys) must exist before password auth is disabled
	"ssh",
	"security", // after ssh: the fail2ban jail follows the configured SSH port
	"docker",
	"nvidia",
	"gpu",
	"cloudflared",
	"storage",
	"network",
	"monitoring",
}

// Registry manages module registration and resolution.
type Registry struct {
	modules map[string]Module
}

// NewRegistry creates a registry with all modules registered.
func NewRegistry() *Registry {
	r := &Registry{modules: make(map[string]Module)}
	r.Register(NewLocaleModule())
	r.Register(NewSystemModule())
	r.Register(NewPackagesModule())
	r.Register(NewSSHModule())
	r.Register(NewSecurityModule())
	r.Register(NewUsersModule())
	r.Register(NewDockerModule())
	r.Register(NewNvidiaModule())
	r.Register(NewGPUModule())
	r.Register(NewCloudflaredModule())
	r.Register(NewStorageModule())
	r.Register(NewNetworkModule())
	r.Register(NewMonitoringModule())
	return r
}

// Register adds or replaces a module.
func (r *Registry) Register(m Module) {
	r.modules[m.Name()] = m
}

// Resolve returns modules in execution order, filtered by config and --module flag.
func (r *Registry) Resolve(cfg *config.Config, filter []string) []Module {
	filterSet := make(map[string]bool, len(filter))
	for _, f := range filter {
		filterSet[f] = true
	}

	var result []Module
	for _, name := range defaultOrder {
		if !cfg.IsModuleEnabled(name) {
			continue
		}
		if len(filterSet) > 0 && !filterSet[name] {
			continue
		}
		if m, ok := r.modules[name]; ok {
			result = append(result, m)
		}
	}
	return result
}

// RunAll executes Check then Apply on each module in order and returns
// one outcome per module. Module errors are reported but do not stop the
// remaining modules; a cancelled context does.
func RunAll(ctx context.Context, modules []Module, rc *RunContext) ([]state.ModuleOutcome, error) {
	var errors []string
	outcomes := make([]state.ModuleOutcome, 0, len(modules))
	rc.startRun(modules)
	defer func() { rc.run = nil }()
	for _, m := range modules {
		out := state.ModuleOutcome{Name: m.Name()}
		if ctx.Err() != nil {
			out.Status = "skipped"
			outcomes = append(outcomes, out)
			continue
		}
		check, err := m.Check(ctx, rc)
		if err != nil {
			rc.run[m.Name()] = true
			fmt.Printf("  ⚠ %s: check error: %v\n", m.Name(), err)
			out.Status, out.Error = "failed", "check: "+err.Error()
			errors = append(errors, fmt.Sprintf("%s: check: %v", m.Name(), err))
			outcomes = append(outcomes, out)
			continue
		}
		if check.Satisfied {
			fmt.Printf("  ✓ %s: already satisfied\n", m.Name())
			out.Status = "satisfied"
			outcomes = append(outcomes, out)
			continue
		}

		// Show pending changes
		for _, c := range check.Changes {
			fmt.Printf("  → %s: %s\n", m.Name(), c.Description)
		}

		if rc.DryRun {
			out.Status = "pending"
			outcomes = append(outcomes, out)
			continue
		}

		result, err := m.Apply(ctx, rc)
		if err != nil {
			rc.run[m.Name()] = true
			fmt.Printf("  ✗ %s: %v\n", m.Name(), err)
			out.Status, out.Error = "failed", err.Error()
			errors = append(errors, fmt.Sprintf("%s: %v", m.Name(), err))
			outcomes = append(outcomes, out)
			continue
		}
		out.Status = "satisfied"
		if result.Changed {
			out.Status = "changed"
			for _, msg := range result.Messages {
				fmt.Printf("  ✓ %s: %s\n", m.Name(), msg)
			}
		}
		for _, w := range result.Warnings {
			fmt.Printf("  ⚠ %s: %s\n", m.Name(), w)
		}
		out.Messages, out.Warnings = result.Messages, result.Warnings
		outcomes = append(outcomes, out)
	}
	if err := ctx.Err(); err != nil {
		return outcomes, fmt.Errorf("interrupted: %w", err)
	}
	if len(errors) > 0 {
		return outcomes, fmt.Errorf("%d module(s) failed: %v", len(errors), errors)
	}
	return outcomes, nil
}

// ensureMetaDir ensures the .rootfiles metadata directory exists under homeBase.
// If a legacy flat file exists at that path (from older rootfiles versions),
// it is migrated into the directory as users.json.
func ensureMetaDir(runner *exec.Runner, homeBase string) error {
	metaDir := filepath.Join(homeBase, ".rootfiles")
	info, err := os.Stat(metaDir)
	if err == nil && !info.IsDir() {
		// Legacy flat file exists — migrate it into the directory.
		tmp := metaDir + ".migrating"
		if err := os.Rename(metaDir, tmp); err != nil {
			return fmt.Errorf("migrating legacy .rootfiles file: %w", err)
		}
		if err := os.MkdirAll(metaDir, 0755); err != nil {
			// Attempt to restore on failure.
			os.Rename(tmp, metaDir)
			return fmt.Errorf("creating .rootfiles dir after migration: %w", err)
		}
		dest := filepath.Join(metaDir, "users.json")
		if err := os.Rename(tmp, dest); err != nil {
			return fmt.Errorf("moving legacy file into .rootfiles dir: %w", err)
		}
		slog.Info("migrated legacy .rootfiles file to directory", "path", dest)
	}
	return runner.MkdirAll(metaDir, 0755)
}

// CheckAll runs Check on each module and returns results.
func CheckAll(ctx context.Context, modules []Module, rc *RunContext) (map[string]*CheckResult, error) {
	results := make(map[string]*CheckResult, len(modules))
	rc.startRun(modules)
	defer func() { rc.run = nil }()
	for _, m := range modules {
		check, err := m.Check(ctx, rc)
		if err != nil {
			return nil, fmt.Errorf("module %s check: %w", m.Name(), err)
		}
		results[m.Name()] = check
	}
	return results, nil
}
