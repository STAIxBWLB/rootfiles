package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/exec"
	"github.com/STAIxBWLB/rootfiles/internal/state"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

func newRollbackCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rollback [BACKUP_ID]",
		Short: "Restore files changed by an earlier run",
		Long: "Every mutating run saves the previous version of each file it overwrites or removes under\n" +
			state.BackupsDir() + "/<id>. rollback restores those files and removes files the run created.\n" +
			"Packages, users and services are not reverted. Without an id, lists available backups.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if len(args) == 0 {
				backups, err := exec.ListBackups(state.BackupsDir())
				if err != nil {
					return err
				}
				ui.WriteSection(out, "Backups")
				if len(backups) == 0 {
					ui.WriteHint(out, "no backups recorded")
					return nil
				}
				for _, b := range backups {
					fmt.Fprintf(out, "  %s  %d path(s) — %s\n", b.ID, len(b.Entries), b.Note)
				}
				return nil
			}

			dryRun, _ := cmd.Flags().GetBool("dry-run")
			yes, _ := cmd.Flags().GetBool("yes")
			preview, err := exec.RestoreBackup(state.BackupsDir(), args[0], true)
			if err != nil {
				return fmt.Errorf("loading backup %s: %w", args[0], err)
			}
			for _, a := range preview {
				fmt.Fprintln(out, "  → "+a)
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
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: removing the kept applied config: %v\n", err)
			}
			auditf(cmd, "rollback", "backup", args[0])
			fmt.Fprintln(out, ui.StyleSuccess.Render(ui.MarkOK+" restored backup "+args[0]))
			fmt.Fprintln(out, "Restart affected services (e.g. ssh, docker) for the restored files to take effect.")
			return nil
		},
	}
	return cmd
}

// forgetRolledBackRun removes the config copy the last recorded apply kept:
// after any restore the host's files no longer match that config, so the
// subcommands fall back to minimal with home-base detection, which follows
// the restored HOME=, until the next apply keeps a new copy.
func forgetRolledBackRun() error {
	return state.SaveAppliedConfig(nil)
}
