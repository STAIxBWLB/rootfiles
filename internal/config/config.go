package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var validFreeSize = regexp.MustCompile(`^([1-9][0-9]{0,9})([KMGT])(?:iB)?$`)

// Config is the root configuration struct, mapped from profile YAML files.
type Config struct {
	Extends       string        `yaml:"extends,omitempty"`
	Locale        string        `yaml:"locale"`
	Timezone      string        `yaml:"timezone"`
	Modules       ModulesConfig `yaml:"modules"`
	Packages      []string      `yaml:"packages"`
	PackagesExtra []string      `yaml:"packages_extra"`
	Users         UsersConfig   `yaml:"users"`
	SSH           SSHConfig     `yaml:"ssh"`
	// Doctor is omitted when unset so existing config fingerprints stay the same.
	Doctor DoctorConfig `yaml:"doctor,omitempty"`
	// Populated at runtime, not from YAML
	System *SystemInfo `yaml:"-"`
}

type ModulesConfig struct {
	Locale      ModuleToggle      `yaml:"locale"`
	System      SystemConfig      `yaml:"system"`
	Packages    ModuleToggle      `yaml:"packages"`
	SSH         ModuleToggle      `yaml:"ssh"`
	Users       ModuleToggle      `yaml:"users"`
	Security    SecurityConfig    `yaml:"security"`
	Docker      DockerConfig      `yaml:"docker"`
	Nvidia      NvidiaConfig      `yaml:"nvidia"`
	Cloudflared CloudflaredConfig `yaml:"cloudflared"`
	Storage     StorageConfig     `yaml:"storage"`
	Network     NetworkConfig     `yaml:"network"`
	Monitoring  MonitoringConfig  `yaml:"monitoring"`
	// omitempty keeps the effective config, and so its fingerprint,
	// unchanged on hosts that never set it.
	Tenant TenantConfig `yaml:"tenant,omitempty"`
}

// TenantConfig prepares a host for `rootfiles tenant` containers.
type TenantConfig struct {
	Enabled bool `yaml:"enabled"`
	// Root holds each tenant's volume image, mount point and system files.
	Root string `yaml:"root,omitempty"`
	// OpsGroup members may run `sudo rootfiles tenant ops` for the tenants
	// that list them as operators.
	OpsGroup string `yaml:"ops_group,omitempty"`
	// SSHPiper routes `ssh <tenant>.<user>@host -p <port>` to the tenants
	// through one port.
	SSHPiper SSHPiperConfig `yaml:"sshpiper,omitempty"`
}

// SSHPiperConfig runs sshpiperd for single-port tenant SSH.
type SSHPiperConfig struct {
	Enabled bool   `yaml:"enabled"`
	Port    int    `yaml:"port,omitempty"`    // default 2222
	Address string `yaml:"address,omitempty"` // default all addresses
	Version string `yaml:"version,omitempty"` // default DefaultSSHPiperVersion
	// FailtobanIgnore lists addresses or CIDRs never banned after failed
	// logins, such as a VPN gateway that many users share.
	FailtobanIgnore []string `yaml:"failtoban_ignore,omitempty"`
}

// DefaultSSHPiperVersion is the sshpiper release rootfiles installs.
const DefaultSSHPiperVersion = "v1.6.1"

// ListenPort returns the port, 2222 when unset.
func (p SSHPiperConfig) ListenPort() int {
	if p.Port == 0 {
		return 2222
	}
	return p.Port
}

// Release returns the version, DefaultSSHPiperVersion when unset.
func (p SSHPiperConfig) Release() string {
	if p.Version == "" {
		return DefaultSSHPiperVersion
	}
	return p.Version
}

// DataRoot returns the tenant root, /raid/tenants when unset.
func (t TenantConfig) DataRoot() string {
	if t.Root == "" {
		return "/raid/tenants"
	}
	return t.Root
}

// Group returns the operator group, rootfiles-tenant-ops when unset.
func (t TenantConfig) Group() string {
	if t.OpsGroup == "" {
		return "rootfiles-tenant-ops"
	}
	return t.OpsGroup
}

// SystemConfig covers host-level basics.
type SystemConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Hostname string `yaml:"hostname,omitempty"`
	// SwapSize creates /swapfile of this size (e.g. "8G") when the host has
	// no swap at all. Existing swap is never modified.
	SwapSize string `yaml:"swap_size,omitempty"`
	// JournaldMaxUse caps persistent journal size (e.g. "2G").
	JournaldMaxUse string `yaml:"journald_max_use,omitempty"`
	// Sysctl entries are written to /etc/sysctl.d/90-rootfiles.conf.
	Sysctl map[string]string `yaml:"sysctl,omitempty"`
	// AptMirror replaces archive.ubuntu.com (not security.ubuntu.com) in
	// the Ubuntu sources, e.g. http://mirror.kakao.com/ubuntu.
	AptMirror string `yaml:"apt_mirror,omitempty"`
}

