package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/module"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

// Action seams keep CLI argument/alias tests independent of host accounts.
var addCLIUser = module.AddUser
var backupCLIUsers = module.BackupUsers
var setCLIUserPasswords = module.SetPasswords

func newUserCmd() *cobra.Command {
	userCmd := &cobra.Command{
		Use:     "user",
		Short:   "Manage system users (custom home, backup, restore)",
		Example: "  rootfiles user list\n  sudo rootfiles user add alice --ssh-pubkey \"$(cat ~/.ssh/id_ed25519.pub)\"",
	}

	userCmd.AddCommand(
		newUserAddCmd(),
		newUserListCmd(),
		newUserBackupCmd(),
		newUserRestoreCmd(),
		newUserRehomeCmd(),
		newUserIDCmd(),
		newUserGroupsCmd(),
		newUserGroupAddCmd(),
		newUserGroupDelCmd(),
		newUserPasswdCmd(),
		newUserDelCmd(),
		newUserLockCmd(),
		newUserUnlockCmd(),
		newUserExpireCmd(),
		newUserKeyCmd(),
		newUserDuCmd(),
		newUserAuditCmd(),
		newUserQuotaCmd(),
	)

	return usesConfigTarget(userCmd)
}

func newUserAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add [USERNAME]",
		Short: "Create a user with custom home directory",
		Long: "The account name comes from the argument, then --user, then $ROOTFILES_USER,\n" +
			"and it is asked for when all three are absent.",
		Args: cobra.MaximumNArgs(1),
		Example: `  sudo rootfiles user add alice --ssh-pubkey "$(cat ~/.ssh/id_ed25519.pub)"
  sudo rootfiles user add bob --groups docker,sudo`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := buildRunContext(cmd, "users")
			if err != nil {
				return err
			}
			username := ""
			if len(args) > 0 {
				username = args[0]
			}
			if username == "" {
				username, _ = cmd.Flags().GetString("user")
			}
			if username == "" {
				username = os.Getenv("ROOTFILES_USER")
			}
			if username == "" {
				var err error
				username, err = ui.Input("Username", "", rc.Yes)
				if err != nil || username == "" {
					msg := "username is required"
					if errors.Is(err, ui.ErrNoTerminal) {
						msg += " and there is no terminal to prompt for it"
					}
					return withNext(errors.New(msg), "pass the username as an argument, with --user, or in ROOTFILES_USER")
				}
			}

			pubkey, _ := cmd.Flags().GetString("ssh-pubkey")
			if pubkey == "" {
				pubkey = os.Getenv("ROOTFILES_SSH_PUBKEY")
			}

			groups, _ := cmd.Flags().GetStringSlice("groups")
			noDocker, _ := cmd.Flags().GetBool("no-docker")

			var pubkeys []string
			if pubkey != "" {
				pubkeys = []string{pubkey}
			}
			return addCLIUser(cmd.Context(), rc, username, pubkeys, groups, noDocker)
		},
	}
	usesRootFlags(cmd, "user", "ssh-pubkey")
	cmd.Flags().StringSlice("groups", nil, "Additional groups")
	cmd.Flags().Bool("no-docker", false, "Do not add to docker group")
	return cmd
}

func newUserListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List managed users (or system users with --system)",
		Example: "  rootfiles user list\n  rootfiles user list --names --system",
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := buildRunContext(cmd, "users")
			if err != nil {
				return err
			}
			system, _ := cmd.Flags().GetBool("system")
			namesOnly, _ := cmd.Flags().GetBool("names")
			if namesOnly {
				// The bare list is a script input, not a report: no header, one
				// name per line.
				if system {
					return module.ListSystemUserNames(cmd.Context(), rc)
				}
				return module.ListUserNames(rc)
			}
			ui.WriteHeader(cmd.OutOrStdout(), cmd.CommandPath())
			if system {
				return module.ListSystemUsers(cmd.Context(), rc)
			}
			return module.ListUsers(rc)
		},
	}
	cmd.Flags().BoolP("system", "s", false, "List system users (UID 1000-65533)")
	cmd.Flags().Bool("names", false, "Print usernames only (one per line)")
	return cmd
}

func newUserBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "backup",
		Short:   "Backup user list and metadata to JSON",
		Example: "  sudo rootfiles user backup\n  sudo rootfiles user backup --file /root/users.json",
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := buildRunContext(cmd, "users")
			if err != nil {
				return err
			}
			output, _ := cmd.Flags().GetString("file")
			return backupCLIUsers(rc, output)
		},
	}
	cmd.Flags().String("file", "", "Output path for backup file")
	return cmd
}

func newUserRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "restore [BACKUP_FILE]",
		Short:   "Restore users from backup (reconnect existing home dirs)",
		Example: "  sudo rootfiles user restore\n  sudo rootfiles user restore /root/users.json",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := buildRunContext(cmd, "users")
			if err != nil {
				return err
			}
			backupPath := ""
			if len(args) > 0 {
				backupPath = args[0]
			}
			return module.RestoreUsers(cmd.Context(), rc, backupPath)
		},
	}
}

func newUserRehomeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "rehome <USERNAME>",
		Short:   "Move user home to custom home base directory",
		Example: "  sudo rootfiles user rehome alice\n  sudo rootfiles user rehome alice --remove-old",
		Long: "Copy the user's home to <home-base>/<user>, verify the copy, switch the account\n" +
			"over and leave a symlink at the old path. The original is kept as\n" +
			"<old>.rootfiles-bak-<ts> unless --remove-old.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := buildRunContext(cmd, "users")
			if err != nil {
				return err
			}
			removeOld, _ := cmd.Flags().GetBool("remove-old")
			return module.RehomeUser(cmd.Context(), rc, args[0], removeOld)
		},
	}
	cmd.Flags().Bool("remove-old", false, "Delete the original home after a verified copy")
	return cmd
}

func newUserIDCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "id <USERNAME>",
		Short:   "Show UID, GID, and groups for a user",
		Example: "  rootfiles user id alice",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := buildRunContext(cmd, "users")
			if err != nil {
				return err
			}
			return module.ShowUserID(cmd.Context(), rc, args[0])
		},
	}
}

func newUserGroupsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "groups [USERNAME]",
		Short:   "List groups (all groups or for a specific user)",
		Example: "  rootfiles user groups\n  rootfiles user groups alice",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := buildRunContext(cmd, "users")
			if err != nil {
				return err
			}
			if len(args) > 0 {
				return module.ListUserGroups(cmd.Context(), rc, args[0])
			}
			return module.ListGroups(cmd.Context(), rc)
		},
	}
}

func newUserGroupAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "group-add <USERNAME>",
		Short:   "Add a user to groups",
		Example: "  sudo rootfiles user group-add alice --groups docker --sudo",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := buildRunContext(cmd, "users")
			if err != nil {
				return err
			}
			groups := collectGroupFlags(cmd)
			if len(groups) == 0 {
				return fmt.Errorf("specify groups with --groups, --docker, or --sudo")
			}
			return module.AddUserToGroups(cmd.Context(), rc, args[0], groups)
		},
	}
	addGroupSelectionFlags(cmd)
	return cmd
}

func newUserGroupDelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "group-del <USERNAME>",
		Short:   "Remove a user from groups",
		Example: "  sudo rootfiles user group-del alice --groups docker",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := buildRunContext(cmd, "users")
			if err != nil {
				return err
			}
			groups := collectGroupFlags(cmd)
			if len(groups) == 0 {
				return fmt.Errorf("specify groups with --groups, --docker, or --sudo")
			}
			return module.RemoveUserFromGroups(cmd.Context(), rc, args[0], groups)
		},
	}
	addGroupSelectionFlags(cmd)
	return cmd
}

// addGroupSelectionFlags registers the --groups / --docker / --sudo flag trio
// shared by group-add and group-del.
func addGroupSelectionFlags(cmd *cobra.Command) {
	cmd.Flags().StringSlice("groups", nil, "Groups to target")
	cmd.Flags().Bool("docker", false, "Include the docker group")
	cmd.Flags().Bool("sudo", false, "Include the sudo group")
}

// collectGroupFlags expands the --groups / --docker / --sudo flags into a single slice.
func collectGroupFlags(cmd *cobra.Command) []string {
	groups, _ := cmd.Flags().GetStringSlice("groups")
	if docker, _ := cmd.Flags().GetBool("docker"); docker {
		groups = append(groups, "docker")
	}
	if sudo, _ := cmd.Flags().GetBool("sudo"); sudo {
		groups = append(groups, "sudo")
	}
	return groups
}

func newUserPasswdCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "passwd [USERNAME...]",
		Short:   "Set passwords for users (batch)",
		Example: "  sudo rootfiles user passwd alice --expire\n  sudo rootfiles user passwd --file /root/new-users.csv",
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := buildRunContext(cmd, "users")
			if err != nil {
				return err
			}
			password, _ := cmd.Flags().GetString("password")
			filePath, _ := cmd.Flags().GetString("file")
			all, _ := cmd.Flags().GetBool("all")
			expire, _ := cmd.Flags().GetBool("expire")

			entries, err := resolvePasswordTargets(cmd.Context(), rc, args, filePath, all)
			if err != nil {
				return err
			}

			if password != "" {
				for i := range entries {
					entries[i].Password = password
				}
			}

			if len(entries) == 0 {
				return fmt.Errorf("no users to process")
			}

			ui.WriteText(cmd.OutOrStdout(), "Target users (%d):\n", len(entries))
			for _, e := range entries {
				ui.WriteText(cmd.OutOrStdout(), "  - %s\n", e.Username)
			}
			ok, err := ui.Confirm("Set passwords for these users?", rc.Yes)
			if err != nil {
				return err
			}
			if !ok {
				ui.WriteLine(cmd.OutOrStdout(), "Cancelled.")
				return nil
			}

			return setCLIUserPasswords(cmd.Context(), rc, entries, expire)
		},
	}
	cmd.Flags().String("password", "", "Set the same password for all users")
	cmd.Flags().Bool("expire", false, "Force a password change at next login (chage -d 0)")
	cmd.Flags().StringP("file", "f", "", "File of usernames (or name,password lines)")
	cmd.Flags().Bool("all", false, "Set passwords for all system users")
	return cmd
}

// resolvePasswordTargets decides which users the passwd subcommand should
// operate on. Sources (in priority): --file, --all, positional args.
func resolvePasswordTargets(ctx context.Context, rc *module.RunContext, args []string, filePath string, all bool) ([]module.PasswordEntry, error) {
	switch {
	case filePath != "":
		return module.LoadPasswordFile(filePath)
	case all:
		users, err := module.ScanSystemUsersExported(ctx, rc)
		if err != nil {
			return nil, fmt.Errorf("scanning system users: %w", err)
		}
		entries := make([]module.PasswordEntry, 0, len(users))
		for _, u := range users {
			entries = append(entries, module.PasswordEntry{Username: u.Name})
		}
		return entries, nil
	case len(args) > 0:
		entries := make([]module.PasswordEntry, 0, len(args))
		for _, name := range args {
			entries = append(entries, module.PasswordEntry{Username: name})
		}
		return entries, nil
	default:
		return nil, fmt.Errorf("specify usernames, --all, or --file")
	}
}
