package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

// buildVersion is recorded in state and audit entries.
var buildVersion = "dev"

func NewRootCmd(version, commit string) *cobra.Command {
	buildVersion = version
	root := &cobra.Command{
		Use:   "rootfiles",
		Short: "Bootstrap Ubuntu, DGX OS and Rocky servers",
		Long: "Declarative server configuration management with modular profiles, on one\n" +
			"host or a fleet.\n\n" +
			"Reading only, start with 'rootfiles status' on a host, or run 'rootfiles fleet\n" +
			"hosts' for the hosts an inventory names. To change a host, 'rootfiles check'\n" +
			"shows what would differ and 'rootfiles apply' converges it.\n\n" +
			"Profiles: " + strings.Join(config.AvailableProfiles(), ", ") + ".",
		PersistentPreRunE: preflight,
		SilenceUsage:      true,
		SilenceErrors:     true, // main prints the error once
	}
	root.Version = version + " (" + commit + ")"
	root.SetUsageTemplate(usageTemplate)

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
	root.AddCommand(newShutdownCmd())
	root.AddCommand(newFleetCmd())
	root.AddCommand(newTenantCmd())

	markMutating(root)
	installOutputBoundary(root)
	return root
}

// Execute runs the CLI; ctx is cancelled on SIGINT/SIGTERM so in-flight
// commands (apt, downloads) are stopped instead of orphaned.
func Execute(ctx context.Context, version, commit string) error {
	defer heldLock.Release()
	return executeCommand(ctx, NewRootCmd(version, commit))
}

// newRootFlags shares flag arity with argument redaction.
func newRootFlags() *pflag.FlagSet {
	flags := pflag.NewFlagSet("rootfiles", pflag.ContinueOnError)
	// Persistent flags for all subcommands
	flags.Bool("yes", false, "Unattended mode (skip all prompts)")
	flags.Bool("dry-run", false, "Show what would be done without executing")
	_ = flags.SetAnnotation("dry-run", annotReadOnly, []string{"true"})
	flags.String("profile", "", "Profile to apply (e.g. dgx)")
	flags.String("config", "", "Path to custom config YAML")

	flags.String("color", "auto", "Color mode: auto, always, never")
	flags.Bool("ascii", false, "Use ASCII markers")
	flags.Bool("verbose", false, "Show diagnostic logs and command details")

	// Flags for unattended mode
	flags.String("home-base", "", "Custom home base directory (e.g., /raid/home)")
	flags.String("user", "", "Username to create")
	flags.String("ssh-pubkey", "", "SSH public key for the user")
	flags.String("tunnel-token", "", "Cloudflare tunnel token")
	flags.String("vlan-address", "", "VLAN address (e.g. 203.0.113.10/32)")

	return flags
}

// usageError marks validation failures without classifying operational errors by text.
func usageError(err error) error {
	return &ExitError{Code: 2, Msg: err.Error(), Usage: true}
}

func configureOutput(cmd *cobra.Command, args ...string) error {
	mode, _ := cmd.Flags().GetString("color")
	if mode == "" {
		mode, _ = cmd.Root().PersistentFlags().GetString("color")
	}
	switch mode {
	case "auto", "always", "never":
	default:
		return usageError(fmt.Errorf("invalid --color %q: choose auto, always or never", mode))
	}
	ascii, _ := cmd.Flags().GetBool("ascii")
	machine, _ := cmd.Flags().GetBool("json")
	machine = machine || rawPassthroughOutput(cmd, args)
	if f := cmd.Flags().Lookup("output"); cmd.Flags().Lookup("json") != nil && f != nil && (f.Value.String() == "json" || f.Value.String() == "prometheus") {
		machine = true
	}
	cmd.SetOut(ui.ConfigureWriter(cmd.OutOrStdout(), mode, ascii, machine))
	cmd.SetErr(ui.ConfigureWriter(cmd.ErrOrStderr(), mode, ascii, false))
	return nil
}

