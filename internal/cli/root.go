package cli

import (
	"context"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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

	root.PersistentFlags().AddFlagSet(newRootFlags())

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

// newRootFlags shares flag arity with argument redaction.
func newRootFlags() *pflag.FlagSet {
	flags := pflag.NewFlagSet("rootfiles", pflag.ContinueOnError)
	// Persistent flags for all subcommands
	flags.Bool("yes", false, "Unattended mode (skip all prompts)")
	flags.Bool("dry-run", false, "Show what would be done without executing")
	_ = flags.SetAnnotation("dry-run", annotReadOnly, []string{"true"})
	flags.Bool("force", false, "Override safety guards (e.g. SSH lockout protection)")
	flags.String("profile", "", "Profile name (base, minimal, dgx, gpu-server, full, rocky)")
	flags.StringSlice("module", nil, "Run specific modules only")
	flags.String("config", "", "Path to custom config YAML")

	// Flags for unattended mode
	flags.String("home-base", "", "Custom home base directory (e.g., /raid/home)")
	flags.String("user", "", "Username to create")
	flags.String("ssh-pubkey", "", "SSH public key for the user")
	flags.String("tunnel-token", "", "Cloudflare tunnel token")
	flags.String("vlan-address", "", "VLAN private network address (e.g., 172.16.229.32/32)")

	return flags
}
