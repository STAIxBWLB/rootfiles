package cli

import (
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/exec"
	"github.com/STAIxBWLB/rootfiles/internal/state"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

func newRollbackCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "rollback [BACKUP_ID]",
		Short:   "Restore files changed by an earlier run",
		Example: "  rootfiles rollback\n  sudo rootfiles rollback 20261101T090413Z --dry-run",
		Long: "Every mutating run saves the previous version of each file it overwrites or\n" +
			"removes under one id in the state directory's backups folder\n" +
			"($ROOTFILES_STATE_DIR/backups, or /var/lib/rootfiles/backups). rollback\n" +
			"restores those files and deletes the files the run created; packages, users\n" +
			"and services are not reverted. Without an id, it lists available backups.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if len(args) == 0 {
				backups, err := exec.ListBackups(state.BackupsDir())
				if err != nil {
					return err
				}
				return renderRollbackList(out, cmd.CommandPath(), backups)
			}

			dryRun, _ := cmd.Flags().GetBool("dry-run")
			yes := yesFlag(cmd)
			preview, err := exec.RestoreBackup(state.BackupsDir(), args[0], true)
			if err != nil {
				return fmt.Errorf("loading backup %s: %w", args[0], err)
			}
			for _, a := range preview {
				ui.WriteBullet(out, ui.PendingMark(), a)
			}
			if dryRun {
				return nil
			}
			// Listing is read-only; restoring needs root and the lock.
			if err := requireRootAndLock(cmd); err != nil {
				return err
			}
			ok, err := ui.Confirm("Restore these files?", yes)
			if err != nil || !ok {
				return err
			}
			if _, err := exec.RestoreBackup(state.BackupsDir(), args[0], false); err != nil {
				return err
			}
			if err := forgetRolledBackRun(); err != nil {
				ui.WriteWarning(cmd.ErrOrStderr(), "removing the kept applied config: "+err.Error(), "")
			}
			auditf(cmd, "rollback", "backup", args[0])
			ui.WriteSuccess(out, "restored backup "+args[0])
			ui.WriteHint(out, "restart affected services (e.g. ssh, docker) for the restored files to take effect")
			return nil
		},
	}
	return cmd
}

// renderRollbackList prints the recorded backups without querying the host, so
// the layout is pinned by a fixture golden.
func renderRollbackList(out io.Writer, title string, backups []exec.BackupManifest) error {
	ui.WriteHeader(out, title)
	ui.WriteSection(out, "Backups")
	if len(backups) == 0 {
		ui.WriteHint(out, "no backups recorded")
		return nil
	}
	rows := make([][]string, 0, len(backups))
	for _, b := range backups {
		rows = append(rows, []string{b.ID, strconv.Itoa(len(b.Entries)), b.Note})
	}
	return ui.WriteTable(out, []string{"BACKUP", "PATHS", "NOTE"}, rows, 2)
}

// forgetRolledBackRun removes the config copy the last recorded apply kept:
// after any restore the host's files no longer match that config, so the
// subcommands fall back to minimal with home-base detection, which follows
// the restored HOME=, until the next apply keeps a new copy.
func forgetRolledBackRun() error {
	return state.SaveAppliedConfig(nil)
}
