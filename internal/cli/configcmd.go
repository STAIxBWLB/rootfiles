package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/module"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect, validate and scaffold configuration",
	}
	cmd.AddCommand(newConfigShowCmd(), newConfigValidateCmd(), newConfigInitCmd())
	return cmd
}

// loadForInspection resolves the target like check/status and applies CLI
// flag overrides, so what is shown is exactly what apply would use.
func loadForInspection(cmd *cobra.Command) (*config.Config, string, error) {
	sys, _ := detectSystem()
	if sys == nil {
		sys = &config.SystemInfo{}
	}
	profile, configPath, applied := resolveTarget(cmd, sys)
	reportProfile, reportPath := reportedTarget(profile, configPath, applied)
	source := "profile " + reportProfile
	if reportPath != "" {
		source = "config " + reportPath
	}
	cfg, err := config.LoadWithOverrides(profile, configPath, sys, loadOverrides(cmd))
	if err != nil {
		return nil, source, err
	}
	applyFlagOverrides(cmd, cfg)
	applyAccountFlags(cmd, cfg)
	if err := cfg.Validate(); err != nil {
		return cfg, source, err
	}
	if sys.OS == "rocky" {
		if err := validateModules(cfg, sys, module.NewRegistry().Resolve(cfg, nil)); err != nil {
			return cfg, source, err
		}
	}
	return cfg, source, nil
}

func newConfigShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print the fully merged configuration (extends, env and flag overrides applied)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, source, err := loadForInspection(cmd)
			if cfg == nil {
				return err
			}
			reveal, _ := cmd.Flags().GetBool("reveal")
			if !reveal {
				cfg.Modules.Cloudflared.TunnelToken = maskSecret(cfg.Modules.Cloudflared.TunnelToken)
			}
			data, merr := config.MarshalYAML(cfg)
			if merr != nil {
				return merr
			}
			fmt.Fprintf(cmd.OutOrStdout(), "# resolved from %s\n%s", source, data)
			return err
		},
	}
	cmd.Flags().Bool("reveal", false, "Show secrets (tunnel token) instead of masking them")
	return usesConfigInputs(cmd)
}

func newConfigValidateCmd() *cobra.Command {
	return usesConfigInputs(&cobra.Command{
		Use:   "validate",
		Short: "Validate a profile or config file (unknown keys, paths, ports, CIDRs, …)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, source, err := loadForInspection(cmd)
			if err != nil {
				return fmt.Errorf("%s: %w", source, err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), ui.StyleSuccess.Render(ui.MarkOK+" "+source+" is valid"))
			return nil
		},
	})
}

func newConfigInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Scaffold a site config (template, or captured from this system)",
		Long: "Writes a config for use with `rootfiles apply --config FILE`.\n" +
			"Default: a small file extending the suggested profile, with commented examples.\n" +
			"--from-system: capture the current host's settings (works on hosts not set up by rootfiles).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fromSystem, _ := cmd.Flags().GetBool("from-system")
			extends, _ := cmd.Flags().GetString("extends")
			file, _ := cmd.Flags().GetString("file")
			overwrite, _ := cmd.Flags().GetBool("overwrite")
			legacyForce, _ := cmd.Flags().GetBool("force")
			overwrite = overwrite || legacyForce

			var data []byte
			if fromSystem {
				cfg, err := config.SnapshotCurrent()
				if err != nil {
					return err
				}
				if data, err = config.MarshalYAML(cfg); err != nil {
					return err
				}
				data = append([]byte("# captured from this system by `rootfiles config init --from-system`\n"), data...)
			} else {
				if extends == "" {
					sys, _ := detectSystem()
					if sys == nil {
						sys = &config.SystemInfo{}
					}
					extends = sys.SuggestProfile()
				}
				data = []byte(configTemplate(extends))
			}

			if file == "" {
				_, err := cmd.OutOrStdout().Write(data)
				return err
			}
			if err := writePrivateConfig(file, data, overwrite); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s — check it with: rootfiles config validate --config %s\n", file, file)
			return nil
		},
	}
	cmd.Flags().Bool("overwrite", false, "Overwrite an existing config file")
	cmd.Flags().Bool("force", false, "Deprecated alias for --overwrite")
	_ = cmd.Flags().MarkDeprecated("force", "use --overwrite instead")
	cmd.Flags().Bool("from-system", false, "Capture settings from the running system")
	cmd.Flags().String("extends", "", "Base profile for the template (default: detected suggestion)")
	cmd.Flags().String("file", "", "Write to FILE instead of stdout")
	return cmd
}

func configTemplate(extends string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `# rootfiles site config — apply with: sudo rootfiles apply --config <this file>
# Any key set here overrides the base profile, including false/0.
extends: %s

# timezone: Asia/Seoul

# users:
#   home_base: /raid/home
#   accounts:
#     - name: admin
#       ssh_pubkeys:
#         - "ssh-ed25519 AAAA... admin@laptop"
#       groups: [sudo]

# ssh:
#   disable_root_login: true
#   disable_password_auth: true   # refused unless an account has an SSH key
#   port: 22

# modules:
#   network:
#     enabled: true
#     ufw: true
#     allowed_ports: [22, 443]    # the SSH port is always allowed
#   cloudflared:
#     enabled: true
#     # tunnel_token: prefer ROOTFILES_TUNNEL_TOKEN in the environment,
#     # or a root-owned 0600 file:
#     # tunnel_token_file: /etc/rootfiles/tunnel-token
`, extends)
	return b.String()
}

// writePrivateConfig writes a config that may hold the tunnel token. The file
// always ends 0600 and owned by the caller: data goes to a new temp file in the
// same directory and is renamed over the destination, so a failed write keeps
// the previous file and a symlink is replaced, never followed.
func writePrivateConfig(file string, data []byte, overwrite bool) error {
	if fi, err := os.Lstat(file); err == nil {
		if !overwrite {
			return fmt.Errorf("%s already exists (use --overwrite to replace it)", file)
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file (symlink or special file); remove it or choose another --file", file)
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() && st.Uid != 0 {
			return fmt.Errorf("%s is owned by uid %d, not the user running this command; remove it or choose another --file", file, st.Uid)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking %s: %w", file, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".rootfiles-config.*.tmp")
	if err != nil {
		return fmt.Errorf("creating a temporary file next to %s: %w (check the directory exists and is writable)", file, err)
	}
	tmpPath := tmp.Name()
	fail := func(step string, err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("%s for %s: %w; the previous file, if any, is unchanged", step, file, err)
	}
	if err := tmp.Chmod(0600); err != nil {
		return fail("setting mode 0600", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail("writing config", err)
	}
	if err := tmp.Sync(); err != nil {
		return fail("syncing config", err)
	}
	if err := tmp.Close(); err != nil {
		return fail("closing config", err)
	}
	if err := os.Rename(tmpPath, file); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replacing %s: %w; the previous file, if any, is unchanged", file, err)
	}
	return nil
}
