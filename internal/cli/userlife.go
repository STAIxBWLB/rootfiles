package cli

import (
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/module"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

func newUserDelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "del <USERNAME>",
		Short:   "Delete a user (home kept by default)",
		Example: "  sudo rootfiles user del alice --archive",
		Long: "Removes the account, its sudoers drop-in, GPU allocation and metadata entry.\n" +
			"The home directory is kept unless --archive (a tar.gz under\n" +
			"<home_base>/.rootfiles/archive, then removed) or --remove-home is given.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := buildRunContext(cmd, "users")
			if err != nil {
				return err
			}
			archive, _ := cmd.Flags().GetBool("archive")
			remove, _ := cmd.Flags().GetBool("remove-home")
			mode := module.KeepHome
			switch {
			case archive && remove:
				return fmt.Errorf("--archive and --remove-home are mutually exclusive")
			case archive:
				mode = module.ArchiveHome
			case remove:
				mode = module.RemoveHome
			}
			if mode == module.RemoveHome {
				ok, err := ui.Confirm(fmt.Sprintf("Permanently delete %s and its home directory?", args[0]), rc.Yes)
				if err != nil || !ok {
					return err
				}
			}
			return module.DeleteUser(cmd.Context(), rc, args[0], mode)
		},
	}
	cmd.Flags().Bool("archive", false, "Archive the home under <home_base>/.rootfiles, then remove it")
	cmd.Flags().Bool("remove-home", false, "Remove the home directory without archiving")
	return cmd
}

func newUserLockCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "lock <USERNAME>",
		Short:   "Block all logins (password and SSH key)",
		Example: "  sudo rootfiles user lock alice",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := buildRunContext(cmd, "users")
			if err != nil {
				return err
			}
			return module.LockUser(cmd.Context(), rc, args[0])
		},
	}
}

func newUserUnlockCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "unlock <USERNAME>",
		Short:   "Re-enable logins for a locked user",
		Example: "  sudo rootfiles user unlock alice",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := buildRunContext(cmd, "users")
			if err != nil {
				return err
			}
			return module.UnlockUser(cmd.Context(), rc, args[0])
		},
	}
}

func newUserExpireCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "expire <USERNAME> <YYYY-MM-DD|never>",
		Short:   "Set or clear the account expiry date",
		Example: "  sudo rootfiles user expire alice 2027-03-31\n  sudo rootfiles user expire alice never",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := buildRunContext(cmd, "users")
			if err != nil {
				return err
			}
			return module.SetExpiry(cmd.Context(), rc, args[0], args[1])
		},
	}
}

func newUserKeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "key",
		Short:   "Manage a user's SSH authorized keys",
		Example: "  rootfiles user key list alice\n  sudo rootfiles user key add alice \"$(cat ~/.ssh/id_ed25519.pub)\"",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:     "list <USERNAME>",
			Short:   "List authorized keys (numbered)",
			Example: "  rootfiles user key list alice",
			Args:    cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				rc, err := buildRunContext(cmd, "users")
				if err != nil {
					return err
				}
				keys, err := keyListCollector(rc, args[0])
				if err != nil {
					return err
				}
				out := cmd.OutOrStdout()
				ui.WriteHeader(out, cmd.CommandPath())
				return renderKeyList(out, args[0], keys)
			},
		},
		&cobra.Command{
			Use:     "add <USERNAME> <KEY>",
			Short:   "Add an authorized key",
			Example: "  sudo rootfiles user key add alice \"$(cat ~/.ssh/id_ed25519.pub)\"",
			Args:    cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				rc, err := buildRunContext(cmd, "users")
				if err != nil {
					return err
				}
				return module.AddKey(cmd.Context(), rc, args[0], args[1])
			},
		},
		&cobra.Command{
			Use:     "rm <USERNAME> <INDEX|KEY|COMMENT>",
			Short:   "Remove authorized key(s) by list index, key or comment",
			Example: "  sudo rootfiles user key rm alice 2\n  sudo rootfiles user key rm alice alice@laptop",
			Args:    cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				rc, err := buildRunContext(cmd, "users")
				if err != nil {
					return err
				}
				return module.RemoveKey(cmd.Context(), rc, args[0], args[1])
			},
		},
	)
	return cmd
}

// keyListCollector reads a managed user's authorized keys; a test replaces it
// with fixture keys so the list layout is pinned without an account.
var keyListCollector = module.AuthorizedKeys

