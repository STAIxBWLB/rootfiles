package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/exec"
	"github.com/STAIxBWLB/rootfiles/internal/module"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

// applyCloudflaredSetup is shared by setup and its install alias; keeping the
// module entry point injectable lets CLI tests validate inputs without a host.
var applyCloudflaredSetup = func(ctx context.Context, rc *module.RunContext) (*module.ApplyResult, error) {
	return module.NewCloudflaredModule().Apply(ctx, rc)
}

func newTunnelCmd() *cobra.Command {
	tunnel := &cobra.Command{
		Use:   "tunnel",
		Short: "Manage cloudflared tunnel and VLAN private network",
	}

	tunnel.AddCommand(usesRootFlags(&cobra.Command{
		Use:     "setup [TOKEN]",
		Aliases: []string{"install"},
		Short:   "Set up the tunnel, its VLAN private network and the cloudflared binary",
		Long: `Sets up the cloudflared binary, the tunnel token and the VLAN private network.
Configured credentials (including tunnel_token_file) and VLAN settings are used
unless an explicit token or VLAN address overrides them. Without credentials only
the binary and any configured VLAN are managed. "install" is an alias of setup.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := buildRunContext(cmd, "cloudflared")
			if err != nil {
				return err
			}
			token := ""
			if len(args) > 0 {
				token = args[0]
			}
			if token == "" {
				token, _ = cmd.Flags().GetString("tunnel-token")
			}
			if token == "" {
				token = os.Getenv("ROOTFILES_TUNNEL_TOKEN")
			}
			vlanAddr, _ := cmd.Flags().GetString("vlan-address")
			if vlanAddr == "" {
				vlanAddr = os.Getenv("ROOTFILES_VLAN_ADDRESS")
			}
			if token != "" {
				rc.Config.Modules.Cloudflared.TunnelToken = token
			}
			if vlanAddr != "" {
				rc.Config.Modules.Cloudflared.PrivateNetwork.Address = vlanAddr
				rc.Config.Modules.Cloudflared.PrivateNetwork.Enabled = true
			}
			result, err := applyCloudflaredSetup(cmd.Context(), rc)
			if err != nil {
				return err
			}
			for _, warning := range result.Warnings {
				fmt.Fprintln(cmd.ErrOrStderr(), ui.MarkWarn+" "+warning)
			}
			if result.Changed {
				fmt.Fprintln(cmd.OutOrStdout(), "Cloudflared setup complete.")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "Cloudflared setup is already current.")
			}
			return nil
		},
	}, "tunnel-token", "vlan-address"))

	tunnel.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show tunnel service and VLAN status",
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := buildConfigFreeRunContext(cmd)
			if err != nil {
				return err
			}
			return module.TunnelStatus(cmd.Context(), rc)
		},
	})

	tunnel.AddCommand(&cobra.Command{
		Use:   "restart",
		Short: "Restart cloudflared service",
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := buildConfigFreeRunContext(cmd, "cloudflared")
			if err != nil {
				return err
			}
			_, err = rc.Runner.Run(cmd.Context(), "systemctl", "restart", "cloudflared")
			if err != nil {
				return fmt.Errorf("restarting cloudflared: %w", err)
			}
			ui.WriteLine(cmd.OutOrStdout(), "cloudflared service restarted.")
			return nil
		},
	})

	updateCmd := &cobra.Command{
		Use:   "update",
		Short: "Upgrade cloudflared binary to latest (or a pinned version)",
		Long: `Download the cloudflared binary from the upstream cloudflare/cloudflared GitHub
releases and replace /usr/local/bin/cloudflared. Uses the tagged release URL when
--version is provided, or the /latest/download/ alias otherwise. The cloudflared
systemd service is restarted when present; pure-binary refreshes on hosts that
don't run the tunnel just update the binary.

Use --check to see the current installed version vs. the latest upstream tag
without downloading or restarting anything.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			checkOnly, _ := cmd.Flags().GetBool("check")
			var modules []string
			if !checkOnly {
				modules = []string{"cloudflared"}
			}
			rc, err := buildConfigFreeRunContext(cmd, modules...)
			if err != nil {
				return err
			}
			ctx := cmd.Context()

			version, _ := cmd.Flags().GetString("version")

			if checkOnly {
				return tunnelUpdateCheck(ctx, rc)
			}
			return module.TunnelUpdate(ctx, rc, version)
		},
	}
	updateCmd.Flags().Bool("check", false, "Report current and latest cloudflared version without downloading (exit 2 when available)")
	_ = updateCmd.Flags().SetAnnotation("check", annotReadOnly, []string{"true"})
	updateCmd.Flags().String("version", "", "Pin to a specific cloudflared release (e.g. 2024.9.1)")
	tunnel.AddCommand(updateCmd)

	tunnel.AddCommand(&cobra.Command{
		Use:   "uninstall",
		Short: "Remove tunnel service, VLAN, and binary",
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := buildRunContext(cmd, "cloudflared")
			if err != nil {
				return err
			}
			return module.TunnelUninstall(cmd.Context(), rc)
		},
	})

	return usesConfigTarget(tunnel)
}