// Raw payloads use the same undecorated stdout path as JSON. Diagnostics keep
// their independent human stderr policy.
func rawPassthroughOutput(cmd *cobra.Command, args []string) bool {
	switch strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" ") {
	case "tenant logs", "config show":
		return true
	case "tenant ops":
		return len(args) > 1 && args[1] == "logs"
	case "fleet targets":
		push, _ := cmd.Flags().GetString("push")
		return push == ""
	default:
		return false
	}
}

// Args runs after parsing but before any preflight or command body. Wrapping
// each validator also covers child commands with their own persistent hooks.
func installOutputBoundary(cmd *cobra.Command) {
	if !cmd.Runnable() {
		cmd.RunE = func(c *cobra.Command, _ []string) error { return c.Help() }
	}
	args := cmd.Args
	cmd.Args = func(c *cobra.Command, values []string) error {
		if err := configureOutput(c, values...); err != nil {
			return err
		}
		if args == nil && c.HasSubCommands() && len(values) > 0 {
			return unknownCommandError(c, values[0])
		}
		if args != nil {
			if err := args(c, values); err != nil {
				return usageError(err)
			}
		}
		if c.Flags().Lookup("json") != nil {
			_, err := outputFormat(c)
			return err
		}
		return nil
	}
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error { return usageError(err) })
	for _, child := range cmd.Commands() {
		installOutputBoundary(child)
	}
	if cmd.Parent() == nil {
		// Every help path (--help, `help <command>`, a group's own page) runs
		// through this one function, so the inherited inputs are scoped and the
		// bool value hint is dropped here rather than in each command's help.
		help := cmd.HelpFunc()
		cmd.SetHelpFunc(func(c *cobra.Command, args []string) {
			_ = configureOutput(c)
			restore := scopeInheritedHelpFlags(c)
			defer restore()
			var page bytes.Buffer
			out, errOut := c.OutOrStdout(), c.ErrOrStderr()
			c.SetOut(&page)
			c.SetErr(&page)
			help(c, args)
			c.SetOut(out)
			c.SetErr(errOut)
			_, _ = io.WriteString(out, withoutBoolValueHint(page.String()))
		})
	}
}

func unknownCommandError(cmd *cobra.Command, name string) error {
	message := fmt.Sprintf("unknown command %q for %q", name, cmd.CommandPath())
	if !cmd.DisableSuggestions {
		if cmd.SuggestionsMinimumDistance <= 0 {
			cmd.SuggestionsMinimumDistance = 2
		}
		if suggestions := cmd.SuggestionsFor(name); len(suggestions) > 0 {
			message += "\n\nDid you mean this?\n\t" + strings.Join(suggestions, "\n\t") + "\n"
		}
	}
	return usageError(errors.New(message))
}

// Generated completion commands need the same argument/output hooks. Create
// them after callers set the writer, which Cobra captures in its closures.
func prepareCompletionBoundary(root *cobra.Command) {
	for _, command := range root.Commands() {
		if command.Name() == "completion" {
			return
		}
	}
	root.InitDefaultCompletionCmd()
	for _, command := range root.Commands() {
		if command.Name() == "completion" {
			installOutputBoundary(command)
			return
		}
	}
}

// executeCommand owns diagnostics so the writer and parsed display flags are
// the same for successful reports, usage failures and operational failures.
func executeCommand(ctx context.Context, root *cobra.Command) error {
	prepareCompletionBoundary(root)
	cmd, err := root.ExecuteContextC(ctx)
	if err == nil {
		return nil
	}
	if cmd == nil {
		cmd = root
	}
	var exit *ExitError
	_ = configureOutput(cmd)
	if errors.As(err, &exit) {
		if exit.Msg != "" {
			ui.WriteError(cmd.ErrOrStderr(), exit.Msg, exit.Next)
		}
		if exit.Usage {
			fmt.Fprintln(cmd.ErrOrStderr(), "Usage: "+cmd.UseLine())
			name := strings.TrimPrefix(cmd.CommandPath(), root.Name())
			fmt.Fprintf(cmd.ErrOrStderr(), "Run '%s help%s' for details.\n", root.Name(), name)
		}
	} else {
		ui.WriteError(cmd.ErrOrStderr(), err.Error(), "")
	}
	return err
}
