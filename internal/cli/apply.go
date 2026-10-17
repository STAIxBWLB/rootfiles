package cli

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/exec"
	"github.com/STAIxBWLB/rootfiles/internal/module"
	"github.com/STAIxBWLB/rootfiles/internal/state"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

var errAborted = errors.New("aborted by user")

func newApplyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "apply",
		Short: "Apply server configuration",
		Long:  "Apply the selected profile's configuration to the system.",
		RunE:  runApply,
	}
}

func runApply(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	yes, _ := cmd.Flags().GetBool("yes")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	profileName, _ := cmd.Flags().GetString("profile")
	moduleFilter, _ := cmd.Flags().GetStringSlice("module")
	configPath, _ := cmd.Flags().GetString("config")
	force, _ := cmd.Flags().GetBool("force")

	// Check ROOTFILES_YES env
	if os.Getenv("ROOTFILES_YES") == "true" {
		yes = true
	}
	// Check ROOTFILES_PROFILE env
	if profileName == "" {
		profileName = os.Getenv("ROOTFILES_PROFILE")
	}

	// Detect system
	sysInfo, err := detectSystem()
	if err != nil {
		return fmt.Errorf("detecting system: %w", err)
	}

	profileName, err = selectProfile(profileName, configPath, sysInfo, yes)
	if err != nil {
		return err
	}

	// Load config
	cfg, err := config.LoadWithHomeBase(profileName, configPath, sysInfo, homeBaseFlag(cmd))
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// Apply CLI flag overrides
	applyFlagOverrides(cmd, cfg)
	applyAccountFlags(cmd, cfg)
	if err := cfg.Validate(); err != nil {
		return err
	}

	// Setup runner
	runner := newRunner(cmd, dryRun)
	apt, err := exec.NewPackageManager(runner, sysInfo)
	if err != nil {
		return err
	}

	// Build module list
	registry := module.NewRegistry()
	modules := registry.Resolve(cfg, moduleFilter)

	if len(modules) == 0 {
		fmt.Println("No modules to apply.")
		return nil
	}

	// Show plan
	fmt.Printf("Profile: %s\n", profileName)
	if dryRun {
		fmt.Println("Mode: dry-run (no changes will be made)")
	}
	fmt.Printf("Modules: ")
	for i, m := range modules {
		if i > 0 {
			fmt.Print(", ")
		}
		fmt.Print(m.Name())
	}
	fmt.Println()

	// Interactive configuration preview & edit
	if err := configureInteractive(cfg, yes, dryRun); err != nil {
		if errors.Is(err, errAborted) {
			fmt.Println("Aborted.")
			return nil
		}
		return err
	}

	// Revalidate interactive edits and reject unsupported capabilities before mutation.
	if err := cfg.Validate(); err != nil {
		return err
	}
	names := make([]string, 0, len(modules))
	for _, m := range modules {
		names = append(names, m.Name())
	}
	if err := config.ValidateCapabilities(cfg, sysInfo, names); err != nil {
		return err
	}
	fingerprint, err := cfg.Fingerprint()
	if err != nil {
		return err
	}

	// Execute modules
	rc := &module.RunContext{
		Config: cfg,
		Runner: runner,
		APT:    apt,
		DryRun: dryRun,
		Yes:    yes,
		Force:  force,
	}

	if err := module.Preflight(ctx, modules, rc); err != nil {
		return err
	}
	fmt.Println()
	started := time.Now().UTC()
	outcomes, runErr := module.RunAll(ctx, modules, rc)
	if dryRun {
		return runErr
	}

	run := state.Run{
		ConfigSHA256: fingerprint,
		Version:      buildVersion,
		Profile:      profileName,
		ConfigPath:   recordedConfigPath(configPath),
		StartedAt:    started,
		FinishedAt:   time.Now().UTC(),
		Success:      runErr == nil,
		Modules:      outcomes,
	}
	if runner.Backup.Used() {
		run.BackupID = runner.Backup.ID
		fmt.Printf("\nFiles changed by this run were backed up (undo with: rootfiles rollback %s)\n", run.BackupID)
	}
	if err := state.Record(run); err != nil {
		fmt.Fprintf(os.Stderr, "warning: recording apply state: %v\n", err)
	}
	// Keep the copy in step with the recorded run, failed or not, as check
	// follows the recorded run; an outdated copy must not outlive a failed save.
	if err := saveAppliedConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "warning: saving the applied config: %v\n", err)
		_ = state.SaveAppliedConfig(nil)
	}
	auditf(cmd, "apply finished", "profile", profileName, "success", runErr == nil, "backup", run.BackupID)
	return runErr
}

// saveAppliedConfig keeps the resolved config of a recorded apply, from a
// profile or a config file, for the subcommands to reuse (resolveRunTarget).
// Root writes it into the root-owned state directory, fully resolved (no
// extends) and without an inline tunnel token.
func saveAppliedConfig(cfg *config.Config) error {
	data, err := cfg.Effective()
	if err != nil {
		return err
	}
	return state.SaveAppliedConfig(data)
}

