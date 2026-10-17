package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/fleet"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"
)

func newFleetCmd() *cobra.Command {
	c := &cobra.Command{Use: "fleet", Short: "Manage and monitor an SSH host fleet", Args: cobra.NoArgs}
	for _, op := range []string{"status", "check", "doctor"} {
		c.AddCommand(newFleetReadCmd(op))
	}
	c.AddCommand(newFleetApplyCmd(), newFleetUpdateCmd(), newFleetBootstrapCmd(), newFleetScheduleCmd(), newFleetTargetsCmd())
	return c
}

func fleetCommonFlags(c *cobra.Command) {
	c.Flags().String("inventory", defaultFleetInventory(), "Operator-owned fleet inventory YAML")
	c.Flags().StringSlice("host", nil, "Select host names (comma-separated or repeated)")
	c.Flags().StringSlice("group", nil, "Select groups (union; comma-separated or repeated)")
	c.Flags().Bool("all", false, "Explicitly select every inventory host")
}

func defaultFleetInventory() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "rootfiles", "fleet.yaml")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "rootfiles", "fleet.yaml")
}

func loadFleetSelection(c *cobra.Command, explicit bool) (*fleet.Inventory, []fleet.NamedHost, error) {
	path, _ := c.Flags().GetString("inventory")
	inv, err := fleet.LoadInventory(path)
	if err != nil {
		return nil, nil, err
	}
	hosts, _ := c.Flags().GetStringSlice("host")
	groups, _ := c.Flags().GetStringSlice("group")
	all, _ := c.Flags().GetBool("all")
	if explicit && !all && len(hosts) == 0 && len(groups) == 0 {
		return nil, nil, fmt.Errorf("mutating fleet commands require --host, --group, or --all")
	}
	selected, err := inv.Select(hosts, groups, all)
	return inv, selected, err
}

func newFleetReadCmd(op string) *cobra.Command {
	c := &cobra.Command{Use: op, Short: "Run a read-only fleet " + op + " report", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		inv, hosts, err := loadFleetSelection(c, false)
		if err != nil {
			return err
		}
		parallel, _ := c.Flags().GetInt("parallel")
		if parallel < 0 || parallel > 128 || (c.Flags().Lookup("parallel").Changed && parallel == 0) {
			return fmt.Errorf("--parallel must be between 1 and 128")
		}
		if !c.Flags().Lookup("parallel").Changed {
			parallel = inv.Defaults.Parallel
		}
		if parallel > len(hosts) {
			parallel = len(hosts)
		}
		runner := fleet.SSHRunner{Binary: "ssh"}
		results := runReadPool(c.Context(), runner, hosts, op, parallel)
		format, _ := c.Flags().GetString("output")
		if err := writeFleetResults(c.OutOrStdout(), results, format); err != nil {
			return err
		}
		if !allOK(results) {
			return &ExitError{Code: 2}
		}
		return nil
	}}
	fleetCommonFlags(c)
	c.Flags().Int("parallel", 0, "Maximum concurrent SSH processes (inventory default when omitted)")
	c.Flags().StringP("output", "o", "text", "Output format: text or json")
	return c
}

func runReadPool(ctx context.Context, r fleet.SSHRunner, hosts []fleet.NamedHost, op string, workers int) []fleet.Result {
	out := make([]fleet.Result, len(hosts))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for n := 0; n < workers; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				out[i] = r.ReadOnly(ctx, hosts[i], op)
			}
		}()
	}
	for i := range hosts {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return out
}

func writeFleetResults(w io.Writer, results []fleet.Result, format string) error {
	if format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"hosts": results})
	}
	if format != "text" {
		return fmt.Errorf("output must be text or json")
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tSTATE\tREASON")
	for _, r := range results {
		reason := r.Reason
		if reason == "" {
			reason = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Host, r.State, strings.ReplaceAll(reason, "\n", " "))
	}
	return tw.Flush()
}
func allOK(rs []fleet.Result) bool {
	for _, r := range rs {
		if r.State != "ok" {
			return false
		}
	}
	return true
}

func authorizeFleetMutation(c *cobra.Command) (*fleet.Inventory, []fleet.NamedHost, bool, error) {
	yes, _ := c.Flags().GetBool("yes")
	if !yes {
		return nil, nil, false, fmt.Errorf("fleet rollout requires --yes")
	}
	inv, hosts, err := loadFleetSelection(c, true)
	if err != nil {
		return nil, nil, false, err
	}
	dry, _ := c.Flags().GetBool("dry-run")
	return inv, hosts, dry, nil
}

