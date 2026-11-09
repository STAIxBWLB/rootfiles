package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/module"
	"github.com/STAIxBWLB/rootfiles/internal/state"
	"github.com/STAIxBWLB/rootfiles/internal/ui"

	"github.com/STAIxBWLB/rootfiles/internal/fleet"
)

// What `backup` archives and where the config snapshot comes from; tests
// point the paths at fixtures and stub the snapshot.
var (
	rootSSHDir          = "/root/.ssh"
	usrLocalBinDir      = "/usr/local/bin"
	snapshotCurrent     = config.SnapshotCurrent
	etcBackupCandidates = []string{
		"/etc/ssh/sshd_config.d/",
		"/etc/docker/daemon.json",
		"/etc/systemd/network/",
		"/etc/ufw/",
		"/etc/fstab",
		"/etc/netplan/",
		"/etc/default/locale",
		"/etc/timezone",
		"/etc/default/useradd",
	}
)

func newBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Full system backup for OS upgrade / restore",
		Long:  "Captures system info, user metadata, config files, and a rootfiles-compatible config snapshot.",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			outputBase := stringFlagOrAlias(cmd, "dir", "output")
			skipDocker, _ := cmd.Flags().GetBool("skip-docker")
			skipEtc, _ := cmd.Flags().GetBool("skip-etc")

			rc, err := buildRunContext(cmd)
			if err != nil {
				return err
			}
			ctx := cmd.Context()

			hostname, _ := os.Hostname()
			dirName := fmt.Sprintf("rootfiles-backup-%s-%s", hostname, time.Now().Format("20060102"))
			// The ownership walk needs an absolute base under configTrustRoot.
			backupDir, err := filepath.Abs(filepath.Join(outputBase, dirName))
			if err != nil {
				return fmt.Errorf("resolving backup directory: %w", err)
			}

			// The backup holds private keys and /etc configs, so it stays
			// root-only: refuse a directory a user other than root could
			// control, and create it 0700 through the runner so a dry run
			// writes nothing.
			if err := config.RootOnlyBase(configTrustRoot, backupDir, configOwnerUID); err != nil {
				return fmt.Errorf("refusing backup directory: %w", err)
			}
			if err := rc.Runner.MkdirAll(backupDir, 0700); err != nil {
				return fmt.Errorf("creating backup directory: %w", err)
			}
			// The directory name is deterministic per day; MkdirAll keeps the
			// mode of one an older run left behind, so tighten it explicitly.
			if _, err := rc.Runner.Run(ctx, "chmod", "0700", backupDir); err != nil {
				return fmt.Errorf("securing backup directory: %w", err)
			}
			ui.WriteText(out, "Backup directory: %s\n\n", backupDir)

			var errors []string

			// 1. system-info.json
			ui.WriteRaw(out, "  system-info.json ... ")
			if err := backupSystemInfo(rc, backupDir, hostname); err != nil {
				errors = append(errors, fmt.Sprintf("system-info: %v", err))
				ui.WriteLine(out, "FAIL")
			} else {
				ui.WriteLine(out, "OK")
			}

			// 2. users.json
			ui.WriteRaw(out, "  users.json ... ")
			if err := backupUsersJSON(rc, backupDir); err != nil {
				errors = append(errors, fmt.Sprintf("users: %v", err))
				ui.WriteLine(out, "SKIP (no user database)")
			} else {
				ui.WriteLine(out, "OK")
			}

			// 3. etc-config.tar.gz
			if !skipEtc {
				ui.WriteRaw(out, "  etc-config.tar.gz ... ")
				if err := backupEtcConfig(ctx, rc, backupDir); err != nil {
					errors = append(errors, fmt.Sprintf("etc-config: %v", err))
					ui.WriteLine(out, "FAIL")
				} else {
					ui.WriteLine(out, "OK")
				}
			}

			// 4. crontab-root.txt
			ui.WriteRaw(out, "  crontab-root.txt ... ")
			if err := backupCrontab(ctx, rc, backupDir); err != nil {
				errors = append(errors, fmt.Sprintf("crontab: %v", err))
				ui.WriteLine(out, "SKIP (no crontab)")
			} else {
				ui.WriteLine(out, "OK")
			}

			// 5. root-ssh.tar.gz
			ui.WriteRaw(out, "  root-ssh.tar.gz ... ")
			if err := backupRootSSH(ctx, rc, backupDir); err != nil {
				errors = append(errors, fmt.Sprintf("root-ssh: %v", err))
				ui.WriteLine(out, "SKIP")
			} else {
				ui.WriteLine(out, "OK")
			}

			// 6. usr-local-bin.tar.gz
			ui.WriteRaw(out, "  usr-local-bin.tar.gz ... ")
			if err := backupUsrLocalBin(ctx, rc, backupDir); err != nil {
				errors = append(errors, fmt.Sprintf("usr-local-bin: %v", err))
				ui.WriteLine(out, "FAIL")
			} else {
				ui.WriteLine(out, "OK")
			}

			// 7. docker-images.txt
			if !skipDocker {
				ui.WriteRaw(out, "  docker-images.txt ... ")
				if err := backupDockerImages(ctx, rc, backupDir); err != nil {
					errors = append(errors, fmt.Sprintf("docker-images: %v", err))
					ui.WriteLine(out, "SKIP (docker not available)")
				} else {
					ui.WriteLine(out, "OK")
				}
			}

			// 8. config-snapshot.yaml
			ui.WriteRaw(out, "  config-snapshot.yaml ... ")
			if err := backupConfigSnapshot(rc, backupDir); err != nil {
				errors = append(errors, fmt.Sprintf("config-snapshot: %v", err))
				ui.WriteLine(out, "FAIL")
			} else {
				ui.WriteLine(out, "OK")
			}

			// A same-day re-run overwrites existing files, and overwriting
			// keeps a file's old mode, so tighten every artifact after the
			// writes as well.
			if files, _ := filepath.Glob(filepath.Join(backupDir, "*")); len(files) > 0 {
				if _, err := rc.Runner.Run(ctx, "chmod", append([]string{"0600"}, files...)...); err != nil {
					return fmt.Errorf("securing backup files: %w", err)
				}
			}

			ui.WriteLine(out)
			if len(errors) > 0 {
				ui.WriteText(out, "Backup completed with %d warning(s):\n", len(errors))
				for _, e := range errors {
					ui.WriteText(out, "  - %s\n", e)
				}
			} else {
				ui.WriteLine(out, "Backup completed successfully.")
			}
			ui.WriteText(out, "\nRestore config with:\n  rootfiles apply --config %s/config-snapshot.yaml --dry-run\n", backupDir)
			return nil
		},
	}
	cmd.Flags().String("dir", defaultBackupDir(), "Backup directory (the archive goes in a rootfiles-backup-* subdirectory)")
	cmd.Flags().StringP("output", "o", "", "Deprecated alias for --dir")
	_ = cmd.Flags().MarkDeprecated("output", "use --dir instead")
	_ = cmd.Flags().MarkShorthandDeprecated("output", "use --dir instead")
	cmd.Flags().Bool("skip-docker", false, "Skip Docker image list")
	cmd.Flags().Bool("skip-etc", false, "Skip /etc/ config tar")
	return usesConfigTarget(cmd)
}