// selectProfile resolves the profile name. Priority: explicit --profile flag
// or --config path (no prompt), then system suggestion. In non-interactive
// (--yes) mode the suggestion is used silently; otherwise the user picks from
// the list of available profiles with the suggestion pre-selected.
func selectProfile(profileName, configPath string, sysInfo *config.SystemInfo, yes bool) (string, error) {
	if profileName != "" || configPath != "" {
		return profileName, nil
	}
	suggested := sysInfo.SuggestProfile()
	if yes {
		return suggested, nil
	}
	return ui.Select("Select profile", config.AvailableProfiles(), suggested, false)
}

// homeBaseFlag returns --home-base, which config loading applies before
// home-base detection.
func homeBaseFlag(cmd *cobra.Command) string {
	v, _ := cmd.Flags().GetString("home-base")
	return v
}

// applyFlagOverrides applies the remaining CLI flags; --home-base is applied
// by config.LoadWithHomeBase before home-base detection.
func applyFlagOverrides(cmd *cobra.Command, cfg *config.Config) {
	if v, _ := cmd.Flags().GetString("tunnel-token"); v != "" {
		cfg.Modules.Cloudflared.TunnelToken = v
	}
	if v, _ := cmd.Flags().GetString("vlan-address"); v != "" {
		cfg.Modules.Cloudflared.PrivateNetwork.Address = v
	}
}

// applyAccountFlags turns --user/--ssh-pubkey (or ROOTFILES_USER /
// ROOTFILES_SSH_PUBKEY) into a declared account, which the users module
// creates before the ssh module hardens authentication.
func applyAccountFlags(cmd *cobra.Command, cfg *config.Config) {
	name, _ := cmd.Flags().GetString("user")
	if name == "" {
		name = os.Getenv("ROOTFILES_USER")
	}
	if name == "" {
		return
	}
	key, _ := cmd.Flags().GetString("ssh-pubkey")
	if key == "" {
		key = os.Getenv("ROOTFILES_SSH_PUBKEY")
	}
	acct := config.AccountConfig{Name: name}
	if key != "" {
		acct.SSHPubkeys = []string{key}
	}
	cfg.Users.AddAccount(acct)
}