func fleetMutationTimeout(c *cobra.Command) (time.Duration, error) {
	timeout, err := c.Flags().GetDuration("timeout")
	if err != nil {
		return 0, fmt.Errorf("reading --timeout: %w", err)
	}
	if err := fleet.ValidateMutationTimeout(timeout); err != nil {
		return 0, fmt.Errorf("invalid --timeout: %w", err)
	}
	return timeout, nil
}

func addFleetMutationTimeoutFlag(c *cobra.Command) {
	c.Flags().Duration("timeout", fleet.DefaultMutationTimeout, "Maximum time to wait for each mutating SSH command (positive, up to 24h)")
}

func addFleetSkipUnreachableFlag(c *cobra.Command) {
	c.Flags().Bool("skip-unreachable", false, "Continue past hosts whose SSH connection fails (any other failure still stops the run)")
}

func fleetMutationRunner(c *cobra.Command, timeout time.Duration) fleet.SSHRunner {
	skip, _ := c.Flags().GetBool("skip-unreachable")
	return fleet.SSHRunner{MutationTimeout: timeout, SkipUnreachable: skip}
}

// fleetRerunCommand rebuilds the invocation for the given hosts, keeping
// every flag the operator set except the host selection.
func fleetRerunCommand(c *cobra.Command, args, hosts []string) string {
	parts := append([]string{c.CommandPath()}, args...)
	c.Flags().Visit(func(f *pflag.Flag) {
		switch f.Name {
		case "host", "group", "all", "skip-unreachable":
			return
		}
		if f.Value.Type() == "bool" {
			if f.Value.String() == "true" {
				parts = append(parts, "--"+f.Name)
			}
			return
		}
		parts = append(parts, "--"+f.Name, shellQuoteCLI(f.Value.String()))
	})
	for _, h := range hosts {
		parts = append(parts, "--host", h)
	}
	return strings.Join(parts, " ")
}

func finishFleetMutation(c *cobra.Command, op string, args []string, results []fleet.Result, dry bool) error {
	format, _ := c.Flags().GetString("output")
	if err := writeFleetResults(c.OutOrStdout(), results, format); err != nil {
		return err
	}
	if skip, _ := c.Flags().GetBool("skip-unreachable"); skip {
		op += " --skip-unreachable"
	}
	if err := fleet.AppendRunLog("", op, results); err != nil {
		return fmt.Errorf("writing fleet run log: %w", err)
	}
	var skipped, pending []string
	for _, r := range results {
		switch r.State {
		case "skipped-unreachable":
			skipped = append(skipped, r.Host)
		case "apply-pending":
			pending = append(pending, r.Host)
		}
	}
	if len(skipped) > 0 {
		fmt.Fprintf(c.ErrOrStderr(), "%d unreachable host(s) skipped; rerun them with:\n  %s\n", len(skipped), fleetRerunCommand(c, args, skipped))
	}
	if len(pending) > 0 {
		fmt.Fprintf(c.ErrOrStderr(), "%d host(s) updated but still need apply; review with 'rootfiles fleet check', then run 'rootfiles fleet apply' for: %s\n", len(pending), strings.Join(pending, ", "))
	}
	if !allOK(results) {
		return &ExitError{Code: 2}
	}
	return nil
}

