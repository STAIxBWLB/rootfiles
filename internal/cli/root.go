package cli

import (
	"context"

	"github.com/spf13/cobra"
)

// buildVersion is recorded in state and audit entries.
var buildVersion = "dev"

func NewRootCmd(version, commit string) *cobra.Command {
	buildVersion = version
	root := &cobra.Command{
		Use:               "rootfiles",
		Short:             "Server bootstrapping tool for Ubuntu and DGX OS",
		Long:              "rootfiles: Declarative server configuration management with modular profiles.",
		PersistentPreRunE: preflight,
		SilenceUsage:      true,
		SilenceErrors:     true, // main prints the error once
	}
	root.Version = version + " (" + commit + ")"

	// Persistent flags for all subcommands
	root.PersistentFlags().Bool("yes", false, "Unattended mode (skip all prompts)")
	root.PersistentFlags().Bool("dry-run", false, "Show what would be done without executing")
	root.PersistentFlags().Bool("force", false, "Override safety guards (e.g. SSH lockout protection)")
	root.PersistentFlags().String("profile", "", "Profile name (base, minimal, dgx, gpu-server, full, rocky)")
	root.PersistentFlags().StringSlice("module", nil, "Run specific modules only")
	root.PersistentFlags().String("config", "", "Path to custom config YAML")

	// Flags for unattended mode
	root.PersistentFlags().String("home-base", "", "Custom home base directory (e.g., /raid/home)")
	root.PersistentFlags().String("user", "", "Username to create")
	root.PersistentFlags().String("ssh-pubkey", "", "SSH public key for the user")
	root.PersistentFlags().String("tunnel-token", "", "Cloudflare tunnel token")
	root.PersistentFlags().String("vlan-address", "", "VLAN private network address (e.g., 172.16.229.32/32)")

	root.AddCommand(newApplyCmd())
	root.AddCommand(newBackupCmd())
	root.AddCommand(newCheckCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newTunnelCmd())
	root.AddCommand(newUpgradeCmd(version))
	root.AddCommand(newUserCmd())
	root.AddCommand(newGPUCmd())
	root.AddCommand(newRollbackCmd())
	root.AddCommand(newConfigCmd())
	root.AddCommand(newDoctorCmd())
	root.AddCommand(newScheduleCmd())
	root.AddCommand(newFleetCmd())
	root.AddCommand(newTenantCmd())

	markMutating(root)
	return root
}

// Execute runs the CLI; ctx is cancelled on SIGINT/SIGTERM so in-flight
// commands (apt, downloads) are stopped instead of orphaned.
func Execute(ctx context.Context, version, commit string) error {
	defer heldLock.Release()
	return NewRootCmd(version, commit).ExecuteContext(ctx)
}