// configureInteractive shows a preview of all key settings and lets the user
// review/modify them before applying. Skipped when --yes is set.
func configureInteractive(cfg *config.Config, yes, dryRun bool) error {
	if yes {
		return nil
	}

	fmt.Println("\n=== Configuration ===")

	// Snapshot the profile values so the summary can flag any setting the
	// operator made less secure than the profile intends.
	profile := *cfg

	var err error

	// --- General ---
	cfg.Timezone, err = ui.Input("Timezone", cfg.Timezone, false)
	if err != nil {
		return err
	}

	// --- SSH ---
	if cfg.IsModuleEnabled("ssh") {
		fmt.Println("\n--- SSH ---")
		cfg.SSH.DisableRootLogin, err = ui.ConfirmDefault("Disable root login?", cfg.SSH.DisableRootLogin, false)
		if err != nil {
			return err
		}
		cfg.SSH.DisablePasswordAuth, err = ui.ConfirmDefault("Disable password authentication?", cfg.SSH.DisablePasswordAuth, false)
		if err != nil {
			return err
		}
		port := cfg.SSH.Port
		if port == 0 {
			port = 22
		}
		cfg.SSH.Port, err = ui.InputInt("SSH port", port, false)
		if err != nil {
			return err
		}
		if cfg.SSH.Port == 22 && profile.SSH.Port == 0 {
			cfg.SSH.Port = 0 // keep sshd's default instead of pinning it
		}
	}

	// --- Users ---
	if cfg.IsModuleEnabled("users") {
		fmt.Println("\n--- Users ---")
		if cfg.Users.HomeBase != "" {
			cfg.Users.HomeBase, err = ui.Input("Home base directory", cfg.Users.HomeBase, false)
			if err != nil {
				return err
			}
		}
		cfg.Users.SudoNopasswd, err = ui.ConfirmDefault("Sudo without password?", cfg.Users.SudoNopasswd, false)
		if err != nil {
			return err
		}
	}

	// --- Docker ---
	if cfg.IsModuleEnabled("docker") {
		fmt.Println("\n--- Docker ---")
		if cfg.Modules.Docker.StorageDir != "" {
			cfg.Modules.Docker.StorageDir, err = ui.Input("Docker storage directory", cfg.Modules.Docker.StorageDir, false)
			if err != nil {
				return err
			}
		}
	}

	// --- Cloudflared ---
	if cfg.IsModuleEnabled("cloudflared") {
		fmt.Println("\n--- Cloudflared ---")
		if f := cfg.Modules.Cloudflared.TunnelTokenFile; f != "" && cfg.Modules.Cloudflared.TunnelToken == "" {
			fmt.Printf("  Tunnel token: read from %s\n", f)
		} else {
			cfg.Modules.Cloudflared.TunnelToken, err = ui.Input("Tunnel token (empty to skip)", cfg.Modules.Cloudflared.TunnelToken, false)
			if err != nil {
				return err
			}
		}
		cfg.Modules.Cloudflared.PrivateNetwork.Enabled, err = ui.ConfirmDefault("Enable VLAN private network?", cfg.Modules.Cloudflared.PrivateNetwork.Enabled, false)
		if err != nil {
			return err
		}
		if cfg.Modules.Cloudflared.PrivateNetwork.Enabled {
			cfg.Modules.Cloudflared.PrivateNetwork.Address, err = ui.Input("VLAN address (e.g. 172.16.229.32/32)", cfg.Modules.Cloudflared.PrivateNetwork.Address, false)
			if err != nil {
				return err
			}
		}
	}

	// --- Network ---
	if cfg.IsModuleEnabled("network") {
		fmt.Println("\n--- Network ---")
		cfg.Modules.Network.UFW, err = ui.ConfirmDefault("Enable UFW firewall?", cfg.Modules.Network.UFW, false)
		if err != nil {
			return err
		}
		if cfg.Modules.Network.UFW {
			cfg.Modules.Network.AllowedPorts, err = ui.InputIntSlice("Allowed ports (comma-separated)", cfg.Modules.Network.AllowedPorts, false)
			if err != nil {
				return err
			}
		}
	}

	// --- Storage ---
	if cfg.IsModuleEnabled("storage") {
		fmt.Println("\n--- Storage ---")
		if cfg.Modules.Storage.DataDir != "" {
			cfg.Modules.Storage.DataDir, err = ui.Input("Data directory", cfg.Modules.Storage.DataDir, false)
			if err != nil {
				return err
			}
		}
	}

	// --- Summary & confirm ---
	fmt.Println("\n=== Summary ===")
	fmt.Printf("  Timezone: %s\n", cfg.Timezone)
	if cfg.IsModuleEnabled("ssh") {
		fmt.Printf("  SSH: root_login=%v, password_auth=%v, port=%d\n",
			!cfg.SSH.DisableRootLogin, !cfg.SSH.DisablePasswordAuth, cfg.SSH.Port)
	}
	if cfg.IsModuleEnabled("users") && cfg.Users.HomeBase != "" {
		fmt.Printf("  Users: home_base=%s, sudo_nopasswd=%v\n", cfg.Users.HomeBase, cfg.Users.SudoNopasswd)
	}
	if cfg.IsModuleEnabled("docker") && cfg.Modules.Docker.StorageDir != "" {
		fmt.Printf("  Docker: storage=%s\n", cfg.Modules.Docker.StorageDir)
	}
	if cfg.IsModuleEnabled("cloudflared") {
		token := cfg.Modules.Cloudflared.TunnelToken
		if token != "" && len(token) > 8 {
			token = token[:8] + "..."
		}
		if token == "" && cfg.Modules.Cloudflared.TunnelTokenFile != "" {
			token = "file:" + cfg.Modules.Cloudflared.TunnelTokenFile
		}
		fmt.Printf("  Cloudflared: token=%s, vlan=%v", token, cfg.Modules.Cloudflared.PrivateNetwork.Enabled)
		if cfg.Modules.Cloudflared.PrivateNetwork.Enabled {
			fmt.Printf(" (%s)", cfg.Modules.Cloudflared.PrivateNetwork.Address)
		}
		fmt.Println()
	}
	if cfg.IsModuleEnabled("network") {
		fmt.Printf("  Network: ufw=%v, ports=%v\n", cfg.Modules.Network.UFW, cfg.Modules.Network.AllowedPorts)
	}
	if cfg.IsModuleEnabled("storage") && cfg.Modules.Storage.DataDir != "" {
		fmt.Printf("  Storage: data_dir=%s\n", cfg.Modules.Storage.DataDir)
	}

	downgrades := securityDowngrades(&profile, cfg)
	for _, d := range downgrades {
		fmt.Println("  " + ui.StyleWarning.Render(ui.MarkWarn+" weaker than profile: "+d))
	}

	if dryRun {
		return nil
	}

	confirmed, err := ui.ConfirmDefault("\nApply this configuration?", len(downgrades) == 0, false)
	if err != nil {
		return err
	}
	if !confirmed {
		return errAborted
	}
	return nil
}

// securityDowngrades lists settings where after is less restrictive than
// the profile values in before.
func securityDowngrades(before, after *config.Config) []string {
	var out []string
	if before.SSH.DisableRootLogin && !after.SSH.DisableRootLogin {
		out = append(out, "SSH root login re-enabled")
	}
	if before.SSH.DisablePasswordAuth && !after.SSH.DisablePasswordAuth {
		out = append(out, "SSH password authentication re-enabled")
	}
	if before.Modules.Network.UFW && !after.Modules.Network.UFW {
		out = append(out, "UFW firewall disabled")
	}
	if !before.Users.SudoNopasswd && after.Users.SudoNopasswd {
		out = append(out, "passwordless sudo enabled")
	}
	return out
}