// SecurityConfig is the baseline hardening applied beyond sshd.
type SecurityConfig struct {
	Enabled bool `yaml:"enabled"`
	// UnattendedUpgrades installs security updates automatically, never
	// reboots, and never touches the NVIDIA/CUDA stack.
	UnattendedUpgrades bool `yaml:"unattended_upgrades"`
	// Fail2ban enables an sshd jail on the SSH port(s).
	Fail2ban bool `yaml:"fail2ban"`
	// TimeSync ensures NTP synchronisation (chrony if installed, else
	// systemd-timesyncd).
	TimeSync bool `yaml:"time_sync"`
}

// MonitoringConfig installs exporters (opt-in).
type MonitoringConfig struct {
	Enabled bool `yaml:"enabled"`
	// NodeExporter installs prometheus-node-exporter; rootfiles' scheduled
	// report also publishes rootfiles_* metrics through its textfile
	// collector.
	NodeExporter      bool     `yaml:"node_exporter"`
	DCGMExporter      bool     `yaml:"dcgm_exporter"`
	NodeExporterPort  int      `yaml:"node_exporter_port,omitempty"`
	DCGMExporterPort  int      `yaml:"dcgm_exporter_port,omitempty"`
	ListenAddress     string   `yaml:"listen_address,omitempty"`
	AllowFrom         []string `yaml:"allow_from,omitempty"`
	PerimeterFirewall bool     `yaml:"perimeter_firewall"`
	// ExporterFirewall installs the iptables guard that admits exporter
	// traffic only from allow_from (IPv4, one /32, default ports).
	ExporterFirewall  bool                `yaml:"exporter_firewall,omitempty"`
	DCGMExporterImage string              `yaml:"dcgm_exporter_image,omitempty"`
	Hub               MonitoringHubConfig `yaml:"hub"`
}

// MonitoringHubConfig declares the opt-in central monitoring stack.
type MonitoringHubConfig struct {
	Enabled                  bool   `yaml:"enabled"`
	DataDir                  string `yaml:"data_dir,omitempty"`
	Retention                string `yaml:"retention,omitempty"`
	AlertmanagerRetention    string `yaml:"alertmanager_retention,omitempty"`
	TargetsFile              string `yaml:"targets_file,omitempty"`
	AlertReceiverFile        string `yaml:"alert_receiver_file,omitempty"`
	ListenAddress            string `yaml:"listen_address,omitempty"`
	GrafanaPort              int    `yaml:"grafana_port,omitempty"`
	PrometheusPort           int    `yaml:"prometheus_port,omitempty"`
	AlertmanagerPort         int    `yaml:"alertmanager_port,omitempty"`
	PrometheusImage          string `yaml:"prometheus_image,omitempty"`
	AlertmanagerImage        string `yaml:"alertmanager_image,omitempty"`
	GrafanaImage             string `yaml:"grafana_image,omitempty"`
	GrafanaAdminPasswordFile string `yaml:"grafana_admin_password_file,omitempty"`
	TelegramBotTokenFile     string `yaml:"telegram_bot_token_file,omitempty"`
}

type ModuleToggle struct {
	Enabled bool `yaml:"enabled"`
}

type NvidiaConfig struct {
	Enabled       bool                `yaml:"enabled"`
	GPUAllocation GPUAllocationConfig `yaml:"gpu_allocation,omitempty"`
	// Persistenced keeps the driver loaded (nvidia-persistenced), avoiding
	// multi-second CUDA init and GPUs dropping out between jobs.
	Persistenced bool `yaml:"persistenced,omitempty"`
	// FabricManager enables nvidia-fabricmanager where installed (required
	// for NVSwitch systems: DGX/HGX A100, H100, H200).
	FabricManager bool `yaml:"fabric_manager,omitempty"`
}

type GPUAllocationConfig struct {
	Enabled bool   `yaml:"enabled"`
	Method  string `yaml:"method,omitempty"` // "env" (default), "cgroup", "both"
}

type DockerConfig struct {
	Enabled    bool   `yaml:"enabled"`
	StorageDir string `yaml:"storage_dir,omitempty"`
}

type CloudflaredConfig struct {
	Enabled     bool   `yaml:"enabled"`
	TunnelToken string `yaml:"tunnel_token,omitempty"`
	// TunnelTokenFile names a root-owned, mode 0600 file holding the token,
	// read at check/apply time so the token never sits in a site config.
	TunnelTokenFile string               `yaml:"tunnel_token_file,omitempty"`
	PrivateNetwork  PrivateNetworkConfig `yaml:"private_network,omitempty"`
}

type PrivateNetworkConfig struct {
	Enabled   bool   `yaml:"enabled"`
	Interface string `yaml:"interface"`
	Address   string `yaml:"address"`
}

type StorageConfig struct {
	Enabled  bool              `yaml:"enabled"`
	DataDir  string            `yaml:"data_dir,omitempty"`
	Symlinks map[string]string `yaml:"symlinks,omitempty"`
}

type NetworkConfig struct {
	Enabled      bool  `yaml:"enabled"`
	UFW          bool  `yaml:"ufw"`
	AllowedPorts []int `yaml:"allowed_ports,omitempty"`
}

