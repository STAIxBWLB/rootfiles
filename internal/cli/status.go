package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/module"
	"github.com/STAIxBWLB/rootfiles/internal/state"
	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

func newStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show full system status at a glance",
		Long: "Read-only overview of the system, profile, modules, GPU allocations, tunnel and users.\n" +
			"Exit code: 0 for the overview, including reported findings; invalid inputs return an error.",
		RunE: runStatus,
	}
	cmd.Flags().StringSlice("module", nil, "Report specific modules only")
	addOutputFlag(cmd)
	return usesConfigTarget(cmd)
}

func runStatus(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	format, err := outputFormat(cmd)
	if err != nil {
		return err
	}
	moduleFilter, _ := cmd.Flags().GetStringSlice("module")
	profileName, _ := cmd.Flags().GetString("profile")
	if profileName == "" {
		profileName = os.Getenv("ROOTFILES_PROFILE")
	}

	sysInfo, _ := detectSystem()
	if sysInfo == nil {
		sysInfo = &config.SystemInfo{}
	}

	last, lastErr := state.Last()
	active, configPath, applied := resolveTarget(cmd, sysInfo)
	// Reports name the recorded target when the kept applied copy was
	// resolved, so status JSON keeps config_path as the recorded path.
	reportProfile, reportPath := reportedTarget(active, configPath, applied)

	cfg, cfgErr := config.LoadWithHomeBase(active, configPath, sysInfo, homeBaseFlag(cmd))
	if cfgErr != nil {
		cfg = config.Fallback(sysInfo, homeBaseFlag(cmd))
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	rc := readOnlyRunContext(cfg, sysInfo, logger)
	rc.Out = cmd.OutOrStdout()

	if rc.Config.Users.HomeBase != "" {
		warnStatusMetadata(rc) // on stderr, so JSON stays clean
	}
	report := collectStatus(ctx, rc, sysInfo, reportProfile, reportPath, cfgErr, last, moduleFilter...)
	if format == "json" {
		return writeJSON(out, report)
	}
	renderStatusText(out, report, profileName, lastErr)
	return nil
}

// renderStatusText only formats the collected snapshot; it performs no queries.
func renderStatusText(out io.Writer, report statusReport, profileFlag string, lastErr error) {
	ui.WriteHeader(out, "rootfiles status")
	renderSystemSection(out, report.System, report.Hostname)
	var cfgErr error
	if report.ConfigErr != "" {
		cfgErr = errors.New(report.ConfigErr)
	}
	renderProfileSection(out, report.Profile, profileFlag, report.System, report.ConfigPath, cfgErr)
	renderLastApplySection(out, report.LastApply, lastErr)
	renderModulesSection(out, report)
	if report.details.gpuSelected {
		renderGPUSection(out, report)
	}
	if report.details.tunnelSelected {
		renderTunnelSection(out, report)
	}
	if report.details.usersSelected {
		renderUsersSection(out, report)
	}
}

func renderSystemSection(out io.Writer, sys *config.SystemInfo, hostname string) {
	ui.WriteSection(out, "System")

	osLine := ui.FirstNonEmpty(sys.OS, "unknown")
	if sys.Version != "" {
		osLine = fmt.Sprintf("%s %s", osLine, sys.Version)
	}
	if sys.Codename != "" {
		osLine = fmt.Sprintf("%s (%s)", osLine, sys.Codename)
	}
	ui.WriteKV(out, "OS", osLine)

	if hostname != "" {
		ui.WriteKV(out, "Hostname", hostname)
	}
	ui.WriteKV(out, "Architecture", ui.FirstNonEmpty(sys.Arch, "unknown"))
	if sys.MemoryGB > 0 {
		ui.WriteKV(out, "Memory", fmt.Sprintf("%d GiB", sys.MemoryGB))
	}
	if sys.CPUCores > 0 {
		ui.WriteKV(out, "CPU Cores", fmt.Sprintf("%d", sys.CPUCores))
	}

	switch {
	case sys.IsDGX:
		ui.WriteKV(out, "GPU", fmt.Sprintf("DGX %s × %s", countStr(sys.GPUCount), ui.FirstNonEmpty(sys.GPUModel, "NVIDIA")))
	case sys.HasNVIDIAGPU:
		ui.WriteKV(out, "GPU", fmt.Sprintf("%s × %s", countStr(sys.GPUCount), ui.FirstNonEmpty(sys.GPUModel, "NVIDIA")))
	default:
		ui.WriteKV(out, "GPU", "none detected")
	}

	if paths := uniqueMountRoots(sys.StorageLayout); len(paths) > 0 {
		ui.WriteKV(out, "Storage", strings.Join(paths, ", "))
	}
}

func renderProfileSection(out io.Writer, active, flagProfile string, sys *config.SystemInfo, configPath string, loadErr error) {
	ui.WriteSection(out, "Profile")

	ui.WriteKV(out, "Active", ui.FirstNonEmpty(active, "(none)"))
	ui.WriteKV(out, "Suggested", ui.FirstNonEmpty(sys.SuggestProfile(), "minimal"))
	switch {
	case configPath != "":
		ui.WriteKV(out, "Config", configPath)
	case flagProfile != "":
		ui.WriteKV(out, "Config", "(embedded profile)")
	default:
		ui.WriteKV(out, "Config", "(auto)")
	}
	if loadErr != nil {
		ui.WriteHint(out, fmt.Sprintf("%s config load failed: %v", ui.WarnMark(), loadErr))
	}
}

// statusDetails preserves text-only display state without extending the JSON schema.
type statusDetails struct {
	homeBase                                   string
	capabilityBlocked                          bool
	moduleSatisfied                            map[string]bool
	gpuSelected, tunnelSelected, usersSelected bool
	gpu                                        *module.GPUAllocationsDB
	tunnelBinary, tunnelInterface              string
	systemUsersKnown                           bool
}

type statusReport struct {
	details    statusDetails
	Version    string             `json:"version,omitempty"`
	System     *config.SystemInfo `json:"system"`
	Hostname   string             `json:"hostname"`
	Profile    string             `json:"profile,omitempty"`
	ConfigPath string             `json:"config_path,omitempty"`
	ConfigErr  string             `json:"config_error,omitempty"`
	// HomeBaseAmbiguous marks a config error that only an explicit
	// home_base settles; fleet rollouts carry one in every site config.
	HomeBaseAmbiguous   bool                     `json:"home_base_ambiguous,omitempty"`
	ModuleCheckError    string                   `json:"module_check_error,omitempty"`
	AppliedConfigSHA256 string                   `json:"applied_config_sha256,omitempty"`
	LastApply           *state.Run               `json:"last_apply"`
	Modules             []checkModule            `json:"modules"`
	GPU                 *module.GPUAllocationsDB `json:"gpu,omitempty"`
	Tunnel              statusTunnel             `json:"tunnel"`
	Users               statusUsers              `json:"users"`
}

type statusTunnel struct {
	Binary  string `json:"binary,omitempty"`
	Service string `json:"service"`
	VLAN    string `json:"vlan,omitempty"`
}

type statusUsers struct {
	HomeBase string `json:"home_base"`
	Managed  int    `json:"managed"`
	System   int    `json:"system"`
}

func collectStatus(ctx context.Context, rc *module.RunContext, sys *config.SystemInfo, profile, configPath string, cfgErr error, last *state.Run, filter ...string) statusReport {
	r := statusReport{System: sys, Profile: profile, ConfigPath: configPath, LastApply: last, Modules: []checkModule{}}
	r.details = statusDetails{
		homeBase:        rc.Config.Users.HomeBase,
		gpuSelected:     statusModuleSelected(filter, "gpu"),
		tunnelSelected:  statusModuleSelected(filter, "cloudflared"),
		usersSelected:   statusModuleSelected(filter, "users"),
		moduleSatisfied: make(map[string]bool),
	}
	r.Version = buildVersion
	r.Hostname, _ = os.Hostname()
	if cfgErr != nil {
		r.ConfigErr = cfgErr.Error()
		r.HomeBaseAmbiguous = errors.Is(cfgErr, config.ErrAmbiguousHomeBase)
	} else if fingerprint, err := appliedFingerprint(rc.Config, profile, configPath); err == nil {
		r.AppliedConfigSHA256 = fingerprint
	}

	modules := module.NewRegistry().Resolve(rc.Config, filter)
	if err := validateModules(rc.Config, sys, modules); err != nil {
		r.details.capabilityBlocked = true
		r.ModuleCheckError = err.Error()
	} else if results, err := module.CheckAll(ctx, modules, rc); err == nil {
		for _, m := range modules {
			cm := checkModule{Name: m.Name(), Satisfied: true, Changes: []module.Change{}}
			if res := results[m.Name()]; res != nil {
				r.details.moduleSatisfied[m.Name()] = res.Satisfied
				cm.Satisfied = res.Satisfied
				if res.Changes != nil {
					cm.Changes = res.Changes
				}
			}
			r.Modules = append(r.Modules, cm)
		}
	} else {
		r.ModuleCheckError = err.Error()
	}

	// An empty home base means the config did not load and none was given:
	// skip the databases instead of reading them under /home.
	homeBase := rc.Config.Users.HomeBase
	if homeBase != "" && r.details.gpuSelected {
		db, err := module.LoadGPUDB(rc)
		r.details.gpu = db // Text historically displayed partial decoded metadata on error.
		if err == nil && db != nil && (db.TotalGPUs > 0 || len(db.Allocations) > 0) {
			r.GPU = db
		}
	}
	if r.details.tunnelSelected {
		observation := module.InspectCloudflared(ctx, rc.Runner, cloudflaredStatusBinary)
		r.details.tunnelBinary = "(not installed)"
		if observation.Installed {
			r.details.tunnelBinary = observation.Path
			if observation.QueryError == nil {
				r.Tunnel.Binary = strings.TrimSpace(observation.RawOutput)
				r.details.tunnelBinary = r.Tunnel.Binary
			}
		}
		r.Tunnel.Service = systemctlStatus(ctx, "cloudflared")
		iface := rc.Config.Modules.Cloudflared.PrivateNetwork.Interface
		if iface == "" {
			iface = "vlan0"
		}
		r.details.tunnelInterface = iface
		r.Tunnel.VLAN = interfaceAddress(iface)
	}

	if statusModuleSelected(filter, "users") {
		r.Users.HomeBase = homeBase
		if homeBase != "" {
			if db, _ := module.LoadUsersDB(rc); db != nil {
				r.Users.Managed = len(db.Users)
			}
		}
		if sysUsers, err := scanStatusUsers(ctx, rc); err == nil {
			r.Users.System = len(sysUsers)
			r.details.systemUsersKnown = true
		}
	}
	return r
}

func statusModuleSelected(filter []string, name string) bool {
	return len(filter) == 0 || slices.Contains(filter, name)
}

func renderLastApplySection(out io.Writer, last *state.Run, err error) {
	ui.WriteSection(out, "Last apply")
	switch {
	case err != nil:
		ui.WriteHint(out, fmt.Sprintf("%s %v", ui.WarnMark(), err))
		return
	case last == nil:
		ui.WriteHint(out, "no apply recorded on this host")
		return
	}
	result := ui.StyleSuccess.Render("success")
	if !last.Success {
		result = ui.StyleError.Render("failed")
	}
	ui.WriteKV(out, "When", last.FinishedAt.Local().Format("2006-01-02 15:04:05"))
	ui.WriteKV(out, "Result", result)
	ui.WriteKV(out, "Version", ui.FirstNonEmpty(last.Version, "unknown"))
	for _, m := range last.Modules {
		if m.Status == "failed" {
			ui.WriteKV(out, "Failed", m.Name+": "+m.Error)
		}
	}
	if last.BackupID != "" {
		ui.WriteKV(out, "Backup", last.BackupID+" (rootfiles rollback "+last.BackupID+")")
	}
}

func renderModulesSection(out io.Writer, report statusReport) {
	if report.ModuleCheckError != "" {
		ui.WriteSection(out, "Modules (check failed)")
		ui.WriteHint(out, fmt.Sprintf("%s %s", ui.WarnMark(), report.ModuleCheckError))
		if report.details.capabilityBlocked {
			ui.WriteHint(out, "Use a supported distribution or disable unsupported modules/options in the selected config.")
		}
		return
	}
	if len(report.Modules) == 0 {
		ui.WriteSection(out, "Modules (none enabled)")
		ui.WriteHint(out, "no profile selected — select one with --profile")
		return
	}
	satisfied := 0
	for _, m := range report.Modules {
		if report.details.moduleSatisfied[m.Name] {
			satisfied++
		}
	}
	ui.WriteSection(out, fmt.Sprintf("Modules (%d/%d satisfied)", satisfied, len(report.Modules)))
	for _, m := range report.Modules {
		marker := ui.OKMark()
		if !report.details.moduleSatisfied[m.Name] {
			marker = ui.PendingMark()
		}
		ui.WriteBullet(out, marker, m.Name)
	}
	pending := len(report.Modules) - satisfied
	if pending > 0 {
		fmt.Fprintln(out)
		ui.WriteHint(out, fmt.Sprintf("%d module(s) need attention — run 'rootfiles check' for details.", pending))
	}
}

func renderGPUSection(out io.Writer, report statusReport) {
	ui.WriteSection(out, "GPU Allocations")
	if report.details.homeBase == "" {
		ui.WriteHint(out, unknownHomeBaseHint)
		return
	}
	db := report.details.gpu

	if db == nil || (db.TotalGPUs == 0 && len(db.Allocations) == 0) {
		ui.WriteKV(out, "Total GPUs", "none recorded")
		ui.WriteHint(out, "run 'rootfiles gpu assign <user> --gpus 0,1' to register an allocation")
		return
	}

	totalLabel := fmt.Sprintf("%d", db.TotalGPUs)
	if db.GPUModel != "" {
		totalLabel = fmt.Sprintf("%d × %s", db.TotalGPUs, db.GPUModel)
	}
	ui.WriteKV(out, "Total GPUs", totalLabel)

	assignedGPUs := 0
	for _, a := range db.Allocations {
		assignedGPUs += len(a.GPUs)
	}
	if len(db.Allocations) == 0 {
		ui.WriteKV(out, "Assigned", "none")
		return
	}
	ui.WriteKV(out, "Assigned", fmt.Sprintf("%d GPU(s) across %d user(s)", assignedGPUs, len(db.Allocations)))

	// Sort allocations by username for deterministic output.
	allocs := append([]module.GPUAllocation(nil), db.Allocations...)
	sort.Slice(allocs, func(i, j int) bool { return allocs[i].Username < allocs[j].Username })
	for _, a := range allocs {
		ids := make([]string, 0, len(a.GPUs))
		for _, g := range a.GPUs {
			ids = append(ids, fmt.Sprintf("%d", g))
		}
		ui.WriteBullet(out, ui.StyleValue.Render(a.Username), strings.Join(ids, ","))
	}
}

// scanStatusUsers keeps system account discovery replaceable in report fixtures.
var scanStatusUsers = module.ScanSystemUsersExported
var warnStatusMetadata = module.WarnUntrustedMetadata

// Empty overrides use the module-owned production binary path.
var cloudflaredStatusBinary string

func renderTunnelSection(out io.Writer, report statusReport) {
	ui.WriteSection(out, "Tunnel")
	ui.WriteKV(out, "cloudflared", report.details.tunnelBinary)
	switch status := report.Tunnel.Service; status {
	case "active":
		ui.WriteKV(out, "Service", ui.StyleSuccess.Render("active"))
	case "unknown":
		ui.WriteKV(out, "Service", ui.StyleHint.Render("unknown"))
	default:
		ui.WriteKV(out, "Service", ui.StyleHint.Render(status))
	}
	iface := report.details.tunnelInterface
	if addr := report.Tunnel.VLAN; addr != "" {
		ui.WriteKV(out, "VLAN", fmt.Sprintf("%s on %s", addr, iface))
	} else {
		ui.WriteKV(out, "VLAN", fmt.Sprintf("%s not configured", iface))
	}
}

func renderUsersSection(out io.Writer, report statusReport) {
	ui.WriteSection(out, "Users")
	if report.details.homeBase == "" {
		ui.WriteKV(out, "Home base", ui.StyleHint.Render("unknown (see GPU Allocations)"))
	} else {
		ui.WriteKV(out, "Home base", report.details.homeBase)
		ui.WriteKV(out, "Managed", fmt.Sprintf("%d user(s)", report.Users.Managed))
	}
	if report.details.systemUsersKnown {
		ui.WriteKV(out, "System", fmt.Sprintf("%d user(s) (UID 1000-65533)", report.Users.System))
	}
}

// unknownHomeBaseHint explains skipped user and GPU data after a config
// load error.
const unknownHomeBaseHint = "config did not load, so the home base is unknown; set --home-base or fix the config to see user and GPU data"

// --- helpers ---

// systemctlStatus returns the systemd unit state (active/inactive/failed/
// unknown) without erroring when systemctl is absent.
func systemctlStatus(ctx context.Context, unit string) string {
	cmd := exec.CommandContext(ctx, "systemctl", "is-active", unit)
	out, err := cmd.Output()
	state := strings.TrimSpace(string(out))
	if state == "" && err != nil {
		return "unknown"
	}
	return state
}

// interfaceAddress returns the first IPv4 inet address on iface or "" if the
// interface does not exist / has no address / `ip` binary is missing.
func interfaceAddress(iface string) string {
	out, err := exec.Command("ip", "-brief", "addr", "show", iface).Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	for _, f := range fields {
		if strings.Contains(f, "/") && strings.Count(f, ".") == 3 {
			return f
		}
	}
	return ""
}

// uniqueMountRoots deduplicates mount paths to their two-level prefix so
// Docker overlay mounts (/raid/docker/overlay2/<hash>/merged) collapse into
// a single "/raid/docker" entry instead of flooding the status output.
func uniqueMountRoots(mounts []config.MountPoint) []string {
	seen := make(map[string]bool, len(mounts))
	var out []string
	for _, m := range mounts {
		root := mountRoot(m.MountPath)
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		out = append(out, root)
	}
	sort.Strings(out)
	return out
}

func mountRoot(p string) string {
	parts := strings.SplitN(strings.TrimPrefix(p, "/"), "/", 3)
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return "/" + parts[0]
	default:
		return "/" + parts[0] + "/" + parts[1]
	}
}

func countStr(n int) string {
	if n <= 0 {
		return "1"
	}
	return fmt.Sprintf("%d", n)
}
