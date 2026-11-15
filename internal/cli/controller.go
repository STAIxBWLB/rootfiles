package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/STAIxBWLB/rootfiles/internal/fleet"
	"github.com/STAIxBWLB/rootfiles/internal/module"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
	"github.com/spf13/cobra"
)

type controllerRun struct {
	Host      string    `json:"host"`
	Time      time.Time `json:"time"`
	Operation string    `json:"operation"`
	State     string    `json:"state"`
}
type controllerSnapshot struct {
	Mode           string          `json:"mode"`
	Version        string          `json:"version"`
	Platform       string          `json:"platform"`
	Inventory      string          `json:"inventory"`
	Hosts          int             `json:"hosts"`
	Groups         int             `json:"groups"`
	Runs           []controllerRun `json:"runs"`
	InventoryError string          `json:"inventory_error,omitempty"`
	LogError       string          `json:"log_error,omitempty"`
}

func controllerPaths(cmd *cobra.Command) (string, string) {
	inventory, _ := cmd.Flags().GetString("inventory")
	if inventory == "" {
		inventory = defaultFleetInventory()
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, _ := os.UserHomeDir()
		state = filepath.Join(home, ".local", "state")
	}
	return inventory, filepath.Join(state, "rootfiles", "fleet.jsonl")
}

func controllerInventory(path string) (*fleet.Inventory, int, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return nil, 0, fmt.Errorf("inventory is not owned by the current user")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return nil, 0, fmt.Errorf("inventory is not a regular file or is writable by group or others")
	}
	inv, err := fleet.LoadInventory(path)
	if err != nil {
		return nil, 0, err
	}
	groups := map[string]bool{}
	for _, host := range inv.Hosts {
		for _, group := range host.Groups {
			groups[group] = true
		}
	}
	return inv, len(groups), nil
}

func controllerHistory(path string) ([]controllerRun, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return []controllerRun{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	latest := map[string]controllerRun{}
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 4096), 4*1024*1024)
	for scan.Scan() {
		var entry fleet.RunRecord
		if err := json.Unmarshal(scan.Bytes(), &entry); err != nil {
			return nil, fmt.Errorf("invalid run log record: %w", err)
		}
		for _, host := range entry.Hosts {
			prev, ok := latest[host.Host]
			if !ok || entry.Time.After(prev.Time) {
				latest[host.Host] = controllerRun{host.Host, entry.Time, entry.Command, host.State}
			}
		}
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	rows := make([]controllerRun, 0, len(latest))
	for _, row := range latest {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Host < rows[j].Host })
	return rows, nil
}

func controllerState(cmd *cobra.Command) (controllerSnapshot, *fleet.Inventory) {
	inventory, log := controllerPaths(cmd)
	s := controllerSnapshot{Mode: "controller", Version: buildVersion, Platform: nativeHostOS, Inventory: inventory, Runs: []controllerRun{}}
	inv, groups, err := controllerInventory(inventory)
	if err != nil {
		s.InventoryError = err.Error()
	} else {
		s.Hosts = len(inv.Hosts)
		s.Groups = groups
	}
	runs, err := controllerHistory(log)
	if err != nil {
		s.LogError = err.Error()
	} else {
		s.Runs = runs
	}
	return s, inv
}

func runControllerStatus(cmd *cobra.Command) error {
	format, err := outputFormat(cmd)
	if err != nil {
		return err
	}
	s, _ := controllerState(cmd)
	if format == "json" {
		return writeJSON(cmd.OutOrStdout(), s)
	}
	out := cmd.OutOrStdout()
	ui.WriteHeader(out, "rootfiles status")
	fmt.Fprintf(out, "  Mode: controller\n  Version: %s\n  Platform: %s\n  Inventory: %s\n", s.Version, s.Platform, s.Inventory)
	if s.InventoryError != "" {
		fmt.Fprintf(out, "  Inventory error: %s\n", s.InventoryError)
	} else {
		fmt.Fprintf(out, "  Hosts: %d  Groups: %d\n", s.Hosts, s.Groups)
	}
	if s.LogError != "" {
		fmt.Fprintf(out, "  Run log error: %s\n", s.LogError)
	}
	rows := [][]string{}
	for _, run := range s.Runs {
		rows = append(rows, []string{run.Host, run.Time.Format(time.RFC3339), run.Operation, run.State})
	}
	return ui.WriteTable(out, []string{"host", "time", "operation", "state"}, rows, -1)
}

func controllerSSH(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "ssh", args...).CombinedOutput()
}
func controllerIdentity(ctx context.Context, inv *fleet.Inventory) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "ssh-add", "-l").Output(); err == nil && len(strings.TrimSpace(string(out))) > 0 {
		return true
	}
	if inv == nil || len(inv.Hosts) == 0 {
		return false
	}
	names := append([]string(nil), inv.Order...)
	if len(names) == 0 {
		for name := range inv.Hosts {
			names = append(names, name)
		}
		sort.Strings(names)
	}
	out, err := controllerSSH(ctx, "-G", "--", inv.Hosts[names[0]].SSH)
	if err != nil {
		return false
	}
	home, _ := os.UserHomeDir()
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "identityfile ") {
			continue
		}
		path := strings.TrimSpace(strings.TrimPrefix(line, "identityfile "))
		path = strings.ReplaceAll(path, "%d", home)
		if strings.HasPrefix(path, "~/") {
			path = filepath.Join(home, path[2:])
		}
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0077 == 0 {
			f, err := os.Open(path)
			if err == nil {
				_ = f.Close()
				return true
			}
		}
	}
	return false
}

