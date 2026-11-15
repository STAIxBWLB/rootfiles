package cli

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	executil "github.com/STAIxBWLB/rootfiles/internal/exec"
	"github.com/STAIxBWLB/rootfiles/internal/fleet"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const fleetSkeleton = `# Replace the example host with your own SSH alias before connecting.
defaults:
  sudo: nopasswd
  parallel: 2
hosts:
  example-host.example:
    ssh: example-host.example
    # groups: [gpu]
    # config: sites/example.yaml
`

func newFleetInitCmd() *cobra.Command {
	c := &cobra.Command{Use: "init", Short: "Create a fleet inventory", Args: cobra.NoArgs,
		Example: "  rootfiles fleet init\n  rootfiles fleet init --from-ssh-config --match 'gpu*'",
		RunE: func(c *cobra.Command, _ []string) error {
			path := fleetInventoryPath(c)
			fromSSH, _ := c.Flags().GetBool("from-ssh-config")
			match, _ := c.Flags().GetString("match")
			group, _ := c.Flags().GetString("group")
			overwrite, _ := c.Flags().GetBool("overwrite")
			dry, _ := c.Flags().GetBool("dry-run")
			if !fromSSH && (c.Flags().Changed("match") || c.Flags().Changed("group")) {
				return fmt.Errorf("--match and --group require --from-ssh-config; add that flag")
			}
			data := []byte(fleetSkeleton)
			if fromSSH {
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				result, err := fleet.ImportSSHConfig(filepath.Join(home, ".ssh", "config"), match)
				if err != nil {
					return err
				}
				for _, skipped := range result.Skipped {
					fmt.Fprintf(c.ErrOrStderr(), "Skipped %s\n", skipped)
				}
				if len(result.Hosts) == 0 {
					return fmt.Errorf("no valid SSH aliases matched; check ~/.ssh/config and --match")
				}
				var root yaml.Node
				root.Kind, root.Tag = yaml.MappingNode, "!!map"
				var hosts yaml.Node
				hosts.Kind, hosts.Tag = yaml.MappingNode, "!!map"
				for _, name := range result.Hosts {
					host := fleet.Host{SSH: name}
					if group != "" {
						host.Groups = []string{group}
					}
					var node yaml.Node
					if err := node.Encode(host); err != nil {
						return err
					}
					hosts.Content = append(hosts.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, &node)
				}
				root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "hosts"}, &hosts)
				data, err = yaml.Marshal(&root)
				if err != nil {
					return err
				}
			}
			if _, err := fleet.ParseInventory(data, path); err != nil {
				return fmt.Errorf("generated inventory is invalid: %w", err)
			}
			if dry {
				_, err := c.OutOrStdout().Write(data)
				return err
			}
			runner := executil.NewRunner(false, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err := runner.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return fmt.Errorf("create inventory directory: %w", err)
			}
			info, err := os.Lstat(path)
			if err == nil {
				if !overwrite {
					return fmt.Errorf("inventory %q already exists; use --overwrite to back it up and replace it", path)
				}
				if !info.Mode().IsRegular() {
					return fmt.Errorf("inventory %q is not a regular file; choose a regular file", path)
				}
				old, err := os.ReadFile(path)
				if err != nil {
					return fmt.Errorf("read existing inventory: %w", err)
				}
				backup := path + ".backup-" + time.Now().UTC().Format("20060102T150405.000000000Z")
				if err := runner.WriteFileExclusive(backup, old, 0600); err != nil {
					return fmt.Errorf("back up inventory %q: %w", backup, err)
				}
				tmp := backup + ".new"
				if err := runner.WriteFileExclusive(tmp, data, 0600); err != nil {
					return fmt.Errorf("create replacement inventory: %w", err)
				}
				if err := runner.Rename(tmp, path); err != nil {
					_ = runner.Remove(tmp)
					return fmt.Errorf("replace inventory after backup %q: %w", backup, err)
				}
				fmt.Fprintf(c.ErrOrStderr(), "Backup: %s\n", backup)
				_, err = fmt.Fprintf(c.OutOrStdout(), "Created inventory %s\n", path)
				return err
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("inspect inventory %q: %w", path, err)
			}
			if err := runner.WriteFileExclusive(path, data, 0600); err != nil {
				return fmt.Errorf("create inventory %q: %w", path, err)
			}
			_, err = fmt.Fprintf(c.OutOrStdout(), "Created inventory %s\n", path)
			return err
		}}
	c.Flags().String("inventory", "", "Inventory file path")
	c.Flags().Bool("from-ssh-config", false, "Import aliases from SSH config")
	c.Flags().String("match", "", "Import aliases matching this glob")
	c.Flags().String("group", "", "Assign imported hosts to a group")
	c.Flags().Bool("overwrite", false, "Back up and replace an existing inventory")
	return c
}

type fleetHostRow struct {
	Name   string   `json:"name"`
	Groups []string `json:"groups"`
	SSH    string   `json:"ssh"`
	Sudo   string   `json:"sudo"`
	Config string   `json:"config"`
}

func newFleetHostsCmd() *cobra.Command {
	c := &cobra.Command{Use: "hosts", Short: "List and validate inventory hosts", Args: cobra.NoArgs,
		Example: "  rootfiles fleet hosts\n  rootfiles fleet hosts --group gpu --json",
		RunE: func(c *cobra.Command, _ []string) error {
			inv, err := loadFleetOnly(c)
			if err != nil {
				return err
			}
			hosts, _ := c.Flags().GetStringSlice("host")
			groups, _ := c.Flags().GetStringSlice("group")
			all, _ := c.Flags().GetBool("all")
			if len(hosts) == 0 && len(groups) == 0 && !all {
				all = true
			}
			selected, err := inv.Select(hosts, groups, all)
			if err != nil {
				return err
			}
			rows := make([]fleetHostRow, 0, len(selected))
			table := make([][]string, 0, len(selected))
			for _, h := range selected {
				groups := h.Host.Groups
				if groups == nil {
					groups = []string{}
				}
				rows = append(rows, fleetHostRow{h.Name, groups, h.Host.SSH, h.Sudo, h.Host.Config})
				table = append(table, []string{h.Name, strings.Join(groups, ","), h.Host.SSH, h.Sudo, h.Host.Config})
			}
			format, err := outputFormat(c)
			if err != nil {
				return err
			}
			if format == "json" {
				return writeJSON(c.OutOrStdout(), rows)
			}
			return ui.WriteTable(c.OutOrStdout(), []string{"host", "groups", "ssh", "sudo", "config"}, table, -1)
		}}
	fleetCommonFlags(c)
	addOutputFlag(c)
	return c
}
