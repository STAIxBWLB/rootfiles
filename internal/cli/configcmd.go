package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/config"
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
	cfg, err := config.LoadWithHomeBase(profile, configPath, sys, homeBaseFlag(cmd))
	if err != nil {
		return nil, source, err
	}
	applyFlagOverrides(cmd, cfg)
	applyAccountFlags(cmd, cfg)
	if err := cfg.Validate(); err != nil {
		return cfg, source, err
	}
	if sys.OS == "rocky" {
		names := []string{}
		for _, name := range []string{"locale", "system", "packages", "users", "ssh", "security", "docker", "nvidia", "gpu", "cloudflared", "storage", "network", "monitoring"} {
			if cfg.IsModuleEnabled(name) {
				names = append(names, name)
			}
		}
		if err := config.ValidateCapabilities(cfg, sys, names); err != nil {
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
	return cmd
}

func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "…" + "(masked)"
}

func newConfigValidateCmd() *cobra.Command {
	return &cobra.Command{
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
	}
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
			force, _ := cmd.Flags().GetBool("force")

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
			if _, err := os.Stat(file); err == nil && !force {
				return fmt.Errorf("%s already exists (use --force to overwrite)", file)
			}
			// 0600: captured configs may contain the tunnel token.
			if err := os.WriteFile(file, data, 0600); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s — check it with: rootfiles config validate --config %s\n", file, file)
			return nil
		},
	}
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