// renderKeyList prints a user's authorized keys, numbered, with the key blob
// shortened in the middle so one long key cannot push the comment off the line.
func renderKeyList(out io.Writer, name string, keys []module.AuthorizedKey) error {
	ui.WriteSection(out, "Authorized keys")
	if len(keys) == 0 {
		ui.WriteHint(out, fmt.Sprintf("%s has no authorized keys", name))
		return nil
	}
	rows := make([][]string, 0, len(keys))
	for i, key := range keys {
		rows = append(rows, []string{strconv.Itoa(i + 1), shortenKeyBlob(key.Blob), key.Comment})
	}
	return ui.WriteTable(out, []string{"#", "KEY", "COMMENT"}, rows, 2)
}

// shortenKeyBlob keeps a key's type and its tail, where the fingerprint differs.
func shortenKeyBlob(blob string) string {
	if len(blob) <= 40 {
		return blob
	}
	return blob[:25] + "..." + blob[len(blob)-12:]
}

func newUserDuCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "du",
		Short:   "Show home directory disk usage per user (largest first)",
		Example: "  rootfiles user du",
		RunE: func(cmd *cobra.Command, _ []string) error {
			rc, err := buildRunContext(cmd, "users")
			if err != nil {
				return err
			}
			rows, err := module.HomesUsage(cmd.Context(), rc)
			if err != nil {
				return err
			}
			return renderHomeUsage(cmd.OutOrStdout(), cmd.CommandPath(), rows)
		},
	}
}

// renderHomeUsage prints the measured home sizes without running du, so a
// fixture golden pins the layout.
func renderHomeUsage(out io.Writer, title string, rows []module.HomeUsage) error {
	ui.WriteHeader(out, title)
	ui.WriteSection(out, "Home usage")
	table := make([][]string, 0, len(rows))
	var total int64
	for _, r := range rows {
		table = append(table, []string{humanBytes(r.Bytes), r.User, r.Home})
		total += r.Bytes
	}
	if err := ui.WriteTable(out, []string{"SIZE", "USER", "HOME"}, table, 2); err != nil {
		return err
	}
	ui.WriteKV(out, "Total", humanBytes(total))
	return nil
}

func newUserAuditCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "audit",
		Short:   "Compare users.json metadata with the accounts on this system",
		Example: "  sudo rootfiles user audit",
		RunE: func(cmd *cobra.Command, _ []string) error {
			rc, err := buildRunContext(cmd, "users")
			if err != nil {
				return err
			}
			findings, err := userAuditCollector(cmd.Context(), rc)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			ui.WriteHeader(out, cmd.CommandPath())
			if len(findings) == 0 {
				ui.WriteSection(out, "Users")
				ui.WriteSuccess(out, "users.json and system accounts agree")
				return nil
			}
			ui.WriteSection(out, "Users")
			for _, f := range findings {
				ui.WriteBullet(out, ui.WarnMark(), fmt.Sprintf("%-16s %s", f.User, f.Detail))
			}
			ui.WriteSummary(out, []ui.SummaryCount{{Marker: ui.WarnMark(), Count: len(findings), Label: "findings"}})
			return &ExitError{Code: exitDrift}
		},
	}
}

// userAuditCollector compares the metadata with the system accounts; a test
// replaces it with fixture findings so the report layout is pinned.
var userAuditCollector = module.AuditUsers

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func newUserQuotaCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "quota",
		Short:   "Limit per-user disk usage on the home base",
		Example: "  rootfiles user quota show\n  sudo rootfiles user quota set alice 500G",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:     "set <USERNAME> <SIZE>",
			Short:   "Set a hard quota, e.g. 500G",
			Example: "  sudo rootfiles user quota set alice 500G",
			Args:    cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				rc, err := buildRunContext(cmd, "users")
				if err != nil {
					return err
				}
				return module.SetQuota(cmd.Context(), rc, args[0], args[1])
			},
		},
		&cobra.Command{
			Use:     "rm <USERNAME>",
			Short:   "Remove a user's limit",
			Example: "  sudo rootfiles user quota rm alice",
			Args:    cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				rc, err := buildRunContext(cmd, "users")
				if err != nil {
					return err
				}
				return module.SetQuota(cmd.Context(), rc, args[0], "0")
			},
		},
		&cobra.Command{
			Use:     "show",
			Short:   "Show the quota report",
			Example: "  rootfiles user quota show",
			RunE: func(cmd *cobra.Command, _ []string) error {
				rc, err := buildRunContext(cmd, "users")
				if err != nil {
					return err
				}
				return module.ShowQuotas(cmd.Context(), rc)
			},
		},
	)
	return cmd
}