// tunnelUpdateCheck prints the installed and upstream cloudflared versions
// via the same ui helpers used by `rootfiles status`, without touching the
// binary. Returns exitDrift when installation or an update is available.
func tunnelUpdateCheck(ctx context.Context, rc *module.RunContext) error {
	latest, err := module.FetchLatestCloudflaredVersion(ctx)
	if err != nil {
		return fmt.Errorf("fetching latest cloudflared release: %w", err)
	}

	out := rc.Output()
	ui.WriteSection(out, "Cloudflared update check")

	current := module.InspectCloudflared(ctx, rc.Runner, cloudflaredPath).Version
	if current == "" {
		ui.WriteKV(out, "Current", ui.StyleHint.Render("(not installed)"))
	} else {
		ui.WriteKV(out, "Current", current)
	}
	ui.WriteKV(out, "Latest", latest)

	switch {
	case current == "":
		ui.WriteHint(out, "run `sudo rootfiles tunnel setup` to install.")
		return &ExitError{Code: exitDrift}
	case normalizeVersion(current) == normalizeVersion(latest):
		ui.WriteLine(out, "  "+ui.StyleSuccess.Render(ui.MarkOK+" up to date."))
	default:
		ui.WriteHint(out, "run `sudo rootfiles tunnel update` to upgrade.")
		return &ExitError{Code: exitDrift}
	}
	return nil
}

// cloudflaredPath is an optional read-only inspection override for tests.
// Empty uses the module's installed binary path.
var cloudflaredPath string

// buildRunContext loads the profile config and returns any load error. A
// zero config would read and write user and GPU state under /home, skipping
// home-base detection, and drop the profile's user defaults.
func buildRunContext(cmd *cobra.Command, modules ...string) (*module.RunContext, error) {
	return loadRunContext(cmd, false, modules)
}

// buildConfigFreeRunContext is buildRunContext for commands that need no
// profile config, such as `tunnel status`: a load error is logged and
// config.Fallback is used.
func buildConfigFreeRunContext(cmd *cobra.Command, modules ...string) (*module.RunContext, error) {
	return loadRunContext(cmd, true, modules)
}

func loadRunContext(cmd *cobra.Command, tolerateLoadErr bool, modules []string) (*module.RunContext, error) {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	yes := yesFlag(cmd)

	logger := newLogger(cmd, true, slog.LevelInfo)

	sysInfo, sysErr := detectSystem()
	if sysErr != nil {
		logger.Warn("system detection failed, using defaults", "err", sysErr)
	}
	profileName, configPath := resolveRunTarget(cmd, logger)
	cfg, cfgErr := config.LoadWithOverrides(profileName, configPath, sysInfo, loadOverrides(cmd))
	if cfgErr != nil {
		if !tolerateLoadErr {
			return nil, cfgErr
		}
		logger.Warn("loading config failed, continuing without it", "profile", profileName, "config", configPath, "err", cfgErr)
		cfg = config.Fallback(sysInfo, homeBaseFlag(cmd))
	}

	// Apply flag overrides
	applyFlagOverrides(cmd, cfg)

	validating := cmd.Annotations[annotMutates] == "true" && len(modules) > 0
	if validating {
		if err := config.ValidateCapabilities(cfg, sysInfo, modules); err != nil {
			return nil, err
		}
	}
	runner := newRunner(cmd, dryRun)
	pm, err := exec.NewPackageManager(runner, sysInfo)
	if err != nil {
		if validating {
			return nil, err
		}
		pm = statusPackageManager(runner, sysInfo)
	}

	return &module.RunContext{
		Config: cfg,
		Out:    cmd.OutOrStdout(),
		Runner: runner,
		APT:    pm,
		DryRun: dryRun,
		Yes:    yes,
	}, nil
}