// defaultBackupDir keeps a full system backup beside rootfiles' own state; an
// operator with a data disk passes --dir.
func defaultBackupDir() string {
	return filepath.Join(state.Dir(), "system-backup")
}

// backupSystemInfo writes system detection results + hostname.
func backupSystemInfo(rc *module.RunContext, backupDir, hostname string) error {
	sysInfo, err := detectSystem()
	if err != nil {
		return err
	}
	info := struct {
		Hostname string `json:"hostname"`
		*config.SystemInfo
	}{
		Hostname:   hostname,
		SystemInfo: sysInfo,
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return rc.Runner.WriteFile(filepath.Join(backupDir, "system-info.json"), data, 0600)
}

// backupUsersJSON backs up user metadata (managed + system users).
func backupUsersJSON(rc *module.RunContext, backupDir string) error {
	outputPath := filepath.Join(backupDir, "users.json")
	return module.BackupUsers(rc, outputPath)
}

// backupEtcConfig creates a tar.gz of key /etc/ config files.
func backupEtcConfig(ctx context.Context, rc *module.RunContext, backupDir string) error {
	// Collect paths that exist
	var existing []string
	for _, p := range etcBackupCandidates {
		if _, err := os.Stat(p); err == nil {
			existing = append(existing, p)
		}
	}
	if len(existing) == 0 {
		return fmt.Errorf("no /etc/ config files found")
	}
	return backupTar(ctx, rc, filepath.Join(backupDir, "etc-config.tar.gz"), existing...)
}

// backupCrontab saves root's crontab.
func backupCrontab(ctx context.Context, rc *module.RunContext, backupDir string) error {
	result, err := rc.Runner.Run(ctx, "crontab", "-l")
	if err != nil {
		return err
	}
	if result.Stdout == "" {
		return fmt.Errorf("empty crontab")
	}
	return rc.Runner.WriteFile(filepath.Join(backupDir, "crontab-root.txt"), []byte(result.Stdout), 0600)
}

// backupRootSSH archives /root/.ssh/.
func backupRootSSH(ctx context.Context, rc *module.RunContext, backupDir string) error {
	if _, err := os.Stat(rootSSHDir); err != nil {
		return fmt.Errorf("%s not found", rootSSHDir)
	}
	return backupTar(ctx, rc, filepath.Join(backupDir, "root-ssh.tar.gz"), rootSSHDir)
}

// backupUsrLocalBin archives /usr/local/bin/.
func backupUsrLocalBin(ctx context.Context, rc *module.RunContext, backupDir string) error {
	if _, err := os.Stat(usrLocalBinDir); err != nil {
		return fmt.Errorf("%s not found", usrLocalBinDir)
	}
	return backupTar(ctx, rc, filepath.Join(backupDir, "usr-local-bin.tar.gz"), usrLocalBinDir)
}

// backupTar creates an archive under umask 077 so local users cannot read
// it whatever the process umask is; RunShell keeps it dry-run aware.
func backupTar(ctx context.Context, rc *module.RunContext, outPath string, paths ...string) error {
	parts := make([]string, 0, len(paths)+2)
	parts = append(parts, "tar czf", fleet.ShellQuote(outPath))
	for _, p := range paths {
		parts = append(parts, fleet.ShellQuote(p))
	}
	_, err := rc.Runner.RunShell(ctx, "umask 077 && "+strings.Join(parts, " "))
	return err
}

// backupDockerImages saves a list of Docker images.
func backupDockerImages(ctx context.Context, rc *module.RunContext, backupDir string) error {
	result, err := rc.Runner.Run(ctx, "docker", "images", "--format", "{{.Repository}}:{{.Tag}}\t{{.Size}}\t{{.ID}}")
	if err != nil {
		return err
	}
	return rc.Runner.WriteFile(filepath.Join(backupDir, "docker-images.txt"), []byte(result.Stdout), 0600)
}

// backupConfigSnapshot generates a rootfiles YAML config from current system state.
func backupConfigSnapshot(rc *module.RunContext, backupDir string) error {
	cfg, err := snapshotCurrent()
	if err != nil {
		return err
	}
	// As in apply's kept copy, the inline tunnel token stays out of the file.
	cfg.Modules.Cloudflared.TunnelToken = ""
	data, err := config.MarshalYAML(cfg)
	if err != nil {
		return err
	}
	header := "# rootfiles config snapshot — generated by `rootfiles backup`\n# Use with: rootfiles apply --config <this-file> [--dry-run]\n\n"
	return rc.Runner.WriteFile(filepath.Join(backupDir, "config-snapshot.yaml"), []byte(header+string(data)), 0600)
}
