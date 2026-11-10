package cli

import (
	"log/slog"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/exec"
	"github.com/STAIxBWLB/rootfiles/internal/module"
)

// validateModules checks only the modules selected by the caller. Commands
// retain their own error/report policy and config loading/flag reader rules.
func validateModules(cfg *config.Config, sys *config.SystemInfo, modules []module.Module) error {
	names := make([]string, 0, len(modules))
	for _, selected := range modules {
		names = append(names, selected.Name())
	}
	return config.ValidateCapabilities(cfg, sys, names)
}

// readOnlyRunContext shares report plumbing without resolving a target,
// mutating config, opening audit/backups or changing the caller's logger.
func readOnlyRunContext(cfg *config.Config, sys *config.SystemInfo, logger *slog.Logger) *module.RunContext {
	runner := exec.NewRunner(true, logger)
	return &module.RunContext{Config: cfg, Runner: runner, APT: statusPackageManager(runner, sys), DryRun: true, Yes: true}
}

// maskSecret keeps the established config-show policy: short secrets are
// hidden completely, and longer ones disclose at most four leading bytes.
func maskSecret(value string) string {
	if value == "" {
		return ""
	}
	if len(value) <= 8 {
		return "****"
	}
	return value[:4] + "…" + "(masked)"
}