func writableDirectory(path string) error {
	for {
		info, err := os.Stat(path)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("%s is not a directory", path)
			}
			return syscall.Access(path, 3)
		}
		if !os.IsNotExist(err) {
			return err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return err
		}
		path = parent
	}
}

func runControllerDoctor(cmd *cobra.Command) error {
	format, err := outputFormat(cmd)
	if err != nil {
		return err
	}
	s, inv := controllerState(cmd)
	_, log := controllerPaths(cmd)
	findings := []module.Finding{}
	version, err := controllerSSH(cmd.Context(), "-V")
	if err != nil {
		findings = append(findings, module.Finding{Check: "ssh", Level: module.LevelFail, Detail: "OpenSSH client unavailable", Hint: "install an OpenSSH client"})
	} else {
		findings = append(findings, module.Finding{Check: "ssh", Level: module.LevelOK, Detail: strings.TrimSpace(string(version))})
	}
	if s.InventoryError != "" {
		hint := "inspect the inventory file and its ownership and permissions"
		if _, statErr := os.Stat(s.Inventory); os.IsNotExist(statErr) {
			hint = "rootfiles fleet init"
			if cmd.Flags().Changed("inventory") {
				hint += " --inventory " + fleet.ShellQuote(s.Inventory)
			}
		}
		findings = append(findings, module.Finding{Check: "inventory", Level: module.LevelFail, Detail: s.InventoryError, Hint: hint})
	} else {
		findings = append(findings, module.Finding{Check: "inventory", Level: module.LevelOK, Detail: fmt.Sprintf("%d hosts, %d groups", s.Hosts, s.Groups)})
	}
	if controllerIdentity(cmd.Context(), inv) {
		findings = append(findings, module.Finding{Check: "identity", Level: module.LevelOK, Detail: "SSH identity available"})
	} else {
		findings = append(findings, module.Finding{Check: "identity", Level: module.LevelWarn, Detail: "no usable SSH identity found", Hint: "add a key to the SSH agent or configure an identity file"})
	}
	if err := writableDirectory(filepath.Dir(log)); err != nil {
		findings = append(findings, module.Finding{Check: "run log", Level: module.LevelFail, Detail: err.Error(), Hint: "make the run log directory writable"})
	} else {
		findings = append(findings, module.Finding{Check: "run log", Level: module.LevelOK, Detail: "directory is writable"})
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
	defer cancel()
	latest, err := fetchLatestVersion(ctx)
	if err != nil {
		findings = append(findings, module.Finding{Check: "version", Level: module.LevelSkip, Detail: "latest release unavailable"})
	} else if latest != buildVersion && buildVersion != "dev" {
		findings = append(findings, module.Finding{Check: "version", Level: module.LevelWarn, Detail: fmt.Sprintf("%s installed; %s available", buildVersion, latest), Hint: "rootfiles update --check"})
	} else {
		findings = append(findings, module.Finding{Check: "version", Level: module.LevelOK, Detail: buildVersion})
	}
	strict, _ := cmd.Flags().GetBool("strict")
	failed := 0
	counts := map[string]int{}
	for _, f := range findings {
		counts[f.Level]++
		if f.Level == module.LevelFail || (strict && f.Level == module.LevelWarn) {
			failed++
		}
	}
	out := cmd.OutOrStdout()
	switch format {
	case "json":
		if err := writeJSON(out, map[string]any{"mode": "controller", "version": buildVersion, "findings": findings, "failed": failed}); err != nil {
			return err
		}
	case "prometheus":
		fmt.Fprintln(out, "# HELP rootfiles_doctor_findings Doctor findings by level.\n# TYPE rootfiles_doctor_findings gauge")
		for _, level := range []string{module.LevelOK, module.LevelWarn, module.LevelFail, module.LevelSkip} {
			fmt.Fprintf(out, "rootfiles_doctor_findings{level=%q} %d\n", level, counts[level])
		}
		fmt.Fprintln(out, "# HELP rootfiles_doctor_check_ok 1 when a doctor check is ok or skipped.\n# TYPE rootfiles_doctor_check_ok gauge")
		for _, f := range findings {
			fmt.Fprintf(out, "rootfiles_doctor_check_ok{check=%q} %d\n", f.Check, boolMetric(f.Level == module.LevelOK || f.Level == module.LevelSkip))
		}
	default:
		ui.WriteHeader(out, "rootfiles doctor")
		for _, f := range findings {
			marker := ui.OKMark()
			if f.Level == module.LevelFail {
				marker = ui.StyleError.Render(ui.MarkFail)
			} else if f.Level == module.LevelWarn {
				marker = ui.WarnMark()
			} else if f.Level == module.LevelSkip {
				marker = ui.StyleHint.Render(ui.MarkAbsent)
			}
			ui.WriteBullet(out, marker, fmt.Sprintf("%-16s %s", f.Check, f.Detail))
			if f.Hint != "" {
				fmt.Fprintf(out, "      %s\n", f.Hint)
			}
		}
		ui.WriteLine(out, fmt.Sprintf("%d failed  %d warnings  %d ok  %d skipped", counts[module.LevelFail], counts[module.LevelWarn], counts[module.LevelOK], counts[module.LevelSkip]))
	}
	if failed > 0 {
		return &ExitError{Code: exitDrift}
	}
	return nil
}