func newFleetApplyCmd() *cobra.Command {
	c := &cobra.Command{Use: "apply", Short: "Apply selected hosts' site configs serially", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		timeout, err := fleetMutationTimeout(c)
		if err != nil {
			return err
		}
		_, hs, dry, err := authorizeFleetMutation(c)
		if err != nil {
			return err
		}
		rs := fleet.Apply(c.Context(), fleetMutationRunner(c, timeout), hs, dry)
		return finishFleetMutation(c, "apply", nil, rs, dry)
	}}
	fleetCommonFlags(c)
	addFleetMutationTimeoutFlag(c)
	addFleetSkipUnreachableFlag(c)
	c.Flags().StringP("output", "o", "text", "Output format: text or json")
	return c
}
func newFleetUpdateCmd() *cobra.Command {
	c := &cobra.Command{Use: "update", Short: "Update selected hosts serially", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		timeout, err := fleetMutationTimeout(c)
		if err != nil {
			return err
		}
		_, hs, dry, err := authorizeFleetMutation(c)
		if err != nil {
			return err
		}
		v, _ := c.Flags().GetString("version")
		rs, err := fleet.Update(c.Context(), fleetMutationRunner(c, timeout), hs, v, dry)
		if err != nil {
			return err
		}
		return finishFleetMutation(c, "update", nil, rs, dry)
	}}
	fleetCommonFlags(c)
	addFleetMutationTimeoutFlag(c)
	addFleetSkipUnreachableFlag(c)
	c.Flags().String("version", "", "Pinned release version (vX.Y.Z)")
	c.Flags().StringP("output", "o", "text", "Output format: text or json")
	return c
}
func newFleetBootstrapCmd() *cobra.Command {
	c := &cobra.Command{Use: "bootstrap", Short: "Install a pinned, checksum-verified release on selected hosts", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		timeout, err := fleetMutationTimeout(c)
		if err != nil {
			return err
		}
		_, hs, dry, err := authorizeFleetMutation(c)
		if err != nil {
			return err
		}
		v, _ := c.Flags().GetString("version")
		rs, err := fleet.Bootstrap(c.Context(), fleetMutationRunner(c, timeout), hs, v, dry)
		if err != nil {
			return err
		}
		return finishFleetMutation(c, "bootstrap", nil, rs, dry)
	}}
	fleetCommonFlags(c)
	addFleetMutationTimeoutFlag(c)
	addFleetSkipUnreachableFlag(c)
	c.Flags().String("version", "", "Pinned release version (vX.Y.Z)")
	c.Flags().StringP("output", "o", "text", "Output format: text or json")
	return c
}
func newFleetScheduleCmd() *cobra.Command {
	c := &cobra.Command{Use: "schedule <enable|disable>", Short: "Enable or disable scheduled reports serially", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		timeout, err := fleetMutationTimeout(c)
		if err != nil {
			return err
		}
		_, hs, dry, err := authorizeFleetMutation(c)
		if err != nil {
			return err
		}
		rs, err := fleet.Schedule(c.Context(), fleetMutationRunner(c, timeout), hs, args[0], dry)
		if err != nil {
			return err
		}
		return finishFleetMutation(c, "schedule "+args[0], args, rs, dry)
	}}
	fleetCommonFlags(c)
	addFleetMutationTimeoutFlag(c)
	addFleetSkipUnreachableFlag(c)
	c.Flags().StringP("output", "o", "text", "Output format: text or json")
	return c
}

func newFleetTargetsCmd() *cobra.Command {
	c := &cobra.Command{Use: "targets", Short: "Render file_sd scrape targets or push them to a monitoring hub", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		inv, err := loadFleetOnly(c)
		if err != nil {
			return err
		}
		data, err := fleet.RenderTargets(inv)
		if err != nil {
			return err
		}
		hub, _ := c.Flags().GetString("push")
		if hub == "" {
			_, err = c.OutOrStdout().Write(append(data, '\n'))
			return err
		}
		timeout, err := fleetMutationTimeout(c)
		if err != nil {
			return err
		}
		yes, _ := c.Flags().GetBool("yes")
		if !yes {
			return fmt.Errorf("pushing fleet targets requires --yes")
		}
		dry, _ := c.Flags().GetBool("dry-run")
		h, ok := inv.Hosts[hub]
		if !ok {
			return fmt.Errorf("unknown hub host %q", hub)
		}
		if h.EffectiveConfig == nil {
			return fmt.Errorf("hub %q has no site config", hub)
		}
		var cfg config.Config
		if err := yaml.Unmarshal(h.EffectiveConfig, &cfg); err != nil {
			return fmt.Errorf("hub %q has no valid site config: %w", hub, err)
		}
		if !cfg.Modules.Monitoring.Hub.Enabled {
			return fmt.Errorf("hub %q does not enable modules.monitoring.hub", hub)
		}
		path, err := validateFleetDiscoveryTarget(cfg)
		if err != nil {
			return fmt.Errorf("hub %q targets_file: %w", hub, err)
		}
		picked, err := inv.Select([]string{hub}, nil, false)
		if err != nil {
			return err
		}
		nh := picked[0]
		if dry {
			res := fleet.Result{Host: hub, Command: "targets", State: "ok", Reason: "dry-run: would atomically update " + path}
			if err := writeFleetResults(c.OutOrStdout(), []fleet.Result{res}, "text"); err != nil {
				return err
			}
			if err := fleet.AppendRunLog("", "targets --push "+hub, []fleet.Result{res}); err != nil {
				return fmt.Errorf("writing fleet run log: %w", err)
			}
			return nil
		}
		script := fleetTargetsPushScript(path)
		remote := "/bin/sh -c " + shellQuoteCLI(script)
		if nh.Sudo == "nopasswd" {
			remote = "sudo -n " + remote
		}
		res := fleet.SSHRunner{MutationTimeout: timeout}.RunMutationRemote(c.Context(), nh, "targets", remote, data)
		if err := writeFleetResults(c.OutOrStdout(), []fleet.Result{res}, "text"); err != nil {
			return err
		}
		if err := fleet.AppendRunLog("", "targets --push "+hub, []fleet.Result{res}); err != nil {
			return fmt.Errorf("writing fleet run log: %w", err)
		}
		if res.State != "ok" {
			return &ExitError{Code: 2}
		}
		return nil
	}}
	fleetCommonFlags(c)
	c.Flags().String("push", "", "Push targets to this inventory hub host")
	addFleetMutationTimeoutFlag(c)
	return c
}