type UsersConfig struct {
	FleetSudoUsers []string `yaml:"fleet_sudo_users,omitempty"`
	HomeBase       string   `yaml:"home_base"`
	DefaultShell   string   `yaml:"default_shell"`
	DefaultGroups  []string `yaml:"default_groups"`
	SudoNopasswd   bool     `yaml:"sudo_nopasswd"`
	// Accounts are converged by the users module: created if missing,
	// missing SSH keys and group memberships added. Never removed.
	Accounts []AccountConfig `yaml:"accounts,omitempty"`
}

// AccountConfig declares a login account.
type AccountConfig struct {
	Name       string   `yaml:"name"`
	SSHPubkeys []string `yaml:"ssh_pubkeys,omitempty"`
	Groups     []string `yaml:"groups,omitempty"` // in addition to users.default_groups
}

// AddAccount merges an account into Accounts (keys and groups are unioned
// when the name already exists).
func (u *UsersConfig) AddAccount(a AccountConfig) {
	for i := range u.Accounts {
		if u.Accounts[i].Name == a.Name {
			u.Accounts[i].SSHPubkeys = appendUnique(u.Accounts[i].SSHPubkeys, a.SSHPubkeys...)
			u.Accounts[i].Groups = appendUnique(u.Accounts[i].Groups, a.Groups...)
			return
		}
	}
	u.Accounts = append(u.Accounts, a)
}

func appendUnique(list []string, items ...string) []string {
	for _, it := range items {
		found := false
		for _, l := range list {
			if l == it {
				found = true
				break
			}
		}
		if !found && it != "" {
			list = append(list, it)
		}
	}
	return list
}

type SSHConfig struct {
	DisableRootLogin    bool `yaml:"disable_root_login"`
	DisablePasswordAuth bool `yaml:"disable_password_auth"`
	Port                int  `yaml:"port,omitempty"`
	// MaxAuthTries limits authentication attempts per connection.
	MaxAuthTries int `yaml:"max_auth_tries,omitempty"`
	// PasswordAuthUsers keep password login while disable_password_auth is
	// on (a Match User block), so password auth can be retired per user.
	PasswordAuthUsers []string `yaml:"password_auth_users,omitempty"`
}

// IsModuleEnabled returns whether a given module name is enabled in this config.
func (c *Config) IsModuleEnabled(name string) bool {
	switch name {
	case "locale":
		return c.Modules.Locale.Enabled
	case "system":
		return c.Modules.System.Enabled
	case "security":
		return c.Modules.Security.Enabled
	case "monitoring":
		return c.Modules.Monitoring.Enabled
	case "packages":
		return c.Modules.Packages.Enabled
	case "ssh":
		return c.Modules.SSH.Enabled
	case "users":
		return c.Modules.Users.Enabled
	case "docker":
		return c.Modules.Docker.Enabled
	case "nvidia":
		return c.Modules.Nvidia.Enabled
	case "gpu":
		return c.Modules.Nvidia.GPUAllocation.Enabled
	case "cloudflared":
		return c.Modules.Cloudflared.Enabled
	case "storage":
		return c.Modules.Storage.Enabled
	case "network":
		return c.Modules.Network.Enabled
	case "tenant":
		return c.Modules.Tenant.Enabled
	default:
		return false
	}
}

// AllPackages returns the merged package list (base + extra).
func (c *Config) AllPackages() []string {
	seen := make(map[string]bool, len(c.Packages)+len(c.PackagesExtra))
	var result []string
	for _, p := range c.Packages {
		if !seen[p] {
			seen[p] = true
			result = append(result, p)
		}
	}
	for _, p := range c.PackagesExtra {
		if !seen[p] {
			seen[p] = true
			result = append(result, p)
		}
	}
	return result
}

// DoctorConfig tunes `rootfiles doctor` grading.
type DoctorConfig struct {
	Disk DoctorDiskConfig `yaml:"disk,omitempty"`
}

// DoctorDiskConfig grades filesystem use. A mount fails only when its use
// reaches FailPercent and, when MinFree is set, less than MinFree is free.
type DoctorDiskConfig struct {
	WarnPercent float64 `yaml:"warn_percent,omitempty"` // default 80
	FailPercent float64 `yaml:"fail_percent,omitempty"` // default 90
	MinFree     string  `yaml:"min_free,omitempty"`     // e.g. 100GiB or 500G
}

// Thresholds returns the warn and fail percentages and the minimum free
// bytes (0 when unset), with defaults filled in.
func (d DoctorDiskConfig) Thresholds() (warn, fail float64, minFree uint64) {
	warn, fail = d.WarnPercent, d.FailPercent
	if warn == 0 {
		warn = 80
	}
	if fail == 0 {
		fail = 90
	}
	minFree, _ = ParseSize(d.MinFree)
	return warn, fail, minFree
}

// ParseSize reads a binary size such as 100GiB, 100G or 512M; "" is 0.
func ParseSize(s string) (uint64, error) {
	if s == "" {
		return 0, nil
	}
	m := validFreeSize.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("%q (want e.g. 100GiB, 500G)", s)
	}
	n, err := strconv.ParseUint(m[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q: %w", s, err)
	}
	return n << (10 * (strings.Index("KMGT", m[2]) + 1)), nil
}
