package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/exec"
	"github.com/STAIxBWLB/rootfiles/internal/module"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

func newTunnelCmd() *cobra.Command {
	tunnel := &cobra.Command{
		Use:   "tunnel",
		Short: "Manage cloudflared tunnel and VLAN private network",
	}

	tunnel.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "Install cloudflared binary",
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := buildRunContext(cmd)
			if err != nil {
				return err
			}
			m := module.NewCloudflaredModule()
			_, err = m.Apply(cmd.Context(), rc)
			return err
		},
	})

	tunnel.AddCommand(&cobra.Command{
		Use:   "setup [TOKEN]",
		Short: "Setup tunnel + VLAN private network",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := buildRunContext(cmd)
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
			return module.TunnelSetup(cmd.Context(), rc, token, vlanAddr)
		},
	})

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
			rc, err := buildConfigFreeRunContext(cmd)
			if err != nil {
				return err
			}
			_, err = rc.Runner.Run(cmd.Context(), "systemctl", "restart", "cloudflared")
			if err != nil {
				return fmt.Errorf("restarting cloudflared: %w", err)
			}
			fmt.Println("cloudflared service restarted.")
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
			rc, err := buildConfigFreeRunContext(cmd)
			if err != nil {
				return err
			}
			ctx := cmd.Context()

			checkOnly, _ := cmd.Flags().GetBool("check")
			version, _ := cmd.Flags().GetString("version")

			if checkOnly {
				return tunnelUpdateCheck(ctx, rc)
			}
			return module.TunnelUpdate(ctx, rc, version)
		},
	}
	updateCmd.Flags().Bool("check", false, "Report current and latest cloudflared version without downloading")
	updateCmd.Flags().String("version", "", "Pin to a specific cloudflared release (e.g. 2024.9.1)")
	tunnel.AddCommand(updateCmd)

	tunnel.AddCommand(&cobra.Command{
		Use:   "uninstall",
		Short: "Remove tunnel service, VLAN, and binary",
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := buildRunContext(cmd)
			if err != nil {
				return err
			}
			return module.TunnelUninstall(cmd.Context(), rc)
		},
	})

	return tunnel
}

// tunnelUpdateCheck prints the installed and upstream cloudflared versions
// via the same ui helpers used by `rootfiles status`, without touching the
// binary. Returns a non-nil error only when the upstream lookup fails and
// we have no useful output to produce.
func tunnelUpdateCheck(ctx context.Context, rc *module.RunContext) error {
	latest, err := module.FetchLatestCloudflaredVersion(ctx)
	if err != nil {
		return fmt.Errorf("fetching latest cloudflared release: %w", err)
	}

	ui.WriteSection(os.Stdout, "Cloudflared update check")

	// currentCloudflaredVersion is package-private in module; derive from
	// `cloudflared --version` here using the same logic.
	current := ""
	if rc.Runner.FileExists(cloudflaredPath) {
		if res, err := rc.Runner.Query(ctx, cloudflaredPath, "--version"); err == nil {
			for _, tok := range strings.Fields(res.Stdout) {
				if len(tok) > 3 && tok[0] >= '0' && tok[0] <= '9' && strings.Contains(tok, ".") {
					current = tok
					break
				}
			}
		}
	}
	if current == "" {
		ui.WriteKV(os.Stdout, "Current", ui.StyleHint.Render("(not installed)"))
	} else {
		ui.WriteKV(os.Stdout, "Current", current)
	}
	ui.WriteKV(os.Stdout, "Latest", latest)

	switch {
	case current == "":
		ui.WriteHint(os.Stdout, "run `rootfiles tunnel install` to install.")
	case current == latest:
		fmt.Println("  " + ui.StyleSuccess.Render(ui.MarkOK+" up to date."))
	default:
		ui.WriteHint(os.Stdout, "run `rootfiles tunnel update` to upgrade.")
	}
	return nil
}

// cloudflaredPath mirrors internal/module/cloudflared.go's cloudflaredBinary
// constant. Duplicated here rather than exported to keep the module surface
// minimal; if it ever drifts, the `tunnel status` / `tunnel update --check`
// pair will report inconsistent paths, which scenario tests will catch.
const cloudflaredPath = "/usr/local/bin/cloudflared"

// buildRunContext loads the profile config and returns any load error. A
// zero config would read and write user and GPU state under /home, skipping
// home-base detection, and drop the profile's user defaults.
func buildRunContext(cmd *cobra.Command) (*module.RunContext, error) {
	return loadRunContext(cmd, false)
}

// buildConfigFreeRunContext is buildRunContext for commands that need no
// profile config, such as `tunnel status`: a load error is logged and
// config.Fallback is used.
func buildConfigFreeRunContext(cmd *cobra.Command) (*module.RunContext, error) {
	return loadRunContext(cmd, true)
}

func loadRunContext(cmd *cobra.Command, tolerateLoadErr bool) (*module.RunContext, error) {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	yes, _ := cmd.Flags().GetBool("yes")
	if os.Getenv("ROOTFILES_YES") == "true" {
		yes = true
	}

	runner := newRunner(cmd, dryRun)
	logger := runner.Logger

	sysInfo, sysErr := detectSystem()
	if sysErr != nil {
		logger.Warn("system detection failed, using defaults", "err", sysErr)
	}
	profileName, configPath := resolveRunTarget(cmd, logger)
	cfg, cfgErr := config.LoadWithHomeBase(profileName, configPath, sysInfo, homeBaseFlag(cmd))
	if cfgErr != nil {
		if !tolerateLoadErr {
			return nil, cfgErr
		}
		logger.Warn("loading config failed, continuing without it", "profile", profileName, "config", configPath, "err", cfgErr)
		cfg = config.Fallback(sysInfo, homeBaseFlag(cmd))
	}

	// Apply flag overrides
	applyFlagOverrides(cmd, cfg)

	force, _ := cmd.Flags().GetBool("force")

	return &module.RunContext{
		Config: cfg,
		Runner: runner,
		APT:    exec.NewAPT(runner),
		DryRun: dryRun,
		Yes:    yes,
		Force:  force,
	}, nil
}