func validateFleetDiscoveryTarget(cfg config.Config) (string, error) {
	h := cfg.Modules.Monitoring.Hub.WithDefaults()
	target := h.TargetsFile
	if err := config.ValidateMonitoringDiscoveryPath(target, h.DataDir, config.MonitoringDiscoveryDir,
		h.AlertReceiverFile, h.TelegramBotTokenFile, h.GrafanaAdminPasswordFile, cfg.Modules.Cloudflared.TunnelTokenFile); err != nil {
		return "", err
	}
	return target, nil
}

func fleetTargetsPushScript(target string) string {
	dirs := []string{"/"}
	parts := strings.Split(strings.Trim(filepath.Clean(filepath.Dir(target)), string(filepath.Separator)), string(filepath.Separator))
	current := "/"
	for _, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		dirs = append(dirs, current)
	}
	quotedDirs := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		quotedDirs = append(quotedDirs, shellQuoteCLI(dir))
	}
	return "set -eu\ntarget=" + shellQuoteCLI(target) + "\n" +
		"check_dir() { [ -d \"$1\" ] && [ ! -L \"$1\" ] || return 1; [ \"$(stat -c %u:%g \"$1\")\" = 0:0 ] || return 1; mode=$(stat -c %a \"$1\"); [ \"$((0$mode & 022))\" -eq 0 ]; }\n" +
		"for dir in " + strings.Join(quotedDirs, " ") + "; do\n" +
		"  if [ -L \"$dir\" ]; then echo 'refusing symlink discovery parent' >&2; exit 1; fi\n" +
		"  if [ -e \"$dir\" ]; then check_dir \"$dir\" || { echo 'untrusted discovery parent' >&2; exit 1; }; fi\n" +
		"done\n" +
		"if [ -L \"$target\" ]; then echo 'refusing symlink discovery target' >&2; exit 1; fi\n" +
		"if [ -e \"$target\" ]; then [ -f \"$target\" ] && [ \"$(stat -c %u:%g \"$target\")\" = 0:0 ] || { echo 'untrusted discovery target' >&2; exit 1; }; mode=$(stat -c %a \"$target\"); [ \"$((0$mode & 022))\" -eq 0 ] || { echo 'writable discovery target' >&2; exit 1; }; fi\n" +
		"for dir in " + strings.Join(quotedDirs, " ") + "; do\n" +
		"  if [ ! -e \"$dir\" ]; then parent=$(dirname \"$dir\"); check_dir \"$parent\" || { echo 'untrusted discovery parent' >&2; exit 1; }; install -d -o root -g root -m 0755 \"$dir\"; fi\n" +
		"  check_dir \"$dir\" || { echo 'untrusted discovery directory' >&2; exit 1; }\n" +
		"done\n" +
		"umask 077\ntmp=$(mktemp \"$(dirname \"$target\")/.targets.XXXXXX\")\ntrap 'rm -f \"$tmp\"' EXIT\ncat > \"$tmp\"\nchown root:root \"$tmp\"\nchmod 0644 \"$tmp\"\n" +
		"if [ -L \"$target\" ]; then echo 'refusing symlink discovery target' >&2; exit 1; fi\n" +
		"if [ -e \"$target\" ]; then [ -f \"$target\" ] && [ \"$(stat -c %u:%g \"$target\")\" = 0:0 ] || { echo 'untrusted discovery target' >&2; exit 1; }; mode=$(stat -c %a \"$target\"); [ \"$((0$mode & 022))\" -eq 0 ] || { echo 'writable discovery target' >&2; exit 1; }; fi\n" +
		"mv -f -- \"$tmp\" \"$target\""
}

func loadFleetOnly(c *cobra.Command) (*fleet.Inventory, error) {
	p, _ := c.Flags().GetString("inventory")
	return fleet.LoadInventory(p)
}
func shellQuoteCLI(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
